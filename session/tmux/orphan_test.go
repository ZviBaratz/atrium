package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOwnsSocketNameAcceptsBothBrandsUnconditionally guards the ownership predicate
// — the only thing that decides whether a process is in scope for the reaper.
//
// It keys on both brands regardless of which one this install resolves to.
// config.RuntimeName() answers from the *reaper's own* HOME, so keying on it alone
// would make a legacy ~/.claude-squad install blind to "atrium" orphans and a fresh
// install blind to "claudesquad" ones — and an orphan is precisely a server started
// by some other run, under some other HOME.
//
// The negative cases are the load-bearing half: a name is ours because it matches,
// never because it failed to look like someone else's (#584).
func TestOwnsSocketNameAcceptsBothBrandsUnconditionally(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"atrium", true},
		{"claudesquad", true},
		// The suffixed forms Atrium actually binds: the managed-config probe (#589)
		// and the ad-hoc verification sockets that accumulate under the socket dir.
		{"atrium-precheck-991-1", true},
		{"atrium-barstyle-test-4471", true},
		{"claudesquad-precheck-12-0", true},

		// Not ours. "atriumfoo" is the one a HasPrefix without the separator would
		// wrongly claim, and "default" is tmux's own — killing that is killing the
		// user's unrelated tmux.
		{"atriumfoo", false},
		{"myatrium", false},
		{"claudesquadron", false},
		{"default", false},
		{"tmux", false},
		{"", false},
	} {
		t.Run(tc.base, func(t *testing.T) {
			require.Equal(t, tc.want, ownsSocketName(tc.base))
		})
	}
}

// stubSocketOwner swaps the socket-probe seam for the duration of a test. It is
// package-level shared state, so no test using it may run in parallel.
func stubSocketOwner(t *testing.T, owner func(context.Context, string) (int, bool)) {
	t.Helper()
	orig := socketOwner
	t.Cleanup(func() { socketOwner = orig })
	socketOwner = owner
}

// stubAmbientPID and stubScanCandidates swap the other two seams, with the same
// no-parallel rule: they are package-level shared state.
func stubAmbientPID(t *testing.T, probe func(context.Context) (int, bool)) {
	t.Helper()
	orig := ambientPID
	t.Cleanup(func() { ambientPID = orig })
	ambientPID = probe
}

func stubSocketPathQuery(t *testing.T, query func(context.Context) (string, bool)) {
	t.Helper()
	orig := socketPathQuery
	t.Cleanup(func() { socketPathQuery = orig })
	socketPathQuery = query
}

func stubScanCandidates(t *testing.T, scan func(context.Context) ([]candidate, ScanGaps)) {
	t.Helper()
	orig := scanCandidates
	t.Cleanup(func() { scanCandidates = orig })
	scanCandidates = scan
}

// unreachableOwner is a socket probe that ran and found nothing listening.
func unreachableOwner(context.Context, string) (int, bool) { return 0, true }

func TestAssembleServersKeepsOnlyOwnedSockets(t *testing.T) {
	stubSocketOwner(t, unreachableOwner)

	got := assembleServers(context.Background(), []candidate{
		{PID: 10, SocketPath: "/tmp/tmux-1000/atrium"},
		{PID: 11, SocketPath: "/tmp/x/atrium-precheck-9-0"},
		{PID: 12, SocketPath: "/tmp/tmux-1000/default"},   // the user's own tmux
		{PID: 13, SocketPath: "/tmp/tmux-1001/atriumfoo"}, // near-miss, not ours
	}, 0, false)

	require.Len(t, got, 2)
	require.Equal(t, 10, got[0].PID)
	require.Equal(t, "atrium", got[0].Socket)
	require.Equal(t, 11, got[1].PID)
	require.Equal(t, "atrium-precheck-9-0", got[1].Socket)
}

// TestAssembleServersExcludesTheLiveServer: the server this Atrium is running on is
// not an orphan. Excluding it by pid rather than by socket name is what keeps the
// exclusion exact — a restarted Atrium re-binds the same path, so a name match would
// exclude the wrong process.
func TestAssembleServersExcludesTheLiveServer(t *testing.T) {
	stubSocketOwner(t, unreachableOwner)

	cands := []candidate{
		{PID: 10, SocketPath: "/tmp/tmux-1000/atrium"},
		{PID: 20, SocketPath: "/tmp/atr123/tmux-1000/atrium"},
	}

	got := assembleServers(context.Background(), cands, 10, true)
	require.Len(t, got, 1)
	require.Equal(t, 20, got[0].PID, "the ambient live server must not be reported as an orphan")

	// When the ambient lookup could not answer, nothing can be excluded by pid, so both
	// rows come back — including, potentially, this Atrium's own server. That is not
	// safe on its own: it is safe because such a server answers its own socket and
	// arrives Reachable, which the default target set excludes, and because
	// ScanGaps.LiveServerUnknown then blocks `--all` and suppresses the report's
	// kill-server remedy. This assertion is the "both rows" half; the guards are
	// asserted in TestReapKillAllRefusesWhenTheLiveServerIsUnknown and the doctor
	// render tests.
	got = assembleServers(context.Background(), cands, 0, false)
	require.Len(t, got, 2)
}

// TestAssembleServersRefusesAProcessWithNoListeningSocket is the regression guard
// for a live misclassification found while exercising the scan on a real host.
//
// A `tmux: client` — the attach proxy for a live Atrium session — passes the comm
// prefilter and has a socket fd, but that fd is a *connected* endpoint, not a
// listening one, so it has no bound path. 14 of the 15 processes reaching this point
// on that host were exactly that. An earlier design read the socket name out of
// `-L <name>` in argv whenever the path was missing, which claimed every one of
// them: live attach clients, listed as orphaned servers and offered up for reaping.
//
// A process that is not listening is not a server. That is positive proof, and it is
// also what keeps argv — which carries injected GH_TOKEN values — out of the scan
// entirely rather than by discipline.
//
// What this pins is the property, not a particular line: deleting assembleServers'
// explicit empty-path check leaves this green, because filepath.Base("") is "." and
// that fails the ownership predicate anyway. Re-introducing an argv fallback is what
// it would actually catch, which is the regression worth catching.
func TestAssembleServersRefusesAProcessWithNoListeningSocket(t *testing.T) {
	stubSocketOwner(t, func(_ context.Context, path string) (int, bool) {
		t.Fatalf("a candidate with no bound socket was probed at %q", path)
		return 0, false
	})

	got := assembleServers(context.Background(), []candidate{
		{PID: 10, SocketPath: ""},
		{PID: 20, SocketPath: ""},
	}, 0, false)

	require.Empty(t, got, "processes with no listening socket are not tmux servers")
}

// TestAssembleServersReachabilityIsAnIdentityTest: a server is reachable only when
// the socket answers *with its own pid*.
//
// os.Stat would answer the wrong question — "is there a file here now" — and a
// restarted Atrium re-binds /tmp/tmux-1000/atrium, so a stale orphan carrying that
// path would stat true, be classified reachable, and have the remedy printed for it
// aimed at the new, live server.
//
// This is not hypothetical. Observed on the development host: two tmux servers both
// reported /tmp/tmux-1000/atrium as their bound path in /proc/net/unix — one from
// 10:04 holding 13 `claude` children, one from 15:56 — and only the newer answered
// the socket. `os.Stat` would have called the older one reachable and printed
// `tmux -S /tmp/tmux-1000/atrium kill-server` for it, which kills the newer.
// pid 10 below is that older server.
func TestAssembleServersReachabilityIsAnIdentityTest(t *testing.T) {
	stubSocketOwner(t, func(_ context.Context, path string) (int, bool) {
		switch path {
		case "/tmp/tmux-1000/atrium":
			return 99, true // a *different* process answers this path now
		case "/tmp/atr1/tmux-1000/atrium-live":
			return 20, true
		}
		return 0, true
	})

	got := assembleServers(context.Background(), []candidate{
		{PID: 10, SocketPath: "/tmp/tmux-1000/atrium"},
		{PID: 20, SocketPath: "/tmp/atr1/tmux-1000/atrium-live"},
		{PID: 30, SocketPath: "/tmp/gone/tmux-1000/atrium-dead"},
	}, 0, false)

	require.Len(t, got, 3)
	require.False(t, got[0].Reachable, "a path now owned by another pid is not this server's")
	require.True(t, got[0].ReachableKnown)
	require.True(t, got[1].Reachable)
	require.True(t, got[2].ReachableKnown)
	require.False(t, got[2].Reachable, "nothing answers the deleted socket — the class-(c) orphan")
}

// TestAssembleServersReachabilityIsUnknownWhenTheProbeCannotRun is the guard that
// keeps a missing tmux from turning the live fleet into kill candidates.
//
// Reachability is computed by running tmux. With tmux off PATH the probe cannot
// answer — and neither can the ambient lookup that excludes the live server, so
// every live session's server would arrive here looking exactly like an unreachable
// orphan. Reporting "unknown" rather than "unreachable" is what lets `reap --kill`
// refuse to touch them: positive proof only.
func TestAssembleServersReachabilityIsUnknownWhenTheProbeCannotRun(t *testing.T) {
	stubSocketOwner(t, func(context.Context, string) (int, bool) {
		return 0, false // tmux is not on PATH
	})

	got := assembleServers(context.Background(), []candidate{
		{PID: 10, SocketPath: "/tmp/tmux-1000/atrium"},
	}, 0, false)

	require.Len(t, got, 1)
	require.False(t, got[0].ReachableKnown, "an unrunnable probe must not read as proof of anything")
	require.False(t, got[0].Reachable)
}

// TestAssembleServersCarriesTheFieldsTheReaperArmsWith: start time is the PID-reuse
// guard's captured value and children are what the confirmation prompt names, so
// both have to survive assembly — for the children too, whose pids are staler than
// the server's and therefore need the same guard.
func TestAssembleServersCarriesTheFieldsTheReaperArmsWith(t *testing.T) {
	stubSocketOwner(t, unreachableOwner)

	started := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	kidStarted := started.Add(time.Second)
	got := assembleServers(context.Background(), []candidate{{
		PID:        10,
		SocketPath: "/tmp/gone/tmux-1000/atrium",
		Started:    started,
		CWDDeleted: true,
		Children:   []ChildProc{{PID: 11, Comm: "claude", Started: kidStarted}},
	}}, 0, false)

	require.Len(t, got, 1)
	require.Equal(t, started, got[0].Started)
	require.True(t, got[0].CWDDeleted)
	require.Equal(t, []ChildProc{{PID: 11, Comm: "claude", Started: kidStarted}}, got[0].Children)
}

// TestScanGapsAny pins the predicate the report and the reaper both branch on.
//
// It is asserted directly rather than by mutating the refusal in cli_reap.go, per the
// project rule about destructive guards: the way to show a kill guard is load-bearing
// is to extract its predicate and test that, not to delete the guard and watch what
// happens. Any() is that predicate, and "no gaps" is the only value that lets a caller
// read an empty result as proof.
func TestScanGapsAny(t *testing.T) {
	require.False(t, ScanGaps{}.Any(), "a complete scan must not claim a gap")
	require.True(t, ScanGaps{SocketTableUnread: true}.Any())
	require.True(t, ScanGaps{ProcTableTruncated: true}.Any())
	require.True(t, ScanGaps{SocketTableUnread: true, ProcTableTruncated: true}.Any())

	// LiveServerUnknown is excluded on purpose, and this is the assertion that says so
	// out loud. Folding it in would read as a tightening but would refuse every kill on
	// a host where tmux merely could not be probed — the over-refusal that blanked the
	// report and stopped the reaper once already. Its consequences are handled where
	// they apply: `--all`, and the report's remedy line.
	require.False(t, ScanGaps{LiveServerUnknown: true}.Any(),
		"an unidentified live server does not make the inventory incomplete; "+
			"it must not block a kill the default target set already proves safe")
	require.True(t, ScanGaps{LiveServerUnknown: true, ProcTableTruncated: true}.Any(),
		"a real inventory gap alongside it must still count")
}

// TestClassifyPIDProbeSeparatesAnEmptySocketFromAnUnaskedQuestion is the rule both tmux
// probes now share.
//
// The distinction is the whole point: a non-zero exit is tmux *reporting* that nothing
// is on the socket, which is evidence and safe to act on, while tmux being absent or
// out of budget is no answer at all. ambientServerPID returned false for both, so an
// unaskable probe was indistinguishable from an empty fleet — and an empty fleet has
// nothing to exclude, whereas an unanswered probe means the live server may be in the
// candidate list.
func TestClassifyPIDProbeSeparatesAnEmptySocketFromAnUnaskedQuestion(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name      string
		ctx       context.Context
		out       string
		err       error
		wantPID   int
		wantKnown bool
	}{
		{"a server answered", context.Background(), "1499239\n", nil, 1499239, true},
		{"tmux said no server is there", context.Background(), "", &exec.ExitError{}, 0, true},
		{"tmux could not be run", context.Background(), "", errors.New("exec: \"tmux\": not found"), 0, false},
		{"unparseable answer", context.Background(), "not a pid", nil, 0, false},
		// The deadline check must come first: a process killed by the context also
		// surfaces as an ExitError, which would otherwise be read as evidence.
		{"budget spent", cancelled, "", &exec.ExitError{}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid, known := classifyPIDProbe(tc.ctx, []byte(tc.out), tc.err)
			require.Equal(t, tc.wantKnown, known)
			require.Equal(t, tc.wantPID, pid)
		})
	}
}

// TestScanServersReportsAnUnidentifiedLiveServer: the flag has to be set by the scan,
// not merely exist. With the ambient probe unable to answer, nothing was excluded by
// pid, and every consumer downstream keys on this one field to know that.
func TestScanServersReportsAnUnidentifiedLiveServer(t *testing.T) {
	if !orphanScanSupported {
		t.Skip("ScanServers returns early off Linux")
	}
	stubScanCandidates(t, func(context.Context) ([]candidate, ScanGaps) { return nil, ScanGaps{} })

	stubAmbientPID(t, func(context.Context) (int, bool) { return 0, false })
	_, _, gaps := ScanServers(context.Background())
	require.True(t, gaps.LiveServerUnknown, "an unanswered ambient probe must be reported")
	require.False(t, gaps.Any(), "but it is not an inventory gap")

	// The determined empty fleet: tmux answered that nothing is on the socket. There is
	// no live server to protect, so this must not raise the flag.
	stubAmbientPID(t, func(context.Context) (int, bool) { return 0, true })
	_, _, gaps = ScanServers(context.Background())
	require.False(t, gaps.LiveServerUnknown,
		"an empty fleet is a determined answer, not an unidentified live server")
}

// TestScanServersWalksProcBeforeAnyTmuxProbe pins the order the gap reporting depends
// on.
//
// A tmux probe against a wedged server spends the entire scan budget; the /proc walk
// spends milliseconds. With the probe first, the walk found ctx.Err() already set on
// its first entry and reported a truncated table — so a wedged live server blanked the
// report and made `reap --kill` refuse, which is precisely the host that needed
// reaping. Order is asserted rather than timed because the property is structural: no
// budget is large enough to make probing-first safe.
func TestScanServersWalksProcBeforeAnyTmuxProbe(t *testing.T) {
	if !orphanScanSupported {
		t.Skip("ScanServers returns early off Linux, so there is no order to assert")
	}
	var order []string
	stubScanCandidates(t, func(context.Context) ([]candidate, ScanGaps) {
		order = append(order, "walk /proc")
		return nil, ScanGaps{}
	})
	stubAmbientPID(t, func(context.Context) (int, bool) {
		order = append(order, "probe tmux")
		return 0, false
	})

	_, _, gaps := ScanServers(context.Background())
	require.Equal(t, []string{"walk /proc", "probe tmux"}, order,
		"the /proc walk must run before anything that can hang, or a hung probe fabricates a gap")
	require.False(t, gaps.Any())
}

// TestEveryTmuxProbeGetsItsOwnBudget: a probe must never inherit the whole scan's
// deadline, or the first wedged socket spends every other probe's time — and, before
// the walk was reordered, the walk's.
//
// The assertion is on the deadline handed *to* the probe, because that is the only
// observable difference at this seam: both probes ran with the caller's context
// verbatim before, so a parent with no deadline handed them none at all.
func TestEveryTmuxProbeGetsItsOwnBudget(t *testing.T) {
	// No deadline on the parent, which is the case that separates the two behaviours.
	parent := context.Background()

	var ownerDeadline, ambientDeadline, socketPathDeadline time.Time
	var ownerOK, ambientOK, socketPathOK bool
	stubSocketOwner(t, func(ctx context.Context, _ string) (int, bool) {
		ownerDeadline, ownerOK = ctx.Deadline()
		return 0, true
	})
	stubAmbientPID(t, func(ctx context.Context) (int, bool) {
		ambientDeadline, ambientOK = ctx.Deadline()
		return 0, false
	})
	stubSocketPathQuery(t, func(ctx context.Context) (string, bool) {
		socketPathDeadline, socketPathOK = ctx.Deadline()
		return "", false
	})

	// The socket-path probe is bounded for a consequence the other two do not have: it
	// runs before every per-file owner probe of the same scan, so unbounded it spends the
	// whole shared budget against a wedged server and leaves each of those probes to fail
	// on an expired context — reporting a directory of unclassifiable files that nothing
	// was wrong with. "Every" in this test's name is the claim, and it was false for this
	// probe when it was extracted.
	SocketDir(parent)
	require.True(t, socketPathOK,
		"the socket-path probe must be bounded even when the caller's context is not")
	require.LessOrEqual(t, time.Until(socketPathDeadline), orphanProbeBudget)

	probeAmbient(parent)
	require.True(t, ambientOK, "the ambient probe must be bounded even when the caller's context is not")
	require.LessOrEqual(t, time.Until(ambientDeadline), orphanProbeBudget)

	assembleServers(parent, []candidate{{PID: 10, SocketPath: "/tmp/tmux-1000/atrium"}}, 0, false)
	require.True(t, ownerOK, "an owner probe must be bounded even when the caller's context is not")
	require.LessOrEqual(t, time.Until(ownerDeadline), orphanProbeBudget)
}

// TestSocketDirNamesWhetherAServerAnsweredIt covers both branches of the provenance
// #598 added, because each one is an assignment nothing else pins: a fromServer
// hardcoded either way passes half of this test and fails the other half.
//
// It matters because fromServer decides what "stale socket files: none in <dir>" is a
// statement *about*. With a server to ask, the directory is one a socket is demonstrably
// bound in. Without one it is a reconstruction of tmux's layout — where tmux *would*
// bind — so the report can be entirely true about a directory no server has ever used.
func TestSocketDirNamesWhetherAServerAnsweredIt(t *testing.T) {
	t.Run("the live server answers", func(t *testing.T) {
		stubSocketPathQuery(t, func(context.Context) (string, bool) {
			return "/tmp/atr9/tmux-1000/atrium", true
		})

		dir, fromServer := SocketDir(t.Context())
		require.Equal(t, "/tmp/atr9/tmux-1000", dir,
			"the server's own answer for where its socket is, not a guess at tmux's layout")
		require.True(t, fromServer)
	})

	t.Run("no server to ask", func(t *testing.T) {
		stubSocketPathQuery(t, func(context.Context) (string, bool) { return "", false })
		root := t.TempDir()
		t.Setenv("TMUX_TMPDIR", root)

		dir, fromServer := SocketDir(t.Context())
		require.Equal(t, filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid())), dir)
		require.False(t, fromServer,
			"a reconstruction is not the server's answer, and the report has to be able to say so")
	})

	// tmux reads a TMUX_TMPDIR naming a missing directory exactly as it reads an empty
	// one, and hardcodes /tmp for both. os.TempDir() would honour $TMPDIR instead —
	// which on macOS is a per-user /var/folders/… path tmux never binds in, so the whole
	// section would report about a directory holding no sockets at all. SocketDir's
	// comment claims this rule and nothing asserted it.
	for _, tc := range []struct{ name, root string }{
		{"empty", ""},
		{"a directory that is not there", "/nonexistent-tmux-tmpdir-598"},
	} {
		t.Run("TMUX_TMPDIR is "+tc.name, func(t *testing.T) {
			stubSocketPathQuery(t, func(context.Context) (string, bool) { return "", false })
			t.Setenv("TMUX_TMPDIR", tc.root)

			dir, fromServer := SocketDir(t.Context())
			require.Equal(t, filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid())), dir,
				"literally /tmp, which is what tmux itself falls back to")
			require.False(t, fromServer)
		})
	}
}
