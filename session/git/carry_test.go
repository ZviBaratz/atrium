package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// The ignore check must be answered from the worktree, not the origin checkout:
// only the worktree's view decides what pause's `git add .` will stage, and the
// two disagree whenever the ignore rule is not committed on this session's base.
// This is the default configuration's own shape — carry_files ships pointing at
// .claude/settings.local.json, whose ignore rule an agent commonly adds without
// committing it — and that file can hold secrets, so leaking it into the session
// branch is the worst outcome here.
func TestSetup_SkipsCarryFileWhoseIgnoreRuleIsUncommitted(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = ".claude/settings.local.json"

	// The rule exists in the origin's working tree but was never committed, so the
	// worktree — checked out from HEAD — does not have it.
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(rel+"\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	abs := filepath.Join(repoPath, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(`{"hooks":{}}`), 0600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	writeCarryConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "carry-uncommitted-rule")

	if _, err := os.Stat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("a file git will not ignore in the worktree must not be carried, stat err = %v", err)
	}
}

// The same property stated as its consequence: whatever carry materializes must
// survive pause's `git add .` without entering the index. Asserting on the commit
// rather than on the copy is what makes the guard's purpose observable — a
// worktree-side ignore verdict is the only thing that can promise this.
func TestSetup_CarriedFilesNeverEnterAPauseCommit(t *testing.T) {
	repoPath := newTestRepo(t)
	const committed = ".claude/settings.local.json"
	const uncommittedRule = "local-notes.txt"

	addGitignoredFile(t, repoPath, committed, `{"hooks":{}}`)
	// A second entry whose rule lands in .gitignore without being committed. This
	// rewrites rather than appends, so it must restate `committed` — addGitignoredFile
	// wrote .gitignore with that single entry and committed it.
	gitignore := filepath.Join(repoPath, ".gitignore")
	body := committed + "\n" + uncommittedRule + "\n"
	if err := os.WriteFile(gitignore, []byte(body), 0644); err != nil {
		t.Fatalf("rewrite .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, uncommittedRule), []byte("notes"), 0600); err != nil {
		t.Fatalf("write %s: %v", uncommittedRule, err)
	}
	writeCarryConfig(t, []string{committed, uncommittedRule})

	wt := setupSessionWorktree(t, repoPath, "carry-pause-commit")
	wtPath := wt.GetWorktreePath()

	// Assert the copy happened before asserting it stays out of the index: without
	// this the test passes with carry disabled entirely (nothing materialized, so
	// nothing can leak), which would make the guard's purpose unobservable — the
	// very thing this test exists to show.
	if _, err := os.Stat(filepath.Join(wtPath, committed)); err != nil {
		t.Fatalf("%s was not carried, so the no-leak assertion below proves nothing: %v", committed, err)
	}

	// Exactly what pause does.
	mustRunGit(t, wtPath, "add", ".")
	staged := mustRunGit(t, wtPath, "ls-files", "-s")
	for _, rel := range []string{committed, uncommittedRule} {
		if strings.Contains(staged, rel) {
			t.Fatalf("carried path %q entered the index — pause would commit it into the session branch:\n%s", rel, staged)
		}
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
