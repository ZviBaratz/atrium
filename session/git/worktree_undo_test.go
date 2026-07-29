package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testUndoRef = "refs/atrium/undo/deadbeefcafe/0000000000000000042-cafebabe"

// objectExists reports whether the repo still holds sha as a real object.
// `cat-file -e` is the cheapest question that distinguishes "git knows this name"
// from "the bytes are still here", which is exactly the distinction a retention
// ref exists to control.
func objectExists(t *testing.T, repo, sha string) bool {
	t.Helper()
	cmd := gitCmd(t, repo, "cat-file", "-e", sha+"^{commit}")
	return cmd == nil
}

func gitCmd(t *testing.T, dir string, args ...string) error {
	t.Helper()
	_, err := localGit(context.Background(), dir, args...)
	return err
}

// sessionWorktree builds a Worktree over repo with a session branch already
// committed on it, and returns it plus that branch's head SHA.
func sessionWorktree(t *testing.T, repo string) (*Worktree, string) {
	t.Helper()
	wt, _, err := NewWorktree(context.Background(), repo, "undo-me")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	require.NoError(t, os.WriteFile(filepath.Join(wt.GetWorktreePath(), "work.txt"), []byte("agent output\n"), 0o644))
	require.NoError(t, wt.CommitChanges("session work"))

	head := revParse(t, wt.GetWorktreePath(), "HEAD")
	require.NotEmpty(t, head)
	return wt, head
}

// TestRetainedObjectsSurviveKillAndGC is the load-bearing claim of the whole
// feature. Kill runs `git branch -D`, which makes the session's commits
// unreachable; `git gc` then prunes unreachable objects. A recorded SHA is
// therefore a promise git does not keep — only a ref keeps the bytes.
func TestRetainedObjectsSurviveKillAndGC(t *testing.T) {
	repo := newTestRepo(t)
	wt, head := sessionWorktree(t, repo)

	sha, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)
	assert.Equal(t, head, sha, "the retained SHA is the branch head at teardown")

	require.NoError(t, wt.Cleanup())
	require.Error(t, gitCmd(t, repo, "show-ref", "--verify", "refs/heads/"+wt.GetBranchName()),
		"cleanup must really have deleted the branch, or this test proves nothing")

	require.NoError(t, gitCmd(t, repo, "-c", "gc.pruneExpire=now", "gc", "--prune=now"))

	assert.True(t, objectExists(t, repo, head), "a retained commit must survive gc")
	got, ok := RefExists(context.Background(), repo, testUndoRef)
	assert.True(t, ok)
	assert.Equal(t, head, got)
}

// TestUnretainedObjectsDoNotSurviveGC is the negative control: without the ref,
// the same sequence loses the commit. Without this, the test above would pass on
// a git that simply never prunes, and the ref would look load-bearing while
// doing nothing.
func TestUnretainedObjectsDoNotSurviveGC(t *testing.T) {
	repo := newTestRepo(t)
	wt, head := sessionWorktree(t, repo)

	require.NoError(t, wt.Cleanup())
	require.NoError(t, gitCmd(t, repo, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all"))
	require.NoError(t, gitCmd(t, repo, "-c", "gc.pruneExpire=now", "gc", "--prune=now"))

	assert.False(t, objectExists(t, repo, head),
		"an unretained commit must be prunable, or the retention ref is decoration")
}

// TestRetainBranchOnAMissingRepoErrors — a project directory the user renamed or
// deleted must surface as an error the teardown can record, not a panic and not a
// silent success that promises a restore which cannot happen.
func TestRetainBranchOnAMissingRepoErrors(t *testing.T) {
	repo := newTestRepo(t)
	wt, _ := sessionWorktree(t, repo)
	require.NoError(t, os.RemoveAll(repo))

	_, err := wt.RetainBranch(testUndoRef)
	require.Error(t, err)
}

// TestCreateBranchAtNeverForces. Undo recreates a branch by name, and by then the
// name may belong to a session the user started since. Moving it would silently
// detach that live session's work.
func TestCreateBranchAtNeverForces(t *testing.T) {
	repo := newTestRepo(t)
	wt, head := sessionWorktree(t, repo)
	_, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)
	branch := wt.GetBranchName()
	require.NoError(t, wt.Cleanup())

	// Someone else takes the name, pointing somewhere else entirely.
	require.NoError(t, gitCmd(t, repo, "branch", branch, "HEAD"))
	occupied := revParse(t, repo, branch)
	require.NotEqual(t, head, occupied)

	err = CreateBranchAt(context.Background(), repo, branch, testUndoRef)
	require.Error(t, err)
	assert.Equal(t, occupied, revParse(t, repo, branch), "the live branch must not have moved")
}

// TestCreateBranchAtRestoresTheKilledTip — the happy path: the branch comes back
// pointing at exactly what it pointed at when the session was killed.
func TestCreateBranchAtRestoresTheKilledTip(t *testing.T) {
	repo := newTestRepo(t)
	wt, head := sessionWorktree(t, repo)
	_, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)
	branch := wt.GetBranchName()
	require.NoError(t, wt.Cleanup())

	require.NoError(t, CreateBranchAt(context.Background(), repo, branch, testUndoRef))
	assert.Equal(t, head, revParse(t, repo, branch))
}

// TestBranchTipDistinguishesAbsentFromMoved. Restoring onto a session's own
// pre-existing branch is only safe when the branch still points where it did:
// otherwise `worktree add` materializes today's tip and silently hands back a
// tree that is not the one that was killed.
func TestBranchTipDistinguishesAbsentFromMoved(t *testing.T) {
	repo := newTestRepo(t)
	wt, head := sessionWorktree(t, repo)
	branch := wt.GetBranchName()

	got, ok := BranchTip(context.Background(), repo, branch)
	require.True(t, ok)
	assert.Equal(t, head, got)

	_, ok = BranchTip(context.Background(), repo, "no-such-branch")
	assert.False(t, ok)
}

// TestDeleteRefIsIdempotent — expiry runs repeatedly over the same journal, and a
// ref already gone must not turn a sweep into an error loop.
func TestDeleteRefIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	wt, _ := sessionWorktree(t, repo)
	_, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)

	require.NoError(t, DeleteRef(context.Background(), repo, testUndoRef))
	_, ok := RefExists(context.Background(), repo, testUndoRef)
	assert.False(t, ok)
	require.NoError(t, DeleteRef(context.Background(), repo, testUndoRef))
}

// TestRetentionRefIsInvisibleToBranchListings. The whole reason the ref lives
// outside refs/heads is that a killed session must not leave a branch lying
// around in the user's `git branch` output — that is the clutter kill exists to
// remove.
func TestRetentionRefIsInvisibleToBranchListings(t *testing.T) {
	repo := newTestRepo(t)
	wt, _ := sessionWorktree(t, repo)
	branch := wt.GetBranchName()
	_, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)
	require.NoError(t, wt.Cleanup())

	out := mustRunGit(t, repo, "branch", "--list", "--all")
	assert.NotContains(t, out, branch)
	assert.NotContains(t, out, "atrium/undo")
}

// TestRefExistsRejectsAnAmbiguousShorthand — RefExists is the gate a restore trusts
// before recreating a branch, so it must verify a full refname rather than let git
// resolve some other object that happens to answer to the same shorthand.
func TestRefExistsRejectsAnAmbiguousShorthand(t *testing.T) {
	repo := newTestRepo(t)
	wt, _ := sessionWorktree(t, repo)
	_, err := wt.RetainBranch(testUndoRef)
	require.NoError(t, err)

	_, ok := RefExists(context.Background(), repo, strings.TrimPrefix(testUndoRef, "refs/"))
	assert.False(t, ok, "only a full refname resolves")
}
