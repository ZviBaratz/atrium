package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiff_RepoStats exercises the real git wiring for commits/behind/dirty/files
// end-to-end, which is where a swapped left/right or wrong base ref would surface.
func TestDiff_RepoStats(t *testing.T) {
	repoPath := newTestRepo(t)
	baseBranch := strings.TrimSpace(mustRunGit(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD"))

	wt, _, err := NewWorktreeFromBase(context.Background(), repoPath, "sess", baseBranch)
	if err != nil {
		t.Fatalf("NewWorktreeFromBase: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	wtPath := wt.GetWorktreePath()

	// Fresh session: even with the base ref, nothing has diverged.
	stats := wt.Diff()
	if stats.Error != nil {
		t.Fatalf("Diff error: %v", stats.Error)
	}
	if stats.Commits != 0 || stats.Behind != 0 || stats.Dirty || stats.FilesChanged != 0 {
		t.Fatalf("fresh session: got commits=%d behind=%d dirty=%v files=%d, want all zero/false",
			stats.Commits, stats.Behind, stats.Dirty, stats.FilesChanged)
	}

	// Uncommitted edit in the worktree → dirty + a changed file, no new commit.
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	stats = wt.Diff()
	if !stats.Dirty {
		t.Errorf("after uncommitted edit: Dirty = false, want true")
	}
	if stats.FilesChanged < 1 {
		t.Errorf("after uncommitted edit: FilesChanged = %d, want >= 1", stats.FilesChanged)
	}
	if stats.Commits != 0 {
		t.Errorf("after uncommitted edit: Commits = %d, want 0", stats.Commits)
	}

	// Commit it in the worktree → one commit ahead, no longer dirty.
	mustRunGit(t, wtPath, "add", ".")
	mustRunGit(t, wtPath, "commit", "-m", "session work")
	stats = wt.Diff()
	if stats.Commits != 1 {
		t.Errorf("after commit: Commits = %d, want 1", stats.Commits)
	}
	if stats.Dirty {
		t.Errorf("after commit: Dirty = true, want false")
	}
	if stats.Behind != 0 {
		t.Errorf("after commit: Behind = %d, want 0", stats.Behind)
	}

	// Advance the base branch in the main repo → the session is now behind by one,
	// still ahead by one. This is the assertion that catches a swapped left/right.
	if err := os.WriteFile(filepath.Join(repoPath, "base.txt"), []byte("moved on\n"), 0644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	mustRunGit(t, repoPath, "add", ".")
	mustRunGit(t, repoPath, "commit", "-m", "base advances")
	stats = wt.Diff()
	if stats.Behind != 1 {
		t.Errorf("after base advances: Behind = %d, want 1", stats.Behind)
	}
	if stats.Commits != 1 {
		t.Errorf("after base advances: Commits = %d, want 1 (unchanged)", stats.Commits)
	}
}
