package main

import (
	"context"
	"fmt"
	"io"
	"time"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/retire"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
)

// The two retirement verbs an agent may run (#835). Before these, retiring a session
// was a TUI action only, so a session orchestrating others could open sessions
// indefinitely and close none — and the cost of never closing one is not only a row in
// a list. Every start brings every stored non-paused session back online: session
// bringOnline reattaches the ones whose pane survived and launches an agent for the
// rest, and newRecoveryBudget rations that second group only when max_sessions is
// unset, so an explicit value — an explicit unlimited included — opts out of the
// staging entirely.
//
// Neither verb is scoped to sessions the caller created, and that is a decision rather
// than an omission. What makes a teardown destructive is the TARGET's tree state, not
// the caller's relation to it: a caller's own worker can be sitting on unpushed
// commits while a stranger's session is provably clean, so parentage would refuse the
// safe case and permit the destructive one. The gate below is on the axis that
// correlates with harm. Nothing here could enforce parentage anyway — the only session
// identity available is $ATRIUM_SESSION, a tmux environment variable the agent can set
// to anything.
//
// kill is gated and pause is not, because they risk different things. A kill deletes
// the branch, so it must establish that nothing is at risk; a pause keeps the branch
// and commits what was uncommitted, so it destroys nothing to gate on. That asymmetry
// is what makes pause the escape valve for everything the gate refuses — an
// orchestrator whose worker has unpushed work can still reclaim it.
var (
	killPathFlag  string
	killWaitFlag  time.Duration
	pausePathFlag string
	pauseWaitFlag time.Duration

	killCmd = &cobra.Command{
		Use:   "kill <session>",
		Short: "Retire a session whose work is provably safe to discard",
		Long: "Removes a session's worktree and deletes its branch, the same teardown the TUI's\n" +
			"kill performs — including the undo journal, so a kill stays reversible in the TUI\n" +
			"for as long as that journal retains its ref.\n\n" +
			"It refuses unless safety is ESTABLISHED rather than merely un-contradicted: the\n" +
			"tree is recomputed at call time and must report no uncommitted changes and no\n" +
			"unpushed commits, and the agent must be idle. A session whose numbers cannot be\n" +
			"computed is refused rather than read as clean, which is why a paused or direct\n" +
			"session — neither of which has a worktree to read — is refused too, and so is a\n" +
			"session whose agent shows nothing in its pane to say a turn is running (aider,\n" +
			"and any program Atrium has no adapter for). The refusal names the condition that\n" +
			"failed. There is no --force: use the TUI, or `atrium pause`, which keeps the\n" +
			"branch and so needs no gate.\n\n" +
			"Delivery is asynchronous, on `atrium new`'s terms: the request is spooled to the\n" +
			"data directory and the running Atrium executes it. Use --wait to block until it\n" +
			"has actually been acted on.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			return runRetire(cmd.OutOrStdout(), cmd.ErrOrStderr(), outbox.ModeKill,
				liveRetireProbe(cmd.Context(), cmd2.MakeExecutor()), args[0], killPathFlag, killWaitFlag)
		},
	}

	pauseCmd = &cobra.Command{
		Use:   "pause <session>",
		Short: "Park a session: stop its agent and free its worktree, keeping the branch",
		Long: "Stops a session's agent and removes its worktree, keeping the branch — the same\n" +
			"park the TUI performs, committing whatever was uncommitted as a marker Atrium\n" +
			"unwinds when the session resumes.\n\n" +
			"It is not gated the way `atrium kill` is, because it destroys nothing: a session\n" +
			"with uncommitted or unpushed work can be paused, which makes this the verb to\n" +
			"reach for when a kill is refused. It does refuse what a park cannot do — an\n" +
			"already-paused session, and a direct (non-git) session, which runs in your own\n" +
			"checkout with no worktree to free.\n\n" +
			"Delivery is asynchronous, on `atrium new`'s terms. Use --wait to block until the\n" +
			"session has actually been parked.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			return runRetire(cmd.OutOrStdout(), cmd.ErrOrStderr(), outbox.ModePause,
				liveRetireProbe(cmd.Context(), cmd2.MakeExecutor()), args[0], pausePathFlag, pauseWaitFlag)
		},
	}
)

// retireProbe gathers the two facts retire.Gate decides on.
//
// Function fields rather than a call into git and tmux directly, because the two
// halves fail in ways no sandbox can stage together: a fake gives every refusal
// branch a hermetic test, while liveRetireProbe's halves are pinned separately
// against a real worktree and a faked tmux. The decision they feed stays in
// internal/retire, shared with the drain, so the two sides of the gate cannot
// disagree about what the numbers mean.
type retireProbe struct {
	stats func(session.InstanceData) *git.DiffStats
	busy  func(session.InstanceData) (bool, error)
}

// liveRetireProbe is the real thing: git against the session's own worktree, tmux
// against its own pane.
func liveRetireProbe(ctx context.Context, exec cmd2.Executor) retireProbe {
	// One config read for both halves, and only for the branch prefix a rehydrated
	// worktree needs. loadStoredConfig rather than config.LoadConfig, for the reason
	// that function documents: this process must not write or sweep anything in a data
	// dir a live TUI owns.
	prefix := loadStoredConfig().BranchPrefix
	return retireProbe{
		stats: func(d session.InstanceData) *git.DiffStats {
			return statsFromWorktree(ctx, d, prefix)
		},
		busy: func(d session.InstanceData) (bool, error) {
			return busyFromPane(ctx, exec, d)
		},
	}
}

// statsFromWorktree recomputes the branch-level numbers a kill would destroy, by
// running git against the worktree as state.json recorded it.
//
// Recomputed, never decoded, and that is the whole reason this exists rather than
// reading DiffStats off the stored instance. The stored numbers have no way to say "I
// don't know": Dirty is a plain bool, so a session whose stats were never computed
// decodes as clean, and `atrium ls --help` says the quiet part — nothing refreshes
// while no TUI is running, which is exactly the condition an agent-driven kill runs
// under. RepoStats is the cheap half of Diff (rev-list plus status --porcelain, no
// untracked walk) and its own documentation names it as the half every destructive
// confirmation reads, so this asks for precisely the numbers the TUI's kill dialog
// asks for and no more.
//
// Failures arrive as stats.Error, which the gate reports as unestablished. Nothing is
// returned nil.
func statsFromWorktree(ctx context.Context, d session.InstanceData, branchPrefix string) *git.DiffStats {
	w := d.Worktree
	return git.NewWorktreeFromStorage(ctx, w.RepoPath, w.WorktreePath, w.SessionName,
		w.BranchName, w.BaseCommitSHA, w.BaseRef, w.IsExistingBranch, branchPrefix).RepoStats()
}

// busyFromPane reports whether the session's agent is mid-turn, by capturing its pane
// and asking the adapter its recorded program resolves to.
//
// Same capture `atrium peek` uses, and the same classification the poll loop applies
// — HasBusyMarker for a turn in flight, BackgroundWorkVisible for sub-agents still
// running after one ended. Both, because the second is the case that reads as finished
// and is not.
//
// A capture that fails is an error rather than "not busy". The distinction is the one
// the gate exists to draw: a pane nobody could read is not an idle pane, and answering
// false would clear a kill on the strength of a missing answer.
func busyFromPane(ctx context.Context, exec cmd2.Executor, d session.InstanceData) (bool, error) {
	// The adapter first, and before the capture, because an adapter that cannot
	// recognise a turn at all makes the capture useless rather than merely
	// inconclusive. agent.Resolve never returns nil — it falls back to Generic — so the
	// case that exists is an adapter that resolves and declares nothing to look for.
	// aider is one and Generic is the other, and for both HasBusyMarker is
	// unconditionally false, which the gate would read as "idle, go ahead".
	adapter := agent.Resolve(d.Program)
	if !adapter.CanDetectBusy() {
		return false, fmt.Errorf("nothing about a %q pane shows whether a turn is running, "+
			"so %q cannot be established as idle — pause it instead, or kill it from the TUI",
			d.Program, d.Title)
	}
	content, err := tmux.CapturePaneForSession(ctx, exec, tmuxSessionName(d), tmux.CaptureOpts{})
	if err != nil {
		return false, fmt.Errorf("could not read %q's pane to tell whether its agent is working: %w", d.Title, err)
	}
	return adapter.HasBusyMarker(content) || adapter.BackgroundWorkVisible(content), nil
}

// runRetire spools a retirement for the session named by selector.
//
// It never writes state.json, for the reason runSend documents: that file has exactly
// one writer at any instant and both writers rewrite it whole, so an outside append
// would be clobbered rather than merged. Removing an instance from it is emphatically
// such a write, which is why no headless process can retire a session directly and why
// this is a producer like `send` and `new`.
//
// The consequence is worth being explicit about rather than discovering: the autoyes
// daemon does not drain the outbox, so a retirement lands only while a TUI is alive
// and its poll loop is not parked. That converts "gated on a human acting" into
// "gated on a TUI running" — an improvement over a TUI-only teardown, not a full
// decoupling from it.
func runRetire(out, errOut io.Writer, mode outbox.Mode, probe retireProbe, selector, path string, wait time.Duration) error {
	if wait < 0 {
		// Cobra parses "--wait -5s" happily; left to the wait > 0 test below it would
		// silently mean "do not wait", so a caller that fat-fingered a sign would be told
		// the session was queued for teardown and never learn what became of it. Same
		// guard, same reason, as runSend's and runNew's.
		return fmt.Errorf("--wait %s is negative; pass a positive duration", wait)
	}
	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	target, err := resolveSession(instances, selector, path)
	if err != nil {
		return err
	}
	if err := admits(mode, target); err != nil {
		return err
	}
	if mode == outbox.ModeKill {
		if err := establishSafety(probe, target); err != nil {
			return err
		}
	}

	spooled, err := outbox.WriteRetire(outbox.Retire{
		Title:    target.Title,
		Path:     target.Path,
		TmuxName: target.TmuxName,
		Mode:     mode,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "queued %s for %s\n", mode, target.Title)

	warnSpoolWaiting(errOut, mode.Gerund(), wait > 0)
	if wait > 0 {
		return waitForRetirement(spooled, mode, wait)
	}
	return nil
}

// admits refuses a target whose state the verb cannot act on, before anything is
// probed or spooled. Both refusals name the state rather than letting it surface as a
// git error from deep inside a probe, or as a rejection receipt a caller has to wait
// for.
//
// A kill needs a worktree to read, and neither a direct session (which never had one)
// nor a paused one (whose worktree is gone, its work folded into the branch) has one.
// Refusing both is the same rule as the gate's, one step earlier: safety that cannot
// be established is not safety. The cost is that pause-then-kill has to go through the
// TUI, which is the conservative direction.
//
// A pause refuses what Instance.Pause refuses — a direct session runs in the user's
// own checkout, so parking it stops a live agent and reclaims no disk or branch in
// exchange — plus a session that is already parked, where there is nothing left to do.
func admits(mode outbox.Mode, d session.InstanceData) error {
	if d.Direct {
		return fmt.Errorf("%q is a direct (non-git) session: it has no worktree or branch, "+
			"so there is nothing to %s", d.Title, mode)
	}
	if d.Status == session.Paused {
		if mode == outbox.ModePause {
			return fmt.Errorf("%q is already paused", d.Title)
		}
		return fmt.Errorf("%q is paused, so its worktree is gone and what a kill would "+
			"discard cannot be established — resume it, or kill it from the TUI", d.Title)
	}
	return nil
}

// establishSafety runs the gate and turns a refusal into the command's error.
//
// The probe's own failure is a refusal too, and deliberately not folded into the
// unestablished verdict: a tmux server that is not running and a tree with unpushed
// commits are different things for a caller to do something about, so the error says
// which it hit.
func establishSafety(probe retireProbe, d session.InstanceData) error {
	busy, err := probe.busy(d)
	if err != nil {
		return fmt.Errorf("refusing to kill %q: %w", d.Title, err)
	}
	if v := retire.Gate(probe.stats(d), busy); !v.Allowed() {
		return fmt.Errorf("refusing to kill %q: %s", d.Title, v.Reason())
	}
	return nil
}

// waitForRetirement blocks until the spooled retirement has been accounted for.
//
// No in-flight companion: the retire spool has no claim step, so the record's
// disappearance is the whole signal. The drain unlinks it whether it acted or refused,
// which is why a refusal leaves a receipt and why awaitSpool's sampling order — not
// this function — is what keeps a refusal from reading as a teardown.
func waitForRetirement(record string, mode outbox.Mode, timeout time.Duration) error {
	return awaitSpool(record, "", timeout, spoolWaitCopy{
		refused: fmt.Sprintf("atrium refused to %s the session", mode),
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s and no atrium TUI acted on the %s; "+
				"it is still queued in the outbox", timeout, mode),
				"A running atrium acts on it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does")
		},
	})
}
