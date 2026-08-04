package doctor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// now is the fixed instant the render fixtures below are aged against. Rendering is
// a pure function of OrphanResult, including its Now, so these assertions do not
// drift with wall-clock time.
var now = time.Date(2026, 8, 4, 0, 6, 0, 0, time.UTC)

func startedAgo(d time.Duration) time.Time { return now.Add(-d) }

// TestRenderOrphansSaysNoneWhenClean: "checked and found nothing" and "silently had
// nothing to say" must not look identical (RenderGates' rule). It matters most here,
// because the failure this section reports is invisible by construction — a user with
// no orphans and a user whose orphan scan quietly broke would otherwise read the same
// output.
func TestRenderOrphansSaysNoneWhenClean(t *testing.T) {
	out := RenderOrphans(OrphanResult{Supported: true, Now: now})
	require.Equal(t, "Orphaned tmux servers:\n  none\n", out)
}

// TestRenderOrphansHeadingNamesTmuxServers: doctor already uses "orphan" for a Claude
// login the account list no longer names, so this section must not claim the bare
// word.
func TestRenderOrphansHeadingNamesTmuxServers(t *testing.T) {
	out := RenderOrphans(OrphanResult{Supported: true, Now: now})
	require.True(t, strings.HasPrefix(out, "Orphaned tmux servers:\n"),
		"heading must name tmux servers, not bare orphans; got %q", out)
}

// TestRenderOrphansOffLinuxNamesWhatIsMissing. The stale-file list is portable, so
// the section is not wholly unavailable off Linux — only the process scan is, and
// saying "unavailable" flatly would misreport the half that still ran.
func TestRenderOrphansOffLinuxNamesWhatIsMissing(t *testing.T) {
	out := RenderOrphans(OrphanResult{Supported: false, SocketDir: "/tmp/tmux-1000", Now: now})
	require.Contains(t, out, "server scan unavailable")
	require.Contains(t, out, "Linux-only")
	require.Contains(t, out, "stale socket files: none in /tmp/tmux-1000",
		"the portable half of the check still ran and must still report")
}

// TestRenderOrphansUnreachableServerSaysNoTmuxCommandCanReachIt is the row that
// matters: for a class-(c) orphan there is no `tmux -S … kill-server` to print,
// because the path it was bound to now answers for someone else. Printing one anyway
// is how a report becomes an instruction to kill the live server.
func TestRenderOrphansUnreachableServerSaysNoTmuxCommandCanReachIt(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true,
		Now:       now,
		Servers: []tmux.OrphanServer{{
			PID: 1499239, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
			Reachable: false, ReachableKnown: true,
			Started:  startedAgo(14*time.Hour + 2*time.Minute),
			Children: []tmux.ChildProc{{PID: 2, Comm: "claude"}, {PID: 3, Comm: "claude"}},
		}},
	})

	require.Contains(t, out, "pid 1499239")
	require.Contains(t, out, "UNREACHABLE")
	require.Contains(t, out, "up 14h2m")
	require.Contains(t, out, "holds 2 processes (claude)")
	require.Contains(t, out, "atrium reap --kill")
	require.NotContains(t, out, "kill-server",
		"an unreachable server has no kill-server command that names it; printing one aims it "+
			"at whichever server answers that path now")
}

// TestRenderOrphansReachableServerPrintsTheExactCommand: class (b) is recoverable
// with existing tooling, and the remedy addresses it by absolute socket path. `-L`
// resolves against TMUX_TMPDIR and falls back to /tmp when that is empty or missing,
// so a printed `-L` command is one wrong environment away from the live fleet.
func TestRenderOrphansReachableServerPrintsTheExactCommand(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true,
		Now:       now,
		Servers: []tmux.OrphanServer{{
			PID: 2989219, Socket: "atrium-smoke", SocketPath: "/tmp/atr1/tmux-1000/atrium-smoke",
			Reachable: true, ReachableKnown: true,
			Started:  startedAgo(3 * time.Minute),
			Children: []tmux.ChildProc{{PID: 9, Comm: "claude"}},
		}},
	})

	require.Contains(t, out, "tmux -S /tmp/atr1/tmux-1000/atrium-smoke kill-server")
	require.NotContains(t, out, "tmux -L", "a remedy must never address a server by name")
	require.Contains(t, out, "holds 1 process (claude)")
	require.Contains(t, out, "up 3m")
}

// TestRenderOrphansUnknownReachabilityPromisesNoKill. With tmux unavailable nothing
// is proven — and the live server could not be excluded either, so these rows may
// well be the running fleet. The row has to say so, because the user reading it is
// deciding whether to reach for `reap --kill`.
func TestRenderOrphansUnknownReachabilityPromisesNoKill(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true,
		Now:       now,
		Servers: []tmux.OrphanServer{{
			PID: 31, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
			ReachableKnown: false, Started: startedAgo(5 * time.Minute),
		}},
	})

	require.Contains(t, out, "reachability unknown")
	require.Contains(t, out, "never kills them")
	require.Contains(t, out, "holds nothing")
	require.NotContains(t, out, "UNREACHABLE",
		"an unrunnable probe is not a finding; only a probe that answered may say unreachable")
}

// TestRenderOrphansStaleSocketsNameExactPathsNotAGlob.
//
// The live socket lives in this same directory. A `find … -name 'atrium-*' -delete`
// re-matches when the user runs it, so it can take a socket bound after the report —
// including the live one. That exact shape wiped thirteen sessions in #584. Naming
// the verified paths cannot.
func TestRenderOrphansStaleSocketsNameExactPathsNotAGlob(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true,
		Now:       now,
		SocketDir: "/tmp/tmux-1000",
		Stale: []tmux.StaleSocket{
			{Path: "/tmp/tmux-1000/atrium-barstyle-test-4471"},
			{Path: "/tmp/tmux-1000/atrium-precheck-991-1"},
		},
	})

	require.Contains(t, out, "stale socket files: 2 in /tmp/tmux-1000")
	require.Contains(t, out,
		"rm -- /tmp/tmux-1000/atrium-barstyle-test-4471 /tmp/tmux-1000/atrium-precheck-991-1")
	require.NotContains(t, out, "find ")
	require.NotContains(t, out, "*", "a remedy over this directory must not carry a glob")
}

// TestRenderOrphansMarksADeletedWorkingDirectory: the signature of a run whose temp
// root was cleaned up around a server that outlived it — the #547 incident itself.
func TestRenderOrphansMarksADeletedWorkingDirectory(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true, Now: now,
		Servers: []tmux.OrphanServer{{
			PID: 7, Socket: "atrium", SocketPath: "/tmp/gone/tmux-1000/atrium",
			ReachableKnown: true, CWDDeleted: true, Started: startedAgo(time.Hour),
		}},
	})
	require.Contains(t, out, "working directory has been deleted")
}

// TestCheckOrphansAssemblesBothHalves covers the wiring, which the render tests
// cannot see: a check that returned an empty result would render a perfectly good
// "none".
func TestCheckOrphansAssemblesBothHalves(t *testing.T) {
	origServers, origStale := orphanScan, staleScan
	t.Cleanup(func() { orphanScan, staleScan = origServers, origStale })

	orphanScan = func(context.Context) ([]tmux.OrphanServer, bool) {
		return []tmux.OrphanServer{{PID: 42, Socket: "atrium"}}, true
	}
	staleScan = func(context.Context) ([]tmux.StaleSocket, string) {
		return []tmux.StaleSocket{{Path: "/tmp/tmux-1000/atrium-old"}}, "/tmp/tmux-1000"
	}

	got := CheckOrphans(t.Context())
	require.True(t, got.Supported)
	require.Len(t, got.Servers, 1)
	require.Equal(t, 42, got.Servers[0].PID)
	require.Len(t, got.Stale, 1)
	require.Equal(t, "/tmp/tmux-1000", got.SocketDir)
	require.False(t, got.Now.IsZero(), "Now must be stamped, or every age renders as time since the epoch")
}

func TestHumanAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Hour, "0s"}, // clock skew must not print a negative uptime
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{14*time.Hour + 2*time.Minute, "14h2m"},
		{50 * time.Hour, "2d2h"},
	} {
		require.Equal(t, tc.want, HumanAge(tc.d), "HumanAge(%s)", tc.d)
	}
}
