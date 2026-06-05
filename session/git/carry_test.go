package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
)

// writeCarryConfig persists a config whose carry_files list is exactly carry,
// so tests control the carried set instead of depending on the built-in
// default. Must run after newTestRepo has sandboxed HOME.
func writeCarryConfig(t *testing.T, carry []string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.CarryFiles = carry
	if err := config.SaveConfig(cfg); err != nil {
		t.Fatalf("save carry config: %v", err)
	}
}

// addGitignoredFile writes .gitignore (committed) plus the ignored file itself
// in the origin repo, returning the file's absolute path.
func addGitignoredFile(t *testing.T, repoPath, rel, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(rel+"\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	mustRunGit(t, repoPath, "add", ".gitignore")
	mustRunGit(t, repoPath, "commit", "-m", "ignore "+rel)

	abs := filepath.Join(repoPath, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return abs
}

// setupSessionWorktree creates and sets up a session worktree for repoPath.
func setupSessionWorktree(t *testing.T, repoPath, session string) *Worktree {
	t.Helper()
	wt, _, err := NewWorktree(context.Background(), repoPath, session)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return wt
}

// The headline behavior: a gitignored config file from the origin checkout is
// materialized into the fresh session worktree (worktrees carry only tracked
// files, so without the copy the agent would lose its local project config).
func TestSetup_CarriesGitignoredFile(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = ".claude/settings.local.json"
	addGitignoredFile(t, repoPath, rel, `{"hooks":{}}`)
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-basic")

	got, err := os.ReadFile(filepath.Join(wt.GetWorktreePath(), rel))
	if err != nil {
		t.Fatalf("carried file missing in worktree: %v", err)
	}
	if string(got) != `{"hooks":{}}` {
		t.Fatalf("carried content = %q, want %q", got, `{"hooks":{}}`)
	}
	// Mode is preserved (the source was 0600 — local config may hold secrets).
	info, err := os.Stat(filepath.Join(wt.GetWorktreePath(), rel))
	if err != nil {
		t.Fatalf("stat carried file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("carried file mode = %v, want 0600", info.Mode().Perm())
	}
}

// Pause commits the worktree with `git add .`, so carrying a file git does NOT
// ignore would silently leak it into the session branch and any PR. Such
// entries must be skipped.
func TestSetup_SkipsNonGitignoredCarryFile(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "not-ignored.json"
	if err := os.WriteFile(filepath.Join(repoPath, rel), []byte("x"), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-unignored")

	if _, err := os.Stat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("non-gitignored file must not be carried, stat err = %v", err)
	}
}

// Entries that point outside the repo (absolute or parent-escaping) are
// rejected: the carry list is repo-relative by contract.
func TestSetup_CarryRejectsUnsafePaths(t *testing.T) {
	repoPath := newTestRepo(t)

	// A real file one level above the repo that "../" would reach.
	parent := filepath.Dir(repoPath)
	if err := os.WriteFile(filepath.Join(parent, "escape.txt"), []byte("nope"), 0644); err != nil {
		t.Fatalf("write escape file: %v", err)
	}
	writeCarryConfig(t, []string{"../escape.txt", "/etc/hostname", ""})

	wt := setupSessionWorktree(t, repoPath, "carry-unsafe")

	wtParent := filepath.Dir(wt.GetWorktreePath())
	if _, err := os.Stat(filepath.Join(wtParent, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape file must not appear above the worktree, stat err = %v", err)
	}
}

// A carry entry that does not exist in the origin checkout is a silent no-op
// (the common case for repos that never created the file).
func TestSetup_CarryMissingSourceIsNoop(t *testing.T) {
	repoPath := newTestRepo(t)
	writeCarryConfig(t, []string{".claude/settings.local.json"})

	wt := setupSessionWorktree(t, repoPath, "carry-missing")

	if _, err := os.Stat(filepath.Join(wt.GetWorktreePath(), ".claude")); !os.IsNotExist(err) {
		t.Fatalf("nothing should be created for a missing source, stat err = %v", err)
	}
}

// A destination that already exists in the worktree (e.g. a tracked file that
// also matches an ignore pattern) is never clobbered.
func TestSetup_CarryDoesNotClobberExistingDestination(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "tracked-but-ignored.json"
	addGitignoredFile(t, repoPath, rel, "origin-local")
	// Force-track the file despite the ignore pattern, with different content.
	if err := os.WriteFile(filepath.Join(repoPath, rel), []byte("tracked"), 0644); err != nil {
		t.Fatalf("write tracked content: %v", err)
	}
	mustRunGit(t, repoPath, "add", "-f", rel)
	mustRunGit(t, repoPath, "commit", "-m", "track "+rel)
	// Origin's working copy diverges from the committed content.
	if err := os.WriteFile(filepath.Join(repoPath, rel), []byte("origin-local"), 0644); err != nil {
		t.Fatalf("rewrite origin content: %v", err)
	}
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-noclobber")

	got, err := os.ReadFile(filepath.Join(wt.GetWorktreePath(), rel))
	if err != nil {
		t.Fatalf("read tracked file in worktree: %v", err)
	}
	if string(got) != "tracked" {
		t.Fatalf("tracked file was clobbered: content = %q, want %q", got, "tracked")
	}
}

// Carry is strictly best-effort: an unreadable source must not fail Setup —
// Setup's callers tear the whole worktree down on error.
func TestSetup_CarryUnreadableSourceStillSucceeds(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: chmod 000 does not block reads")
	}
	repoPath := newTestRepo(t)
	const rel = ".claude/settings.local.json"
	abs := addGitignoredFile(t, repoPath, rel, "secret")
	if err := os.Chmod(abs, 0000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(abs, 0600) })
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-unreadable") // Setup must not error

	if _, err := os.Stat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("unreadable source must not be carried, stat err = %v", err)
	}
}

// Pause removes the worktree and resume re-runs Setup at the same path: the
// carried file must reappear in the recreated worktree.
func TestSetup_CarryReappliesAfterPauseResume(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = ".claude/settings.local.json"
	addGitignoredFile(t, repoPath, rel, "local")
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-resume")
	carried := filepath.Join(wt.GetWorktreePath(), rel)
	if _, err := os.Stat(carried); err != nil {
		t.Fatalf("carried file missing after initial Setup: %v", err)
	}

	// Pause: remove the worktree, keep the branch. Resume: Setup again.
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup (resume): %v", err)
	}

	if _, err := os.Stat(carried); err != nil {
		t.Fatalf("carried file missing after resume Setup: %v", err)
	}
}
