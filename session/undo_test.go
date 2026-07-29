package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const undoRef = "refs/atrium/undo/deadbeefcafe/0000000000000000042-cafebabe"

// newTestWorktreeFromExistingBranch builds a worktree flagged as sitting on a
// branch the user already owned.
//
// No code path sets that flag today — it round-trips false through state.json
// forever — but Cleanup reads it (worktree_ops.go: `if !g.isExistingBranch`) to
// decide whether to run `branch -D`, so a stored true is reachable through data
// alone and the teardown must agree with it. Rehydrating through
// NewWorktreeFromStorage is how that state is reached without inventing a setter.
func newTestWorktreeFromExistingBranch(t *testing.T) *git.Worktree {
	t.Helper()
	base := newTestWorktree(t)
	return git.NewWorktreeFromStorage(
		context.Background(),
		base.GetRepoPath(),
		base.GetWorktreePath(),
		"sess",
		base.GetBranchName(),
		base.GetBaseCommitSHA(),
		base.GetBaseRef(),
		true, // isExistingBranch
		"zvi/",
	)
}

// killableInstance is a started, running session over wt with a tmux session that
// reports itself alive, which is all PrepareUndo needs to see.
func killableInstance(t *testing.T, wt *git.Worktree) *Instance {
	t.Helper()
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", newRecordingPtyFactory(t, nil), aliveExec)
	return &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}
}

// TestPrepareUndoCommitsDirtyWorkBeforeRetaining. The order is the whole point: a
// ref captured before the commit would retain a tip that predates the user's
// uncommitted edits, so undo would restore the branch and silently lose the work
// it was invoked to rescue.
func TestPrepareUndoCommitsDirtyWorkBeforeRetaining(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()
	repoPath := wt.GetRepoPath()
	inst := killableInstance(t, wt)

	beforeSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	captured, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)
	assert.True(t, captured.Committed, "dirty work must be folded into the retained commits")

	afterSHA := gitOutput(t, repoPath, "rev-parse", wt.GetBranchName())
	assert.NotEqual(t, beforeSHA, afterSHA, "the WIP must have been committed")
	assert.Equal(t, afterSHA, captured.SHA, "the retained SHA is the post-commit tip, not the tip we started from")

	retained, ok := git.RefExists(context.Background(), repoPath, undoRef)
	require.True(t, ok)
	assert.Equal(t, afterSHA, retained)
}

// TestPrepareUndoUsesPausesOwnCommitMarker. Resume unwinds the auto-commit by
// matching these exact affixes; a kill that invented its own marker would leave
// the WIP as a permanent commit in restored history. The suffix is load-bearing,
// not cosmetic, which is why this asserts through isAutoPauseCommit rather than a
// hand-typed string.
func TestPrepareUndoUsesPausesOwnCommitMarker(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()
	inst := killableInstance(t, wt)
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	_, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)

	subject := gitOutput(t, wt.GetRepoPath(), "log", "-1", "--format=%s", wt.GetBranchName())
	assert.True(t, isAutoPauseCommit(subject),
		"the kill's WIP commit must be one Resume will unwind, got %q", subject)
}

// TestPrepareUndoOnACleanSessionRetainsWithoutCommitting — the common case. Nothing
// to save means no commit, but the branch still has to be pinned or the kill is
// irreversible.
func TestPrepareUndoOnACleanSessionRetainsWithoutCommitting(t *testing.T) {
	wt := newTestWorktree(t)
	inst := killableInstance(t, wt)

	head := gitOutput(t, wt.GetRepoPath(), "rev-parse", wt.GetBranchName())
	captured, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)
	assert.False(t, captured.Committed)
	assert.Equal(t, head, captured.SHA)
	assert.Equal(t, head, gitOutput(t, wt.GetRepoPath(), "rev-parse", wt.GetBranchName()),
		"a clean session's branch must not gain a commit")
}

// TestPrepareUndoLeavesAPreExistingBranchAlone. Cleanup never deletes a branch the
// user already had, so nothing here can be taken back later: a WIP commit written
// onto it would outlive the journal entry, survive expiry, and turn up in the
// user's own branch — possibly one they have already pushed.
func TestPrepareUndoLeavesAPreExistingBranchAlone(t *testing.T) {
	wt := newTestWorktreeFromExistingBranch(t)
	wtPath := wt.GetWorktreePath()
	inst := killableInstance(t, wt)

	head := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	captured, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)
	assert.False(t, captured.Committed, "uncommitted work on a user-owned branch is not preserved")
	assert.Equal(t, head, gitOutput(t, wt.GetRepoPath(), "rev-parse", wt.GetBranchName()),
		"the user's branch must not gain an [atrium] commit")
	assert.Equal(t, head, captured.SHA, "the killed tip is still recorded, so a moved branch is detectable")

	_, ok := git.RefExists(context.Background(), wt.GetRepoPath(), undoRef)
	assert.True(t, ok, "a pre-existing branch can still move, so its tip is still retained")
}

// TestPrepareUndoStillRecordsDirtItRefusedToCommit. Declining to write a commit
// onto the user's own branch does not make the working tree survive: Cleanup runs
// `git worktree remove -f` on every session alike, and only `branch -D` is gated on
// the flag. So this is a restore that comes back incomplete, and the record has to
// say so — Dirty && !Committed is exactly the shape the post-restore notice reads.
// Leaving Dirty false would report the loss as a whole restore.
func TestPrepareUndoStillRecordsDirtItRefusedToCommit(t *testing.T) {
	wt := newTestWorktreeFromExistingBranch(t)
	wtPath := wt.GetWorktreePath()
	inst := killableInstance(t, wt)

	head := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	captured, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)

	assert.True(t, captured.Dirty, "the tree had uncommitted work that the kill is about to destroy")
	assert.False(t, captured.Committed, "and nothing preserved it, so the restore must admit that")
	assert.Equal(t, head, gitOutput(t, wt.GetRepoPath(), "rev-parse", wt.GetBranchName()),
		"recording the dirt must not start writing to the user's branch")
}

// TestPrepareUndoOnADirectSessionIsANoOp — a direct session runs in the user's own
// directory with no worktree and no branch. There is nothing to commit and nothing
// to retain, and reaching for git would only log a misleading failure.
func TestPrepareUndoOnADirectSessionIsANoOp(t *testing.T) {
	inst := &Instance{Title: "sess", status: Running, started: true, direct: true}

	captured, err := inst.PrepareUndo(undoRef)
	require.NoError(t, err)
	assert.Empty(t, captured.SHA)
	assert.False(t, captured.Committed)
}

// TestRestoreFromDataIsReadyForResume. Resume refuses anything that is not both
// started and Paused, and FromInstanceData sets neither — only the unexported
// reattach does, and that path reattaches every session. This is the seam that
// closes the gap without exporting `started`.
func TestRestoreFromDataIsReadyForResume(t *testing.T) {
	wt := newTestWorktree(t)
	inst := killableInstance(t, wt)
	inst.Program = "claude"
	data := inst.ToInstanceData()
	data.Status = Running // whatever it was at kill time must not leak through

	restored, err := RestoreFromData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	assert.True(t, restored.Started(), "Resume refuses an instance that was never started")
	assert.True(t, restored.Paused(), "Resume only resumes a paused instance")
}

// TestRestoreFromDataKeepsTheExactWorktreePath — byte for byte. The agent's
// conversation is keyed by its working directory, so `--continue` only finds it
// when the worktree comes back at the identical path. Re-deriving the path yields
// a fresh UnixNano suffix that passes every structural check and silently
// destroys the resume.
func TestRestoreFromDataKeepsTheExactWorktreePath(t *testing.T) {
	wt := newTestWorktree(t)
	inst := killableInstance(t, wt)
	data := inst.ToInstanceData()

	restored, err := RestoreFromData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	got, err := restored.GetGitWorktree()
	require.NoError(t, err)
	assert.Equal(t, wt.GetWorktreePath(), got.GetWorktreePath())
	assert.Equal(t, wt.GetBranchName(), got.GetBranchName())
	assert.Equal(t, wt.GetRepoPath(), got.GetRepoPath())
}

// TestRestoreFromDataRunsNoSubprocesses — it is called on the UI thread to decide
// whether a restore is even possible, before the async work starts. Touching git
// or tmux here would freeze the frame.
func TestRestoreFromDataRunsNoSubprocesses(t *testing.T) {
	wt := newTestWorktree(t)
	inst := killableInstance(t, wt)
	data := inst.ToInstanceData()
	require.NoError(t, os.RemoveAll(wt.GetRepoPath()))

	restored, err := RestoreFromData(context.Background(), data, "zvi/")
	require.NoError(t, err, "rehydration must not depend on anything being on disk")
	assert.True(t, restored.Paused())
}
