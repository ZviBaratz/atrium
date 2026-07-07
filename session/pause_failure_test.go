package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// startedInstance wires a started, Running instance around a real worktree and a
// dead tmux session, so pause()/RecoverLostSession runs its teardown without a
// live server. HOME is already redirected by newTestWorktree.
func startedInstance(t *testing.T, wt *git.Worktree) *Instance {
	t.Helper()
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", newRecordingPtyFactory(t, nil), deadExec())
	inst := &Instance{Title: "sess", status: Running, Program: "claude", gitWorktree: wt, tmuxSession: ts}
	inst.started = true
	return inst
}

// TestPause_CommitFailureParksPausedAndKeepsWIP asserts the #270 fix: when the
// auto-pause commit fails (here: no git identity), pause must NOT remove the
// worktree — that would destroy the uncommitted WIP — yet must still end Paused
// (so lost-session recovery neither loops nor freezes the row at Running) and
// return the error for one-time surfacing. The WIP stays on disk for rescue.
func TestPause_CommitFailureParksPausedAndKeepsWIP(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	// Break the identity so an auto-pause commit fails deterministically (HOME is a
	// fresh temp dir, so there is no global identity to fall back on).
	runGit(t, repoPath, "config", "--unset", "user.email")
	runGit(t, repoPath, "config", "--unset", "user.name")
	// Make the worktree dirty so pause attempts the (now-failing) commit.
	wipPath := filepath.Join(wt.GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wipPath, []byte("uncommitted work\n"), 0o644))

	inst := startedInstance(t, wt)
	err := inst.RecoverLostSession()

	require.Error(t, err, "a commit failure must surface an error to the caller")
	require.True(t, inst.Paused(), "recovery must still park the session as Paused, not leave it Running")
	require.FileExists(t, wipPath, "uncommitted WIP must be left on disk, not destroyed")
	require.DirExists(t, wt.GetWorktreePath(), "the worktree must not be removed when the commit failed")
}

// TestPause_RemoveFailureFallsBackToParkedPaused asserts the other #270 branch:
// when `git worktree remove` fails (here: the base repo was deleted out from
// under the session), pause falls back to a best-effort directory removal and
// still ends Paused with the error surfaced — never stuck Running.
func TestPause_RemoveFailureFallsBackToParkedPaused(t *testing.T) {
	wt := newTestWorktree(t)
	worktreePath := wt.GetWorktreePath()
	// Delete the base repo so `git -C <repo> worktree remove` can't run, while the
	// worktree directory itself still exists (IsValidWorktree stays true).
	require.NoError(t, os.RemoveAll(wt.GetRepoPath()))
	require.DirExists(t, worktreePath, "precondition: the worktree dir still exists")

	inst := startedInstance(t, wt)
	err := inst.RecoverLostSession()

	require.Error(t, err, "a remove failure must surface an error to the caller")
	require.True(t, inst.Paused(), "recovery must degrade to Paused via the orphan fallback")
	require.NoDirExists(t, worktreePath, "the orphan fallback must remove the worktree directory")
}

// TestResume_ReusesInPlaceWorktreePreservingWIP guards the resume half of the
// commit-failure park (#270): a Paused instance whose worktree is still
// materialized on disk (with uncommitted WIP) must be resumable — Resume must
// reuse it in place rather than treat its own worktree as a foreign branch
// checkout (BranchCheckoutPath) and refuse, or rebuild it via Setup and discard
// the WIP. Without the fix the "press r to resume" guidance would be a dead end.
func TestResume_ReusesInPlaceWorktreePreservingWIP(t *testing.T) {
	wt := newTestWorktree(t)
	wipPath := filepath.Join(wt.GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wipPath, []byte("uncommitted work\n"), 0o644))

	pty := newRecordingPtyFactory(t, nil)
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
	inst := &Instance{Title: "sess", status: Paused, started: true, Program: "claude", gitWorktree: wt, tmuxSession: ts}

	require.NoError(t, inst.Resume(), "a valid in-place worktree must be resumable")
	require.Equal(t, Running, inst.GetStatus())
	require.FileExists(t, wipPath, "resume must reuse the worktree in place, preserving WIP")
	require.NotEmpty(t, pty.cmds, "the agent must be (re)launched")
}
