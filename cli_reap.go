package main

import (
	"bufio"
	"context"
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
			"prompt, for scripts). A server whose reachability could not be determined — tmux\n" +
			"missing, a probe that could not run — is never killed, with or without --all,\n" +
			"because nothing about it has been established.\n\n" +
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

	res := reapCheck(ctx)
	reapf(w, "%s", doctor.RenderOrphans(res))

	if !opts.kill {
		if len(res.Servers) > 0 {
			reapf(w, "\nnothing was killed. Run `%s reap --kill` to stop the unreachable ones.\n", binName)
		}
		return nil
	}
	if !res.Supported {
		return fmt.Errorf("cannot reap on this platform: finding a server whose socket no longer resolves needs a process inventory (Linux only)")
	}

	targets := reapTargets(res.Servers, opts.all)
	if len(targets) == 0 {
		reapf(w, "\nnothing to kill.\n")
		return nil
	}

	reapf(w, "\n")
	reader := bufio.NewReader(in)
	var killed, skipped, survived int
	for _, s := range targets {
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
	if survived > 0 {
		return fmt.Errorf("%d server(s) survived SIGKILL", survived)
	}
	return nil
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
// been established about it, and absence of an answer must never mean "safe to act":
// when tmux cannot be run, the ambient live server cannot be excluded either, so the
// unknown rows may be the running fleet.
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

// confirmReap names everything that dies with this server and asks. Defaults to no:
// anything but an explicit yes leaves the server alone.
func confirmReap(w io.Writer, in *bufio.Reader, s tmux.OrphanServer) bool {
	reach := "unreachable"
	if s.Reachable {
		reach = "reachable"
	}
	reapf(w, "pid %d  socket %s  up %s  %s\n",
		s.PID, s.Socket, humanAgeSince(s.Started), reach)
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
	if err != nil && line == "" {
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
	if !terminate(s.PID) {
		reapf(w, "  ⚠ pid %d survived SIGTERM and SIGKILL\n", s.PID)
		return reapSurvived
	}
	reapf(w, "  killed pid %d\n", s.PID)

	var survivors int
	for _, kid := range s.Children {
		if !reapAlive(kid.PID) {
			continue
		}
		// A child's pid was read before its parent's death and is staler by exactly
		// that much, so it gets the same guard — a recycled pid is just as alive.
		if !stillTheScannedProcess(kid.PID, kid.Started) {
			reapf(w, "    left pid %d alone: no longer the process that was scanned\n", kid.PID)
			continue
		}
		if !terminate(kid.PID) {
			reapf(w, "    ⚠ child pid %d (%s) survived SIGTERM and SIGKILL\n", kid.PID, kid.Comm)
			survivors++
			continue
		}
		reapf(w, "    killed child pid %d (%s)\n", kid.PID, kid.Comm)
	}
	if survivors > 0 {
		return reapSurvived
	}
	if len(s.Children) > 0 {
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
// gone. SIGTERM goes first so a tmux server can exit cleanly and run its hooks.
func terminate(pid int) bool {
	if !reapAlive(pid) {
		return true
	}
	_ = reapSignal(pid, syscall.SIGTERM)
	if awaitGone(pid, reapTermGrace) {
		return true
	}
	_ = reapSignal(pid, syscall.SIGKILL)
	return awaitGone(pid, reapKillGrace)
}

// awaitGone polls until pid is gone or the budget runs out. It checks before it ever
// sleeps, so a process that died on the first signal costs nothing.
func awaitGone(pid int, budget time.Duration) bool {
	for waited := time.Duration(0); ; waited += reapPoll {
		if !reapAlive(pid) {
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
		"skip the per-server confirmation, for scripts")
}
