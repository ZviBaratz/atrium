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

// TestRenderOrphansNeverSaysNoneOnABlindScan is the follow-up to the rule above, for
// the case that rule did not originally cover.
//
// A clean host and a host whose scan could not read /proc/net/unix produced byte-
// identical output — "none" — because an unreadable socket table dropped every
// candidate. That is the same conflation RenderGates forbids, arrived at from the other
// direction: not an empty section, but a positive claim of health manufactured out of
// having seen nothing. Both gaps are asserted to break the "none" fast path, since each
// reaches it by a different route.
//
// The "both" case names both sentences rather than either one: two gaps have two
// different consequences and two remedies, so a renderer that printed only the first it
// matched would drop half of what the user has to act on — and asserting one substring
// there would not notice.
func TestRenderOrphansNeverSaysNoneOnABlindScan(t *testing.T) {
	const (
		blind      = "/proc/net/unix could not be read"
		incomplete = "did not finish"
	)
	for _, tc := range []struct {
		name string
		gaps tmux.ScanGaps
		want []string
	}{
		{"socket table unreadable", tmux.ScanGaps{SocketTableUnread: true}, []string{blind}},
		{"proc walk truncated", tmux.ScanGaps{ProcTableTruncated: true}, []string{incomplete}},
		{"both", tmux.ScanGaps{SocketTableUnread: true, ProcTableTruncated: true}, []string{blind, incomplete}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderOrphans(OrphanResult{Supported: true, Gaps: tc.gaps, Now: now})
			require.NotContains(t, out, "  none\n",
				"a scan that could not see must never render as a clean host: %q", out)
			for _, want := range tc.want {
				require.Contains(t, out, want)
			}
			require.Contains(t, out, "refuses to act",
				"the row must say the reap will decline, because it does")
		})
	}
}

// TestRenderOrphansStillListsRowsFoundDespiteAGap: a truncated /proc walk understates
// what is out there, but what it did find is still real. The gap note is printed in
// addition to those rows, not instead of them — reporting the gap by suppressing the
// evidence would trade one blind spot for another.
func TestRenderOrphansStillListsRowsFoundDespiteAGap(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true,
		Now:       now,
		Gaps:      tmux.ScanGaps{ProcTableTruncated: true},
		Servers: []tmux.OrphanServer{{
			PID: 1499239, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
			Reachable: false, ReachableKnown: true, Started: startedAgo(time.Hour),
		}},
	})
	require.Contains(t, out, "did not finish", "the gap must be stated")
	require.Contains(t, out, "pid 1499239", "and the server it did find must still be listed")
	require.Contains(t, out, "UNREACHABLE")
}

// TestRenderOrphansWithholdsAKillServerItCannotVouchFor is the report-side half of the
// unidentified-live-server guard.
//
// A reachable server's remedy is `tmux -S <path> kill-server`, naming an exact path.
// That is safe only because the live server was excluded by pid before classification —
// and when the ambient probe cannot answer, it was not. The live server answers its own
// socket, so it arrives here Reachable, and the report would hand the user a verified
// command for killing their own fleet. This is the #584 shape reached through the report
// rather than through a glob, which is why the assertion is on the *absence* of the
// command and not merely on the presence of a warning: a caution printed beside a
// working kill-server is still a working kill-server.
func TestRenderOrphansWithholdsAKillServerItCannotVouchFor(t *testing.T) {
	reachable := []tmux.OrphanServer{{
		PID: 1952486, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
		Reachable: true, ReachableKnown: true, Started: startedAgo(time.Hour),
	}}

	unknown := RenderOrphans(OrphanResult{
		Supported: true, Now: now, Servers: reachable,
		Gaps: tmux.ScanGaps{LiveServerUnknown: true},
	})
	require.NotContains(t, unknown, "kill-server",
		"with the live server unidentified this row may be the live server; no kill command may be printed: %q", unknown)
	require.Contains(t, unknown, "pid 1952486", "the row itself must still be reported")
	require.Contains(t, unknown, "could not be identified")

	// The control: with the live server identified, the exclusion happened and the
	// remedy is exactly what makes the row useful. A fix that simply stopped printing
	// kill-server would pass the assertions above.
	known := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: reachable})
	require.Contains(t, known, "tmux -S /tmp/tmux-1000/atrium kill-server",
		"an identified live server means this row is provably not it, and the remedy is the point of the row")
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

	orphanScan = func(context.Context) ([]tmux.OrphanServer, bool, tmux.ScanGaps) {
		return []tmux.OrphanServer{{PID: 42, Socket: "atrium"}}, true,
			tmux.ScanGaps{ProcTableTruncated: true}
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
	// Carried, not dropped: the gap is the scan's own statement about how much of the
	// host it saw, and CheckOrphans is the only thing between the scan and the renderer.
	require.True(t, got.Gaps.ProcTableTruncated, "the scan's gaps must reach the result")
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
