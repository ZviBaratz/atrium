package git

import (
	"context"
	"os"
	"os/exec"
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
// guard does the work: the force-tracked file makes `git worktree add`
// materialize node_modules/ as a real directory, so the clobber guard's
// Lstat(dst) matches and returns first — the ignore check below it is never
// reached here (it would also refuse the entry, since check-ignore consults the
// index and reports force-tracked content as NOT ignored, so it is a backstop
// rather than the gate). TestLinkLocalPath_NeverClobbersExistingDestination
// exercises the clobber guard where nothing else can stand in for it.
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

	wt.linkLocalPath("link_paths", rel)

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

// Seeding is itself a writer of destination directories, so an earlier entry can
// occupy a later entry's destination: linking node_modules/.cache first creates
// node_modules/ as a real directory, after which node_modules can only be skipped.
// The never-clobber guard must hold (the .cache link stays intact) — and unlike
// carry's silent equivalent this skip is warned about, because nothing the user
// did put that directory there, and a session missing its dependencies otherwise
// looks like the feature simply not working.
func TestSetup_LinkSkippedWhenAnEarlierEntryOccupiedTheDestination(t *testing.T) {
	repoPath := newTestRepo(t)
	commitGitignore(t, repoPath, "node_modules")
	makeDepsDir(t, repoPath, "node_modules/.cache")
	writeLinkConfig(t, []string{"node_modules/.cache", "node_modules"})

	wt := setupSessionWorktree(t, repoPath, "link-order")
	wtPath := wt.GetWorktreePath()

	cache, err := os.Lstat(filepath.Join(wtPath, "node_modules/.cache"))
	if err != nil {
		t.Fatalf("the first entry must still be linked: %v", err)
	}
	if cache.Mode()&os.ModeSymlink == 0 {
		t.Errorf("node_modules/.cache must be a symlink, got mode %v", cache.Mode())
	}
	parent, err := os.Lstat(filepath.Join(wtPath, "node_modules"))
	if err != nil {
		t.Fatalf("lstat node_modules: %v", err)
	}
	if parent.Mode()&os.ModeSymlink != 0 {
		t.Errorf("node_modules was created as a parent dir, so it must not be replaced by a link; got mode %v", parent.Mode())
	}
}

// A nested entry whose parent dirs are not in the checkout makes seedLocalPaths
// create them, leaving a directory whose only content is the ignored link. Such a
// directory must not read as untracked: the diff intent-adds every untracked path
// on each 500ms tick, so a permanently-listed one would run `add -N` for the life
// of the session — the churn #167 removed.
func TestSetup_NestedLinkLeavesNothingUntracked(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "container/agent-runner/node_modules"
	commitGitignore(t, repoPath, "node_modules")
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupSessionWorktree(t, repoPath, "link-nested-untracked")
	wtPath := wt.GetWorktreePath()

	if _, err := os.Lstat(filepath.Join(wtPath, rel)); err != nil {
		t.Fatalf("nested link missing: %v", err)
	}
	// Assert on the production listing itself: it being empty is exactly what stops
	// the per-tick `add -N` from running. Index residue cannot stand in for it —
	// adding a directory of ignored content leaves none whether or not it was added.
	if paths := wt.untrackedPathsToIntentAdd(wtPath); len(paths) != 0 {
		t.Errorf("the parent dirs created for a nested link must not read as untracked (the diff would intent-add them every tick), got %q", paths)
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

// An entry is canonicalized before anything derives from it, so the path the
// ignore guard asks git about is always the path the symlink is created at.
// Without that they diverge: filepath.Join silently drops a trailing separator
// while the raw entry still reaches `git check-ignore`, and a trailing slash
// makes git resolve the pathspec as a *directory* — which a dir-only pattern
// matches even though the slash-less symlink (a file to git) does not. The guard
// would report "ignored", the link would be created un-ignored, and pause's
// `git add .` would commit it. Spellings that mean the same path must therefore
// reach the same verdict as the plain one.
func TestSetup_LinkEntrySpellingsAreCanonicalized(t *testing.T) {
	for _, tc := range []struct {
		name     string
		entry    string
		pattern  string
		wantLink bool
	}{
		// A dir-only pattern never covers the symlink, however the entry is spelled.
		{"trailing slash, dir-only pattern", "node_modules/", "node_modules/", false},
		{"dot suffix, dir-only pattern", "node_modules/.", "node_modules/", false},
		// A slash-less pattern does cover it, and canonicalizing must not break that.
		{"trailing slash, slash-less pattern", "node_modules/", "node_modules", true},
		{"dot-slash prefix, slash-less pattern", "./node_modules", "node_modules", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := newTestRepo(t)
			const rel = "node_modules"
			commitGitignore(t, repoPath, tc.pattern)
			makeDepsDir(t, repoPath, rel)
			writeLinkConfig(t, []string{tc.entry})

			wt := setupSessionWorktree(t, repoPath, "link-canon")
			wtPath := wt.GetWorktreePath()

			info, err := os.Lstat(filepath.Join(wtPath, rel))
			if !tc.wantLink {
				if err == nil {
					t.Fatalf("entry %q under pattern %q must be refused (it would leak into `git add .`), got mode %v",
						tc.entry, tc.pattern, info.Mode())
				}
				return
			}
			if err != nil {
				t.Fatalf("entry %q under pattern %q must still be linked: %v", tc.entry, tc.pattern, err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("entry %q must be linked as a symlink, got mode %v", tc.entry, info.Mode())
			}
			// The link the guard did allow must genuinely be ignored.
			mustRunGit(t, wtPath, "add", ".")
			if staged := mustRunGit(t, wtPath, "ls-files", "-s"); strings.Contains(staged, rel) {
				t.Fatalf("linked path must not be staged by `git add .`, got:\n%s", staged)
			}
		})
	}
}

// Hermeticity guard for every ignore-state assertion in this package. git reads
// its global excludes from $XDG_CONFIG_HOME/git/ignore *before*
// $HOME/.config/git/ignore, so sandboxing HOME alone leaves the developer's real
// global gitignore deciding these tests. A host that globally ignores
// node_modules — a common entry — makes a correct implementation fail
// TestSetup_LinkRefusesDirOnlyIgnorePattern, because the link then really is
// ignored and is created as designed.
func TestTestReposSeeNoGlobalGitignore(t *testing.T) {
	if v, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		t.Fatalf("XDG_CONFIG_HOME must be unset for tests (git prefers $XDG_CONFIG_HOME/git/ignore over the sandboxed $HOME/.config/git/ignore); got %q", v)
	}

	repoPath := newTestRepo(t)
	// Plant a rule where the per-user lookups would find it, then confirm none of
	// them reaches the repo: only the committed .gitignore may decide ignore state.
	globalIgnore := filepath.Join(os.Getenv("HOME"), ".config", "git", "ignore")
	if err := os.MkdirAll(filepath.Dir(globalIgnore), 0755); err != nil {
		t.Fatalf("mkdir global ignore dir: %v", err)
	}
	if err := os.WriteFile(globalIgnore, []byte("node_modules\n"), 0644); err != nil {
		t.Fatalf("write global ignore: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), "git", "-C", repoPath, "check-ignore", "-q", "--", "node_modules")
	if err := cmd.Run(); err == nil {
		t.Fatal("node_modules reads as ignored in a repo whose .gitignore is empty — a global gitignore is leaking into the tests")
	}
}

// resolveSeedPaths is the only thing standing between an unsafe entry and the
// filesystem, so it is asserted directly: the Setup-level test cannot observe it
// (the later Lstat/check-ignore guards refuse the same inputs for their own
// reasons, so it passes with the containment checks deleted).
func TestResolveSeedPaths_RejectsUnsafeEntries(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "resolve-unsafe")

	for _, entry := range []string{"", "/etc", "../escape", "node_modules/../../escape", "."} {
		if _, _, _, ok := wt.resolveSeedPaths("link_paths", entry); ok {
			t.Errorf("entry %q must be rejected as unsafe", entry)
		}
	}
}

// The canonical form is what callers must use as the git pathspec, since that is
// the spelling the filesystem paths were derived from.
func TestResolveSeedPaths_CanonicalizesEntries(t *testing.T) {
	repoPath := newTestRepo(t)
	wt := setupSessionWorktree(t, repoPath, "resolve-canon")

	for _, entry := range []string{"node_modules", "node_modules/", "./node_modules", "node_modules/."} {
		canon, src, dst, ok := wt.resolveSeedPaths("link_paths", entry)
		if !ok {
			t.Fatalf("entry %q must resolve", entry)
		}
		if canon != "node_modules" {
			t.Errorf("entry %q: canon = %q, want %q", entry, canon, "node_modules")
		}
		// Expected against the worktree's own repo path, not the test's: on macOS
		// /var is a symlink to /private/var, so the resolved form the Worktree holds
		// is the only thing src can be compared to.
		if want := filepath.Join(wt.repoPath, "node_modules"); src != want {
			t.Errorf("entry %q: src = %q, want %q", entry, src, want)
		}
		if want := filepath.Join(wt.GetWorktreePath(), "node_modules"); dst != want {
			t.Errorf("entry %q: dst = %q, want %q", entry, dst, want)
		}
	}
}
