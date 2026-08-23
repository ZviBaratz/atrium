package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// The blank relaunch (#712). Atrium relaunches a dead agent with its resume flag, and
// for every agent but claude nothing has asked whether there is a conversation to
// resume — the transcript adapter that answers that question exists only for claude,
// and ResumeProbe answers a different one (does the binary support the flag). Today agy,
// codex and gemini all survive the flag with nothing to resume, driven and recorded
// beside each adapter's Resume; RepairResumingLaunch is what keeps a vendor changing
// its mind from costing a session.
//
// Every fixture here runs the whole launch path — Resume → recreateSession →
// startResuming → tmux.Session.start — against the fake tmux server, because the claim
// is about which command each launch carried, and only the real path chooses that.

// dyingResumingInstance builds a paused instance whose worktree is still on disk and
// whose claude transcript makes startResuming elect `--continue`, resumes it, and leaves
// the resulting session dead: the fake kills any launch carrying the resume flag. That
// is the state the poll loop reaches RepairResumingLaunch in.
//
// Paused with a materialized worktree is the park that reaches recreateSession without
// re-running Worktree.Setup, so the launch is the only thing under test.
func dyingResumingInstance(t *testing.T) (*Instance, *fakeTmux) {
	t.Helper()
	fake := newFakeTmux(t, "")
	fake.dieOnAttachWhenLaunchContains("--continue")
	inst := resumableClaudeInstance(t, fake, fake.exec())
	require.NoError(t, inst.Resume())
	require.False(t, fake.sessionExists(), "the fixture must leave the resuming launch dead")
	return inst, fake
}

// resumableClaudeInstance wires a paused, transcript-bearing claude session to fake,
// answering tmux commands through cmdExec (fake.exec() unless a test needs to script
// the probe itself).
func resumableClaudeInstance(t *testing.T, fake *fakeTmux, cmdExec cmd_test.MockCmdExec) *Instance {
	t.Helper()
	wt := newTestWorktree(t)
	cfgDir := t.TempDir()
	writeClaudeTranscript(t, cfgDir, wt.GetWorktreePath())
	ts := tmux.NewSessionWithDeps(context.Background(), "resumed", "claude", fake, cmdExec)
	return &Instance{
		ident: identity{title: "resumed"}, status: Paused, started: true, Program: "claude",
		claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts,
	}
}

// launchedPrograms renders each new-session command line as one string, which is the
// form the claim is about: the program is a single trailing argv element.
func launchedPrograms(fake *fakeTmux) []string {
	var out []string
	for _, argv := range fake.newSessionArgvs() {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// The repair itself: a resuming launch that died at birth comes back blank. This is
// #699's failure — the agent exits, tmux tears the session down, and the session is
// parked with the worktree Atrium had just rebuilt removed — made survivable for every
// agent, without modelling any vendor's on-disk conversation store.
func TestRepairResumingLaunch_RelaunchesBlankWhenTheResumingLaunchDied(t *testing.T) {
	inst, fake := dyingResumingInstance(t)

	require.True(t, inst.RepairResumingLaunch(15*time.Second))

	launched := launchedPrograms(fake)
	require.Len(t, launched, 2, "the dead resuming launch must be relaunched exactly once")
	require.Contains(t, launched[0], "--continue", "the first launch is the resuming one")
	require.NotContains(t, launched[1], "--continue",
		"and the repair must be blank — relaunching the same command would die the same way")
	require.True(t, fake.sessionExists(), "the repair must leave a live session behind")
	require.Equal(t, Running, inst.GetStatus(), "and a session that was never parked")
	// Weak on its own — the fixture's own Resume() already wrote Running, so this passes
	// for an implementation that leaves the status untouched. What the repair owes the row
	// is pinned in TestRepairResumingLaunch_WritesRunningOverTheDeadAgentsStatus.

	resumed, known := inst.ResumedConversation()
	require.False(t, resumed, "the conversation did not come back, and the restore notice reads this")
	require.True(t, known, "claude has a transcript adapter, so 'started fresh' is known rather than a guess")
}

// The row is not refreshed by the relaunch on its own: applyMetadataResults skips a
// sessionLost result, so whatever the dying agent last painted is what the row still
// shows. The repair therefore has to write the status its relaunch implies, and it has to
// write it BEFORE arming the unread suppression.
//
// Both halves are asserted because both fail silently. Without the write, a brand-new
// agent boots under a row advertising the dead one's finished turn. Without the ordering,
// the arm is never consumed — ArmReadySuppression is a one-shot spent by an edge INTO
// Ready, and arming on a row that is already Ready leaves it sitting there to eat some
// later genuine turn-end instead: no ding, no unread glyph, skipped by NextUnread.
func TestRepairResumingLaunch_WritesRunningOverTheDeadAgentsStatus(t *testing.T) {
	inst, _ := dyingResumingInstance(t)
	// The state the poll loop actually reaches the repair in: the agent finished a turn,
	// the user saw it, and then the pane died.
	inst.SetStatus(Ready)
	inst.MarkSeen()

	require.True(t, inst.RepairResumingLaunch(15*time.Second))
	require.Equal(t, Running, inst.GetStatus(),
		"a fresh agent is booting, and the row must not go on advertising the dead one's Ready")

	// The boot settle. It must spend the arm rather than flag unread...
	inst.SetStatus(Ready)
	require.False(t, inst.Unread(), "the post-boot idle is a boot artifact, not a finished turn")
	// ...and it must actually spend it: an arm left standing is the one that eats a real
	// turn-end later, which is the failure the ordering above exists to prevent.
	inst.mu.RLock()
	dangling := inst.suppressNextUnread
	inst.mu.RUnlock()
	require.False(t, dangling, "the suppression belongs to this boot, not to whatever comes next")
}

// There must be a worktree git still recognises, not merely a directory that stats.
// WorkingDirGone counts only fs.ErrNotExist, so a base repo that moved or was deleted
// leaves the tree on disk with a dangling .git file — and an agent launched there fails
// every git operation it and the diff worker make. recoverInPlace asks the same question
// for the same reason; the park, which keeps the branch, is what this case belongs to.
func TestRepairResumingLaunch_RefusesAnOrphanedWorktree(t *testing.T) {
	inst, fake := dyingResumingInstance(t)
	// Break the link to the base repo without touching the directory, so WorkingDirGone
	// still reports false and only the worktree check can catch it.
	require.NoError(t, os.RemoveAll(filepath.Join(inst.WorkingDir(), ".git")))

	require.False(t, inst.RepairResumingLaunch(15*time.Second))
	require.Len(t, launchedPrograms(fake), 1, "the directory is still there; what is gone is the worktree")
}

// Once per launch. The blank relaunch carries no resume flag, so there is nothing left
// for a second repair to change — and a session that keeps dying must reach the park
// rather than be relaunched every tick (the #270 dead-end).
func TestRepairResumingLaunch_RepairsOnlyOnce(t *testing.T) {
	inst, fake := dyingResumingInstance(t)
	require.True(t, inst.RepairResumingLaunch(15*time.Second))
	// The blank agent dies too, so the second call meets a session that really is gone.
	// Without that the refusal below would prove only that the repair left something
	// alive, and a repair that never cleared its flag would pass just as happily.
	fake.killSession()

	require.False(t, inst.RepairResumingLaunch(15*time.Second),
		"the relaunch it already made carried no resume flag, so there is nothing left to repair")
	require.Len(t, launchedPrograms(fake), 2, "and nothing new was launched")
}

// A launch that carried no resume flag is never repaired, whatever happened to its pane:
// the repair's only move is to drop the flag, and there is none to drop. Here the flag is
// absent because the transcript gate elected a blank start — claude with no conversation,
// the case #201 added — so a relaunch would be a second identical launch.
func TestRepairResumingLaunch_RefusesALaunchThatCarriedNoResumeFlag(t *testing.T) {
	wt := newTestWorktree(t)
	fake := newFakeTmux(t, "")
	fake.dieOnAttach()
	ts := tmux.NewSessionWithDeps(context.Background(), "blank", "claude", fake, fake.exec())
	// No transcript written, so HasResumable reports (false, true) and startResuming
	// elects the blank launch.
	inst := &Instance{
		ident: identity{title: "blank"}, status: Paused, started: true, Program: "claude",
		claudeConfigDir: t.TempDir(), gitWorktree: wt, tmuxSession: ts,
	}
	require.NoError(t, inst.Resume())

	require.False(t, inst.RepairResumingLaunch(15*time.Second))
	require.Len(t, launchedPrograms(fake), 1, "a blank launch has no flag to drop")
}

// The same refusal reached the other way: an agent with no resume support at all. Its
// launch IS the plain program — tmux.StartContinue reports that nothing was resumed —
// so a dead pane is the park's business, not this repair's.
func TestRepairResumingLaunch_RefusesAnAgentWithNoResumeSupport(t *testing.T) {
	wt := newTestWorktree(t)
	fake := newFakeTmux(t, "")
	fake.dieOnAttach()
	ts := tmux.NewSessionWithDeps(context.Background(), "plain", "aider", fake, fake.exec())
	inst := &Instance{
		ident: identity{title: "plain"}, status: Paused, started: true, Program: "aider",
		gitWorktree: wt, tmuxSession: ts,
	}
	require.NoError(t, inst.Resume())

	require.False(t, inst.RepairResumingLaunch(15*time.Second))
	require.Len(t, launchedPrograms(fake), 1, "aider has no resume flag, so this is not the repair's business")
}

// A session that ran and then died did not die OF its launch. The user quit the agent,
// or the machine did — relaunching that is a resurrection nobody asked for, and the park
// is what the death is supposed to produce.
func TestRepairResumingLaunch_RefusesASessionThatDiedLongAfterItsLaunch(t *testing.T) {
	inst, fake := dyingResumingInstance(t)
	inst.mu.Lock()
	inst.startedAt = time.Now().Add(-time.Hour)
	inst.mu.Unlock()

	require.False(t, inst.RepairResumingLaunch(15*time.Second),
		"an hour-old launch is outside the crash-at-launch window whatever flag it carried")
	require.Len(t, launchedPrograms(fake), 1)
}

// And there must be somewhere to relaunch into. A working directory that has gone is the
// park's case — it commits nothing, removes nothing, and leaves the branch — so the
// repair has to decline rather than launch an agent into a path that is not there.
func TestRepairResumingLaunch_RefusesWhenTheWorkingDirectoryIsGone(t *testing.T) {
	inst, fake := dyingResumingInstance(t)
	require.NoError(t, os.RemoveAll(inst.WorkingDir()))

	require.False(t, inst.RepairResumingLaunch(15*time.Second))
	require.Len(t, launchedPrograms(fake), 1)
}

// A parked session has no agent by definition, and its worktree is normally gone with
// it. Nothing in the poll loop offers one here — it skips Paused instances — so this is
// the guard that keeps that a property of this function rather than of its caller.
func TestRepairResumingLaunch_RefusesAPausedSession(t *testing.T) {
	inst, fake := dyingResumingInstance(t)
	inst.SetStatus(Paused)

	require.False(t, inst.RepairResumingLaunch(15*time.Second))
	require.Len(t, launchedPrograms(fake), 1)
}

// An INCONCLUSIVE probe must not authorise the repair. The pane may be dead — or the
// server may be up with the agent on it and simply unreachable (a socket that cannot be
// opened for any reason but its absence), in which case the close-and-relaunch below
// would kill a live agent and start a second over it.
//
// This is why RepairResumingLaunch asks Session.Gone rather than !DoesSessionExist:
// that predicate folds every indeterminate answer into "dead", and swapping it in here
// is a mutation this test catches and the tmux-level Gone tests do not.
func TestRepairResumingLaunch_RefusesWhenLivenessCannotAnswer(t *testing.T) {
	fake := newFakeTmux(t, "")
	fake.dieOnAttachWhenLaunchContains("--continue")
	inner := fake.exec()
	var sealed atomic.Bool
	// The fake answers everything until the resume has happened; from then on every
	// has-session reports a socket tmux could not open, which is not an answer.
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if sealed.Load() && slices.Contains(c.Args, "has-session") {
				return fmt.Errorf("error connecting to /tmp/sock (Permission denied)")
			}
			return inner.RunFunc(c)
		},
		OutputFunc: inner.OutputFunc,
	}
	inst := resumableClaudeInstance(t, fake, cmdExec)
	require.NoError(t, inst.Resume())
	sealed.Store(true)

	require.False(t, inst.RepairResumingLaunch(15*time.Second),
		"an unreachable socket is not a dead pane; relaunching over one risks a second agent")
	require.Len(t, launchedPrograms(fake), 1)
}
