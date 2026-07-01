package session

import (
	"context"
	"os/exec"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// reattachableInstance builds an instance whose injected tmux session reports as
// existing (has-session succeeds) and whose Restore (attach) succeeds, so reattach
// takes the reattach-success path. saved is the status at save time. HOME is
// redirected to a temp dir because reattach builds tmux commands whose socket/conf
// paths resolve through the config dir under $HOME.
func reattachableInstance(t *testing.T, saved Status) *Instance {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pty := newRecordingPtyFactory(t, nil)
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil }, // has-session succeeds -> session exists
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, aliveExec)
	return &Instance{Title: "sess", status: saved, Program: "claude", tmuxSession: ts}
}

// TestReattach_ArmsSuppressionOnlyWhenSavedReady pins the reattach path that had
// no test through the old FromInstanceData: a surviving session reattaches to
// Running, and ready-suppression is armed ONLY when the session was Ready at save
// time (an idle-at-save session's first synthetic settle must not flag unread; a
// session that was genuinely Running at save has a real first completion).
func TestReattach_ArmsSuppressionOnlyWhenSavedReady(t *testing.T) {
	t.Run("saved Ready arms suppression", func(t *testing.T) {
		inst := reattachableInstance(t, Ready)

		inst.reattach()
		require.True(t, inst.started, "a reattached session is marked started")
		require.Equal(t, Running, inst.GetStatus(), "a surviving session reattaches to Running")

		inst.SetStatus(Ready) // the first poll settles the reattached (idle-at-save) agent
		require.False(t, inst.Unread(), "a saved-Ready reattach must arm suppression so the synthetic Ready doesn't flag")
	})

	t.Run("saved non-Ready does not arm", func(t *testing.T) {
		inst := reattachableInstance(t, Running)

		inst.reattach()
		require.True(t, inst.started)
		require.Equal(t, Running, inst.GetStatus())

		inst.SetStatus(Ready) // the agent was genuinely working at save time; its completion is real
		require.True(t, inst.Unread(), "a non-Ready-at-save reattach must NOT arm; the first real completion flags")
	})
}

// TestReattach_SessionGoneRecoversInPlace asserts reattach routes to recoverInPlace
// when the tmux session no longer exists. With an orphaned worktree, recovery
// cannot relaunch and degrades to Paused (never aborting), and no session is
// relaunched.
func TestReattach_SessionGoneRecoversInPlace(t *testing.T) {
	inst, pty := orphanedWorktreeInstance(t)

	inst.reattach()

	require.True(t, inst.started, "a recovered instance must be marked started")
	require.True(t, inst.Paused(), "a gone session with an orphaned worktree must degrade to Paused")
	require.Empty(t, pty.cmds, "no session should be relaunched when the worktree is gone")
}

// TestReattach_PausedDoesNoIO asserts a paused instance is only marked started —
// reattach must not probe or launch any tmux session (it has one constructed for a
// later Resume, but no live session to reattach).
func TestReattach_PausedDoesNoIO(t *testing.T) {
	pty := newRecordingPtyFactory(t, nil)
	// deadExec would error if reattach touched the session; a paused one must not.
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{Title: "sess", status: Paused, Program: "claude", tmuxSession: ts}

	inst.reattach()

	require.True(t, inst.started, "a paused instance is marked started")
	require.True(t, inst.Paused(), "a paused instance stays Paused — no reattach")
	require.Empty(t, pty.cmds, "a paused instance must not launch or attach any tmux session")
}
