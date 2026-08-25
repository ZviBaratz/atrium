package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAndCommit commits content at name in repo, replacing any previous version.
func writeAndCommit(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustRunGit(t, repo, "add", name)
	mustRunGit(t, repo, "commit", "-m", "add "+name)
}

func TestRepoRoot(t *testing.T) {
	repo := newTestRepo(t)
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := RepoRoot(context.Background(), sub)
	if err != nil {
		t.Fatalf("RepoRoot from a subdirectory: %v", err)
	}
	// git prints a resolved path; compare resolved to resolved (macOS /var → /private/var).
	wantRoot, _ := filepath.EvalSymlinks(repo)
	if gotResolved, _ := filepath.EvalSymlinks(root); gotResolved != wantRoot {
		t.Fatalf("RepoRoot = %q, want the repo root %q", root, wantRoot)
	}

	if _, err := RepoRoot(context.Background(), t.TempDir()); err == nil {
		t.Fatal("RepoRoot of a non-repo directory should error")
	}
}

func TestHeadFile(t *testing.T) {
	const maxCfg = 1 << 20

	t.Run("returns the committed bytes exactly, trailing newline included", func(t *testing.T) {
		repo := newTestRepo(t)
		// The trailing newline is the point: the trust ledger hashes these bytes and
		// compares them against a plain read of the checked-out file, so a reader
		// that trimmed (as localGit does) would make every grant unmatchable.
		const content = "{\"repo_scripts\":[{\"setup_script\":\"make deps\"}]}\n"
		writeAndCommit(t, repo, ".atrium.json", content)

		data, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("HeadFile: present=%v err=%v", present, err)
		}
		if string(data) != content {
			t.Fatalf("HeadFile bytes = %q, want %q", data, content)
		}
	})

	t.Run("an untracked file is absent", func(t *testing.T) {
		// A worktree materializes only tracked files, so an untracked .atrium.json
		// never executes — and must therefore never be offered for trust. This is the
		// property that keeps the create-time prompt honest.
		repo := newTestRepo(t)
		if err := os.WriteFile(filepath.Join(repo, ".atrium.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("HeadFile: %v", err)
		}
		if present {
			t.Fatal("an untracked file must read as absent — it will not be in any worktree")
		}
	})

	t.Run("reads HEAD, not the working tree", func(t *testing.T) {
		repo := newTestRepo(t)
		const committed = "{\"repo_scripts\":[{\"setup_script\":\"make v1\"}]}\n"
		writeAndCommit(t, repo, ".atrium.json", committed)
		// Uncommitted edits are bytes no worktree will hold; the reader must not see them.
		if err := os.WriteFile(filepath.Join(repo, ".atrium.json"), []byte("{\"edited\":true}"), 0o644); err != nil {
			t.Fatal(err)
		}

		data, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("HeadFile: present=%v err=%v", present, err)
		}
		if string(data) != committed {
			t.Fatalf("HeadFile read the working tree: got %q", data)
		}
	})

	t.Run("no such file", func(t *testing.T) {
		repo := newTestRepo(t)
		_, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("HeadFile: %v", err)
		}
		if present {
			t.Fatal("a file HEAD does not hold must be absent")
		}
	})

	t.Run("an unborn HEAD is absent, not an error", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "newborn")
		mustRunGit(t, "", "init", repo)

		_, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("HeadFile on an unborn HEAD: %v", err)
		}
		if present {
			t.Fatal("an unborn HEAD checks out nothing")
		}
	})

	t.Run("an oversized blob is present but refused, never truncated", func(t *testing.T) {
		repo := newTestRepo(t)
		writeAndCommit(t, repo, ".atrium.json", strings.Repeat("x", 200))

		data, present, err := HeadFile(context.Background(), repo, ".atrium.json", 100)
		if !present {
			t.Fatal("an oversized file is still present — the caller must say so, not fall silent")
		}
		if err == nil {
			t.Fatal("an oversized file must be refused")
		}
		if len(data) != 0 {
			t.Fatalf("an oversized read must return no bytes, got %d", len(data))
		}
	})

	t.Run("checkout filters apply to the bytes", func(t *testing.T) {
		// Enforcement hashes the checked-out worktree file. If attributes rewrite the
		// content at checkout (eol here, standing in for any smudge), the grant must
		// hash the same rewritten form or the two sides never agree.
		repo := newTestRepo(t)
		if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte(".atrium.json text eol=crlf\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRunGit(t, repo, "add", ".gitattributes")
		mustRunGit(t, repo, "commit", "-m", "attrs")
		writeAndCommit(t, repo, ".atrium.json", "{\"repo_scripts\":[]}\n")

		data, present, err := HeadFile(context.Background(), repo, ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("HeadFile: present=%v err=%v", present, err)
		}
		if !strings.Contains(string(data), "\r\n") {
			t.Fatalf("eol=crlf should reach the read bytes; got %q", data)
		}
	})
}
