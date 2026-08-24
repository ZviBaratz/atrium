//go:build linux

package tmux

// Guards for the procfs cost reader in proccost_linux_test.go.
//
// The measurement that reader serves is opt-in and `just ci` skips it; these run on
// every gate. The instrument being expensive to exercise is not a reason for the
// thing doing the measuring to be unguarded — a reader that quietly picks the wrong
// procfs column reports a confident wrong number rather than failing.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// statLine builds a /proc/<pid>/stat line with the given comm and the four CPU tick
// fields, padding the rest so the field offsets are the real ones.
//
// The padding matters: parseProcCPU re-bases past the comm, so a fixture with the
// wrong number of leading or trailing fields would agree with an off-by-one reader.
func statLine(comm string, utime, stime, cutime, cstime string) string {
	// Fields 1..2 are pid and comm; the rest are counted from field 3 (state).
	fields := make([]string, 0, 52)
	fields = append(fields, "1234", "("+comm+")")
	for f := 3; f <= 52; f++ {
		switch f {
		case statUTimeField:
			fields = append(fields, utime)
		case statSTimeField:
			fields = append(fields, stime)
		case statCUTimeField:
			fields = append(fields, cutime)
		case statCSTimeField:
			fields = append(fields, cstime)
		case statStateField:
			fields = append(fields, "S")
		default:
			fields = append(fields, "0")
		}
	}
	out := fields[0]
	for _, f := range fields[1:] {
		out += " " + f
	}
	return out + "\n"
}

// TestParseProcCPUReadsAllFourTickFields holds parseProcCPU to reporting utime+stime
// as own time and cutime+cstime as child time — separately, and at USER_HZ.
//
// Both halves, because #546's investigation read only utime+stime and missed a third
// of Atrium's cost in the reaped-children fields. A reader that dropped either half
// would still return a plausible number.
func TestParseProcCPUReadsAllFourTickFields(t *testing.T) {
	own, children, ok := parseProcCPU(statLine("atrium", "700", "300", "1100", "400"))
	require.True(t, ok)
	require.Equal(t, 10*time.Second, own, "utime 700 + stime 300 ticks at 100Hz")
	require.Equal(t, 15*time.Second, children, "cutime 1100 + cstime 400 ticks at 100Hz")
}

// TestParseProcCPUSurvivesACommWithSpacesAndParens is the case the naive whole-line
// split gets wrong on exactly the processes this file prices: a tmux server's comm is
// "tmux: server", and the kernel neither escapes nor quotes it, so an embedded space
// shifts every later column.
func TestParseProcCPUSurvivesACommWithSpacesAndParens(t *testing.T) {
	for _, comm := range []string{"tmux: server", "tmux: client", "weird ) name", "a (b) c"} {
		t.Run(comm, func(t *testing.T) {
			own, children, ok := parseProcCPU(statLine(comm, "150", "50", "0", "0"))
			require.True(t, ok)
			require.Equal(t, 2*time.Second, own)
			require.Equal(t, time.Duration(0), children)
		})
	}
}

// TestParseProcCPURejectsUnreadableLines holds the reader to failing rather than
// guessing. A truncated line is what a racing read of a dying process returns, and a
// zero from it would read as "this client cost nothing" — the exact conclusion this
// measurement must not reach by accident.
func TestParseProcCPURejectsUnreadableLines(t *testing.T) {
	full := statLine("atrium", "1", "1", "1", "1")
	cases := map[string]string{
		"no parens":        "1234 atrium S 0 0\n",
		"truncated fields": "1234 (atrium) S 0 0 0\n",
		"non-numeric tick": statLine("atrium", "1", "x", "1", "1"),
		"empty":            "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, ok := parseProcCPU(raw)
			require.False(t, ok)
		})
	}
	// The control: the same builder with every field well-formed does parse, so the
	// cases above fail for the reason named and not because the fixture is broken.
	_, _, ok := parseProcCPU(full)
	require.True(t, ok)
}

// TestTicksToDurationDoesNotOverflow covers the reason the conversion is split: a
// straight ticks*time.Second overflows int64 above ~9.2e9 ticks, which is three
// years of CPU — reachable by a long-lived server, and it wraps to a negative
// duration rather than failing.
func TestTicksToDurationDoesNotOverflow(t *testing.T) {
	const threeYearsOfTicks = 3 * 365 * 24 * 60 * 60 * userHZ
	got := ticksToDuration(threeYearsOfTicks)
	require.Equal(t, 3*365*24*time.Hour, got)
	require.Positive(t, got)
	require.Equal(t, 10*time.Millisecond, ticksToDuration(1), "one tick at USER_HZ 100")
}

// TestParsePssBytesReadsTheRollup pins the unit conversion: procfs states kB, the
// caller wants bytes, and a report that silently mixed the two would be off by 1024
// in the direction of "this is fine".
func TestParsePssBytesReadsTheRollup(t *testing.T) {
	rollup := "Rss:               7532 kB\nPss:                571 kB\nPss_Dirty:          400 kB\n"
	got, ok := parsePssBytes(rollup)
	require.True(t, ok)
	require.Equal(t, int64(571*1024), got)
}

// TestParsePssBytesPrefersPssOverRssAndPssDirty guards the line the reader picks.
// "Pss:" is a prefix of "Pss_Dirty:" only after the colon is included, and Rss sits
// above it in the file — a reader keyed on a looser match would answer with the
// number that over-counts shared pages, which is the whole thing Pss is here to avoid.
func TestParsePssBytesPrefersPssOverRssAndPssDirty(t *testing.T) {
	got, ok := parsePssBytes("Pss_Dirty:          400 kB\nRss:               7532 kB\nPss:                571 kB\n")
	require.True(t, ok)
	require.Equal(t, int64(571*1024), got)
}

// TestParsePssBytesRejectsAMissingOrMalformedLine keeps a rollup without a usable Pss
// from being priced at zero, which is what sends readProcCost to the statm fallback.
func TestParsePssBytesRejectsAMissingOrMalformedLine(t *testing.T) {
	for name, raw := range map[string]string{
		"no Pss line":  "Rss:               7532 kB\n",
		"no unit":      "Pss:                571\n",
		"wrong unit":   "Pss:                571 MB\n",
		"not a number": "Pss:                 many kB\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := parsePssBytes(raw)
			require.False(t, ok)
		})
	}
}

// TestParseStatmRSSBytesReadsTheSecondField pins which column resident size is.
// statm's first field is total program size and its second is resident — adjacent,
// both plausible, and the first is always the larger, so reading it would inflate
// every memory number this measurement reports.
func TestParseStatmRSSBytesReadsTheSecondField(t *testing.T) {
	got, ok := parseStatmRSSBytes("2451 1883 1204 12 0 176 0\n")
	require.True(t, ok)
	require.Equal(t, int64(1883)*int64(os.Getpagesize()), got)

	_, ok = parseStatmRSSBytes("2451\n")
	require.False(t, ok, "a single field cannot name a resident size")
}

// TestCountFDClassesSeparatesPtysFromPaneSlaves is the classification that makes the
// fanout countable: an attach client's pty master (/dev/ptmx, held by Atrium) is the
// per-session cost, while /dev/pts/N is the slave end a pane legitimately holds. A
// substring match on "pts" would count both and double the reported fanout.
func TestCountFDClassesSeparatesPtysFromPaneSlaves(t *testing.T) {
	dir := t.TempDir()
	targets := []string{
		"/dev/ptmx", "/dev/ptmx", "/dev/ptmx",
		"/dev/pts/7",
		"anon_inode:[eventpoll]",
		"anon_inode:[eventpoll]",
		"anon_inode:[pidfd]",
		"socket:[12345]",
		"/home/zvi/.atrium/state.json",
	}
	for i, target := range targets {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, strconv.Itoa(i))))
	}

	got, ok := countFDClassesIn(dir)
	require.True(t, ok)
	require.Equal(t, fdCounts{
		Ptmx:      3,
		EventPoll: 2,
		PidFD:     1,
		Socket:    1,
		Other:     2, // the pane slave and the state file
		Total:     len(targets),
	}, got)
}

// TestCountFDClassesAcceptsBothSpellingsOfThePtyMaster covers the container case: a
// devpts mounted with newinstance makes /dev/ptmx a symlink, so an fd on the master
// resolves to /dev/pts/ptmx instead. A classifier that knows only the first spelling
// reports a fleet holding zero ptys — "the fanout is free" from a broken instrument.
//
// The slave in the same table is what stops the fix being a loose "pts" match: it must
// still land in Other, because a pane legitimately holds one and counting it would
// double the reported fanout.
func TestCountFDClassesAcceptsBothSpellingsOfThePtyMaster(t *testing.T) {
	dir := t.TempDir()
	targets := []string{"/dev/ptmx", "/dev/pts/ptmx", "/dev/pts/7", "/dev/pts/113"}
	for i, target := range targets {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, strconv.Itoa(i))))
	}
	got, ok := countFDClassesIn(dir)
	require.True(t, ok)
	require.Equal(t, 2, got.Ptmx, "both spellings of the master")
	require.Equal(t, 2, got.Other, "both slaves stay out of the master count")
}

// TestReadProcCostMemorySources walks the three states the memory reading can be in,
// against a procfs built file by file.
//
// A real host cannot reach the last two: smaps_rollup always answers here, so the
// statm fallback and the priced-nothing case would ship measured by nothing. They are
// what decides whether a reported figure is a Pss or a resident size several times
// larger, and whether a zero means "holds no memory" or "would not say".
func TestReadProcCostMemorySources(t *testing.T) {
	const rollup = "Rss:               7532 kB\nPss:                571 kB\n"
	const statm = "2451 1883 1204 12 0 176 0\n"

	cases := []struct {
		name      string
		files     map[string]string
		wantPriv  int64
		wantIsPss bool
		wantKnown bool
	}{
		{
			name:      "smaps_rollup wins when present",
			files:     map[string]string{"smaps_rollup": rollup, "statm": statm},
			wantPriv:  571 * 1024,
			wantIsPss: true,
			wantKnown: true,
		},
		{
			name:      "statm is the fallback, and is not labelled a Pss",
			files:     map[string]string{"statm": statm},
			wantPriv:  1883 * int64(os.Getpagesize()),
			wantIsPss: false,
			wantKnown: true,
		},
		{
			name:      "neither file leaves the memory unknown, not zero",
			files:     nil,
			wantKnown: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"),
				[]byte(statLine("atrium", "700", "300", "0", "0")), 0o600))
			for name, body := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
			}

			cost, ok := readProcCostIn(dir)
			require.True(t, ok, "the CPU half stays readable however the memory half goes")
			require.Equal(t, 10*time.Second, cost.CPU)
			require.Equal(t, tc.wantKnown, cost.PrivateKnown)
			require.Equal(t, tc.wantIsPss, cost.PrivateIsPss)
			require.Equal(t, tc.wantPriv, cost.Private)
		})
	}
}

// TestReadProcCostFailsWhenStatIsUnreadable is the stronger contract beside it: no CPU
// reading means no sample at all, rather than a sample reporting zero cost.
func TestReadProcCostFailsWhenStatIsUnreadable(t *testing.T) {
	_, ok := readProcCostIn(t.TempDir())
	require.False(t, ok)
}

// TestCountFDClassesFailsOnAnUnreadableDirectory holds the reader to reporting "could
// not look" rather than an empty inventory. A process owned by another uid returns
// exactly this, and a zeroed fdCounts from it would read as a session holding no
// descriptors at all.
func TestCountFDClassesFailsOnAnUnreadableDirectory(t *testing.T) {
	_, ok := countFDClassesIn(filepath.Join(t.TempDir(), "does-not-exist"))
	require.False(t, ok)
}

// TestReadProcCostPricesThisProcess is the end-to-end read against real procfs: the
// test binary has burned some CPU and holds some memory by the time it runs, so a
// reader wired to the wrong fields or the wrong file shows up as a zero.
func TestReadProcCostPricesThisProcess(t *testing.T) {
	// Burn CPU deliberately rather than assuming the test binary has. USER_HZ is
	// 100, so a whole test run can finish inside one tick and a bare reading of a
	// just-started process is legitimately 0 — an assertion on it would be flaky in
	// the direction of "the measurement works", which is the worst direction.
	before, ok := readProcCost(os.Getpid())
	require.True(t, ok)
	burnCPU(80 * time.Millisecond)

	cost, ok := readProcCost(os.Getpid())
	require.True(t, ok)
	require.Greater(t, cost.CPU, before.CPU, "the burn must show up as own CPU")
	require.Positive(t, cost.Private, "the test binary holds memory")

	fds, ok := countFDClasses(os.Getpid())
	require.True(t, ok)
	require.Positive(t, fds.Total, "at least stdin/stdout/stderr")
}

// burnCPU spins for at least d of wall time doing arithmetic the compiler cannot
// elide, so the caller's own utime moves by more than procfs's 10ms granularity.
func burnCPU(d time.Duration) {
	deadline := time.Now().Add(d)
	sink := 0
	for time.Now().Before(deadline) {
		for i := 0; i < 1000; i++ {
			sink += i * i
		}
	}
	burnSink = sink
}

// burnSink keeps burnCPU's work observable so it is not optimised away.
var burnSink int
