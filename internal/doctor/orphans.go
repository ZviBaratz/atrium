package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// OrphanResult is the orphaned-tmux-server snapshot the doctor reports (#547).
//
// Servers are live tmux servers Atrium owns that are not the one it is running on.
// Stale are socket *files* in SocketDir with no server behind them — the cosmetic
// half of the same leak, since tmux never unlinks a socket when its server dies.
// Supported is false off Linux, where a process scan has no /proc to read; the stale
// file list still works there, which is why it is not gated on the same flag.
//
// Now is the instant the snapshot was taken, carried so that rendering an age is a
// pure function of the result rather than of when it happens to be printed.
//
// Each half carries its own account of what it could not see, and neither is evidence
// without it: an empty Servers slice means a clean host only when
// Gaps.IncompleteInventory() is false, and an empty Stale means a clean directory only
// when StaleGaps.Any() is false. The two predicates are named differently because they
// are not the same kind of thing — StaleGaps has no field held back from its Any(),
// while ScanGaps does, and the name says which.
type OrphanResult struct {
	Supported bool
	Servers   []tmux.OrphanServer
	Gaps      tmux.ScanGaps
	SocketDir string
	// SocketDirFromServer reports that SocketDir came from the live server rather than
	// from reconstructing tmux's layout. It is not a gap — the scan of SocketDir was
	// complete — but it decides what "none in SocketDir" is a statement *about*, so the
	// renderer words the sentence around it (#598).
	SocketDirFromServer bool
	Stale               []tmux.StaleSocket
	StaleGaps           tmux.StaleGaps
	Now                 time.Time
}

// orphanScan and staleScan are seams so CheckOrphans' assembly is testable without a
// live tmux server, mirroring oom.go's.
var (
	orphanScan = tmux.ScanServers
	staleScan  = tmux.ScanStaleSockets
)

// CheckOrphans gathers the orphan snapshot. Like the other doctor checks it never
// fails and never mutates: it reads /proc and issues read-only `tmux -S …
// display-message` probes, so it is safe beside a live TUI. It deletes nothing and
// signals nothing — every remedy it can offer is printed for the user to run.
func CheckOrphans(ctx context.Context) OrphanResult {
	servers, supported, gaps := orphanScan(ctx)
	stale := staleScan(ctx)
	return OrphanResult{
		Supported:           supported,
		Servers:             servers,
		Gaps:                gaps,
		SocketDir:           stale.Dir,
		SocketDirFromServer: stale.DirFromServer,
		Stale:               stale.Stale,
		StaleGaps:           stale.Gaps,
		Now:                 time.Now(),
	}
}

// RenderOrphans formats the orphan snapshot under an "Orphaned tmux servers:"
// header, parallel to RenderOOM.
//
// The header says "tmux servers" rather than a bare "orphans" because doctor already
// uses that word for a Claude login the account list no longer names
// (ui/overlay/accounts.go) — two unrelated things called orphans in one report is one
// too many.
//
// A clean host prints "none", not an empty section: per RenderGates' doc comment,
// "the check ran and found nothing" and "the check silently had nothing to say" must
// not look identical. That matters more here than anywhere else in doctor, because
// the failure this section exists for is invisible by construction.
func RenderOrphans(r OrphanResult) string {
	var b strings.Builder
	b.WriteString("Orphaned tmux servers:\n")

	switch {
	case !r.Supported:
		// The stale-file list below still works off Linux, so this names what is
		// missing rather than declaring the whole section unavailable.
		b.WriteString("  server scan unavailable — finding a server whose socket no longer resolves\n")
		b.WriteString("  needs a process inventory, which is Linux-only\n")
	case r.Gaps.IncompleteInventory():
		// Before the emptiness test, never instead of it: a scan that could not see is
		// the one case where "none" would be a fabrication rather than a finding, and
		// any rows that did survive still print below.
		renderScanGaps(&b, r.Gaps)
	// The stale half's gaps have to be tested *here*, in the server half's fast path,
	// even though renderStaleSockets is what reports them. This case writes "none" and
	// returns, so an empty host whose stale probes all failed lands on it and never
	// reaches the renderer below — a gap wired in anywhere further down renders as
	// nothing at all.
	case len(r.Servers) == 0 && len(r.Stale) == 0 && !r.StaleGaps.Any():
		b.WriteString("  none\n")
		return b.String()
	}

	for _, s := range r.Servers {
		renderOrphanServer(&b, s, r.Now, r.Gaps)
	}
	renderStaleSockets(&b, r)
	return b.String()
}

// renderScanGaps says what the scan could not see, and what that does to the list
// underneath it.
//
// Each gap gets its own sentence because the consequences differ: an unreadable socket
// table suppresses every row, while a truncated /proc walk leaves the rows it did
// produce standing but possibly understated. Both print the retry, since both are
// transient conditions rather than diagnoses.
func renderScanGaps(b *strings.Builder, g tmux.ScanGaps) {
	if g.SocketTableUnread {
		b.WriteString("  ⚠ scan blind: /proc/net/unix could not be read. A server is identified by\n")
		b.WriteString("    the socket it listens on, so with that table unavailable no server can be\n")
		b.WriteString("    named at all — the list below is empty because nothing was looked at,\n")
		b.WriteString("    not because nothing is there\n")
	}
	if g.ProcTableTruncated {
		b.WriteString("  ⚠ scan incomplete: the walk over /proc did not finish, so a server may be\n")
		b.WriteString("    missing below, and a server that is listed may show fewer child processes\n")
		b.WriteString("    than it actually holds\n")
	}
	b.WriteString("      → re-run to get a complete answer; `atrium reap --kill` refuses to act on\n")
	b.WriteString("        an inventory this incomplete\n")
}

// renderOrphanServer writes one server's row plus the remedy that fits its class.
// The remedy is the point of the row: an unreachable server has no tmux command that
// can name it, and saying so is the difference between a report and a dead end.
//
// gaps decides what a reachable row may be told to do. When the scan did not establish
// which server this Atrium is running on, the live server could not be excluded by pid and
// may be one of these rows — and it would arrive here Reachable, since it answers its own
// socket. The remedy for a reachable server is a `kill-server` naming its exact path, so
// printing one unconditionally is how this report becomes an instruction to kill the live
// fleet. That is the #584 shape, arrived at through the report rather than through a glob.
//
// The whole gaps value rather than one "identified" bool, because the two ways it can be
// unidentified need different sentences: one is an unrunnable probe and one is a probe that
// answered about the wrong socket, and only the first leaves the row uninspectable (#603).
//
// The connected-client note goes below the switch rather than into a case of it, because it
// is orthogonal to reachability: it says what the server *is* — something is using it — where
// the cases say what can be aimed at it. Its subject on an unreachable row is #614, the one
// shape this report could otherwise present as an abandoned server.
func renderOrphanServer(b *strings.Builder, s tmux.OrphanServer, now time.Time, gaps tmux.ScanGaps) {
	switch {
	case !s.ReachableKnown:
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachability unknown  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		b.WriteString("      → tmux could not be run, so nothing here is proven; `atrium reap` lists\n")
		b.WriteString("        these and never kills them\n")
	case s.Reachable && gaps.LiveServerUnknown:
		// No command is printed at all here. The honest remedy is to re-run once the
		// probe works, because the one command that would stop this server is also the
		// one that would stop the fleet if this row is the fleet.
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachable  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		b.WriteString("      → no remedy offered: this Atrium's own server could not be identified,\n")
		b.WriteString("        so this row may be it — and the command that would stop it is the\n")
		b.WriteString("        command that would stop your live sessions. Re-run first\n")
	case s.Reachable && gaps.EmptyFleetUnproven && s.OnAnAmbientSocket():
		// The command stays, with the claim it used to carry retracted beside it. This is
		// the opposite call from the case above, and the difference is what a user can do
		// next: there tmux could not be run, so the row cannot be inspected and a re-run
		// is the only move; here tmux demonstrably runs, this server answers, and the
		// probe will keep answering about the same wrong socket however often it is
		// re-run. Withholding would leave a real and verified remedy unnamed and offer
		// advice that cannot help — so the row is annotated rather than blanked, and it is
		// `--all`, which needs no per-row judgement, that refuses (#603).
		//
		// OnAnAmbientSocket keeps the hedge off rows it would be false about. The caution
		// says this may be a live fleet, and a `-precheck-` or verification socket cannot
		// be one — no Atrium addresses its own server by a suffixed name — so such a row
		// falls through to the plain reachable case and keeps its unqualified remedy.
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachable  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		fmt.Fprintf(b, "      → tmux -S %s kill-server\n", s.SocketPath)
		b.WriteString("        caution: nothing answered on this run's own Atrium socket, yet this\n")
		b.WriteString("        server answers its own — it may be a live fleet started under another\n")
		b.WriteString("        TMUX_TMPDIR or brand rather than an orphan. `tmux -S <path> ls` first;\n")
		b.WriteString("        `atrium reap --kill --all` refuses to take it\n")
	case s.Reachable:
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachable  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		fmt.Fprintf(b, "      → tmux -S %s kill-server\n", s.SocketPath)
	default:
		fmt.Fprintf(b, "  ⚠ pid %d  socket %s  up %s  UNREACHABLE  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		fmt.Fprintf(b, "      → no tmux command can name this server: %s does not answer for it.\n", s.SocketPath)
		b.WriteString("        `atrium reap --kill` is the only thing that can stop it\n")
	}
	if s.ConnectedClients > 0 {
		fmt.Fprintf(b, "        %d %s connected to it, so something is using it right now;\n",
			s.ConnectedClients, plural(s.ConnectedClients, "client is", "clients are"))
		b.WriteString("        `atrium reap --kill --yes` leaves it alone for that reason\n")
		if !s.Reachable {
			// Only on this row, because only here is the clause true and only here does the
			// remedy above it read "reap --kill is the only thing that can stop it". A
			// reachable server's socket file is answering, so nothing about it was deleted.
			b.WriteString("        a client stays on a server when its socket file goes, so this may be a\n")
			b.WriteString("        live fleet whose socket was deleted rather than an abandoned server\n")
		}
	}
	if s.CWDDeleted {
		b.WriteString("        its working directory has been deleted, so its run is long gone\n")
	}
}

// renderStaleSockets writes the class-(a) list: socket files with no server behind
// them.
//
// The remedy names the exact files this run probed, rather than a `find … -name
// 'atrium-*' -delete` glob. A glob re-matches at the moment the user runs it, so it
// can take a socket bound between the report and the command — including the live
// one, which lives in this same directory. #584 shipped a glob delete over this
// directory and it cost thirteen running sessions; naming verified paths cannot do
// that. Atrium prints the command and never runs it.
//
// "none in <dir>" prints only when the scan established it. A pass that could not list
// the directory, or could not probe the files in it, produced the same empty list a
// clean directory does, so claiming "none" off that emptiness is a fabrication rather
// than a finding (#598) — the same rule RenderGates' doc comment states, and the same
// one the server half above now follows. A gap note prints in addition to whatever
// files were found, never instead of them: the files that were classified are real
// whatever else went unseen.
func renderStaleSockets(b *strings.Builder, r OrphanResult) {
	switch {
	case len(r.Stale) > 0:
		fmt.Fprintf(b, "  stale socket files: %d in %s — files only, no server behind them\n",
			len(r.Stale), r.SocketDir)
	case r.StaleGaps.Any():
		// Deliberately no count and no "none": the pass established neither. The gap
		// sentences below carry the whole of what is known.
		fmt.Fprintf(b, "  stale socket files: not established%s\n", inDir(r.SocketDir))
	case r.SocketDir != "":
		fmt.Fprintf(b, "  stale socket files: none in %s\n", r.SocketDir)
		if !r.SocketDirFromServer {
			// Which directory this is, is the whole content of the sentence above. With
			// no server's answer to use, it is where tmux *would* bind rather than where
			// one is bound — a true "none" about a directory nothing need ever have used.
			//
			// "no server answered", not "no server is running": the query comes back
			// empty when tmux is off PATH or the probe's budget was spent just as it does
			// on an empty fleet, and a server may well be running in those first two.
			// That is the distinction #599 drew for the pid probe, and claiming the
			// stronger fact here would be the same error one sentence over.
			b.WriteString("      note: no server answered for Atrium's socket, so that is where tmux\n")
			b.WriteString("        would bind rather than where a server is bound — one started under\n")
			b.WriteString("        another TMUX_TMPDIR left its files somewhere else\n")
		}
	default:
		// A zero-value result: no directory, nothing found, nothing unseen. Saying
		// "none in " would name no subject at all.
		return
	}
	renderStaleGaps(b, r.StaleGaps, len(r.Stale) > 0)
	if len(r.Stale) > 0 {
		paths := make([]string, 0, len(r.Stale))
		for _, s := range r.Stale {
			paths = append(paths, s.Path)
		}
		fmt.Fprintf(b, "      → rm -- %s\n", strings.Join(paths, " "))
	}
}

// renderStaleGaps says what the stale-socket pass could not establish. Each gap gets
// its own sentence and its own remedy because the two differ in kind: an unlistable
// directory is an existence or permission problem, while an unrunnable probe means
// tmux itself could not be executed.
//
// Neither prints renderScanGaps' "`atrium reap --kill` refuses to act" line, and that
// asymmetry is deliberate. That refusal is real for the server inventory, where a short
// Children list would buy consent against an understatement of what dies. Nothing acts
// on this list at all — reap prints it and the remedy is an `rm --` line the user runs —
// so promising a refusal here would be a false claim of its own, in the renderer whose
// subject is false claims.
//
// found says whether files were listed above, which is the difference between "these
// are the only files there and none could be read" and "the list you just read may be
// short by this many".
func renderStaleGaps(b *strings.Builder, g tmux.StaleGaps, found bool) {
	if g.DirUnread {
		b.WriteString("      ⚠ the directory could not be listed, so nothing about its contents is\n")
		b.WriteString("        known: tmux leaves a socket file behind for every server that dies,\n")
		b.WriteString("        and none of them can be named until this directory can be read\n")
		b.WriteString("        → check that it exists and is readable, then re-run\n")
	}
	if g.Unprobed > 0 {
		// Two pre-wrapped sentences rather than one with a clause spliced into it: the
		// consequence genuinely differs between the two cases, and a variable-length
		// insert wraps at an unpredictable column — which on a real host ran this line
		// well past the width every other line here keeps to.
		if found {
			fmt.Fprintf(b, "      ⚠ %d further socket %s there could not be classified, so the\n",
				g.Unprobed, plural(g.Unprobed, "file", "files"))
			b.WriteString("        list above may be short\n")
		} else {
			fmt.Fprintf(b, "      ⚠ %d socket %s there could not be classified, so the list above\n",
				g.Unprobed, plural(g.Unprobed, "file", "files"))
			b.WriteString("        is empty for want of an answer, not for want of files\n")
		}
		// The remedy leads with the re-run and offers PATH second, because the sentence
		// above has more than one cause: the probe fails when tmux is off PATH *and* when
		// the scan's budget was spent. Naming only PATH — as this did — sends a user whose
		// PATH is fine to check it, and never names the cause that a re-run fixes.
		b.WriteString("        → re-run to get a complete answer; if it persists, check that tmux\n")
		b.WriteString("          is on PATH\n")
	}
}

// plural picks a word for n. Spelled out rather than appending "s", so a caller reading
// the format string sees both forms where they are used.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// inDir renders the " in <dir>" clause, or nothing when there is no directory to name.
// A dangling "in " names no subject, which is the failure mode this whole section is
// about.
func inDir(dir string) string {
	if dir == "" {
		return ""
	}
	return " in " + dir
}

// childSummary describes what a server is holding, which is what makes killing it a
// decision rather than a formality: these are real agents, and they may hold unpushed
// work (#267). Distinct command names are listed, not pids — `atrium reap` names
// every pid at the point where one is about to die.
func childSummary(kids []tmux.ChildProc) string {
	if len(kids) == 0 {
		return "holds nothing"
	}
	seen := map[string]bool{}
	var comms []string
	for _, k := range kids {
		if k.Comm != "" && !seen[k.Comm] {
			seen[k.Comm] = true
			comms = append(comms, k.Comm)
		}
	}
	sort.Strings(comms)
	unit := "processes"
	if len(kids) == 1 {
		unit = "process"
	}
	if len(comms) == 0 {
		return fmt.Sprintf("holds %d %s", len(kids), unit)
	}
	return fmt.Sprintf("holds %d %s (%s)", len(kids), unit, strings.Join(comms, ", "))
}

// HumanAge renders a duration in the largest two units that stay readable. A
// negative age — clock skew, or a start time from another machine — clamps to zero
// rather than printing a negative uptime.
//
// Exported so `atrium reap`'s confirmation prompt dates a server exactly the way the
// doctor row does: the user reads one and then the other about the same process, and
// two spellings of the same uptime read as two different facts.
func HumanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
