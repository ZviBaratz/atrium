package git

import (
	"os"
	"strings"
	"testing"
)

// Rename fixes a typo across the branch and the worktree directory at once: the session
// branch is renamed in place (HEAD in the checked-out worktree follows) and the worktree
// directory is moved to match the corrected name, all without losing the commit history.
func TestRename_MovesBranchAndWorktree(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, oldBranch, err := NewGitWorktree(repoPath, "formalize-packaing")
	if err != nil {
		t.Fatalf("NewGitWorktree error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	oldPath := wt.GetWorktreePath()
	sha := revParse(t, oldPath, "HEAD")

	if err := wt.Rename("formalize-packaging"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	newBranch := wt.GetBranchName()
	if newBranch == oldBranch {
		t.Fatalf("branch name unchanged after rename: %q", newBranch)
	}
	// New branch exists at the same commit; old branch is gone.
	if got := revParse(t, repoPath, newBranch); got != sha {
		t.Fatalf("new branch tip = %q, want %q", got, sha)
	}
	if out := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--list", oldBranch)); out != "" {
		t.Fatalf("old branch %q still present: %q", oldBranch, out)
	}

	// Worktree directory was moved.
	newPath := wt.GetWorktreePath()
	if newPath == oldPath {
		t.Fatalf("worktree path unchanged after rename: %q", newPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new worktree dir missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old worktree dir still exists, err = %v", err)
	}
	// The checked-out HEAD in the moved worktree followed the branch rename.
	if got := strings.TrimSpace(mustRunGit(t, newPath, "branch", "--show-current")); got != newBranch {
		t.Fatalf("worktree HEAD branch = %q, want %q", got, newBranch)
	}
}

// Renaming to a name whose branch already belongs to another session must fail loudly and
// leave the instance completely untouched, rather than force-clobbering the other branch.
func TestRename_TargetBranchCollisionErrors(t *testing.T) {
	repoPath := newTestRepo(t)
	a, aBranch, err := NewGitWorktree(repoPath, "alpha")
	if err != nil {
		t.Fatalf("NewGitWorktree(alpha) error = %v", err)
	}
	if err := a.Setup(); err != nil {
		t.Fatalf("Setup(alpha) error = %v", err)
	}
	b, _, err := NewGitWorktree(repoPath, "beta")
	if err != nil {
		t.Fatalf("NewGitWorktree(beta) error = %v", err)
	}
	if err := b.Setup(); err != nil {
		t.Fatalf("Setup(beta) error = %v", err)
	}

	if err := a.Rename("beta"); err == nil {
		t.Fatal("Rename() expected an error when the target branch already exists")
	}
	if a.GetBranchName() != aBranch {
		t.Fatalf("branch mutated on collision: %q", a.GetBranchName())
	}
	if _, err := os.Stat(a.GetWorktreePath()); err != nil {
		t.Fatalf("worktree dir disturbed on collision: %v", err)
	}
}

// A paused session has had its worktree directory removed but keeps its branch. Rename must
// still rename the branch and recompute the stored worktree path (so a later Resume lands at
// the corrected path) without attempting an impossible move or creating a stray directory.
func TestRename_OrphanedWorktreeSkipsMove(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, oldBranch, err := NewGitWorktree(repoPath, "alpha")
	if err != nil {
		t.Fatalf("NewGitWorktree error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	// Simulate pause: remove the worktree dir but keep the branch.
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := wt.Prune(); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	oldPath := wt.GetWorktreePath()

	if err := wt.Rename("alpha-fixed"); err != nil {
		t.Fatalf("Rename() on orphaned worktree error = %v", err)
	}

	newBranch := wt.GetBranchName()
	if newBranch == oldBranch {
		t.Fatalf("branch name unchanged: %q", newBranch)
	}
	if out := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--list", newBranch)); out == "" {
		t.Fatalf("renamed branch %q not found", newBranch)
	}
	if wt.GetWorktreePath() == oldPath {
		t.Fatalf("worktree path not recomputed: %q", wt.GetWorktreePath())
	}
	if _, err := os.Stat(wt.GetWorktreePath()); !os.IsNotExist(err) {
		t.Fatalf("Rename created a worktree dir for an orphaned worktree, err = %v", err)
	}
}

// If the worktree move fails (here forced via a locked worktree), the branch rename must be
// rolled back so the session is left fully intact on its original names.
func TestRename_RollbackWhenMoveFails(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, oldBranch, err := NewGitWorktree(repoPath, "alpha")
	if err != nil {
		t.Fatalf("NewGitWorktree error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	oldPath := wt.GetWorktreePath()

	// Locking the worktree makes `git worktree move` refuse, exercising the rollback path.
	mustRunGit(t, repoPath, "worktree", "lock", oldPath)
	defer func() { _ = execUnlock(repoPath, oldPath) }()

	if err := wt.Rename("alpha-fixed"); err == nil {
		t.Fatal("Rename() expected an error when the worktree move fails")
	}

	if wt.GetBranchName() != oldBranch {
		t.Fatalf("branch not rolled back: %q, want %q", wt.GetBranchName(), oldBranch)
	}
	if wt.GetWorktreePath() != oldPath {
		t.Fatalf("worktree path not rolled back: %q, want %q", wt.GetWorktreePath(), oldPath)
	}
	if out := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--list", "*alpha-fixed*")); out != "" {
		t.Fatalf("rolled-back branch still present: %q", out)
	}
}

// execUnlock unlocks a worktree, ignoring errors (best-effort test cleanup).
func execUnlock(repoPath, worktreePath string) error {
	g := &GitWorktree{repoPath: repoPath}
	_, err := g.runGitCommand(repoPath, "worktree", "unlock", worktreePath)
	return err
}
