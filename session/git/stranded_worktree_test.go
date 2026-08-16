package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strandedRepo builds a repo with one commit plus a worktree at wtPath on branch, which
// is the state `git worktree add` leaves when the process building a session is killed
// before it can record the row.
func strandedRepo(t *testing.T, wtPath, branch string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	if wtPath != "" {
		run("worktree", "add", "-b", branch, wtPath)
	}
	return repo
}

// TestStrandedWorktreeForFindsAManagedWorktree is the lookup #716's adoption turns on.
// The branch alone is not what blocks a second attempt — resolveWorktreePaths stamps
// each worktree directory with the current nanosecond, so the retry asks for a different
// path and git refuses it with "already used by worktree" against the first one.
func TestStrandedWorktreeForFindsAManagedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	wt := filepath.Join(root, "fix-auth_deadbeef")
	repo := strandedRepo(t, wt, "zvi/fix-auth")

	got, managed := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	assert.True(t, managed, "a worktree under the data dir's worktrees/ tree is Atrium's own")
	assert.Equal(t, wt, got)
}

// TestStrandedWorktreeForRefusesToClaimAHandMadeWorktree is the licence's negative
// control. A checkout somebody made deliberately holds the branch just as firmly, and
// the difference is the only thing standing between recovery and deleting a person's
// working directory — so it must be reported unmanaged rather than merely found.
func TestStrandedWorktreeForRefusesToClaimAHandMadeWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := filepath.Join(t.TempDir(), "my-own-checkout")
	repo := strandedRepo(t, wt, "zvi/fix-auth")

	got, managed := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NotEmpty(t, got, "it is still found; what differs is the licence to remove it")
	assert.False(t, managed)

	assert.Error(t, ReleaseManagedWorktree(context.Background(), repo, got),
		"and releasing it must be refused, not merely skipped by the caller")
	assert.DirExists(t, got, "the person's checkout survives")
}

// TestStrandedWorktreeForReportsNothingWhenTheBranchIsFree: the case-2 path. Nothing
// holds the branch, so there is nothing to release and the retry can just build.
func TestStrandedWorktreeForReportsNothingWhenTheBranchIsFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := strandedRepo(t, "", "")

	got, managed := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	assert.Empty(t, got)
	assert.False(t, managed)
}

// TestReleaseManagedWorktreeFreesTheBranchAndKeepsIt is the whole point of the release:
// the directory goes so the branch can be checked out again, and the BRANCH stays,
// because it holds whatever the interrupted build committed.
func TestReleaseManagedWorktreeFreesTheBranchAndKeepsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	wt := filepath.Join(root, "fix-auth_deadbeef")
	repo := strandedRepo(t, wt, "zvi/fix-auth")

	require.NoError(t, ReleaseManagedWorktree(context.Background(), repo, wt))

	assert.NoDirExists(t, wt)
	assert.True(t, LocalBranchExists(context.Background(), repo, "zvi/fix-auth"),
		"the branch is the work; only its checkout was in the way")
	got, _ := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	assert.Empty(t, got, "and git no longer reports it as held")

	// The proof that matters: the branch can be checked out again, which is exactly what
	// the adopting Setup does next.
	second := filepath.Join(root, "fix-auth_cafebabe")
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "worktree", "add", second, "zvi/fix-auth")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "git worktree add: %s", out)
}
