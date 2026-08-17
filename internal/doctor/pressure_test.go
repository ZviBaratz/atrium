package doctor

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	kib = 1 << 10
	mib = 1 << 20
	gib = 1 << 30
	tib = 1 << 40
)

// withPressureSeams points every platform reader at canned data and restores them, so
// the assembly and all threshold arithmetic run identically on any platform. Mirrors
// oom_test.go's seam swap.
type pressureSeams struct {
	swapTotal, swapFree uint64
	swapOK              bool
	availRAM            uint64
	availOK             bool
	zram                uint64
	zramOK              bool
	ram                 uint64
	ramOK               bool
	fs                  map[string]fsStat
	socketDir           string
}

func withPressureSeams(t *testing.T, s pressureSeams) {
	t.Helper()
	prevSwap, prevAvail, prevZram, prevFS, prevMem, prevSock :=
		readSwap, readAvailRAM, readZram, readFS, readMem, socketDirOf
	t.Cleanup(func() {
		readSwap, readAvailRAM, readZram, readFS, readMem, socketDirOf =
			prevSwap, prevAvail, prevZram, prevFS, prevMem, prevSock
	})

	readSwap = func() (uint64, uint64, bool) { return s.swapTotal, s.swapFree, s.swapOK }
	readAvailRAM = func() (uint64, bool) { return s.availRAM, s.availOK }
	readZram = func() (uint64, bool) { return s.zram, s.zramOK }
	readMem = func() (uint64, bool) { return s.ram, s.ramOK }
	readFS = func(path string) (fsStat, bool) {
		st, ok := s.fs[path]
		return st, ok
	}
	socketDirOf = func(context.Context) string { return s.socketDir }
}

// TestPressureThresholds pins every threshold, because a threshold that exists only in
// a comment is unverified and one off-by-one makes the comment a lie.
func TestPressureThresholds(t *testing.T) {
	require.Equal(t, 80, swapWarnPct)
	require.Equal(t, 70, tmpfsWarnPct)
	require.Equal(t, 90, diskWarnPct)
	require.Equal(t, 80, inodeWarnPct)
	require.Equal(t, 25, tmpfsRAMSharePct)

	// A tmpfs must warn earlier than a disk: its contents cost memory, so the same
	// percentage is not the same danger. This is the design decision, not an accident
	// of two numbers that happen to differ.
	require.Less(t, tmpfsWarnPct, diskWarnPct,
		"a tmpfs is charged against RAM and must trip before a disk at the same fullness")
}

// TestPressureWarnsAtTheIncidentReadings is the test that makes the thresholds
// falsifiable rather than a guess. Both columns are measured, not invented: the left is
// the 2026-08-04 host at the moment every shell command failed with exit 1 and no
// output, the right is the same host after cleanup, when everything worked.
//
// A section that warns on both is noise; one that warns on neither is decoration.
func TestPressureWarnsAtTheIncidentReadings(t *testing.T) {
	const (
		hostRAM   = 30 * gib
		tmpfsSize = 15*gib + 900*mib // /tmp, size=50% of RAM
		inodeCap  = 1048576          // nr_inodes=1m
	)

	for _, tc := range []struct {
		name                                    string
		swapUsed, swapTotal                     uint64
		tmpfsUsed                               uint64
		inodesUsed                              uint64
		wantSwap, wantBytes, wantRAMShare, want bool
	}{
		{
			name: "at the incident: swap 91%, tmpfs 85% full holding 43% of RAM",
			// Measured: swap 21.0 of 23.2 GiB; /tmp 13 GiB across 462,655 files.
			swapUsed: 21 * gib, swapTotal: 23*gib + 200*mib,
			tmpfsUsed: 13 * gib, inodesUsed: 462655,
			wantSwap: true, wantBytes: true, wantRAMShare: true, want: true,
		},
		{
			name: "same host after cleanup: swap 69%, tmpfs 41% full holding 21% of RAM",
			// Measured: swap 16.0 of 23.2 GiB; /tmp 6.3 GiB across 199,739 files.
			swapUsed: 16 * gib, swapTotal: 23*gib + 200*mib,
			tmpfsUsed: 6*gib + 300*mib, inodesUsed: 199739,
			wantSwap: false, wantBytes: false, wantRAMShare: false, want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withPressureSeams(t, pressureSeams{
				swapTotal: tc.swapTotal, swapFree: tc.swapTotal - tc.swapUsed, swapOK: true,
				ram: hostRAM, ramOK: true,
				availRAM: 8 * gib, availOK: true,
				socketDir: "/tmp/tmux-1000",
				fs: map[string]fsStat{
					"/tmp/tmux-1000": {
						TotalBytes: tmpfsSize, AvailBytes: tmpfsSize - tc.tmpfsUsed,
						TotalInodes: inodeCap, FreeInodes: inodeCap - tc.inodesUsed,
						Tmpfs: true, Dev: 42,
					},
				},
			})

			r := gatherPressure(context.Background())
			require.Equal(t, tc.wantSwap, r.SwapWarn, "swap headroom verdict")

			fs := findFS(t, r, "tmux socket dir")
			require.True(t, fs.Known)
			require.True(t, fs.Tmpfs)
			require.Equal(t, tc.wantBytes, fs.BytesWarn, "tmpfs fullness verdict")
			require.Equal(t, tc.wantRAMShare, fs.RAMShareWarn, "tmpfs share-of-RAM verdict")

			// The inode reading would not have caught this incident, and the section
			// must not pretend otherwise. 462,655 of 1,048,576 is 44%.
			require.False(t, fs.InodesWarn,
				"inode headroom did not trip at either reading; it is not this incident's signal")

			require.Equal(t, tc.want, PressureWarned(r))
		})
	}
}

// findFS returns the watched filesystem carrying label.
func findFS(t *testing.T, r PressureResult, label string) Filesystem {
	t.Helper()
	for _, fs := range r.Filesystems {
		if fs.Label == label {
			return fs
		}
	}
	t.Fatalf("no watched filesystem labelled %q in %+v", label, r.Filesystems)
	return Filesystem{}
}

// TestClassifyFilesystemAppliesTheTmpfsSplit checks the boundary either side of both
// fullness thresholds, since the tmpfs/disk split is the load-bearing decision.
func TestClassifyFilesystemAppliesTheTmpfsSplit(t *testing.T) {
	const ram = 100 * gib // large enough that the RAM-share rule never interferes

	for _, tc := range []struct {
		name      string
		tmpfs     bool
		usedPct   uint64
		wantBytes bool
	}{
		{"tmpfs just under its threshold", true, 69, false},
		{"tmpfs at its threshold", true, 70, true},
		{"disk at the tmpfs threshold is fine", false, 70, false},
		{"disk just under its own threshold", false, 89, false},
		{"disk at its threshold", false, 90, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const total = 100 * mib
			fs := Filesystem{
				Tmpfs:      tc.tmpfs,
				TotalBytes: total,
				AvailBytes: total - tc.usedPct*mib,
			}
			classifyFilesystem(&fs, ram, true)
			require.Equal(t, tc.wantBytes, fs.BytesWarn)
		})
	}
}

// TestClassifyFilesystemRAMShareOnlyAppliesToTmpfs guards the reasoning behind the
// rule: a disk costs no memory however full it is, so reporting its share of RAM would
// be a warning about nothing.
func TestClassifyFilesystemRAMShareOnlyAppliesToTmpfs(t *testing.T) {
	const ram = 30 * gib
	// Half of RAM in use, but only 30% of a generous filesystem — under both fullness
	// thresholds, so the share rule is the only thing that can fire.
	build := func(tmpfs bool) Filesystem {
		return Filesystem{Tmpfs: tmpfs, TotalBytes: 50 * gib, AvailBytes: 35 * gib}
	}

	onTmpfs := build(true)
	classifyFilesystem(&onTmpfs, ram, true)
	require.True(t, onTmpfs.RAMShareWarn, "15 GiB of tmpfs is half this host's RAM")
	require.False(t, onTmpfs.BytesWarn, "and it is only 30% of the tmpfs's own cap")

	onDisk := build(false)
	classifyFilesystem(&onDisk, ram, true)
	require.False(t, onDisk.RAMShareWarn, "a disk's contents cost no memory")

	// Unknown RAM cannot yield a share of it.
	unknownRAM := build(true)
	classifyFilesystem(&unknownRAM, 0, false)
	require.False(t, unknownRAM.RAMShareWarn)
}

// TestPressureDedupesByDevice: three watched paths on one filesystem must be reported
// once. The second and third say which row already carried the numbers rather than
// repeating them, so a reader cannot mistake one filesystem for three.
func TestPressureDedupesByDevice(t *testing.T) {
	shared := fsStat{TotalBytes: 100 * gib, AvailBytes: 50 * gib, TotalInodes: 1000, FreeInodes: 500, Dev: 7}
	withPressureSeams(t, pressureSeams{
		swapOK: true, swapTotal: 8 * gib, swapFree: 8 * gib,
		ram: 30 * gib, ramOK: true,
		socketDir: "/tmp/tmux-1000",
		fs: map[string]fsStat{
			"/tmp/tmux-1000": shared,
			"/tmp":           shared,
			// The data dir resolves through the ancestor walk to "/" below.
			"/": shared,
		},
	})

	r := gatherPressure(context.Background())
	var withNumbers, deduped int
	for _, fs := range r.Filesystems {
		if fs.SameAs != "" {
			deduped++
			require.False(t, fs.Known, "a deduped row carries no readings of its own")
			require.Zero(t, fs.TotalBytes)
			continue
		}
		withNumbers++
	}
	require.Equal(t, 1, withNumbers, "one filesystem, one set of numbers")
	require.Equal(t, 2, deduped)

	out := RenderPressure(r)
	require.Contains(t, renderedRow(t, out, "temp dir"), "same filesystem as")
}

// TestMeasureFSWalksToAnExistingAncestor covers the fresh-install case: GetConfigDir
// returns a path it never creates, and the filesystem it will land on is the useful
// answer — provided the report says that is what it measured.
func TestMeasureFSWalksToAnExistingAncestor(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does", "not", "exist", "yet")

	prev := readFS
	t.Cleanup(func() { readFS = prev })
	readFS = func(path string) (fsStat, bool) {
		if path != root {
			return fsStat{}, false
		}
		return fsStat{TotalBytes: 10 * gib, AvailBytes: 5 * gib, Dev: 3}, true
	}

	st, measured, ok := measureFS(missing)
	require.True(t, ok)
	require.Equal(t, root, measured, "the ancestor actually measured")
	require.Equal(t, uint64(10*gib), st.TotalBytes)

	// The substitution has to be visible in the output, not silently attributed to the
	// path that does not exist.
	fs := Filesystem{Label: "data dir", Path: missing, Measured: root, Known: true,
		TotalBytes: 10 * gib, AvailBytes: 5 * gib}
	row := renderedRow(t, RenderPressure(PressureResult{
		Supported: true, Filesystems: []Filesystem{fs},
	}), "data dir")
	require.Contains(t, row, "not yet created")
	require.Contains(t, row, root)
}

// TestMeasureFSGivesUpAtTheRoot: a readFS that never answers must terminate rather
// than loop on filepath.Dir("/") == "/".
func TestMeasureFSGivesUpAtTheRoot(t *testing.T) {
	prev := readFS
	t.Cleanup(func() { readFS = prev })
	readFS = func(string) (fsStat, bool) { return fsStat{}, false }

	_, _, ok := measureFS("/nowhere/at/all")
	require.False(t, ok)
}

// TestRenderPressureMatchesTheSectionConvention holds the section to the shape every
// other doctor section keeps. Mirrors TestRenderSchemeMatchesTheSectionConvention.
func TestRenderPressureMatchesTheSectionConvention(t *testing.T) {
	withPressureSeams(t, pressureSeams{
		swapOK: true, swapTotal: 8 * gib, swapFree: 4 * gib,
		availRAM: 12 * gib, availOK: true, ram: 30 * gib, ramOK: true,
		zram: 4 * gib, zramOK: true,
		socketDir: "/tmp/tmux-1000",
		fs: map[string]fsStat{
			"/tmp/tmux-1000": {TotalBytes: 16 * gib, AvailBytes: 15 * gib, TotalInodes: 1 << 20, FreeInodes: 1 << 19, Tmpfs: true, Dev: 1},
			"/tmp":           {TotalBytes: 16 * gib, AvailBytes: 15 * gib, Tmpfs: true, Dev: 2},
			"/":              {TotalBytes: 2 * tib, AvailBytes: 1 * tib, TotalInodes: 1 << 27, FreeInodes: 1 << 26, Dev: 3},
		},
	})

	for _, out := range []string{
		RenderPressure(gatherPressure(context.Background())),
		RenderPressure(PressureResult{}), // the unsupported branch keeps the shape too
	} {
		require.False(t, strings.HasPrefix(out, "\n"), "the blank separator is main.go's job")
		require.True(t, strings.HasSuffix(out, "\n"), "sections are newline-terminated")
		require.False(t, strings.HasSuffix(out, "\n\n"), "no trailing blank line")

		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		require.Equal(t, "Host pressure:", lines[0],
			"header is a bare Title-case line ending in a colon, like Host capacity:")
		for _, l := range lines[1:] {
			require.True(t, strings.HasPrefix(l, "  "), "row %q must be indented two spaces", l)
		}
	}
}

// TestRenderPressureUnavailableWhenUnsupported: off Linux the section says what is
// missing instead of rendering an empty block or a half-true one.
func TestRenderPressureUnavailableWhenUnsupported(t *testing.T) {
	out := RenderPressure(PressureResult{Supported: false})
	require.Contains(t, out, "unavailable")
	require.Contains(t, out, "Linux-only")
	require.False(t, PressureWarned(PressureResult{Supported: false, SwapWarn: true}),
		"an unsupported platform has taken no reading, so it cannot have a verdict")
}

// TestCheckPressureShortCircuitsWhenUnsupported: the seams must not even be consulted
// off Linux, or a stub returning zeroes would render as a host with no swap and no
// filesystems rather than as an unavailable section.
func TestCheckPressureShortCircuitsWhenUnsupported(t *testing.T) {
	if pressureSupported {
		t.Skip("platform supports pressure readings; the short-circuit is unreachable here")
	}
	called := false
	prev := readSwap
	t.Cleanup(func() { readSwap = prev })
	readSwap = func() (uint64, uint64, bool) { called = true; return 0, 0, false }

	r := CheckPressure(context.Background())
	require.False(t, r.Supported)
	require.False(t, called, "no reader should run on an unsupported platform")
	require.Empty(t, r.Filesystems)
}

// TestRenderPressureReportsUnknownsAsUnknown: every unreadable value renders as
// unknown rather than as a plausible zero, and the section still exists.
func TestRenderPressureReportsUnknownsAsUnknown(t *testing.T) {
	out := RenderPressure(PressureResult{
		Supported: true,
		Filesystems: []Filesystem{
			{Label: "data dir", Path: "/var/lib/atrium"}, // Known false
			{Label: "temp dir"},                          // Path empty too
		},
	})
	require.Contains(t, renderedRow(t, out, "swap"), "unknown")
	require.Contains(t, renderedRow(t, out, "available RAM"), "unknown")
	require.Contains(t, renderedRow(t, out, "data dir"), "unreadable")
	require.Contains(t, renderedRow(t, out, "temp dir"), "unknown")
	require.False(t, PressureWarned(PressureResult{Supported: true}),
		"nothing readable is not a warning")
}

// TestRenderPressureNotesZramSwap: the note is the difference between a correct report
// and a misleading one, so it must appear when zram backs the swap and stay away when
// it does not.
func TestRenderPressureNotesZramSwap(t *testing.T) {
	base := PressureResult{Supported: true, SwapKnown: true, SwapTotal: 23 * gib, SwapFree: 2 * gib}

	withZram := base
	withZram.ZramBytes, withZram.ZramKnown = 15*gib, true
	out := RenderPressure(withZram)
	require.Contains(t, out, "zram")
	require.Contains(t, out, "lives in RAM")
	require.Contains(t, out, "15.0 GiB of this swap")

	noZram := base
	noZram.ZramKnown = true // read successfully, and there is none
	require.NotContains(t, RenderPressure(noZram), "zram")

	// Unreadable is not "none": say nothing rather than assert an absence.
	require.NotContains(t, RenderPressure(base), "zram")
}

// TestRenderPressureNotesASwaplessHost: no swap is not a warning, but it changes what
// a full tmpfs does, so it earns the note.
func TestRenderPressureNotesASwaplessHost(t *testing.T) {
	out := RenderPressure(PressureResult{Supported: true, SwapKnown: true, SwapTotal: 0})
	require.Contains(t, renderedRow(t, out, "swap"), "none configured")
	require.Contains(t, out, "nowhere")
}

// TestRenderPressureHintsOnlyOnATrippedRow: a hint is an instruction, and a healthy
// host has nothing to do about a tmpfs it is nowhere near filling.
func TestRenderPressureHintsOnlyOnATrippedRow(t *testing.T) {
	roomy := Filesystem{Label: "tmux socket dir", Path: "/tmp/tmux-1000", Measured: "/tmp/tmux-1000",
		Known: true, Tmpfs: true, TotalBytes: 16 * gib, AvailBytes: 15 * gib}
	out := RenderPressure(PressureResult{Supported: true, Filesystems: []Filesystem{roomy}})
	require.Contains(t, out, "tmpfs", "the row still says what kind of filesystem it is")
	require.NotContains(t, out, "ENOSPC", "no instruction for a host with nothing to do")

	full := roomy
	full.AvailBytes, full.BytesWarn = 2*gib, true
	require.Contains(t, RenderPressure(PressureResult{Supported: true, Filesystems: []Filesystem{full}}),
		"ENOSPC")
}

// TestWarnGlyphGoesInTheValueNotTheLabel: fmt pads %-Ns by bytes while a terminal lays
// it out by display width, so a three-byte one-cell glyph inside a padded field
// misaligns the column. Every doctor section keeps ⚠ in the trailing unpadded field;
// this asserts that rather than trusting it.
func TestWarnGlyphGoesInTheValueNotTheLabel(t *testing.T) {
	fs := Filesystem{Label: "tmux socket dir", Path: "/tmp", Measured: "/tmp", Known: true,
		Tmpfs: true, TotalBytes: 16 * gib, AvailBytes: 1 * gib, BytesWarn: true,
		TotalInodes: 100, FreeInodes: 10, InodesWarn: true}
	out := RenderPressure(PressureResult{
		Supported: true, SwapKnown: true, SwapTotal: 8 * gib, SwapFree: 1 * gib, SwapWarn: true,
		Filesystems: []Filesystem{fs},
	})

	for _, label := range []string{"swap", "space", "inodes"} {
		row := renderedRow(t, out, label)
		require.Contains(t, row, "⚠", "row %q should be flagged", label)
		before, _, _ := strings.Cut(row, "⚠")
		require.Contains(t, before, label,
			"the glyph must follow the label, not precede it inside the padded field")
	}

	// The values line up: every warned and unwarned row starts its value at the same
	// column, which is the whole point of keeping the glyph out of the padding.
	require.Equal(t, valueColumn(t, out, "swap"), valueColumn(t, out, "available RAM"))
	require.Equal(t, valueColumn(t, out, "space"), valueColumn(t, out, "inodes"))
}

// valueColumn returns the display column at which a row's value begins.
func valueColumn(t *testing.T, out, label string) int {
	t.Helper()
	row := renderedRow(t, out, label)
	idx := strings.Index(row, label) + len(label)
	rest := row[idx:]
	return idx + len(rest) - len(strings.TrimLeft(rest, " "))
}

// TestPercentRounds guards the figure the user reads against the verdict the threshold
// takes: they come from different expressions, and a truncating percent that printed
// "79%" beside a fired 80% warning would read as a bug in the check.
func TestPercentRounds(t *testing.T) {
	require.Equal(t, 0, percent(0, 0), "a zero total must not divide")
	require.Equal(t, 50, percent(1, 2))
	require.Equal(t, 91, percent(21*gib, 23*gib+200*mib), "the incident's swap reading")
	require.Equal(t, 100, percent(10, 10))
}

// TestAtOrOverPct: the boundary is inclusive, and a zero total never warns — many
// filesystems report no inode cap at all, and dividing by it would read as 100% full.
func TestAtOrOverPct(t *testing.T) {
	require.True(t, atOrOverPct(80, 100, 80), "the threshold is inclusive")
	require.False(t, atOrOverPct(79, 100, 80))
	require.False(t, atOrOverPct(0, 0, 80), "no cap is not a full cap")
	require.False(t, atOrOverPct(1<<40, 0, 1), "nor is it full of anything")
}

// TestHumanizeBytesPicksAReadableUnit — a small tmpfs must not render as "0.0 GiB".
func TestHumanizeBytesPicksAReadableUnit(t *testing.T) {
	require.Equal(t, "64 MiB", humanizeBytes(64*mib))
	require.Equal(t, "0 MiB", humanizeBytes(1*kib))
	require.Equal(t, "1.0 GiB", humanizeBytes(gib))
	require.Equal(t, "15.5 GiB", humanizeBytes(15*gib+512*mib))
	require.Equal(t, "1.9 TiB", humanizeBytes(2*tib-100*gib))
}

// TestStatfsPathReadsThisFilesystem exercises the real platform reader against a
// directory that certainly exists.
func TestStatfsPathReadsThisFilesystem(t *testing.T) {
	if !pressureSupported {
		t.Skipf("filesystem readings unsupported on %s", runtime.GOOS)
	}
	st, ok := statfsPath(t.TempDir())
	require.True(t, ok)
	require.NotZero(t, st.TotalBytes, "a real filesystem has a size")
	require.LessOrEqual(t, st.AvailBytes, st.TotalBytes)
	require.LessOrEqual(t, st.FreeInodes, st.TotalInodes)
	require.NotZero(t, st.Dev, "the device id is what identifies the filesystem")

	_, ok = statfsPath(filepath.Join(t.TempDir(), "definitely-absent"))
	require.False(t, ok, "a missing path yields no reading, not a zeroed one")
}

// TestHostSwapReadsSwap and TestAvailRAMBytesReadsMemory exercise the kernel readers.
// A host may legitimately run without swap, so only the invariant is asserted.
func TestHostSwapReadsSwap(t *testing.T) {
	if !pressureSupported {
		t.Skipf("swap readings unsupported on %s", runtime.GOOS)
	}
	total, free, ok := hostSwap()
	require.True(t, ok)
	require.LessOrEqual(t, free, total, "free swap cannot exceed the total")
}

func TestAvailRAMBytesReadsMemory(t *testing.T) {
	if !pressureSupported {
		t.Skipf("memory readings unsupported on %s", runtime.GOOS)
	}
	avail, ok := availRAMBytes()
	require.True(t, ok, "MemAvailable is present on every supported kernel")
	require.NotZero(t, avail)

	ram, ramOK := hostMemBytes()
	require.True(t, ramOK)
	require.Less(t, avail, ram, "available memory is a subset of the total")
}

// TestZramSwapBytesReadsProcSwaps: a host with no zram must report (0, true) — "read
// it, there is none" — so the renderer can tell that from "could not read it".
func TestZramSwapBytesReadsProcSwaps(t *testing.T) {
	if !pressureSupported {
		t.Skipf("swap readings unsupported on %s", runtime.GOOS)
	}
	_, ok := zramSwapBytes()
	require.True(t, ok, "/proc/swaps is readable on Linux even with no swap configured")
}
