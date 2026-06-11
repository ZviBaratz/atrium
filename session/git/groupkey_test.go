package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BranchNameForSession is the single source of the session-branch slug: the form's
// duplicate prediction and the worktree layer must mint identical names, so the
// derivation lives in one exported function.
func TestBranchNameForSession(t *testing.T) {
	cases := []struct{ prefix, title, want string }{
		{"zvi/", "Fix Bug", "zvi/fix-bug"},
		{"zvi/", "fix-bug", "zvi/fix-bug"},
		{"", "Hello World", "hello-world"},
		{"me/", "v1.2 cleanup", "me/v1.2-cleanup"},
	}
	for _, c := range cases {
		if got := BranchNameForSession(c.prefix, c.title); got != c.want {
			t.Fatalf("BranchNameForSession(%q, %q) = %q, want %q", c.prefix, c.title, got, c.want)
		}
	}
}

// LocalBranchExists must consult local heads only — an exact ref lookup, not the
// capped/origin-merged SearchBranches used by the base-branch picker.
func TestLocalBranchExists(t *testing.T) {
	repo := newTestRepo(t)
	mustRunGit(t, repo, "branch", "zvi/taken")

	if !LocalBranchExists(context.Background(), repo, "zvi/taken") {
		t.Fatal("LocalBranchExists() = false for an existing branch")
	}
	if LocalBranchExists(context.Background(), repo, "zvi/free") {
		t.Fatal("LocalBranchExists() = true for a missing branch")
	}
	if LocalBranchExists(context.Background(), t.TempDir(), "zvi/taken") {
		t.Fatal("LocalBranchExists() = true for a non-repo dir")
	}
}

// RepoGroupKey predicts the list's repo-group key from a form target path: the repo
// root's basename even when the target is a subdirectory, and the directory's own
// basename outside a repo (mirroring how direct sessions group).
func TestRepoGroupKey(t *testing.T) {
	repo := newTestRepo(t)
	want := filepath.Base(repo)

	if got := RepoGroupKey(context.Background(), repo); got != want {
		t.Fatalf("RepoGroupKey(root) = %q, want %q", got, want)
	}

	sub := filepath.Join(repo, "nested", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoGroupKey(context.Background(), sub); got != want {
		t.Fatalf("RepoGroupKey(subdir) = %q, want %q", got, want)
	}

	plain := t.TempDir()
	if got := RepoGroupKey(context.Background(), plain); got != filepath.Base(plain) {
		t.Fatalf("RepoGroupKey(non-repo) = %q, want %q", got, filepath.Base(plain))
	}
}
