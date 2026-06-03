package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo initializes a git repo with one commit under a sandboxed HOME (so the cs
// worktree directory resolves inside tempHome) and returns the repo path.
func newTestRepo(t *testing.T) string {
	t.Helper()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "Test User")
	mustRunGit(t, repoPath, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunGit(t, repoPath, "add", "README.md")
	mustRunGit(t, repoPath, "commit", "-m", "initial")
	return repoPath
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(mustRunGit(t, dir, "rev-parse", ref))
}

// Selecting an existing branch that is already checked out in another worktree must still
// create the session: we branch off it rather than checking it out, so git's "already used
// by worktree" error never arises. This is the exact failure reported from the picker.
func TestSetup_BranchOffBusyBranch(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	featSHA := revParse(t, repoPath, "feat")

	// Make "feat" busy by checking it out in a separate worktree (as the main repo would).
	busyWorktree := filepath.Join(t.TempDir(), "busy")
	mustRunGit(t, repoPath, "worktree", "add", busyWorktree, "feat")

	wt, branch, err := NewWorktreeFromBase(repoPath, "mysess", "feat")
	if err != nil {
		t.Fatalf("NewWorktreeFromBase error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v (branch-off must succeed even when base is checked out elsewhere)", err)
	}

	if branch == "feat" {
		t.Fatalf("session branch must differ from the base branch, got %q", branch)
	}
	// The session branch exists and starts at feat's tip.
	if got := revParse(t, repoPath, branch); got != featSHA {
		t.Fatalf("session branch tip = %q, want feat tip %q", got, featSHA)
	}
	// baseCommitSHA is recorded as the start point so the diff pane has a correct base.
	if got := wt.GetBaseCommitSHA(); got != featSHA {
		t.Fatalf("baseCommitSHA = %q, want %q", got, featSHA)
	}
}

// Basing on a branch that exists only on the remote resolves via origin/<branch>.
func TestSetup_BranchOffRemoteOnlyBase(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	featSHA := revParse(t, repoPath, "feat")

	// Publish feat to a bare origin, then drop the local branch so only origin/feat remains.
	bare := filepath.Join(t.TempDir(), "origin.git")
	mustRunGit(t, "", "init", "--bare", bare)
	mustRunGit(t, repoPath, "remote", "add", "origin", bare)
	mustRunGit(t, repoPath, "push", "origin", "feat")
	mustRunGit(t, repoPath, "fetch", "origin")
	mustRunGit(t, repoPath, "branch", "-D", "feat")

	wt, branch, err := NewWorktreeFromBase(repoPath, "remotesess", "feat")
	if err != nil {
		t.Fatalf("NewWorktreeFromBase error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v (should resolve origin/feat)", err)
	}
	if got := revParse(t, repoPath, branch); got != featSHA {
		t.Fatalf("session branch tip = %q, want origin/feat tip %q", got, featSHA)
	}
}

// An unknown base branch fails cleanly rather than producing a confusing git error.
func TestSetup_UnknownBaseBranchErrors(t *testing.T) {
	repoPath := newTestRepo(t)

	wt, _, err := NewWorktreeFromBase(repoPath, "sess", "does-not-exist")
	if err != nil {
		t.Fatalf("NewWorktreeFromBase error = %v", err)
	}
	err = wt.Setup()
	if err == nil {
		t.Fatal("Setup() expected an error for an unknown base branch")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Setup() error = %v, want it to mention the base branch was not found", err)
	}
}

func TestSetupFromExistingBranch_RemovesOrphanedDirectory(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tempHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer func() {
		_ = os.Setenv("HOME", originalHome)
	}()

	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "Test User")
	mustRunGit(t, repoPath, "config", "user.email", "test@example.com")

	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	mustRunGit(t, repoPath, "add", "README.md")
	mustRunGit(t, repoPath, "commit", "-m", "initial")
	mustRunGit(t, repoPath, "branch", "feature/test")

	worktreePath := filepath.Join(tempHome, ".claude-squad", "worktrees", "feature-test")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir orphaned worktree: %v", err)
	}

	junkPath := filepath.Join(worktreePath, "orphan.txt")
	if err := os.WriteFile(junkPath, []byte("orphaned\n"), 0644); err != nil {
		t.Fatalf("write orphan marker: %v", err)
	}

	g := &Worktree{
		repoPath:         repoPath,
		worktreePath:     worktreePath,
		branchName:       "feature/test",
		isExistingBranch: true,
	}

	if err := g.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if _, err := os.Stat(junkPath); !os.IsNotExist(err) {
		t.Fatalf("orphan marker still exists after Setup, err = %v", err)
	}

	if valid, err := g.IsValidWorktree(); err != nil {
		t.Fatalf("IsValidWorktree() error = %v", err)
	} else if !valid {
		t.Fatal("expected Setup() to recreate a valid worktree")
	}

	currentBranch := mustRunGit(t, worktreePath, "branch", "--show-current")
	if currentBranch != "feature/test\n" {
		t.Fatalf("current branch = %q, want %q", currentBranch, "feature/test\n")
	}
}

// removeOrphanedWorktreeDir is Cleanup's fallback when git can no longer manage a
// worktree (e.g. the project repo was renamed/removed). It must delete the dir when
// it lives under the managed worktrees/ tree and refuse anything outside it.
func TestRemoveOrphanedWorktreeDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	root, err := getWorktreeDirectory()
	if err != nil {
		t.Fatalf("getWorktreeDirectory: %v", err)
	}

	// Inside the managed tree → removed, contents and all.
	inside := filepath.Join(root, "sess_abc")
	if err := os.MkdirAll(filepath.Join(inside, "sub"), 0755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inside, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	if err := removeOrphanedWorktreeDir(inside); err != nil {
		t.Fatalf("expected removal of managed worktree dir, got %v", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("managed worktree dir still exists, err = %v", err)
	}

	// Outside the managed tree → refused, dir left intact.
	outside := filepath.Join(t.TempDir(), "important")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := removeOrphanedWorktreeDir(outside); err == nil {
		t.Fatal("expected refusal to remove path outside the managed tree")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside dir must be left intact, got %v", err)
	}

	// The worktrees root itself must never be wiped.
	if err := removeOrphanedWorktreeDir(root); err == nil {
		t.Fatal("expected refusal to remove the worktrees root itself")
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}

	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

// TestCleanupWorktrees_DeletesBranchFromNonGitCWD is the regression test for the
// repo-aware fix: CleanupWorktrees must remove the worktree directory AND delete
// the session branch even when invoked from a directory that is not a git repo
// (the bug was that the bare git commands resolved against the CWD and silently
// failed, leaving stale branches behind).
func TestCleanupWorktrees_DeletesBranchFromNonGitCWD(t *testing.T) {
	repoPath := newTestRepo(t)

	wt, branch, err := NewWorktree(repoPath, "cleanup-test")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	worktreePath := wt.GetWorktreePath()

	// Sanity-check the starting state: the worktree dir and its branch both exist.
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree dir missing before cleanup: %v", err)
	}
	if out := mustRunGit(t, repoPath, "branch", "--list", branch); !strings.Contains(out, branch) {
		t.Fatalf("branch %q not found before cleanup", branch)
	}

	// Run cleanup from a non-git directory; the fix relies on `git -C <repoPath>`
	// rather than the working directory. Restore the CWD afterward so this test
	// does not leak its directory change into the shared test process.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir to non-git dir: %v", err)
	}

	if err := CleanupWorktrees([]string{repoPath}); err != nil {
		t.Fatalf("CleanupWorktrees: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after cleanup: %v", err)
	}
	if out := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--list", branch)); out != "" {
		t.Errorf("branch %q still exists after cleanup: %q", branch, out)
	}
}

// TestCleanupWorktrees_EmptyRepoPathsStillRemovesDirs verifies that passing no
// repo paths is safe and still removes leftover worktree directories (it just
// cannot delete branches, since it has no repo to run git in).
func TestCleanupWorktrees_EmptyRepoPathsStillRemovesDirs(t *testing.T) {
	repoPath := newTestRepo(t)

	wt, _, err := NewWorktree(repoPath, "orphan-test")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	worktreePath := wt.GetWorktreePath()

	if err := CleanupWorktrees(nil); err != nil {
		t.Fatalf("CleanupWorktrees(nil): %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after cleanup: %v", err)
	}
}
