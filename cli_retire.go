package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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
// the branch, so it must establish that nothing is at risk; a pause keeps the branch and
// commits what was uncommitted, so nothing git TRACKS is at risk — and the gate's whole
// vocabulary is tracked work. That asymmetry is what makes pause the escape valve for
// everything the gate refuses: an orchestrator whose worker has unpushed work can still
// reclaim it.
//
// "Nothing to gate on" is not "nothing is lost", and the difference is worth stating
// because three doc sites once said the shorter thing. A pause removes the worktree, so
// files git ignores that lived in it go with it — pauseConfirmMessage is the copy the TUI
// is required to show for the same teardown, and it calls that loss unconditional. The
// verb prints it rather than gating on it: measuring the loss means a per-session
// `git status --ignored`, and a park nobody warned about is the failure mode, not a park
// somebody chose.
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
			"unpushed commits, and the agent must be idle. A tree whose numbers could not be\n" +
			"MEASURED is refused rather than read as clean — which is why a paused, starting\n" +
			"or direct session is refused too, none of them having a worktree to read, and so\n" +
			"is a session whose agent shows nothing in its pane to say a turn is running\n" +
			"(aider, and any program Atrium has no adapter for). The refusal names the\n" +
			"condition that failed. There is no --force: use the TUI, or `atrium pause`, which\n" +
			"keeps the branch and so needs no gate.\n\n" +
			"The session is addressed by name — its title, its tmux name, or either in any\n" +
			"case — and not by a substring of one, which `peek` and `send` accept and a verb\n" +
			"that deletes a branch should not. It also will not retire the session it is run\n" +
			"from.\n\n" +
			"Delivery is asynchronous, on `atrium new`'s terms: the request is spooled to the\n" +
			"data directory and the running Atrium executes it, re-checking every condition\n" +
			"first. Use --wait to block until the teardown has reported back; a refusal then\n" +
			"arrives as a non-zero exit naming its reason.",
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
			"It is not gated the way `atrium kill` is, because nothing git tracks is at risk:\n" +
			"a session with uncommitted or unpushed work can be paused, which makes this the\n" +
			"verb to reach for when a kill is refused.\n\n" +
			"It is not free, though. Freeing the worktree deletes the directory, so files git\n" +
			"ignores that lived in it are gone for good — a local .env, a build cache, a\n" +
			"session's installed dependencies — and resume rebuilds the worktree without them\n" +
			"(only carry_files entries are re-seeded). The TUI's pause dialog warns about the\n" +
			"same loss; this command prints it.\n\n" +
			"It does refuse what a park cannot do — an\n" +
			"already-paused session, a direct (non-git) session, which runs in your own\n" +
			"checkout with no worktree to free, and one still starting up, where the park\n" +
			"would race the setup it is removing.\n\n" +
			"It addresses a session by name rather than by a substring, and will not park the\n" +
			"session it is run from, for the reasons `atrium kill --help` gives.\n\n" +
			"Delivery is asynchronous, on `atrium new`'s terms. Use --wait to block until the\n" +
			"park has reported back; a failure then arrives as a non-zero exit naming it.",
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
	// panes lists a session's tmux pane ids, for the self-retirement guard alone. It is
	// the only identity a rename cannot move: tmux mints a pane id once and never
	// reissues it, while the session NAME the caller's process was launched with is
	// frozen in its environment the moment it starts.
	//
	// nil means no pane evidence is available, which leaves the guard on its name
	// comparison. That is the shape every hermetic test uses — none of them is a process
	// running inside the session it is retiring — and the shape of a machine with no
	// tmux server to ask.
	panes func(session.InstanceData) []string
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
		panes: func(d session.InstanceData) []string {
			ids, err := tmux.PaneIDsForSession(ctx, exec, tmuxSessionName(d))
			if err != nil {
				// Not an error the caller can act on, and not a reason to refuse: a
				// session with no panes to list is one nothing can be running inside.
				// The guard falls back to its name comparison.
				log.WarningLog.Printf("could not list %q's panes for the self-retirement check: %v", d.Title, err)
				return nil
			}
			return ids
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
// A failure it could not measure reports itself rather than reading as a clean tree, and
// that takes two fields rather than one. stats.Error carries only the failures that stop
// computation before it starts — in practice an unset base commit — because
// computeRepoStats is best-effort by design and swallows every git subprocess failure
// into a zero value: a repository moved out from under the worktree, or a `git status`
// that times out on a cold mount, would otherwise arrive reporting no error, no changes
// and nothing unpushed. stats.BranchStatsMeasured is what separates that from a tree
// that was measured and found clean, and retire.Gate reads both. Nothing is returned nil.
func statsFromWorktree(ctx context.Context, d session.InstanceData, branchPrefix string) *git.DiffStats {
	w := d.Worktree
	return git.NewWorktreeFromStorage(ctx, w.RepoPath, w.WorktreePath, w.SessionName,
		w.BranchName, w.BaseCommitSHA, w.BaseRef, w.IsExistingBranch, branchPrefix).RepoStats()
}

// busyFromPane reports whether the session's agent is mid-turn, by capturing its pane
// and asking the adapter its recorded program resolves to.
//
// Same capture `atrium peek` uses, normalized through the same tmux.CleanForDetection the
// poll's own reads go through, and then all three of the adapter's single-capture
// signals:
//
//   - HasBusyMarker, for the footer marker a turn in flight lights.
//   - LiveSpinner, for the turns where that marker is absent. It is not a belt-and-braces
//     second opinion, it is the only reader of the states registry.go documents the
//     spinner as existing for: claude's footer hint is lit off its narrowest notion of
//     busy and drops mid-turn, and the footer truncates on overflow, so a narrow pane
//     loses the marker while the turn runs. Those "fail safe" for the poll, which falls
//     back to content-change detection across ticks; for a one-shot gate they would fail
//     into a cleared teardown. It also covers the adapter shape CanDetectBusy admits and
//     HasBusyMarker cannot see at all — one whose only signal IS the spinner, for which
//     HasBusyMarker short-circuits to false unconditionally.
//   - BackgroundWorkVisible, for sub-agents and shells still running after a turn ended,
//     which is the case that reads as finished and is not.
//
// Where the poll gates the spinner behind an animation frame change and this does not:
// two captures is what tells an animating spinner from one quoted in the scrollback, and
// a one-shot probe has only one capture. The false positive that costs is a refusal to
// kill a session that was idle, which is the direction this is allowed to be wrong in.
//
// A capture that fails is an error rather than "not busy". The distinction is the one
// the gate exists to draw: a pane nobody could read is not an idle pane, and answering
// false would clear a kill on the strength of a missing answer.
//
// One failure is not that, and it is the ordinary end of an agent's life. When the agent
// process exits, tmux takes its window and then its session with it, so `capture-pane`
// fails for a session that is not there rather than one that cannot be read — and there
// is no turn in flight in a session that does not exist. Folding the two together left a
// finished session with no headless way to retire it: the capture refused the kill, and
// once the TUI's own recovery parked the row, retire.Admits refused both verbs after
// that. Only the tmux server's own answer counts, never an inconclusive probe, which is
// the split SessionConfirmedAbsent exists to make.
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
	raw, err := tmux.CapturePaneForSession(ctx, exec, tmuxSessionName(d), tmux.CaptureOpts{})
	if err != nil {
		if tmux.SessionConfirmedAbsent(ctx, exec, tmuxSessionName(d)) {
			return false, nil
		}
		return false, fmt.Errorf("could not read %q's pane to tell whether its agent is working: %w", d.Title, err)
	}
	content := tmux.CleanForDetection(raw)
	if adapter.LiveSpinner != nil && adapter.LiveSpinner(content) {
		return true, nil
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
	// resolveSessionNamed, not resolveSession: a verb that deletes a branch must not
	// resolve a substring of a title, or of the mutable display label. See that
	// function for why the tier is right for peek and send and wrong here.
	target, err := resolveSessionNamed(instances, selector, path)
	if err != nil {
		return err
	}
	if err := refuseSelfRetirement(mode, target, probe.panes); err != nil {
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

	if mode == outbox.ModePause {
		// The one surface a headless park has for the loss the TUI puts in a dialog.
		// pauseConfirmMessage calls it unconditional and this verb is not gated on it, so
		// without this line the only two places that describe `atrium pause` — its help
		// and the guide — would be the only warning, and neither is read at the moment
		// somebody parks a session. stderr, so it cannot corrupt a parsed stdout.
		_, _ = fmt.Fprintln(errOut, "note: parking removes the worktree, so files git ignores "+
			"that live in it (a local .env, a build cache, installed dependencies) are deleted "+
			"for good; resume rebuilds the worktree without them")
	}
	warnSpoolWaiting(errOut, mode.Gerund(), wait > 0)
	if wait > 0 {
		return waitForRetirement(spooled, mode, wait)
	}
	return nil
}

// admits refuses a target whose state the verb cannot act on, before anything is probed
// or spooled, so the refusal names the state rather than surfacing as a git error from
// deep inside a probe or as a receipt the caller has to wait for.
//
// The rule itself lives in internal/retire, shared with the drain, and that sharing is
// the point rather than tidiness. This screen used to be here only: the drain re-ran the
// tree gate and not this one, so a session parked or started in the window between the
// spool and the tick was torn down by the very path whose command refuses it. Both sides
// now ask retire.Admits, and there is one place to read what the states mean.
func admits(mode outbox.Mode, d session.InstanceData) error {
	v := retire.Admits(retireVerb(mode), retire.State{
		Direct:  d.Direct,
		Paused:  d.Status == session.Paused,
		Loading: d.Status == session.Loading,
	})
	if v.Allowed() {
		return nil
	}
	return fmt.Errorf("refusing to %s %q: %s", mode, d.Title, v.Reason())
}

// retireVerb translates the spool's mode into the verb the shared rules take. Two enums
// rather than one because internal/retire must not import the spool — the decision is
// meant to be reachable from a table test with no data dir in sight — and app has its own
// copy of this translation for the same reason.
func retireVerb(mode outbox.Mode) retire.Verb {
	switch mode {
	case outbox.ModeKill:
		return retire.Kill
	case outbox.ModePause:
		return retire.Pause
	default:
		return retire.VerbUnknown
	}
}

// sessionNameEnv is the tmux environment variable Atrium sets in every session's pane to
// that session's own tmux name (session/tmux.Session.start). Read here to recognise the
// caller.
const sessionNameEnv = "ATRIUM_SESSION"

// paneIDEnv is tmux's own answer to "which pane is this process in". tmux sets it in
// every pane it spawns and nothing outside one has it, so an empty value means the
// command was not run from inside a session at all — which is nobody's own session.
const paneIDEnv = "TMUX_PANE"

// refuseSelfRetirement refuses a target that is the session the command is running in.
//
// What it prevents is not hypothetical. `atrium guide` hands agents both verbs and tells
// them to retire a finished session themselves, pause is ungated end to end, and nothing
// else in this path treats the caller's own row as different from any other. Retiring it
// runs kill-session against the pane the agent is running in and `git worktree remove`
// against the directory its shell is cwd'd to: the process that would report what
// happened is the one that died, and a --wait dies with the pane, so the outcome reaches
// nobody.
//
// Two identities are compared, and the order is by strength. The pane id is asked first
// because it is the one a rename cannot move: tmux mints it once and never reissues it,
// while ATRIUM_SESSION is written into the session environment at launch and a deep
// rename changes the session's name without touching it — so a renamed session's agent
// carries a name that matches nothing, and the name comparison alone lets it retire
// itself. TMUX_PANE is tmux's own answer to "which pane is this", set in every pane it
// spawns, and unset outside one.
//
// The name comparison stays as the fallback for the machine with no tmux server to ask,
// and it goes through tmuxSessionName rather than the raw field: TmuxName is absent from
// state written before the field existed, and comparing against an empty string made the
// guard silently pass for every one of those sessions.
//
// Neither is the parentage check the header rejects, and the difference is what makes
// this worth having. Scoping kill rights by who spawned a session cannot be enforced —
// the only identity available is what the caller's own environment says, and the agent
// has a shell to overwrite it with — but an agent that lies about its OWN identity only
// spares itself, so a guard that needs no enforcement is exactly what fits here.
func refuseSelfRetirement(mode outbox.Mode, d session.InstanceData, panes func(session.InstanceData) []string) error {
	refuse := fmt.Errorf("refusing to %s %q: that is its own session, and an agent that "+
		"retires the pane it is running in cannot report what happened — ask another "+
		"session, or retire it from the TUI", mode, d.Title)

	if self := strings.TrimSpace(os.Getenv(paneIDEnv)); self != "" && panes != nil {
		for _, id := range panes(d) {
			if id == self {
				return refuse
			}
		}
	}
	self := strings.TrimSpace(os.Getenv(sessionNameEnv))
	if self != "" && tmuxSessionName(d) == self {
		return refuse
	}
	return nil
}

// establishSafety runs the gate and turns a refusal into the command's error.
//
// The probe's own failure is a refusal too, and deliberately not folded into the
// unestablished verdict: a tmux server that is not running and a tree with unpushed
// commits are different things for a caller to do something about, so the error says
// which it hit.
//
// The tree is measured first, and the order is the same argument Gate makes internally.
// A caller acts on the first reason it is given; a dirty tree is theirs to fix and stays
// true until they do, while an unreadable pane is a fact about the machine. Probing tmux
// first told the owner of a dirty aider session the one thing they could do nothing
// about, and never mentioned the work at risk. It also spends the cheaper of the two
// checks first — no capture at all for a session that was never going to clear.
func establishSafety(probe retireProbe, d session.InstanceData) error {
	stats := probe.stats(d)
	if v := retire.Gate(stats, false); !v.Allowed() {
		return fmt.Errorf("refusing to kill %q: %s", d.Title, v.Reason())
	}
	busy, err := probe.busy(d)
	if err != nil {
		return fmt.Errorf("refusing to kill %q: %w", d.Title, err)
	}
	if v := retire.Gate(stats, busy); !v.Allowed() {
		return fmt.Errorf("refusing to kill %q: %s", d.Title, v.Reason())
	}
	return nil
}

// waitForRetirement blocks until the spooled retirement has been accounted for.
//
// The record's disappearance is the whole signal, and what makes that a truthful signal
// is that the drain does not unlink at dispatch. It claims the record and answers it from
// the teardown's OUTCOME — removed if the session was retired, receipted with the reason
// if it was not — so the two things this can report are the two things that happened. It
// did unlink at dispatch once, which made a success here mean only "a TUI picked this up":
// killIOCmd still refuses a branch checked out in the base repo, and every Instance.Pause
// failure is collected rather than fatal, so both of those exited 0 with the session
// untouched.
//
// No in-flight companion, but there IS an in-flight window and the wording owes it an
// account. The create spool names its window with a claim file so a request mid-creation
// can be described as in progress; here the record itself stays put for that window
// instead, which is what makes a claimed retirement indistinguishable from a queued one
// from the outside. It is bounded by the drain's settle grace, which is generous enough
// to cover a recursive worktree delete on a cold mount — so a timeout cannot assert that
// nothing picked the record up, and does not.
//
// The refusal lead-in is neutral for the same reason. Four different things write a
// receipt to a retirement's record and only one of them is a refusal: the drain's own
// gate verdict, a teardown that partly happened, a dispatch whose outcome never came
// back, and `atrium reset` clearing the spool. Calling all four "refused" told a producer
// its session was untouched when the record it was waiting on had been torn down and only
// half tidied — and told it to go looking for a session that was already gone. Every body
// is a full explanation, so the lead-in only has to name what the sentence is about.
func waitForRetirement(record string, mode outbox.Mode, timeout time.Duration) error {
	return awaitSpool(record, "", timeout, spoolWaitCopy{
		refused: fmt.Sprintf("atrium answered the %s request", mode),
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s and the %s has not been accounted for; "+
				"its record is still in the outbox", timeout, mode),
				"A running atrium acts on it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does. A record that has "+
					"been picked up looks the same from here, so a teardown may be running")
		},
	})
}
