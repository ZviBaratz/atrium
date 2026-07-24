package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
)

// writeLinkConfig persists a config whose link_paths list is exactly paths (and
// whose carry list is empty, so the two features are tested in isolation). Must
// run after newTestRepo has sandboxed HOME.
func writeLinkConfig(t *testing.T, paths []string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{}
	cfg.LinkPaths = paths
	if err := config.SaveConfig(cfg); err != nil {
		t.Fatalf("save link config: %v", err)
	}
}

// commitGitignore writes and commits a .gitignore holding exactly patterns.
func commitGitignore(t *testing.T, repoPath string, patterns ...string) {
	t.Helper()
	body := strings.Join(patterns, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(body), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	mustRunGit(t, repoPath, "add", ".gitignore")
	mustRunGit(t, repoPath, "commit", "-m", "ignore")
}

// makeDepsDir creates rel as a directory in the origin checkout with one file
// inside, and returns the directory's absolute path.
func makeDepsDir(t *testing.T, repoPath, rel string) string {
	t.Helper()
	abs := filepath.Join(repoPath, rel)
	if err := os.MkdirAll(abs, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(filepath.Join(abs, "marker.txt"), []byte("installed"), 0644); err != nil {
		t.Fatalf("write marker in %s: %v", rel, err)
	}
	return abs
}

// The headline behavior: a gitignored dependency directory is symlinked (not
// copied) into the fresh worktree, with an absolute target, so the project's
// tooling resolves through it exactly as it does in the origin checkout.
func TestSetup_LinksGitignoredDirectory(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	origin := makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-basic")
	dst := filepath.Join(wt.GetWorktreePath(), rel)

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("linked path missing in worktree: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("linked path must be a symlink, got mode %v (a copy would be huge and slow)", info.Mode())
	}

	target, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("symlink target must be absolute, got %q", target)
	}
	originInfo, err := os.Stat(origin)
	if err != nil {
		t.Fatalf("stat origin dir: %v", err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat symlink target %q: %v", target, err)
	}
	if !os.SameFile(originInfo, targetInfo) {
		t.Fatalf("symlink target %q is not the origin checkout's %q", target, origin)
	}

	// The whole point: content resolves through the link.
	got, err := os.ReadFile(filepath.Join(dst, "marker.txt"))
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(got) != "installed" {
		t.Fatalf("content through symlink = %q, want %q", got, "installed")
	}
}

// A dir-only ignore pattern ("node_modules/") matches the origin *directory*
// but not the symlink we create, which git stores as a file. Checking in the
// origin checkout would therefore false-pass and leak the link into the session
// branch, so the check runs in the worktree and this entry must be refused.
func TestSetup_LinkRefusesDirOnlyIgnorePattern(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel+"/") // trailing slash: directories only
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-dironly") // Setup must not error

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("a dir-only ignore pattern does not cover the symlink, so it must be refused; lstat err = %v", err)
	}
}

// The property the ignore guard exists to protect, end to end: a correctly
// ignored link is invisible to both consumers of ignore state — `git add .`
// (pause commits the worktree with it) and the untracked listing the diff
// stages on every poll tick.
func TestSetup_LinkedPathStaysOutOfTheIndex(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-index")
	wtPath := wt.GetWorktreePath()

	if others := mustRunGit(t, wtPath, "ls-files", "--others", "--exclude-standard", "--directory"); strings.Contains(others, rel) {
		t.Fatalf("linked path must not be listed as untracked (the diff would stage it every tick), got:\n%s", others)
	}
	mustRunGit(t, wtPath, "add", ".")
	if staged := mustRunGit(t, wtPath, "ls-files", "-s"); strings.Contains(staged, rel) {
		t.Fatalf("linked path must not be staged by `git add .` (pause would commit it), got:\n%s", staged)
	}
}

// Characterization test for the blast radius the ignore guard bounds: git stores
// an un-ignored symlinked directory as a single mode-120000 blob holding the
// target path — it does not recurse into the tree. Bad enough to keep refusing
// (a machine-specific absolute path committed to the session branch), but not
// the recursive import a copy would be. Built by hand because the guard makes
// this state unreachable through Setup.
func TestLinkedDirIsStagedAsSymlinkNotRecursed(t *testing.T) {
	repoPath := newTestRepo(t)
	origin := makeDepsDir(t, repoPath, "node_modules") // not gitignored here

	wtPath := filepath.Join(t.TempDir(), "wt")
	mustRunGit(t, repoPath, "worktree", "add", wtPath, "-b", "probe")
	if err := os.Symlink(origin, filepath.Join(wtPath, "node_modules")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	mustRunGit(t, wtPath, "add", ".")
	staged := mustRunGit(t, wtPath, "ls-files", "-s")
	if !strings.Contains(staged, "120000") {
		t.Fatalf("expected the symlink staged as mode 120000, got:\n%s", staged)
	}
	if strings.Contains(staged, "node_modules/marker.txt") {
		t.Fatalf("git must not recurse into a symlinked directory, got:\n%s", staged)
	}
}

// A link entry whose origin path does not exist yet (fresh clone, no install
// run) is a silent no-op, matching carry's "not present: nothing to carry".
func TestSetup_LinkMissingOriginIsNoop(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-missing")

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("nothing should be created for a missing origin path, lstat err = %v", err)
	}
}

// A destination that already holds tracked content survives Setup. Note which
// guard does the work: `git check-ignore` consults the index, so a directory
// with a force-tracked file inside reports as NOT ignored (verified: the same
// path is ignored under --no-index), and the ignore check refuses the entry
// before the clobber guard is even consulted. The clobber guard is tested
// directly by TestLinkLocalPath_NeverClobbersExistingDestination.
func TestSetup_LinkDoesNotClobberExistingDestination(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	makeDepsDir(t, repoPath, rel)
	// Force-track one file inside the ignored directory, so the worktree
	// materializes node_modules/ as a real directory.
	tracked := filepath.Join(repoPath, rel, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("tracked"), 0644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	mustRunGit(t, repoPath, "add", "-f", filepath.Join(rel, "tracked.txt"))
	mustRunGit(t, repoPath, "commit", "-m", "track one dep file")
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-noclobber")
	dst := filepath.Join(wt.GetWorktreePath(), rel)

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("an existing destination must never be replaced by a symlink")
	}
	if _, err := os.Stat(filepath.Join(dst, "tracked.txt")); err != nil {
		t.Fatalf("tracked file inside the destination was lost: %v", err)
	}
}

// The never-clobber guard, tested where it is the only thing standing between
// the link and existing content: the path is properly ignored (so the ignore
// check passes) and something real already sits at the destination. os.Symlink
// refusing to overwrite is a second line of defense, so the guard's value shows
// up against a future "clear the destination first" change — this test fails if
// the guard is removed and any such clearing is introduced, and it must never
// see the destination replaced.
func TestLinkLocalPath_NeverClobbersExistingDestination(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-clobber")
	dst := filepath.Join(wt.GetWorktreePath(), rel)

	// Replace the link Setup created with real content, then re-run the seeding
	// step (Setup re-seeds on every materialization).
	if err := os.Remove(dst); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	precious := filepath.Join(dst, "precious.txt")
	if err := os.WriteFile(precious, []byte("do not delete"), 0644); err != nil {
		t.Fatalf("write precious file: %v", err)
	}

	wt.linkLocalPath(rel)

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("an existing destination must never be replaced by a symlink")
	}
	got, err := os.ReadFile(precious)
	if err != nil {
		t.Fatalf("content at an existing destination was destroyed: %v", err)
	}
	if string(got) != "do not delete" {
		t.Fatalf("content at destination = %q, want %q", got, "do not delete")
	}
}

// Entries that point outside the repo (absolute or parent-escaping) are
// rejected: link_paths is repo-relative by contract.
func TestSetup_LinkRejectsUnsafePaths(t *testing.T) {
	repoPath := newTestRepo(t)
	parent := filepath.Dir(repoPath)
	if err := os.MkdirAll(filepath.Join(parent, "escape"), 0755); err != nil {
		t.Fatalf("mkdir escape dir: %v", err)
	}
	writeLinkConfig(t, []string{"../escape", "/etc", ""})

	wt := setupSessionWorktree(t, repoPath, "link-unsafe")

	wtParent := filepath.Dir(wt.GetWorktreePath())
	if _, err := os.Lstat(filepath.Join(wtParent, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escape entry must not appear above the worktree, lstat err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "etc")); !os.IsNotExist(err) {
		t.Fatalf("absolute entry must not be materialized, lstat err = %v", err)
	}
}

// The real nanoclaw shape: a nested dependency directory under a tracked parent.
func TestSetup_LinksNestedPath(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "container/agent-runner/node_modules"
	// A tracked file makes container/agent-runner exist in the worktree.
	if err := os.MkdirAll(filepath.Join(repoPath, "container/agent-runner"), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "container/agent-runner/index.js"), []byte("//\n"), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	mustRunGit(t, repoPath, "add", "container")
	mustRunGit(t, repoPath, "commit", "-m", "add runner")
	commitGitignore(t, repoPath, "node_modules")
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-nested")
	dst := filepath.Join(wt.GetWorktreePath(), rel)

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("nested linked path missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("nested linked path must be a symlink, got mode %v", info.Mode())
	}
	if _, err := os.ReadFile(filepath.Join(dst, "marker.txt")); err != nil {
		t.Fatalf("read through nested symlink: %v", err)
	}
}

// Data-loss guard: tearing a session down must never delete through the link.
// Both removal paths are exercised — git's own `worktree remove -f` (Cleanup)
// and the RemoveAll fallback Cleanup/pause use when git can no longer manage the
// worktree.
func TestLinkedOriginSurvivesWorktreeTeardown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(t *testing.T, wt *Worktree)
	}{
		{"cleanup", func(t *testing.T, wt *Worktree) {
			if err := wt.Cleanup(); err != nil {
				t.Fatalf("Cleanup: %v", err)
			}
		}},
		{"orphan-dir-fallback", func(t *testing.T, wt *Worktree) {
			if err := removeOrphanedWorktreeDir(wt.GetWorktreePath()); err != nil {
				t.Fatalf("removeOrphanedWorktreeDir: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := newTestRepo(t)
			const rel = "node_modules"
			commitGitignore(t, repoPath, rel)
			origin := makeDepsDir(t, repoPath, rel)
			writeLinkConfig(t, []string{rel})

			wt := setupSessionWorktree(t, repoPath, "link-teardown")
			if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), rel)); err != nil {
				t.Fatalf("link missing before teardown: %v", err)
			}

			tc.remove(t, wt)

			if _, err := os.Stat(filepath.Join(origin, "marker.txt")); err != nil {
				t.Fatalf("teardown deleted through the symlink — the origin checkout's deps are gone: %v", err)
			}
		})
	}
}

// Pause removes the worktree and resume re-runs Setup at the same path: the link
// must reappear, or the resumed session is as broken as a fresh one.
func TestSetup_LinkReappliesAfterPauseResume(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-resume")
	dst := filepath.Join(wt.GetWorktreePath(), rel)
	if _, err := os.Lstat(dst); err != nil {
		t.Fatalf("link missing after initial Setup: %v", err)
	}

	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup (resume): %v", err)
	}

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("link missing after resume Setup: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("resumed worktree must hold a symlink, got mode %v", info.Mode())
	}
}
