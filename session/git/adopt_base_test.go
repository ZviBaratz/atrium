package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupOnAnExistingBranchSeedsABaseCommit covers the gap adoption opened.
//
// Every session created before #716 reached Setup on a branch that did not exist, took
// setupNewWorktree, and had its base commit written there — the only place that wrote
// one. Adoption is the first CREATION routed deliberately down the other side, and it
// arrives with an empty base (a brand-new Instance; only a resume rehydrates the
// persisted value). Left empty, diffFrom returns errBaseCommitNotSet before running any
// git, so the session's diff tab, its +/- chip and — through the same early return —
// the Dirty/Unpushed numbers killDataWarning reads for the "uncommitted changes"
// warning are all dead for the life of the session.
func TestSetupOnAnExistingBranchSeedsABaseCommit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := strandedRepo(t, "", "")
	head := revParse(t, repo, "HEAD")
	requireBranch(t, repo, "zvi/fix-auth")

	wt := worktreeFor(t, repo, "zvi/fix-auth")
	require.NoError(t, wt.Setup())
	t.Cleanup(func() { _ = wt.Cleanup() })

	assert.Equal(t, head, wt.GetBaseCommitSHA(),
		"the adopted branch was cut from the start point and holds no commits, so its tip IS the base")
}

// TestSetupOnAnExistingBranchKeepsARestoredBase is that seeding's negative control, and
// the reason it is guarded on empty rather than written unconditionally.
//
// A resume takes the same code path with a base already restored from state.json, and
// that value spans the session's whole history. Overwriting it with the branch tip would
// silently reset every resumed session's diff to empty — a far larger blast radius than
// the case the seeding exists for.
func TestSetupOnAnExistingBranchKeepsARestoredBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := strandedRepo(t, "", "")
	restored := revParse(t, repo, "HEAD")
	requireBranch(t, repo, "zvi/fix-auth")
	commitOn(t, repo, "zvi/fix-auth")
	tip := revParse(t, repo, "zvi/fix-auth")
	require.NotEqual(t, restored, tip, "the branch must have moved, or this proves nothing")

	wt := worktreeFor(t, repo, "zvi/fix-auth")
	wt.setBaseCommitSHA(restored)
	require.NoError(t, wt.Setup())
	t.Cleanup(func() { _ = wt.Cleanup() })

	assert.Equal(t, restored, wt.GetBaseCommitSHA(), "a resume keeps the base it was persisted with")
}

// TestLookupLocalBranchSeparatesAbsentFromUnreadable is the whole reason this exists
// beside LocalBranchExists, which is `err == nil` and so cannot tell them apart.
//
// The create-recovery path acts destructively on the negative (#716): reading a git
// failure as "the branch does not exist" re-queues the request into gates that refuse it
// for that very branch, and a refusal unlinks the record — spending the one recovery a
// stranded request gets and keeping the orphan forever.
func TestLookupLocalBranchSeparatesAbsentFromUnreadable(t *testing.T) {
	repo := strandedRepo(t, "", "")
	requireBranch(t, repo, "zvi/fix-auth")

	found, err := LookupLocalBranch(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
	assert.True(t, found)

	found, err = LookupLocalBranch(context.Background(), repo, "zvi/nope")
	require.NoError(t, err, "a branch that is simply not there is an answer, not a failure")
	assert.False(t, found)

	found, err = LookupLocalBranch(context.Background(), t.TempDir(), "zvi/fix-auth")
	require.Error(t, err, "a directory that is not a repository must not answer 'no such branch'")
	assert.False(t, found)
}

// TestLookupLocalBranchDoesNotMatchANestedRef pins the exactness for-each-ref does not
// give for free: its pattern matches at path boundaries, so refs/heads/zvi/fix-auth also
// matches refs/heads/zvi/fix-auth/wip. A prefix answer here would report a branch that
// does not exist and send the recovery at it.
func TestLookupLocalBranchDoesNotMatchANestedRef(t *testing.T) {
	repo := strandedRepo(t, "", "")
	requireBranch(t, repo, "zvi/fix-auth/wip")

	found, err := LookupLocalBranch(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
	assert.False(t, found, "only the exact ref counts")

	found, err = LookupLocalBranch(context.Background(), repo, "zvi/fix-auth/wip")
	require.NoError(t, err)
	assert.True(t, found, "while the ref that does exist is still found")
}

func worktreeFor(t *testing.T, repo, branch string) *Worktree {
	t.Helper()
	return NewWorktreeFromStorage(context.Background(), repo, filepath.Join(t.TempDir(), "wt"),
		"fix-auth", branch, "", "", true, "zvi/")
}

func requireBranch(t *testing.T, repo, branch string) {
	t.Helper()
	_, err := localGit(context.Background(), repo, "branch", branch)
	require.NoError(t, err)
}

func commitOn(t *testing.T, repo, branch string) {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "scratch")
	_, err := localGit(context.Background(), repo, "worktree", "add", wt, branch)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wt, "more"), []byte("y"), 0o644))
	_, err = localGit(context.Background(), wt, "add", ".")
	require.NoError(t, err)
	_, err = localGit(context.Background(), wt, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-m", "work")
	require.NoError(t, err)
	_, err = localGit(context.Background(), repo, "worktree", "remove", "--force", wt)
	require.NoError(t, err)
}

// TestLocalBranchSetAnswersTheWholeNamespaceAtOnce pins what the per-name siblings above
// cannot be asked cheaply: a caller scanning a numbered series needs every local branch,
// and getting them one fork at a time costs it a subprocess and a gitLocalTimeout per
// candidate. The set has to hold exactly the local heads — nested refs included, since a
// name derived from a prefix can be one — and it has to keep "git could not be asked" out
// of the answer, because its caller acts on the negative by CHOOSING a name.
func TestLocalBranchSetAnswersTheWholeNamespaceAtOnce(t *testing.T) {
	repo := strandedRepo(t, "", "")
	requireBranch(t, repo, "zvi/fix-auth")
	// A sibling rather than a child of the branch above: refs/heads/zvi/fix-auth and
	// refs/heads/zvi/fix-auth/wip cannot both exist, so asking for both is a git error
	// rather than a fixture.
	requireBranch(t, repo, "zvi/bake/wip")

	names, err := LocalBranchSet(context.Background(), repo)
	require.NoError(t, err)
	assert.True(t, names["zvi/fix-auth"])
	assert.True(t, names["zvi/bake/wip"],
		"a nested ref is a local head like any other, and a derived name can be one")
	assert.False(t, names["zvi/nope"], "and a name nothing owns is simply absent")

	_, err = LocalBranchSet(context.Background(), t.TempDir())
	require.Error(t, err, "a directory that is not a repository must not answer 'no branches'")
}
