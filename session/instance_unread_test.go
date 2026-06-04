package session

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// TestUnread_FlagsOnTransitionIntoReady asserts the unread bit is edge-triggered:
// a Running→Ready transition flags it (and stamps unreadAt for the dwell floor).
func TestUnread_FlagsOnTransitionIntoReady(t *testing.T) {
	inst := &Instance{Title: "u", status: Running}

	inst.SetStatus(Ready)

	require.True(t, inst.Unread(), "an into-Ready transition must flag unread")
	require.False(t, inst.UnreadAt().IsZero(), "flagging must stamp unreadAt")
}

// TestUnread_ReadyToReadyDoesNotReflag asserts the 500ms poll loop's repeated
// SetStatus(Ready) on an idle session is a no-op for the unread bit: only an
// edge (non-Ready → Ready) flags, so MarkSeen sticks while the session stays idle.
func TestUnread_ReadyToReadyDoesNotReflag(t *testing.T) {
	inst := &Instance{Title: "u", status: Running}
	inst.SetStatus(Ready)
	inst.MarkSeen()

	inst.SetStatus(Ready) // the next idle poll tick

	require.False(t, inst.Unread(), "Ready→Ready must not re-flag a seen session")
}

// TestUnread_ReflagsAfterWorkingPhase asserts a seen session that does another
// turn of work (Ready→Running→Ready) becomes unread again.
func TestUnread_ReflagsAfterWorkingPhase(t *testing.T) {
	inst := &Instance{Title: "u", status: Running}
	inst.SetStatus(Ready)
	inst.MarkSeen()

	inst.SetStatus(Running)
	inst.SetStatus(Ready)

	require.True(t, inst.Unread(), "a new completion after MarkSeen must re-flag")
}

// TestUnread_SuppressionConsumedWithoutFlagging asserts the one-shot suppression:
// the next into-Ready transition is swallowed (synthetic lifecycle transition),
// and the cycle after it flags normally again.
func TestUnread_SuppressionConsumedWithoutFlagging(t *testing.T) {
	inst := &Instance{Title: "u", status: Running}
	inst.ArmReadySuppression()

	inst.SetStatus(Ready)
	require.False(t, inst.Unread(), "the suppressed synthetic Ready must not flag")

	inst.SetStatus(Running)
	inst.SetStatus(Ready)
	require.True(t, inst.Unread(), "suppression is one-shot: the next genuine completion must flag")
}

// TestUnread_SuppressionClearedByObservedWorking asserts that an observed
// working phase (PaneWorking → SetStatus(Running)) cancels a pending
// suppression: the agent demonstrably did new work, so its completion must flag.
func TestUnread_SuppressionClearedByObservedWorking(t *testing.T) {
	inst := &Instance{Title: "u", status: Ready}
	inst.ArmReadySuppression()

	inst.SetStatus(Running) // observed working phase clears the pending suppression
	inst.SetStatus(Ready)

	require.True(t, inst.Unread(), "an observed Running must clear suppression so the completion flags")
}

// TestRecoverInPlace_ArmsReadySuppression asserts that reboot-style recovery
// (agent restarted via --continue) suppresses the post-boot idle settle: the
// recovered session's first Ready is a boot artifact, not new output.
func TestRecoverInPlace_ArmsReadySuppression(t *testing.T) {
	wt := newTestWorktree(t)
	pty := &recordingPtyFactory{}
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls == 1 {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, liveExec)
	inst := &Instance{Title: "sess", status: Running, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()
	require.Equal(t, Running, inst.GetStatus())

	inst.SetStatus(Ready) // the first poll settles the restarted agent to idle
	require.False(t, inst.Unread(), "recovery's post-boot Ready must not flag unread")
}

// TestResume_ArmsReadySuppression asserts that resuming a paused session
// suppresses the post-boot idle settle — the resumed agent shows its old
// conversation, nothing new. Uses a direct (worktree-less) session to exercise
// Resume without git plumbing.
func TestResume_ArmsReadySuppression(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pty := &recordingPtyFactory{}
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		// Resume's own DoesSessionExist and the launch's duplicate-name guard must
		// both report "gone" so the recreate proceeds; the poll after must see it alive.
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls <= 2 {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, liveExec)
	inst := &Instance{Title: "sess", status: Paused, started: true, direct: true, Path: t.TempDir(), tmuxSession: ts}

	require.NoError(t, inst.Resume())
	require.Equal(t, Running, inst.GetStatus())

	inst.SetStatus(Ready) // the first poll settles the resumed agent to idle
	require.False(t, inst.Unread(), "Resume's post-boot Ready must not flag unread")
}
