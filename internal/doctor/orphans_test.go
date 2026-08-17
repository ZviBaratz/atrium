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
	require.Contains(t, unknown, "check that tmux runs and that Atrium's own socket opens",
		"a bare re-run is a loop on the #730 cause, which answers the same way every time; "+
			"the check that can actually clear it has to ship with it")

	// The control: with the live server identified, the exclusion happened and the
	// remedy is exactly what makes the row useful. A fix that simply stopped printing
	// kill-server would pass the assertions above.
	known := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: reachable})
	require.Contains(t, known, "tmux -S /tmp/tmux-1000/atrium kill-server",
		"an identified live server means this row is provably not it, and the remedy is the point of the row")
	require.NotContains(t, known, "caution:",
		"an identified live server needs no hedge; the row is proven not to be the fleet")
}

// TestRenderOrphansCautionsAKillServerItCannotVouchFor is the report side of #603, and it
// deliberately reaches the opposite conclusion from the test above.
//
// There the ambient probe was never answered, so which server is the fleet is still an open
// question and a re-run is the move that may close it — which is why that row prints no
// command. Here the probe ran and answered "nothing on the socket I asked about", while this
// row answers its own socket by absolute path: the probe looked somewhere else (another
// TMUX_TMPDIR, the other brand, a config that would not parse), and re-running it asks the
// same wrong socket again. tmux demonstrably works, so the user can look at the server with
// `tmux -S <path> ls` before stopping it. Withholding the command would leave a verified
// remedy unnamed and print advice that cannot help, so the row keeps its command and loses
// the claim it used to carry with it. The refusal that needs no per-row judgement lives in
// `reap --kill --all`.
func TestRenderOrphansCautionsAKillServerItCannotVouchFor(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true, Now: now,
		Servers: []tmux.OrphanServer{{
			PID: 1952486, Socket: "atrium", SocketPath: "/tmp/atr-other/tmux-1000/atrium",
			Reachable: true, ReachableKnown: true, Started: startedAgo(time.Hour),
		}},
		Gaps: tmux.ScanGaps{EmptyFleetUnproven: true},
	})

	require.Contains(t, out, "pid 1952486", "the row itself must still be reported")
	require.Contains(t, out, "tmux -S /tmp/atr-other/tmux-1000/atrium kill-server",
		"the command is verified and stays: this server answers, and nothing else can stop it by name")
	require.Contains(t, out, "caution:",
		"but the claim that it is not this Atrium's own fleet has to be withdrawn beside it")
	require.Contains(t, out, "another\n        TMUX_TMPDIR or brand",
		"and the caution has to name the reason, which is where the probe looked")
	require.NotContains(t, out, "Re-run first",
		"re-running asks the same wrong socket; that advice belongs to the unanswered-probe case")
}

// TestRenderOrphansKeepsTheHedgeOffAProbeSocket: the caution says a row may be a live fleet,
// and for a `-precheck-` socket that is a claim the scan can already disprove — no Atrium
// addresses its own server by a suffixed name, so no ambient probe was asking about this one.
//
// Both rows are rendered from one result, because the flag is a property of the scan rather
// than of a row: the bare-brand row is why it is set, and the probe row must not inherit its
// hedge. Asserting on one row alone would pass with the branch keyed on the flag only.
func TestRenderOrphansKeepsTheHedgeOffAProbeSocket(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true, Now: now,
		Servers: []tmux.OrphanServer{
			{
				PID: 7777, Socket: "atrium-precheck-991-1",
				SocketPath: "/tmp/tmux-1000/atrium-precheck-991-1",
				Reachable:  true, ReachableKnown: true, Started: startedAgo(time.Minute),
			},
			{
				PID: 1952486, Socket: "atrium", SocketPath: "/tmp/atr-other/tmux-1000/atrium",
				Reachable: true, ReachableKnown: true, Started: startedAgo(time.Hour),
			},
		},
		Gaps: tmux.ScanGaps{EmptyFleetUnproven: true},
	})

	require.Contains(t, out, "tmux -S /tmp/tmux-1000/atrium-precheck-991-1 kill-server",
		"the probe socket keeps its plain remedy")
	require.Equal(t, 1, strings.Count(out, "caution:"),
		"exactly one row may carry the hedge — the bare-brand one that raised the flag: %q", out)
	require.Contains(t, out, "tmux -S /tmp/atr-other/tmux-1000/atrium kill-server",
		"and that row keeps its command too, cautioned")
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
	out := RenderOrphans(OrphanResult{
		Supported: false, SocketDir: "/tmp/tmux-1000", SocketDirFromServer: true, Now: now,
	})
	require.Contains(t, out, "server scan unavailable")
	require.Contains(t, out, "Linux-only")
	require.Contains(t, out, "stale socket files: none in /tmp/tmux-1000",
		"the portable half of the check still ran and must still report")
}

// TestRenderOrphansNeverSaysNoneOnAStaleScanThatEstablishedNothing is #598: the same
// conflation TestRenderOrphansNeverSaysNoneOnABlindScan fixed for servers, in the stale
// half beside it.
//
// A file reaches the list only when a probe positively answered that nothing holds it,
// so a directory that could not be listed and a directory whose every probe failed both
// produce the empty list a genuinely clean directory produces — and the section printed
// "none in <dir>" off it, having established nothing at all.
//
// The result here is otherwise empty on purpose, because that is where the trap is:
// RenderOrphans' switch has a `len(Servers) == 0 && len(Stale) == 0` case that writes
// "none" and *returns* before renderStaleSockets runs. A gap wired in below it renders
// as nothing whatsoever, so this asserts on the one input that reaches that case.
//
// It also asserts what must *not* appear. Unlike the server inventory, nothing acts on
// this list — reap prints it and the remedy is an `rm --` line the user runs — so
// borrowing the server gaps' "refuses to act" line would promise a refusal that never
// happens, in the renderer whose whole subject is claims that are not true.
func TestRenderOrphansNeverSaysNoneOnAStaleScanThatEstablishedNothing(t *testing.T) {
	const (
		unlistable = "could not be listed"
		unprobed   = "could not be classified"
	)
	for _, tc := range []struct {
		name string
		gaps tmux.StaleGaps
		want []string
	}{
		{"directory unlistable", tmux.StaleGaps{DirUnread: true}, []string{unlistable}},
		{"every probe failed", tmux.StaleGaps{Unprobed: 3}, []string{unprobed}},
		{"both", tmux.StaleGaps{DirUnread: true, Unprobed: 3}, []string{unlistable, unprobed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderOrphans(OrphanResult{
				Supported: true, SocketDir: "/tmp/tmux-1000", SocketDirFromServer: true,
				StaleGaps: tc.gaps, Now: now,
			})

			require.NotContains(t, out, "  none\n",
				"a stale scan that established nothing must not render as a clean host: %q", out)
			require.NotContains(t, out, "none in ",
				"nor as a clean directory: %q", out)
			for _, want := range tc.want {
				require.Contains(t, out, want)
			}
			require.NotContains(t, out, "refuses to act",
				"nothing acts on the stale list, so this must not promise a refusal that never happens")
		})
	}
}

// TestRenderOrphansStaleGapCountsWhatCouldNotBeRead: the count is in the sentence
// because "one file here is unproven" and "every file here is unproven" are different
// facts about the same empty list, and a user deciding whether to trust it needs which.
func TestRenderOrphansStaleGapCountsWhatCouldNotBeRead(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true, SocketDir: "/tmp/tmux-1000", SocketDirFromServer: true,
		StaleGaps: tmux.StaleGaps{Unprobed: 1}, Now: now,
	})
	require.Contains(t, out, "1 socket file there could not be classified",
		"one file reads as one file, not as a plural hedge")
	require.Contains(t, out, "empty for want of an answer",
		"with nothing listed, the unprobed files are the whole reason the list is empty")

	// The remedy must not name PATH as the only cause. A probe also fails to run when the
	// scan's budget was spent, and "check that tmux is on PATH" is wrong advice on a host
	// whose PATH is fine — it sends the user to inspect the one thing that is not broken
	// and never names the cause a re-run fixes. So the re-run leads.
	//
	// The `ls -l` tail is asserted in the same expression, not a separate Contains: it is
	// the only move offered for #730's cause, which neither the re-run nor the PATH check
	// clears, and its order relative to the other two is the whole point of the sentence.
	require.Regexp(t, `re-run to get a complete answer[\s\S]*is on PATH[\s\S]*can be opened \(ls -l\)`, out,
		"the re-run must come first: it is the remedy for the cause checking PATH cannot explain")
}

// TestRenderOrphansStaleGapPrintsBesideFilesFound: a gap note goes *in addition to* the
// files that were classified, never instead of them. Those files are real whatever else
// went unseen, and suppressing them to report the gap would trade one blind spot for
// another — the same rule the server half follows for a truncated /proc walk.
func TestRenderOrphansStaleGapPrintsBesideFilesFound(t *testing.T) {
	out := RenderOrphans(OrphanResult{
		Supported: true, Now: now,
		SocketDir: "/tmp/tmux-1000", SocketDirFromServer: true,
		Stale:     []tmux.StaleSocket{{Path: "/tmp/tmux-1000/atrium-precheck-991-1"}},
		StaleGaps: tmux.StaleGaps{Unprobed: 2},
	})

	require.Contains(t, out, "stale socket files: 1 in /tmp/tmux-1000",
		"what was found is still stated as found")
	require.Contains(t, out, "2 further socket files there could not be classified")
	require.Contains(t, out, "list above may be short")
	require.Contains(t, out, "rm -- /tmp/tmux-1000/atrium-precheck-991-1",
		"and the remedy for the verified files still prints")
}

// TestRenderOrphansNoneNamesHowItKnowsTheDirectory covers #598's fourth flavour, which
// is a wrong *subject* rather than an unproven claim — hence wording, not a gap flag.
//
// With no server to ask, SocketDir reconstructs $TMUX_TMPDIR/tmux-<uid>: where tmux
// *would* bind. "none in <dir>" is then perfectly true about a directory no server need
// ever have bound in, which is not the question the user is asking.
//
// Both fixtures carry a server row, because that is what makes the stale line render at
// all: on a wholly clean host the switch above collapses the section to a bare "none"
// and neither wording appears. The combination is #547's own scenario rather than a
// contrivance — an orphan found by /proc while nothing answers the ambient socket is
// exactly when SocketDir has no server to ask.
func TestRenderOrphansNoneNamesHowItKnowsTheDirectory(t *testing.T) {
	// Scoped to the stale half: the server row above it carries a ⚠ of its own, and the
	// assertion below is about whether *this* sentence is flagged as a gap.
	render := func(fromServer bool) string {
		out := RenderOrphans(OrphanResult{
			Supported: true, Now: now,
			SocketDir: "/tmp/tmux-1000", SocketDirFromServer: fromServer,
			Servers: []tmux.OrphanServer{{
				PID: 7, Socket: "atrium", SocketPath: "/tmp/gone/tmux-1000/atrium",
				ReachableKnown: true, Started: startedAgo(time.Hour),
			}},
		})
		_, stale, ok := strings.Cut(out, "stale socket files:")
		require.True(t, ok, "the stale half must render at all: %q", out)
		return stale
	}

	answered := render(true)
	require.Contains(t, answered, "none in /tmp/tmux-1000")
	require.NotContains(t, answered, "would bind",
		"a directory a live server named needs no hedge about where tmux would bind")

	reconstructed := render(false)
	require.Contains(t, reconstructed, "none in /tmp/tmux-1000")
	require.Contains(t, reconstructed, "would bind",
		"with no server's answer to use, the sentence must say which directory it is about")
	require.NotContains(t, reconstructed, "⚠",
		"the pass over that directory was complete: this is a wrong subject, not a gap")

	// The note may claim only what SocketDir actually established. Its query comes back
	// empty when tmux is off PATH or the probe's budget was spent exactly as it does on
	// an empty fleet, and a server may well be running in the first two — so "no server
	// answered" is the fact, and "no server is running" would be #599's conflation
	// reintroduced one sentence away from where it was fixed.
	require.Contains(t, reconstructed, "no server answered")
	require.NotContains(t, reconstructed, "no tmux server is running",
		"an unanswered probe does not establish that nothing is running")
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

// TestRenderOrphansUnknownReachabilityPromisesNoKill. With the probe unanswered nothing
// is proven — and the live server could not be excluded either, so these rows may
// well be the running fleet. The row has to say so, because the user reading it is
// deciding whether to reach for `reap --kill`.
//
// It also has to give the user something to do, which is why the path is asserted here and
// not merely in the format string. `ls -l` is the only handle this row offers, and it is a
// read the scan could not make — making it is exactly what failed. Since #730 this is where
// a live server behind an unopenable socket lands, along with the residual that fix accepted
// — an orphan behind ENOTDIR/ELOOP, which reap can no longer take. A row that withheld the
// path would leave the user with a verdict and no next step.
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
	require.Contains(t, out, "`ls -l /tmp/tmux-1000/atrium`",
		"the row must name the file to look at: it is the only read that narrows the causes")
	require.Contains(t, out, "could not open the socket",
		"and it must name the cause that neither a re-run nor a PATH check can clear")
	require.NotContains(t, out, "UNREACHABLE",
		"an unanswered probe is not a finding; only a probe that answered may say unreachable")
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

// TestRenderOrphansSaysWhenAClientIsConnected is #614's report half.
//
// An UNREACHABLE row already carries the strongest sentence in this section — no tmux
// command can name it, `reap --kill` is the only thing that can stop it — and that is
// right for an orphan and wrong for a live fleet whose socket file was deleted, which is
// classified identically. A connected client is the only thing in the scan that tells them
// apart, so the row has to say so before the user acts on the remedy above it.
//
// The negative case is asserted alongside because a note printed on every row is not
// evidence of anything, and would be read as boilerplate on the rows where it is the
// point.
func TestRenderOrphansSaysWhenAClientIsConnected(t *testing.T) {
	row := func(clients int) tmux.OrphanServer {
		return tmux.OrphanServer{
			PID: 7, Socket: "atrium", SocketPath: "/tmp/gone/tmux-1000/atrium",
			ReachableKnown: true, ConnectedClients: clients, Started: startedAgo(time.Hour),
		}
	}

	out := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: []tmux.OrphanServer{row(4)}})
	require.Contains(t, out, "UNREACHABLE", "the classification is unchanged; only what is said about it grows")
	require.Contains(t, out, "4 clients are connected to it")
	require.Contains(t, out, "live fleet whose socket was deleted")
	require.Contains(t, out, "`atrium reap --kill --yes` leaves it alone",
		"this row is a default target, so plain --kill --yes is the invocation the count changes")

	// The plural is built at runtime, and this section's whole subject is sentences that
	// are false in one of their forms.
	one := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: []tmux.OrphanServer{row(1)}})
	require.Contains(t, one, "1 client is connected to it")

	none := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: []tmux.OrphanServer{row(0)}})
	require.NotContains(t, none, "connected to it",
		"with no client on it there is nothing to report, and a note on every row is not evidence")

	// The deleted-socket clause is scoped to the row it is true of. A reachable server's
	// socket file is answering probes, so nothing about it was deleted — and that row's
	// remedy is a `kill-server` that works, not "reap is the only thing that can stop it".
	// Measured on this host: a leaked server under another TMUX_TMPDIR, reachable, with two
	// clients on it, was told its socket file had been deleted.
	reachable := row(2)
	reachable.Reachable = true
	live := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: []tmux.OrphanServer{reachable}})
	require.Contains(t, live, "2 clients are connected to it",
		"a reachable server in use still says so — --yes leaves it alone under --all too")
	require.NotContains(t, live, "socket was deleted",
		"its socket answers, so this clause would be a false claim about it")
	// And the invocation named has to be the one that would have taken it. Plain `--kill`
	// spares a reachable row for being reachable, which the remedy above already says, so
	// naming that one here would credit the sparing to the client count instead.
	require.Contains(t, live, "`atrium reap --kill --all --yes` leaves it alone",
		"--all is what selects a reachable row, so --all is what the count changes")

	// A row whose reachability was never established gets the count and nothing else.
	// reapTargets drops it whatever the flags, so the count is not why reap spares it — the
	// row's own remedy line already gives the reason — and Reachable is documented meaningless
	// without ReachableKnown, so a deleted-socket claim would rest on a probe that never ran.
	// It would print two lines under "nothing here is proven".
	unknown := row(3)
	unknown.ReachableKnown = false
	blind := RenderOrphans(OrphanResult{Supported: true, Now: now, Servers: []tmux.OrphanServer{unknown}})
	require.Contains(t, blind, "3 clients are connected to it",
		"the count itself is read from /proc and is established whatever tmux could do")
	require.NotContains(t, blind, "socket was deleted",
		"no probe ran, so nothing is known about whether this socket answers")
	require.NotContains(t, blind, "leaves it alone for that reason",
		"reap never kills this row at all, so the client count is not the reason it survives")
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
	staleScan = func(context.Context) tmux.StaleScan {
		return tmux.StaleScan{
			Stale:         []tmux.StaleSocket{{Path: "/tmp/tmux-1000/atrium-old"}},
			Dir:           "/tmp/tmux-1000",
			DirFromServer: true,
			Gaps:          tmux.StaleGaps{Unprobed: 2},
		}
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
	// The stale half's own two facts, for the same reason. Dropping either here would
	// leave the renderer with a bare empty list and no way to know what it means.
	require.Equal(t, 2, got.StaleGaps.Unprobed, "the stale scan's gaps must reach the result")
	require.True(t, got.SocketDirFromServer, "and so must how it knows which directory it read")
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
