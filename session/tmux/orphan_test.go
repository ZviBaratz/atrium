package tmux

import (
	"context"
	"net"
	"os"
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

	// When the ambient lookup could not answer, there is nothing to exclude — the
	// no-server case. Both rows come back, and the reachability guard below is what
	// keeps that from being dangerous.
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

// TestStaleSocketsInReportsOnlyUnansweredAtriumSockets covers the class-(a) list:
// socket files tmux left behind when their servers died.
//
// The live socket sits in the same directory, so "a file is here" can never be the
// test — a socket that answers belongs to a running server, and reporting it would
// put the live fleet on a list headed "remove these". Everything not positively
// proven dead stays off the list.
func TestStaleSocketsInReportsOnlyUnansweredAtriumSockets(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"atrium", "atrium-precheck-9-0", "atrium-unprobeable", "default"} {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "unix", filepath.Join(dir, name))
		require.NoError(t, err)
		// Keep the file after Close; a tmux server's socket outlives its server, and
		// that is exactly the artifact under test.
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		require.NoError(t, ln.Close())
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atrium-notasocket"), nil, 0o600))

	stubSocketOwner(t, func(_ context.Context, path string) (int, bool) {
		switch filepath.Base(path) {
		case "atrium":
			return 4242, true // the live server answers here
		case "atrium-unprobeable":
			return 0, false // tmux could not run
		}
		return 0, true // probed, nothing there
	})

	stale := staleSocketsIn(t.Context(), dir)
	require.Len(t, stale, 1)
	require.Equal(t, filepath.Join(dir, "atrium-precheck-9-0"), stale[0].Path,
		"only a socket positively proven to have no server behind it may be listed")
	require.False(t, stale[0].ModTime.IsZero())
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
