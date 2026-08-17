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

	got, managed, err := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
	assert.True(t, managed, "a worktree under the data dir's worktrees/ tree is Atrium's own")
	// Compared symlink-resolved: the path comes from git, which reports the one it
	// registered — on macOS /private/var where the test built /var. What matters is that
	// it names the same directory, not that it spells it the same way.
	assert.Equal(t, resolvePath(wt), resolvePath(got))
}

// TestUnderManagedWorktreesSurvivesASymlinkedDataDir is a regression test for a
// macOS-only CI failure that also broke a pre-existing guard
// (TestIsolatedSessionDoesNotWarnForADirOnlyIgnoreRule).
//
// The containment check compares the worktrees root with a candidate path. Resolving
// symlinks on each side INDEPENDENTLY is unsound, because resolvePath falls back to
// Clean for a path that does not exist — and the busiest caller,
// removeOrphanedWorktreeDir, runs right after a `git worktree remove` has deleted the
// directory. The root still resolves (/private/var) while the gone worktree does not
// (/var), Rel answers "../..", and a worktree squarely inside the managed tree is
// refused as outside it. On the delete path that is a warning; on the recovery path it
// silently declines to free a branch it owns.
//
// Reproduced on any platform by making the data dir reachable through a symlink, which
// is what /var is on macOS. Asserted for a path that EXISTS and one that does not,
// because only the second distinguishes the two comparisons.
func TestUnderManagedWorktreesSurvivesASymlinkedDataDir(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(target, link))
	t.Setenv("HOME", link)

	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NotEqual(t, resolvePath(root), filepath.Clean(root),
		"precondition: the root is reached through a symlink, as /var is on macOS")

	present := filepath.Join(root, "present_deadbeef")
	require.NoError(t, os.MkdirAll(present, 0o755))
	_, managed, err := underManagedWorktrees(present)
	require.NoError(t, err)
	assert.True(t, managed, "a worktree that exists is inside the managed tree")

	_, managed, err = underManagedWorktrees(filepath.Join(root, "already-gone_deadbeef"))
	require.NoError(t, err)
	assert.True(t, managed,
		"and so is one already deleted — the case a per-side resolve gets wrong")

	_, managed, err = underManagedWorktrees(filepath.Join(t.TempDir(), "elsewhere"))
	require.NoError(t, err)
	assert.False(t, managed, "while a path outside it is still refused")
}

// TestStrandedWorktreeForRefusesToClaimAHandMadeWorktree is the licence's negative
// control. A checkout somebody made deliberately holds the branch just as firmly, and
// the difference is the only thing standing between recovery and deleting a person's
// working directory — so it must be reported unmanaged rather than merely found.
func TestStrandedWorktreeForRefusesToClaimAHandMadeWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := filepath.Join(t.TempDir(), "my-own-checkout")
	repo := strandedRepo(t, wt, "zvi/fix-auth")

	got, managed, err := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
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

	got, managed, err := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
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
	got, _, err := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
	assert.Empty(t, got, "and git no longer reports it as held")

	// The proof that matters: the branch can be checked out again, which is exactly what
	// the adopting Setup does next.
	second := filepath.Join(root, "fix-auth_cafebabe")
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "worktree", "add", second, "zvi/fix-auth")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "git worktree add: %s", out)
}

// TestCleanupWorktreesReportedPathsAreAlreadyResolved is the measurement that settled
// whether CleanupWorktrees needs the containment helper its three siblings use.
//
// A review argued it did: its check is a hand-rolled HasPrefix over resolvePath of both
// sides, and resolvePath falls back to Clean for a path that does not EXIST, so a
// worktree directory a partial teardown already deleted would compare on a different
// basis from the still-present root — the /var vs /private/var mix that broke the
// sibling. The argument is sound and the premise is false: git NORMALISES the path it
// reports, so a worktree added through a symlinked root comes back resolved, before and
// after its directory is deleted. Both sides are therefore already on one basis.
//
// That makes this a characterisation test, not a regression test. It fails if git ever
// starts reporting the path it was given, which is the day CleanupWorktrees does need
// the helper — and it is the reason the code keeps a single resolved comparison instead
// of adopting underManagedWorktrees, whose extra literal comparison would newly accept a
// path inside the tree that resolves outside it, on a loop that runs `git branch -D`.
func TestCleanupWorktreesReportedPathsAreAlreadyResolved(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(target, link))
	t.Setenv("HOME", link)

	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NotEqual(t, resolvePath(root), filepath.Clean(root),
		"precondition: the root is reached through a symlink, as /var is on macOS")

	wt := filepath.Join(root, "fix-auth_deadbeef")
	repo := strandedRepo(t, wt, "zvi/fix-auth")

	reported, _, err := StrandedWorktreeFor(context.Background(), repo, "zvi/fix-auth")
	require.NoError(t, err)
	assert.Equal(t, resolvePath(reported), reported,
		"git reports a worktree at its resolved path even when added through a symlink")

	require.NoError(t, os.RemoveAll(wt), "the directory goes; git's registration stays")
	require.True(t, LocalBranchExists(context.Background(), repo, "zvi/fix-auth"))

	require.NoError(t, CleanupWorktrees(context.Background(), []string{repo}))

	assert.False(t, LocalBranchExists(context.Background(), repo, "zvi/fix-auth"),
		"so reset collects the session branch even with the directory already gone")
}

// TestCleanupWorktreesLeavesABranchOutsideTheManagedTree is that sweep's negative
// control, and the reason the containment check above must not be widened casually:
// reset deletes every branch it collects, and a branch checked out in a person's own
// worktree is not reset's to delete.
func TestCleanupWorktreesLeavesABranchOutsideTheManagedTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	mine := filepath.Join(t.TempDir(), "my-own-checkout")
	repo := strandedRepo(t, mine, "feature/theirs")

	require.NoError(t, CleanupWorktrees(context.Background(), []string{repo}))

	assert.True(t, LocalBranchExists(context.Background(), repo, "feature/theirs"),
		"a branch held outside the managed tree survives a reset")
	assert.DirExists(t, mine, "and so does the checkout holding it")
}

// TestStrandedWorktreeForReportsAFailureRatherThanAbsence pins the contract the
// create-recovery path turns on: "git could not be asked" must not arrive as "no
// worktree holds this branch". Folded together, a failed `git worktree list` yields
// claimAdopt with no release, and the retry dies on git's "already used by worktree" —
// the dead end StrandedWorktreeFor exists to prevent.
func TestStrandedWorktreeForReportsAFailureRatherThanAbsence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, managed, err := StrandedWorktreeFor(context.Background(), t.TempDir(), "zvi/fix-auth")
	require.Error(t, err, "a directory that is not a repository cannot answer this question")
	assert.Empty(t, got)
	assert.False(t, managed)
}
