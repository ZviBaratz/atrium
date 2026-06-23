package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDiff_UntrackedFilesAppearInDiff is the core behavior-preservation test: a brand
// new untracked file (including one nested in a new untracked directory) must still be
// surfaced by Diff() and counted by DiffNumstat() even though intentAddUntracked now
// scopes `git add -N` to just the untracked paths instead of running `add -N .`.
func TestDiff_UntrackedFilesAppearInDiff(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "sess")
	wtPath := wt.GetWorktreePath()

	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("brand new\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wtPath, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "sub", "nested.txt"), []byte("nested new\n"), 0644); err != nil {
		t.Fatalf("write sub/nested.txt: %v", err)
	}

	stats := wt.Diff()
	if stats.Error != nil {
		t.Fatalf("Diff error: %v", stats.Error)
	}
	if !strings.Contains(stats.Content, "new.txt") || !strings.Contains(stats.Content, "brand new") {
		t.Errorf("Diff content missing untracked file:\n%s", stats.Content)
	}
	if !strings.Contains(stats.Content, "nested.txt") {
		t.Errorf("Diff content missing nested untracked file:\n%s", stats.Content)
	}
	if stats.FilesChanged != 2 {
		t.Errorf("Diff FilesChanged = %d, want 2", stats.FilesChanged)
	}

	wt.invalidateStatsCache()
	num := wt.DiffNumstat()
	if num.Error != nil {
		t.Fatalf("DiffNumstat error: %v", num.Error)
	}
	if num.FilesChanged != 2 {
		t.Errorf("DiffNumstat FilesChanged = %d, want 2", num.FilesChanged)
	}
	if num.Added != 2 {
		t.Errorf("DiffNumstat Added = %d, want 2 (one line per new file)", num.Added)
	}
}

// stagedEntries returns the worktree's intent-to-add / staged entries (git diff --cached
// --name-only). It must be empty whenever there are no untracked files, proving
// intentAddUntracked left no residue in the index the agent is using.
func stagedEntries(t *testing.T, wtPath string) string {
	t.Helper()
	return strings.TrimSpace(mustRunGit(t, wtPath, "diff", "--cached", "--name-only"))
}

// TestDiff_TrackedOnlyChange_NoIndexResidue is the regression test for the steady-state
// index-write elimination and the `git stash` interference: when only a tracked file is
// modified (no untracked files present), the change still diffs correctly AND no
// intent-to-add entry is left in the index, because add -N is skipped entirely.
func TestDiff_TrackedOnlyChange_NoIndexResidue(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "sess")
	wtPath := wt.GetWorktreePath()

	// README.md is a tracked file (created by newTestRepo); modify it in the worktree.
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("hello\nmodified\n"), 0644); err != nil {
		t.Fatalf("modify README: %v", err)
	}

	stats := wt.Diff()
	if stats.Error != nil {
		t.Fatalf("Diff error: %v", stats.Error)
	}
	if !strings.Contains(stats.Content, "README.md") || !strings.Contains(stats.Content, "+modified") {
		t.Errorf("tracked change missing from diff:\n%s", stats.Content)
	}
	if got := stagedEntries(t, wtPath); got != "" {
		t.Errorf("add -N residue after tracked-only change: staged entries = %q, want none", got)
	}
}

// TestDiff_UntrackedThenCommitted_NoResidue confirms the skip path engages once the
// worktree is clean of untracked files: an untracked file is surfaced, committed, and a
// later Diff() leaves no intent-to-add residue.
func TestDiff_UntrackedThenCommitted_NoResidue(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "sess")
	wtPath := wt.GetWorktreePath()

	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	if stats := wt.Diff(); stats.Error != nil {
		t.Fatalf("Diff (untracked) error: %v", stats.Error)
	}

	mustRunGit(t, wtPath, "add", ".")
	mustRunGit(t, wtPath, "commit", "-m", "add new.txt")
	wt.invalidateStatsCache()

	if stats := wt.Diff(); stats.Error != nil {
		t.Fatalf("Diff (after commit) error: %v", stats.Error)
	}
	if got := stagedEntries(t, wtPath); got != "" {
		t.Errorf("add -N residue after committing untracked file: staged entries = %q, want none", got)
	}
}

// TestDiff_IgnoredFileExcluded verifies the scoped intent-add honors .gitignore exactly
// like `add -N .` did: an ignored file appears in neither the untracked set nor the diff.
func TestDiff_IgnoredFileExcluded(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "sess")
	wtPath := wt.GetWorktreePath()

	if err := os.WriteFile(filepath.Join(wtPath, ".gitignore"), []byte("ignored.txt\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "ignored.txt"), []byte("secret\n"), 0644); err != nil {
		t.Fatalf("write ignored.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "visible.txt"), []byte("shown\n"), 0644); err != nil {
		t.Fatalf("write visible.txt: %v", err)
	}

	stats := wt.Diff()
	if stats.Error != nil {
		t.Fatalf("Diff error: %v", stats.Error)
	}
	// Match the file's diff header (not the bare name, which also appears as a line
	// inside .gitignore's own diff) and its content.
	if strings.Contains(stats.Content, "b/ignored.txt") || strings.Contains(stats.Content, "secret") {
		t.Errorf("ignored file leaked into diff:\n%s", stats.Content)
	}
	if !strings.Contains(stats.Content, "visible.txt") {
		t.Errorf("visible untracked file missing from diff:\n%s", stats.Content)
	}
}

// TestDiff_UntrackedShownDespiteShowUntrackedFilesNo is the regression test for the
// config-independence of untracked discovery: a user (or repo) may set
// `status.showUntrackedFiles=no`, which makes `git status` hide untracked files. The old
// `git add -N .` ignored that setting, so untracked files always showed in the diff;
// intentAddUntracked must preserve that by listing via `git ls-files --others` (which is
// not governed by the setting) rather than parsing `git status`.
func TestDiff_UntrackedShownDespiteShowUntrackedFilesNo(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "sess")
	wtPath := wt.GetWorktreePath()

	// Hide untracked files from `git status` in this worktree's config. If
	// intentAddUntracked derived its paths from status, new.txt would vanish from the diff.
	mustRunGit(t, wtPath, "config", "status.showUntrackedFiles", "no")

	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("brand new\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	stats := wt.Diff()
	if stats.Error != nil {
		t.Fatalf("Diff error: %v", stats.Error)
	}
	if !strings.Contains(stats.Content, "new.txt") || !strings.Contains(stats.Content, "brand new") {
		t.Errorf("untracked file hidden from diff under status.showUntrackedFiles=no:\n%s", stats.Content)
	}
	if stats.FilesChanged != 1 {
		t.Errorf("Diff FilesChanged = %d, want 1", stats.FilesChanged)
	}
}

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
	// The first Diff() above cached dirty=false; backdate the cache entry past
	// dirtyCacheTTL so the next Diff() re-runs git status without a real sleep.
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	wt.statsCacheMu.Lock()
	wt.statsCache.dirtyComputedAt = time.Now().Add(-(dirtyCacheTTL + time.Millisecond))
	wt.statsCacheMu.Unlock()
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
	// Direct git commit bypasses CommitChanges, so invalidate the cache manually.
	wt.invalidateStatsCache()
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
	// The base branch advanced externally; invalidate so the next Diff re-runs rev-list.
	wt.invalidateStatsCache()
	stats = wt.Diff()
	if stats.Behind != 1 {
		t.Errorf("after base advances: Behind = %d, want 1", stats.Behind)
	}
	if stats.Commits != 1 {
		t.Errorf("after base advances: Commits = %d, want 1 (unchanged)", stats.Commits)
	}
}
