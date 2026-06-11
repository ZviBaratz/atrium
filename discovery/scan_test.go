package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkRepo creates rel under base with a .git directory and returns its path.
func mkRepo(t *testing.T, base, rel string) string {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// mkRepoFile creates rel under base with a .git *file* (the linked-worktree /
// submodule layout) and returns its path.
func mkRepoFile(t *testing.T, base, rel string) string {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkDir(t *testing.T, base, rel string) string {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func scan(t *testing.T, opts Options) []string {
	t.Helper()
	return Scan(context.Background(), opts)
}

func assertPaths(t *testing.T, got []string, want ...string) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	if len(got) != len(want) {
		t.Fatalf("got %d repos %v, want %d %v", len(got), got, len(want), want)
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Fatalf("missing %q in %v", w, got)
		}
	}
}

func TestScanFindsGitDirAndGitFileRepos(t *testing.T) {
	root := t.TempDir()
	alpha := mkRepo(t, root, "alpha")
	beta := mkRepoFile(t, root, "beta")
	mkDir(t, root, "gamma") // plain dir, not a repo

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 2})
	assertPaths(t, got, alpha, beta)
}

func TestScanDoesNotDescendIntoFoundRepos(t *testing.T) {
	root := t.TempDir()
	outer := mkRepo(t, root, "outer")
	mkRepo(t, root, "outer/inner") // nested repo: must not be reported

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 5})
	assertPaths(t, got, outer)
}

func TestScanRespectsMaxDepth(t *testing.T) {
	root := t.TempDir()
	shallow := mkRepo(t, root, "a/r1") // depth 2
	mkRepo(t, root, "a/b/r2")          // depth 3: beyond MaxDepth 2
	atRoot := mkRepo(t, root, "r0")    // depth 1

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 2})
	assertPaths(t, got, shallow, atRoot)
}

func TestScanSkipsIgnoredAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	ok := mkRepo(t, root, "ok/w")
	mkRepo(t, root, "node_modules/x")
	mkRepo(t, root, "vendor/y")
	mkRepo(t, root, ".hidden/z")
	mkRepo(t, root, "snap/app")
	mkRepo(t, root, "venv/lib")

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 3})
	assertPaths(t, got, ok)
}

func TestScanSkipsSkipPaths(t *testing.T) {
	root := t.TempDir()
	ok := mkRepo(t, root, "keep/repo")
	data := mkDir(t, root, "data")
	mkRepo(t, root, "data/worktrees/wt1")

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 4, SkipPaths: []string{data}})
	assertPaths(t, got, ok)
}

func TestScanHandlesMissingAndDuplicateRoots(t *testing.T) {
	root := t.TempDir()
	repo := mkRepo(t, root, "repo")
	missing := filepath.Join(root, "does-not-exist")

	got := scan(t, Options{Roots: []string{missing, root, root}, MaxDepth: 2})
	assertPaths(t, got, repo)
}

func TestScanRootRepoRecordedAndStillDescended(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := mkRepo(t, root, "sub")

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 2})
	assertPaths(t, got, filepath.Clean(root), sub)
}

func TestScanDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	realRepo := mkRepo(t, outside, "real")
	root := t.TempDir()
	if err := os.Symlink(realRepo, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 3})
	assertPaths(t, got)
}

func TestScanOrdersByGitActivityNewestFirst(t *testing.T) {
	root := t.TempDir()
	oldest := mkRepo(t, root, "oldest")
	middle := mkRepo(t, root, "middle")
	newest := mkRepo(t, root, "newest")

	base := time.Now().Add(-time.Hour)
	for i, p := range []string{oldest, middle, newest} {
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(p, ".git"), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 1})
	want := []string{newest, middle, oldest}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: got %v, want %v", got, want)
		}
	}
}

func TestScanCanceledContextReturnsPromptly(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, "repo")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := Scan(ctx, Options{Roots: []string{root}, MaxDepth: 2})
	if len(got) != 0 {
		t.Fatalf("canceled scan returned %v, want none", got)
	}
}

func TestScanExpandsTildeRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := mkRepo(t, home, "proj/repo")

	got := scan(t, Options{Roots: []string{"~"}, MaxDepth: 2})
	assertPaths(t, got, repo)

	got = scan(t, Options{Roots: []string{"~/proj"}, MaxDepth: 1})
	assertPaths(t, got, repo)
}

func TestScanCapsEntriesPerDirectory(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxEntriesPerDir+20; i++ {
		mkRepo(t, root, filepath.Join("repos", "r"+string(rune('a'+i%26))+itoa(i)))
	}

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 3})
	if len(got) == 0 || len(got) > maxEntriesPerDir {
		t.Fatalf("got %d repos, want 1..%d", len(got), maxEntriesPerDir)
	}
}

// itoa avoids importing strconv just for the cap test's unique names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// mkGitmodules writes a .gitmodules declaring the given submodule paths.
func mkGitmodules(t *testing.T, repo string, paths ...string) {
	t.Helper()
	var b []byte
	for _, p := range paths {
		b = append(b, []byte("[submodule \""+p+"\"]\n\tpath = "+p+"\n\turl = git@example.com:x.git\n")...)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDiscoversInitializedSubmodules(t *testing.T) {
	// The repo-prune rule hides everything inside a repo, but submodules are
	// projects in their own right: declared paths with a .git present must be
	// recorded; declared-but-uninitialized ones (no .git) must not.
	root := t.TempDir()
	repo := mkRepo(t, root, "platform")
	sub := mkRepoFile(t, root, "platform/src/box")
	mkDir(t, root, "platform/ghost") // declared below but never initialized
	mkGitmodules(t, repo, "src/box", "ghost")

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 1})
	assertPaths(t, got, repo, sub)
}

func TestScanDiscoversNestedSubmodules(t *testing.T) {
	// The metric-namespace case: a submodule that is itself a superproject.
	root := t.TempDir()
	repo := mkRepo(t, root, "platform")
	sub := mkRepoFile(t, root, "platform/src/box")
	nested := mkRepoFile(t, root, "platform/src/box/metric-namespace")
	mkGitmodules(t, repo, "src/box")
	mkGitmodules(t, sub, "metric-namespace")

	got := scan(t, Options{Roots: []string{root}, MaxDepth: 1})
	assertPaths(t, got, repo, sub, nested)
}

func TestScanSubmodulePathsCannotEscapeRepo(t *testing.T) {
	// A .gitmodules path pointing outside its repo (.. or absolute) is never
	// followed — the file is repo-controlled data, not a trusted walk input.
	root := t.TempDir()
	repo := mkRepo(t, root, "ignored/inner")
	outside := mkRepoFile(t, root, "outside")
	mkGitmodules(t, repo, "../../outside")
	mkGitmodules(t, repo) // overwrite below with explicit content incl. absolute
	if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte(
		"[submodule \"a\"]\n\tpath = ../../outside\n[submodule \"b\"]\n\tpath = "+outside+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Root is the repo itself so only .gitmodules could surface "outside".
	got := scan(t, Options{Roots: []string{repo}, MaxDepth: 0})
	assertPaths(t, got, repo)
}
