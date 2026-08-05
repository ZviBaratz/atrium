package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// Host-pressure warning thresholds, as percentages of the relevant total (#594).
//
// The tmpfs/disk split is the point, not an inconsistency: 90% of a 2 TB disk leaves
// 200 GB and is fine, while 70% of a 16 GiB tmpfs on a 30 GiB host is already
// dangerous, because a tmpfs's contents are charged against memory. tmpfsRAMSharePct
// catches the same magnitude independently of how generously the tmpfs was sized —
// a host that mounts /tmp at 90% of RAM would otherwise never trip tmpfsWarnPct until
// the machine was already dead.
//
// Calibrated against the 2026-08-04 incident (swap 91%, a 16 GiB tmpfs 85% full
// holding 43% of RAM: all three fire) and against the same host after cleanup (69%,
// 41%, 21%: none fire). TestPressureWarnsAtTheIncidentReadings pins both columns —
// without it these numbers are a guess. Every value here is asserted by
// TestPressureThresholds, because a threshold stated only in a comment is unverified.
const (
	swapWarnPct      = 80 // of total swap used
	tmpfsWarnPct     = 70 // of a tmpfs's own size cap used
	diskWarnPct      = 90 // of a non-tmpfs filesystem used
	inodeWarnPct     = 80 // of any filesystem's inode cap used
	tmpfsRAMSharePct = 25 // a tmpfs holding at least this share of total RAM
)

// fsStat is one filesystem's raw statfs reading. Dev is the device id from stat(2),
// which is what identifies "the same filesystem" — deliberately not statfs's Fsid,
// which Linux reports as zero for every tmpfs and would merge /tmp with /dev/shm.
type fsStat struct {
	TotalBytes  uint64
	AvailBytes  uint64
	TotalInodes uint64
	FreeInodes  uint64
	Tmpfs       bool
	Dev         uint64
}

// Filesystem is one watched path's headroom.
//
// Path is what Atrium cares about; Measured is what statfs could actually answer for,
// which differs when Path does not exist yet (a fresh install's data dir). SameAs
// names an earlier row sharing this filesystem, and when it is set every reading is
// zero — the row exists to say "already reported above", not to repeat it.
type Filesystem struct {
	Label    string
	Path     string
	Measured string
	SameAs   string

	Tmpfs       bool
	TotalBytes  uint64
	AvailBytes  uint64
	TotalInodes uint64
	FreeInodes  uint64
	Known       bool

	BytesWarn    bool
	InodesWarn   bool
	RAMShareWarn bool
}

// PressureResult is the live host-pressure snapshot the doctor reports (#594).
//
// Where CapacityResult answers "how big is this machine", this answers "how close to
// the wall is it right now". Supported is false off Linux. Every reading carries its
// own Known flag: like the other doctor checks, an unreadable value is reported as
// unknown rather than as an error.
//
// AvailRAM is present as context and is never a warning trigger. At the incident this
// section exists for, 7.7 GiB was still "available" while nothing on the machine could
// run — a free-RAM check would have reported a healthy host.
type PressureResult struct {
	Supported bool

	RAMBytes      uint64
	RAMKnown      bool
	AvailRAMBytes uint64
	AvailRAMKnown bool

	SwapTotal uint64
	SwapFree  uint64
	SwapKnown bool
	SwapWarn  bool

	// ZramBytes is how much of SwapTotal is backed by zram. It matters because zram
	// lives in RAM: swapping a tmpfs page there compresses it but does not free it,
	// so a host with mostly-zram swap has far less real relief than SwapFree implies.
	ZramBytes uint64
	ZramKnown bool

	Filesystems []Filesystem
}

// Seams so gatherPressure's assembly and every threshold decision are exercisable on
// any platform with hand-built data, mirroring oom.go's. Production wires the
// platform readers and the real socket-directory lookup.
//
// gatherPressure, not CheckPressure: CheckPressure returns before reading any seam on
// an unsupported platform, so a test driving it there gets an empty result however the
// seams are set. That is what makes the split below worth having rather than a
// stylistic preference — this comment claimed "any platform" while the macOS job proved
// otherwise.
var (
	readSwap     = hostSwap
	readAvailRAM = availRAMBytes
	readZram     = zramSwapBytes
	readFS       = statfsPath
	readMem      = hostMemBytes
	// tmux.SocketDir also reports whether it got that path from the live server or
	// reconstructed it (#598). This section wants only the path, because its question is
	// how much headroom the filesystem underneath has, and that is the same filesystem
	// either way. Adapted here rather than by widening the seam, so a stub stays a
	// one-line func returning a path.
	socketDirOf = func(ctx context.Context) string { dir, _ := tmux.SocketDir(ctx); return dir }
)

// CheckPressure gathers the live host-pressure snapshot. Like the other doctor checks
// it never fails and never mutates: it reads two kernel counters, two small /proc
// files and one statfs per watched filesystem, plus a read-only `tmux display-message`
// to locate the socket directory. Safe to run beside a live TUI.
//
// The context bounds only that tmux probe; nothing else here can block.
//
// The platform gate lives here and the readings live in gatherPressure, so that an
// unsupported platform consults no reader — not even a stub — and in particular never
// spawns the tmux probe. Splitting them also keeps the assembly reachable in a test on
// every platform, which the gate would otherwise make impossible.
func CheckPressure(ctx context.Context) PressureResult {
	if !pressureSupported {
		return PressureResult{Supported: false}
	}
	return gatherPressure(ctx)
}

// gatherPressure takes every reading and applies the thresholds.
//
// Supported is true here because it describes the readings this function took, not the
// platform it ran on — CheckPressure owns that question. A test may therefore call this
// directly on any platform to exercise the assembly against hand-built data.
func gatherPressure(ctx context.Context) PressureResult {
	r := PressureResult{Supported: true}

	r.RAMBytes, r.RAMKnown = readMem()
	r.AvailRAMBytes, r.AvailRAMKnown = readAvailRAM()
	r.SwapTotal, r.SwapFree, r.SwapKnown = readSwap()
	if r.SwapKnown && r.SwapTotal > 0 {
		r.SwapWarn = atOrOverPct(r.SwapTotal-r.SwapFree, r.SwapTotal, swapWarnPct)
	}
	r.ZramBytes, r.ZramKnown = readZram()

	r.Filesystems = watchedFilesystems(ctx, r.RAMBytes, r.RAMKnown)
	return r
}

// watchedFilesystems measures the three paths whose exhaustion breaks Atrium, in a
// fixed order so the report is deterministic.
//
// The socket directory and the temp directory are asked for separately and by
// different means on purpose. tmux ignores $TMPDIR and hardcodes /tmp, so
// tmux.SocketDir is the only thing that can name where the sockets are; os.TempDir
// honours $TMPDIR and is the right answer for where Go's t.TempDir, agent scratch and
// internal/profile.Dir write. Both are correct for their own question — do not
// "fix" either to agree with the other.
func watchedFilesystems(ctx context.Context, ram uint64, ramKnown bool) []Filesystem {
	dataDir, err := config.GetConfigDir()
	if err != nil {
		dataDir = ""
	}
	watched := []Filesystem{
		{Label: "data dir", Path: dataDir},
		{Label: "tmux socket dir", Path: socketDirOf(ctx)},
		{Label: "temp dir", Path: os.TempDir()},
	}

	out := make([]Filesystem, 0, len(watched))
	seen := map[uint64]string{}
	for _, fs := range watched {
		if fs.Path == "" {
			out = append(out, fs)
			continue
		}
		st, measured, ok := measureFS(fs.Path)
		if !ok {
			out = append(out, fs)
			continue
		}
		if label, dup := seen[st.Dev]; dup {
			fs.Measured, fs.SameAs = measured, label
			out = append(out, fs)
			continue
		}
		seen[st.Dev] = fs.Label

		fs.Measured = measured
		fs.Known = true
		fs.Tmpfs = st.Tmpfs
		fs.TotalBytes, fs.AvailBytes = st.TotalBytes, st.AvailBytes
		fs.TotalInodes, fs.FreeInodes = st.TotalInodes, st.FreeInodes
		classifyFilesystem(&fs, ram, ramKnown)
		out = append(out, fs)
	}
	return out
}

// classifyFilesystem applies the thresholds to one measured filesystem. Named for its
// subject rather than the bare "classify" that check.go's drift classifier already owns.
func classifyFilesystem(fs *Filesystem, ram uint64, ramKnown bool) {
	used := fs.TotalBytes - fs.AvailBytes
	// uint64 throughout, so no threshold has to be widened at the comparison — a
	// signed-to-unsigned conversion there is exactly the overflow gosec's G115 flags.
	var limit uint64 = diskWarnPct
	if fs.Tmpfs {
		limit = tmpfsWarnPct
	}
	fs.BytesWarn = atOrOverPct(used, fs.TotalBytes, limit)
	fs.InodesWarn = atOrOverPct(fs.TotalInodes-fs.FreeInodes, fs.TotalInodes, inodeWarnPct)
	// A tmpfs's contents are charged against RAM, so its absolute size matters even
	// when it is nowhere near its own cap. A non-tmpfs costs no memory, so this
	// reading is meaningless there.
	if fs.Tmpfs && ramKnown {
		fs.RAMShareWarn = atOrOverPct(used, ram, tmpfsRAMSharePct)
	}
}

// measureFS resolves path to the nearest ancestor statfs can answer for, and reports
// which path that was.
//
// The walk exists because config.GetConfigDir() returns a path it never creates, so a
// fresh install's data dir does not exist yet — and the headroom of the filesystem it
// will land on is exactly as informative as the headroom of the directory itself.
// Reporting the measured path keeps that substitution visible instead of quietly
// attributing one directory's numbers to another.
func measureFS(path string) (st fsStat, measured string, ok bool) {
	for p := path; ; {
		if st, ok := readFS(p); ok {
			return st, p, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return fsStat{}, "", false
		}
		p = parent
	}
}

// atOrOverPct reports whether used is at least pct percent of total. A zero total is
// never a warning: a filesystem reporting no inode cap (many do) would otherwise
// divide by zero or read as 100% full.
//
// Cross-multiplied rather than dividing, so a threshold is compared exactly instead of
// against a percentage that integer division already rounded.
func atOrOverPct(used, total, pct uint64) bool {
	if total == 0 {
		return false
	}
	return used*100 >= total*pct
}

// PressureWarned reports whether any reading in the snapshot tripped a threshold. It
// is the one place that answers "is this host in trouble", so a caller outside doctor
// (a TUI warning, #595) cannot disagree with the rendered section about it.
func PressureWarned(r PressureResult) bool {
	if !r.Supported {
		return false
	}
	if r.SwapWarn {
		return true
	}
	for _, fs := range r.Filesystems {
		if fs.BytesWarn || fs.InodesWarn || fs.RAMShareWarn {
			return true
		}
	}
	return false
}

// RenderPressure formats the live-pressure snapshot under a "Host pressure:" header,
// directly after RenderCapacity's static one.
//
// Healthy rows are printed, not suppressed — the same rule RenderGates and
// RenderOrphans state: "the check ran and everything is fine" and "the check silently
// had nothing to say" must not look identical. The hints appear only on a row that
// tripped, because a hint is an instruction and a healthy host has nothing to do.
//
// The ⚠ goes in the value, never the label: fmt pads %-18s by bytes while a terminal
// lays it out by display width, so a three-byte one-cell glyph inside a padded field
// silently misaligns the column. Every other doctor section puts it in the trailing
// unpadded field for the same reason.
func RenderPressure(r PressureResult) string {
	var b strings.Builder
	b.WriteString("Host pressure:\n")

	if !r.Supported {
		b.WriteString("  unavailable — swap headroom and tmpfs detection are read here\n")
		b.WriteString("  through Linux-only interfaces\n")
		return b.String()
	}

	renderSwap(&b, r)
	fmt.Fprintf(&b, "  %-18s %s\n", "available RAM", availRAMValue(r))
	for _, fs := range r.Filesystems {
		renderFilesystem(&b, fs)
	}
	return b.String()
}

// renderSwap writes the swap row, plus the zram note when zram is a material share of
// it. That note is the difference between a correct report and a misleading one: swap
// that lives in RAM is not headroom, so "2 GiB free" of mostly-zram swap promises
// relief it cannot deliver.
func renderSwap(b *strings.Builder, r PressureResult) {
	if !r.SwapKnown {
		fmt.Fprintf(b, "  %-18s unknown\n", "swap")
		return
	}
	if r.SwapTotal == 0 {
		// Not a warning — a swapless host is a deliberate configuration, and
		// CheckPressure leaves SwapWarn false here. It still earns the note, because
		// it changes what a full tmpfs does.
		fmt.Fprintf(b, "  %-18s none configured\n", "swap")
		b.WriteString("         → with no swap, a tmpfs holds its contents in RAM with nowhere\n")
		b.WriteString("           to go; the OOM killer is the only relief\n")
		return
	}

	used := r.SwapTotal - r.SwapFree
	fmt.Fprintf(b, "  %-18s %s%s used of %s (%d%%), %s free\n", "swap",
		warnGlyph(r.SwapWarn), humanizeBytes(used), humanizeBytes(r.SwapTotal),
		percent(used, r.SwapTotal), humanizeBytes(r.SwapFree))

	if r.ZramKnown && r.ZramBytes > 0 {
		fmt.Fprintf(b, "         → %s of this swap is zram, which lives in RAM: evicting a\n",
			humanizeBytes(r.ZramBytes))
		b.WriteString("           tmpfs page there compresses it, it does not free memory\n")
	}
}

// renderFilesystem writes one watched path: its location, then space and inodes as
// separate rows, then the tmpfs hint when one applies. Nested labels are indented four
// spaces against a narrower field so their values land in the same column as the
// top-level rows'.
func renderFilesystem(b *strings.Builder, fs Filesystem) {
	switch {
	case fs.Path == "":
		fmt.Fprintf(b, "  %-18s unknown\n", fs.Label)
		return
	case fs.SameAs != "":
		fmt.Fprintf(b, "  %-18s %s — same filesystem as %s\n", fs.Label, fs.Path, fs.SameAs)
		return
	case !fs.Known:
		fmt.Fprintf(b, "  %-18s %s — unreadable\n", fs.Label, fs.Path)
		return
	}

	kind := "disk"
	if fs.Tmpfs {
		kind = "tmpfs"
	}
	fmt.Fprintf(b, "  %-18s %s — %s\n", fs.Label, location(fs), kind)

	used := fs.TotalBytes - fs.AvailBytes
	fmt.Fprintf(b, "    %-16s %s%s used of %s (%d%%), %s free\n", "space",
		warnGlyph(fs.BytesWarn), humanizeBytes(used), humanizeBytes(fs.TotalBytes),
		percent(used, fs.TotalBytes), humanizeBytes(fs.AvailBytes))

	if fs.TotalInodes > 0 {
		usedNodes := fs.TotalInodes - fs.FreeInodes
		fmt.Fprintf(b, "    %-16s %s%d used of %d (%d%%)\n", "inodes",
			warnGlyph(fs.InodesWarn), usedNodes, fs.TotalInodes,
			percent(usedNodes, fs.TotalInodes))
	}

	if fs.Tmpfs && (fs.BytesWarn || fs.RAMShareWarn) {
		fmt.Fprintf(b, "         → a tmpfs is charged against RAM, so this %s competes with the\n",
			humanizeBytes(used))
		b.WriteString("           swap above; when both are exhausted, writes here fail with\n")
		b.WriteString("           ENOSPC — which surfaces as every command exiting 1 with no output\n")
	}
}

// location names the path, and the ancestor actually measured when they differ.
func location(fs Filesystem) string {
	if fs.Measured != "" && fs.Measured != fs.Path {
		return fmt.Sprintf("%s (not yet created; measured %s)", fs.Path, fs.Measured)
	}
	return fs.Path
}

// availRAMValue renders the context row. It never carries a ⚠ — see PressureResult.
func availRAMValue(r PressureResult) string {
	if !r.AvailRAMKnown {
		return "unknown"
	}
	if !r.RAMKnown {
		return humanizeBytes(r.AvailRAMBytes) + " available"
	}
	return fmt.Sprintf("%s available of %s", humanizeBytes(r.AvailRAMBytes), humanizeBytes(r.RAMBytes))
}

// warnGlyph is the "⚠ " prefix for a tripped reading, or "" — so a row's format string
// is the same either way and the two spellings cannot drift apart.
func warnGlyph(warn bool) string {
	if warn {
		return "⚠ "
	}
	return ""
}

// percent is used/total as a rounded whole percent, guarding a zero total.
//
// Clamped at 100 before narrowing to int. A filesystem cannot be more than full, so a
// higher figure would mean the two readings disagree — printing "104%" would send the
// reader hunting for a bug in their host rather than in this arithmetic.
func percent(used, total uint64) int {
	if total == 0 {
		return 0
	}
	p := (used*100 + total/2) / total
	if p > 100 {
		return 100
	}
	return int(p)
}

// humanizeBytes renders a byte count in the largest unit that keeps it readable,
// mirroring humanizeRAM's one-decimal GiB for the range a host's memory lives in and
// widening to TiB for a disk. MiB below a gigabyte keeps a small tmpfs from rendering
// as "0.0 GiB".
func humanizeBytes(n uint64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
		tib = 1 << 40
	)
	switch {
	case n >= tib:
		return fmt.Sprintf("%.1f TiB", float64(n)/tib)
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	default:
		return fmt.Sprintf("%.0f MiB", float64(n)/mib)
	}
}
