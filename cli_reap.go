package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
)

var (
	reapKillFlag bool
	reapAllFlag  bool
	reapYesFlag  bool

	reapCmd = &cobra.Command{
		Use:   "reap",
		Short: "List tmux servers Atrium left behind, and stop them on request",
		Long: "Lists tmux servers Atrium started that outlived the run that started them, and\n" +
			"with --kill stops them. Without --kill it only reports, and reports exactly what\n" +
			"`atrium doctor` does.\n\n" +
			"The case this exists for is a server whose socket file was deleted along with the\n" +
			"temp directory holding it. It keeps running, but the path no longer resolves, so\n" +
			"`tmux ls`, `atrium reset` and both clean scripts address a socket that is not\n" +
			"there — nothing but a process scan can name it, and nothing but a signal can stop\n" +
			"it. A server that is still reachable is reported with the tmux command that stops\n" +
			"it, and is left alone unless you pass --all.\n\n" +
			"It never kills anything you have not been shown first. These servers hold live\n" +
			"agents, and an agent may hold work that was never pushed, so each server is named\n" +
			"with every process that dies with it and confirmed one at a time (--yes skips the\n" +
			"prompt, for scripts). A server whose reachability could not be determined is never\n" +
			"killed, with or without --all, because nothing about it has been established. That\n" +
			"covers tmux missing or a probe that could not run, and also a socket that is there\n" +
			"but cannot be opened: a server behind one is unreachable to Atrium and may be\n" +
			"perfectly healthy, so it is listed and left alone rather than treated as empty.\n\n" +
			"An unreachable server can also be a live fleet whose socket file was deleted, and\n" +
			"the scan counts the clients connected to each one to tell those apart: once the\n" +
			"file is gone nothing can connect, so a client still on it was there before, while\n" +
			"an orphan's clients died with the run that made it. That count is printed with the\n" +
			"confirmation, for you to judge. Under --yes there is nobody to judge, so any server\n" +
			"a client is connected to is left alone — reachable ones under --all included, since\n" +
			"the rule is about the missing human rather than about the socket — and the command\n" +
			"exits non-zero; drop --yes to decide per server. A fleet with no client left on it\n" +
			"is indistinguishable from an orphan, and the list of processes that die with it is\n" +
			"what stands in for this.\n\n" +
			"Two cases suspend what is said above about reachable servers, both of them the\n" +
			"same fact: this Atrium's own tmux server was not identified, so a reachable server\n" +
			"listed here may be that server, and --all refuses rather than target it. Either\n" +
			"way, dropping --all still works — the servers proven unreachable are still proven,\n" +
			"and still killable.\n\n" +
			"When the probe could not answer at all, nothing tells the row apart from the live\n" +
			"server, so it is printed with no tmux command beside it; re-run once the probe\n" +
			"works. When it answered that nothing is on the socket this run computed from its\n" +
			"own environment, while a reachable server answers its own, that answer was not\n" +
			"about the fleet — another TMUX_TMPDIR, the other brand, or a config it could not\n" +
			"parse. Re-running asks the same socket again, so the row keeps its kill-server\n" +
			"command with a caution beside it: check the server with `tmux -S <path> ls` first.\n\n" +
			"It deletes nothing. A killed server's socket file is inert; `atrium doctor` lists\n" +
			"the leftover files and prints the command to remove them.\n\n" +
			"Linux only: finding a server whose socket no longer resolves needs a process\n" +
			"inventory.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			// No TUI lock, deliberately. An orphan is by definition unrelated to the
			// server the running TUI is on, so requiring a closed TUI would make this
			// command refuse in exactly the situation it exists for: a stuck server
			// eating memory while the user is mid-session.
			return runReap(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), reapOpts{
				kill: reapKillFlag,
				all:  reapAllFlag,
				yes:  reapYesFlag,
			})
		},
	}
)

// Signal budgets. SIGTERM gets the longer one so a server can run its hooks and exit
// cleanly; SIGKILL cannot be refused, so its wait only covers the kernel getting
// round to it.
const (
	reapTermGrace = 5 * time.Second
	reapKillGrace = 2 * time.Second
	reapPoll      = 50 * time.Millisecond
)

// Seams, so the signalling ladder is testable without killing anything.
var (
	reapCheck     = doctor.CheckOrphans
	reapStartTime = tmux.ProcessStartTime
	reapSignal    = signalProcess
	reapAlive     = processAlive
	reapSleep     = time.Sleep
)

// reapOpts is what the flags decide: whether to kill at all, whether reachable
// servers are included, and whether each kill is confirmed.
type reapOpts struct {
	kill bool
	all  bool
	yes  bool
}

// reapOutcome is what happened to one server the reaper was pointed at.
type reapOutcome int

const (
	reapKilled reapOutcome = iota
	// reapSkipped: the PID-reuse guard refused, or the user declined. Nothing was
	// signalled, and that is a normal result rather than a failure.
	reapSkipped
	// reapSurvived: it was signalled, with SIGKILL, and is still there.
	reapSurvived
)

// runReap lists orphaned tmux servers and, with opts.kill, stops them.
//
// It writes to w and reads confirmations from in, so the whole flow is drivable in a
// test against a buffer. The listing comes from doctor.CheckOrphans, which is the
// same snapshot `atrium doctor` renders — one source, so the two can never disagree
// about what is out there.
func runReap(ctx context.Context, w io.Writer, in io.Reader, opts reapOpts) error {
	if !opts.kill && (opts.all || opts.yes) {
		return fmt.Errorf("--all and --yes only apply to --kill; without it %s reap just reports", binName)
	}

	// The same probe budget the doctor section gets, and for the same reason: the
	// scan runs one `tmux -S <path> display-message` per candidate, and a wedged
	// server must not hang the command. Without it reap inherits Cobra's context,
	// which has no deadline at all.
	scanCtx, cancelScan := context.WithTimeout(ctx, doctor.ProbeTimeout)
	defer cancelScan()
	res := reapCheck(scanCtx)
	reapf(w, "%s", doctor.RenderOrphans(res))

	if !opts.kill {
		// Keyed on what `--kill` would actually act on, not on the row count: a
		// listing of only reachable or unknown servers has nothing for --kill to do,
		// and pointing the user at it would send them to "nothing to kill."
		//
		// A gap silences it for the same reason at one remove: --kill refuses on an
		// incomplete inventory, the section above has already said so, and naming the
		// command anyway would have one output both promise a kill and decline it.
		if !res.Gaps.IncompleteInventory() && len(reapTargets(res.Servers, false)) > 0 {
			reapf(w, "\nnothing was killed. Run `%s reap --kill` to stop the unreachable ones.\n", binName)
		}
		return nil
	}
	if !res.Supported {
		return fmt.Errorf("cannot reap on this platform: finding a server whose socket no longer resolves needs a process inventory (Linux only)")
	}
	// Positive proof only, applied to the inventory itself. A truncated /proc walk
	// attributes children from a partial table, so a server's Children list can be
	// short — and that list is exactly what the confirmation prompt shows the user
	// before they consent. Killing on it would be consent obtained against an
	// understatement of what dies. An unreadable socket table is the milder case (it
	// yields no targets at all), but the same rule covers both.
	if res.Gaps.IncompleteInventory() {
		return fmt.Errorf("refusing to kill on an incomplete scan: the process inventory could not see everything, so what a kill would destroy is not fully known — re-run %s reap --kill once the scan comes back complete", binName)
	}

	targets := reapTargets(res.Servers, opts.all)
	// --all is the one mode that kills a *reachable* server, and the only thing keeping
	// it off this Atrium's own is the pid exclusion in assembleServers. With the live
	// server unidentified there is no exclusion, and the live server answers its own
	// socket — so it arrives Reachable and --all would target it, taking the fleet and
	// every agent in it. The default set needs no such guard: it is unreachable-only,
	// and a server that answered is proof it is not that.
	//
	// Keyed on LiveServerUnidentified() rather than on LiveServerUnknown alone, because
	// "identified" has to mean a pid was positively determined. A probe that answered
	// about the socket this process computed from its own HOME and TMUX_TMPDIR excluded
	// nothing when it answered pid 0, and the flag for an unanswered probe stays low —
	// so keying on that one left this guard silent in exactly the case where a reachable
	// row is the live fleet (#603).
	//
	// Keyed on the selected targets rather than on the flag, and that scoping matters:
	// with nothing reachable to select, --all picks exactly what the default picks, and
	// refusing there would repeat #593's shape — a probe that could not answer becoming
	// a refusal to reap on the very host that needed it. Placed below reapTargets for
	// that reason, and still above the only reapServer call, which is what makes it a
	// guard.
	//
	// That scoping does its work for LiveServerUnknown only. EmptyFleetUnproven is set
	// *because* a reachable server on a bare-brand socket is in the inventory, and a
	// Reachable row is always ReachableKnown and so always selected under --all — so for
	// that cause the conjunct cannot be false and the two spellings coincide. It stays
	// because the other cause needs it, and because a reader is entitled to see the
	// condition state the whole rule rather than the half that currently bites.
	if opts.all && res.Gaps.LiveServerUnidentified() && tmux.AnyReachable(targets) {
		return fmt.Errorf("refusing --all: %s", unidentifiedLiveServerReason(res.Gaps))
	}

	if len(targets) == 0 {
		reapf(w, "\nnothing to kill.\n")
		return nil
	}

	reapf(w, "\n")
	reader := bufio.NewReader(in)
	var killed, skipped, survived, refused int
	for _, s := range targets {
		// --yes waives the confirmation, so on this path there is nobody to show a
		// connected client to — and a client connected to an unreachable server is the
		// one thing in the scan that says this row is a live fleet whose socket file was
		// deleted rather than an abandoned server (#614). Refused per row rather than up
		// front: one such row must not block the reaping of real orphans beside it.
		if opts.yes && s.ConnectedClients > 0 {
			reapf(w, "  left pid %d alone: %s, and --yes has no one to ask\n",
				s.PID, connectedClientSummary(s.ConnectedClients))
			skipped++
			refused++
			continue
		}
		// The confirmation carries the target it armed with: everything below acts on
		// this copy — its pid, its start time, its children — not on a fresh scan
		// (#502). What the user was shown is what gets signalled, or nothing does.
		if !opts.yes && !confirmReap(w, reader, s) {
			reapf(w, "  skipped pid %d\n", s.PID)
			skipped++
			continue
		}
		switch reapServer(w, s) {
		case reapKilled:
			killed++
		case reapSkipped:
			skipped++
		case reapSurvived:
			survived++
		}
	}

	reapf(w, "\n%d killed, %d skipped, %d survived\n", killed, skipped, survived)
	if refused > 0 {
		// Broken out of the skipped count rather than folded silently into it. A refusal and
		// a declined prompt are both skips, but they cannot both have happened in one run:
		// refusals need --yes, which is the mode where nothing was asked. "3 skipped" alone
		// therefore reads as three decisions someone made, and nobody made any.
		reapf(w, "of those, %d left alone with a client connected\n", refused)
	}
	var errs []error
	if survived > 0 {
		// "left a process that" rather than "server(s) survived": reapSurvived also
		// covers the server dying and one of its agents outliving it, which is the
		// half-reaped state the verify step exists to surface.
		errs = append(errs, fmt.Errorf("%d target(s) left a process that survived SIGKILL", survived))
	}
	if refused > 0 {
		// Non-zero, not a line in the output: the caller passed --yes, which means no one
		// is reading. A script that asked for a kill and did not get one has to be able to
		// notice, exactly as it does for a survivor.
		errs = append(errs, fmt.Errorf("%d target(s) left alone: a client is connected, so the server may be "+
			"a live fleet whose socket file was deleted rather than an orphan — re-run without --yes to see "+
			"what each one holds and decide", refused))
	}
	// Joined rather than first-wins: a run can both leave a survivor and refuse a
	// connected server, and each is a separate thing the caller has to act on.
	return errors.Join(errs...)
}

// connectedClientSummary words how many clients a server is holding a connection to,
// for the two places that have to say it: the confirmation and the --yes refusal.
//
// "connected", never "attached": what the scan counts is the sockets the server accepted
// on its path (OrphanServer.ConnectedClients), and a `tmux -S <path> ls` holds one of
// those without attaching to anything.
func connectedClientSummary(n int) string {
	return fmt.Sprintf("%d client%s connected to it", n, plural(n))
}

// reapTargets narrows the scan to what may be killed.
//
// Unreachable-only by default, and this is the answer to "how do you avoid killing a
// legitimate second Atrium": a smoke run in flight, or a second Atrium under its own
// TMUX_TMPDIR, answers its own socket and is therefore reachable — reported with the
// exact tmux command, and left alone. With reachability an identity test rather than
// a file test, that default is sound.
//
// A server whose reachability is unknown is excluded even under --all. Nothing has
// been established about it, and absence of an answer must never mean "safe to act".
// Both of its causes put a live server in this set: when tmux cannot be run the ambient
// live server cannot be excluded either, so the unknown rows may be the running fleet;
// and a socket that exists but cannot be opened is a server Atrium cannot address while
// the agents inside it work on (#730). This one field is the whole guard for the second
// case — nothing downstream re-checks it — which is why classifyPIDProbe has to get the
// classification right rather than leave it to a later refusal.
func reapTargets(servers []tmux.OrphanServer, all bool) []tmux.OrphanServer {
	var targets []tmux.OrphanServer
	for _, s := range servers {
		if !s.ReachableKnown {
			continue
		}
		if s.Reachable && !all {
			continue
		}
		targets = append(targets, s)
	}
	return targets
}

// unidentifiedLiveServerReason words the --all refusal for the cause at hand.
//
// The two causes leave the *reaper* in one position — no selected row is proven not to be
// the live fleet — but not the user. A probe that answered about the wrong socket answers
// the same way every time, so a re-run would send a user whose tmux works fine to re-run
// until they stopped reading; what fits that case is the reachable row itself, which can be
// inspected and stopped by name. LiveServerUnknown is tested first: when nothing was
// established, the weaker claim is the true one.
//
// An unanswered probe is *usually* fixed by re-running, and this message used to say so
// flatly ("re-run once the probe works"). #730 added a cause it is false for: an ambient
// socket that exists and cannot be opened raises LiveServerUnknown on every run, so the
// re-run leads and the thing that actually clears it follows, rather than the user being
// left with the one instruction this branch cannot make good on.
func unidentifiedLiveServerReason(g tmux.ScanGaps) string {
	if g.LiveServerUnknown {
		return fmt.Sprintf("this %s's own tmux server could not be identified, so a reachable server here may be it — re-run, and if that keeps happening check that tmux runs and that this %s's own socket can be opened; or drop --all to kill only the unreachable ones", binName, binName)
	}
	return fmt.Sprintf("nothing answered on this run's own %s socket, yet a reachable server here answers its own — so the fleet may be running under another TMUX_TMPDIR or brand, and this row may be it. Check it with `tmux -S <path> ls` and stop it with the kill-server the listing above prints, or drop --all to kill only the unreachable ones", binName)
}

// confirmReap names everything that dies with this server and asks. Defaults to no:
// anything but an explicit yes leaves the server alone.
//
// A connected client is stated alongside the children, because the two answer different
// questions. The child list says how much dies; the client count says what this server
// *is* — an unreachable row nothing can connect to any more, yet with a client still on
// it, is a live fleet whose socket file was deleted rather than an abandoned one (#614).
// Under --yes this is a refusal instead, since there is nobody here to read it.
func confirmReap(w io.Writer, in *bufio.Reader, s tmux.OrphanServer) bool {
	reach := "unreachable"
	if s.Reachable {
		reach = "reachable"
	}
	reapf(w, "pid %d  socket %s  up %s  %s\n",
		s.PID, s.Socket, humanAgeSince(s.Started), reach)
	if s.ConnectedClients > 0 {
		reapf(w, "  %s, so something is using it right now\n", connectedClientSummary(s.ConnectedClients))
		if !s.Reachable {
			// Scoped the way the doctor's note is (renderOrphanServer), and for the same
			// reason: a reachable server's socket file is answering probes, so nothing about
			// it was deleted. `--all` routes reachable targets straight through here, so
			// without this branch the prompt tells a row exactly what the report was fixed
			// for declining to tell it. !Reachable is proven rather than assumed here —
			// reapTargets drops every row that is not ReachableKnown.
			reapf(w, "  a client stays on a server when its socket file goes, so this may be a\n"+
				"  live fleet whose socket was deleted rather than an abandoned server\n")
		}
	}
	if len(s.Children) == 0 {
		reapf(w, "  holds no child processes\n")
	} else {
		reapf(w, "  these %d processes die with it, and may hold work that was never pushed:\n", len(s.Children))
		for _, kid := range s.Children {
			reapf(w, "    pid %-8d %s  (up %s)\n", kid.PID, kid.Comm, humanAgeSince(kid.Started))
		}
	}
	reapf(w, "kill? [y/N]: ")

	line, err := in.ReadString('\n')
	// Only a clean EOF may still carry an answer — a terminal closed after "y" with
	// no newline. Any other read error is not consent: a partially-read buffer that
	// happens to say "y" must not be what kills an agent's unpushed work.
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// reapServer stops one server and then verifies its children, returning what
// actually happened.
//
// Verifying rather than trusting is the difference between reaping the orphan and
// quietly halving it. Killing the server does take its agents down — the kernel hangs
// up their ptys when the server's masters close — but that is a consequence, not a
// guarantee this code is entitled to assume, so every captured child is re-checked
// and any survivor is signalled directly.
func reapServer(w io.Writer, s tmux.OrphanServer) reapOutcome {
	if !stillTheScannedProcess(s.PID, s.Started) {
		reapf(w, "  skipped pid %d: no longer the process that was scanned — it exited, and the pid may have been reused\n", s.PID)
		return reapSkipped
	}
	if !terminate(s.PID, s.Started) {
		reapf(w, "  ⚠ pid %d survived SIGTERM and SIGKILL\n", s.PID)
		return reapSurvived
	}
	reapf(w, "  killed pid %d\n", s.PID)

	var survivors, leftAlone int
	for _, kid := range s.Children {
		if !reapAlive(kid.PID) {
			continue
		}
		// A child's pid was read before its parent's death and is staler by exactly
		// that much, so it gets the same guard — a recycled pid is just as alive.
		if !stillTheScannedProcess(kid.PID, kid.Started) {
			reapf(w, "    left pid %d alone: no longer the process that was scanned\n", kid.PID)
			leftAlone++
			continue
		}
		if !terminate(kid.PID, kid.Started) {
			reapf(w, "    ⚠ child pid %d (%s) survived SIGTERM and SIGKILL\n", kid.PID, kid.Comm)
			survivors++
			continue
		}
		reapf(w, "    killed child pid %d (%s)\n", kid.PID, kid.Comm)
	}
	if survivors > 0 {
		return reapSurvived
	}
	switch {
	case leftAlone > 0:
		// "none left" would be a lie here: a pid that failed the reuse guard is still
		// running, deliberately unsignalled. Say so rather than reporting a clean tree.
		reapf(w, "  verified %d child process(es); %d still running, left alone by the pid-reuse guard\n",
			len(s.Children), leftAlone)
	case len(s.Children) > 0:
		reapf(w, "  verified %d child process(es); none left\n", len(s.Children))
	}
	return reapKilled
}

// stillTheScannedProcess reports whether pid is the same process the scan recorded,
// by re-reading its start time and comparing it to the captured one. An unreadable
// start time answers no: the process is gone, or something about it cannot be
// established, and neither is grounds for signalling it.
func stillTheScannedProcess(pid int, scanned time.Time) bool {
	now, ok := reapStartTime(pid)
	return ok && now.Equal(scanned)
}

// terminate sends SIGTERM, waits, then SIGKILL, and reports whether the process is
// gone.
//
// SIGTERM goes first so a server that will exit cleanly gets to, and runs its hooks
// on the way out. The SIGKILL rung is not a formality: a tmux server with a live
// session was measured still running five seconds after SIGTERM on the tmux 3.2 CI
// job, while the same test passed on 3.6 — so on some of the versions Atrium
// supports, escalation is the path every reap takes, and reapTermGrace is a cost
// paid per server rather than an unlikely branch.
//
// The PID-reuse guard runs for the whole ladder, not once at the top. Up to seven
// seconds pass between the first signal and the last, and a pid freed inside that
// window can be recycled onto an unrelated process — which then answers the liveness
// poll and takes the SIGKILL. Identity is therefore re-checked on every poll, so the
// escalation can only ever land on the process that was scanned.
func terminate(pid int, scanned time.Time) bool {
	if scannedProcessGone(pid, scanned) {
		return true
	}
	_ = reapSignal(pid, syscall.SIGTERM)
	if awaitGone(pid, scanned, reapTermGrace) {
		return true
	}
	_ = reapSignal(pid, syscall.SIGKILL)
	return awaitGone(pid, scanned, reapKillGrace)
}

// scannedProcessGone reports that the process the scan recorded is no longer running:
// either the pid is unused, or it now belongs to something else. Both mean the target
// is gone, and both mean it must not be signalled again.
func scannedProcessGone(pid int, scanned time.Time) bool {
	if !reapAlive(pid) {
		return true
	}
	return !stillTheScannedProcess(pid, scanned)
}

// awaitGone polls until the scanned process is gone or the budget runs out. It checks
// before it ever sleeps, so a process that died on the first signal costs nothing.
func awaitGone(pid int, scanned time.Time, budget time.Duration) bool {
	for waited := time.Duration(0); ; waited += reapPoll {
		if scannedProcessGone(pid, scanned) {
			return true
		}
		if waited >= budget {
			return false
		}
		reapSleep(reapPoll)
	}
}

// reapf writes one line of the reaper's output.
//
// Write errors are dropped on purpose, and the helper exists so that is stated once
// rather than as fifteen bare assignments. The destination is a terminal or a test
// buffer; a reaper that abandoned a kill half-done because its stdout had gone away
// would leave exactly the split state — server dead, agents alive — that the
// verify-the-children step exists to prevent.
func reapf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// humanAgeSince renders how long ago t was, reusing doctor's formatting so the reap
// prompt and the doctor row date the same server the same way.
func humanAgeSince(t time.Time) string {
	return doctor.HumanAge(time.Since(t))
}

func init() {
	reapCmd.Flags().BoolVar(&reapKillFlag, "kill", false,
		"stop the servers listed, confirming each (without this, reap only reports)")
	reapCmd.Flags().BoolVar(&reapAllFlag, "all", false,
		"also stop servers that are still reachable, which existing tmux commands can already stop")
	reapCmd.Flags().BoolVar(&reapYesFlag, "yes", false,
		"skip the per-server confirmation, for scripts; a server a client is still connected to is then left alone")
}
