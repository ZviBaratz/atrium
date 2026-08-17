package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// resumableInstance wires a Paused instance around a real worktree and the tmux session
// a park leaves behind: gone until the relaunch's new-session brings it back, classified
// by verb rather than by how many commands have gone past (see relaunchableInstance for
// what that cost).
func resumableInstance(t *testing.T, wt *git.Worktree) *Instance {
	t.Helper()
	return &Instance{Title: "sess", status: Paused, started: true, Program: "claude",
		gitWorktree: wt, tmuxSession: parkedTmux(t)}
}

// TestResume_StampsStartedAtForLaunchCrashDetection guards the #270 fix that
// DiedAtLaunch must keep working across Resume: recreateSession stamps startedAt
// on every relaunch, so a program that crashes moments after a resume is still
// classified as a launch crash. Without the stamp, startedAt stays frozen at the
// original Start and every resume-crash past the first is misread as a long-lived
// death — dropping the diagnostic modal exactly when the user needs it.
func TestResume_StampsStartedAtForLaunchCrashDetection(t *testing.T) {
	wt := newTestWorktree(t)
	inst := resumableInstance(t, wt)
	// A stale timestamp from the original Start, well outside the launch window.
	inst.startedAt = time.Now().Add(-time.Hour)

	require.NoError(t, inst.Resume())
	require.True(t, inst.DiedAtLaunch(15*time.Second),
		"Resume must refresh startedAt so a crash right after resume is still a launch crash")
}

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
	// Break the identity so an auto-pause commit fails deterministically. Unsetting
	// alone is not portable — git auto-detects user@host on some CI (macOS), so the
	// commit would succeed. user.useConfigOnly forces git to require an explicit
	// identity and refuse the auto-detect fallback on every platform.
	runGit(t, repoPath, "config", "user.useConfigOnly", "true")
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

	// The park left no session behind, so the close is a no-op and the launch's
	// duplicate-name guard sees nothing; new-session then brings it up.
	srv := newParkedTmuxServer(t)
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", srv, srv.exec())
	inst := &Instance{Title: "sess", status: Paused, started: true, Program: "claude", gitWorktree: wt, tmuxSession: ts}

	require.NoError(t, inst.Resume(), "a valid in-place worktree must be resumable")
	require.Equal(t, Running, inst.GetStatus())
	require.FileExists(t, wipPath, "resume must reuse the worktree in place, preserving WIP")
	require.NotEmpty(t, srv.pty.cmds, "the agent must be (re)launched")
}
