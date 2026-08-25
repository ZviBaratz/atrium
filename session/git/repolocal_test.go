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

func TestFileAtRef(t *testing.T) {
	const maxCfg = 1 << 20

	t.Run("returns the committed bytes exactly, trailing newline included", func(t *testing.T) {
		repo := newTestRepo(t)
		// The trailing newline is the point: the trust ledger hashes these bytes and
		// compares them against a plain read of the checked-out file, so a reader
		// that trimmed (as localGit does) would make every grant unmatchable.
		const content = "{\"repo_scripts\":[{\"setup_script\":\"make deps\"}]}\n"
		writeAndCommit(t, repo, ".atrium.json", content)

		data, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("FileAtRef: present=%v err=%v", present, err)
		}
		if string(data) != content {
			t.Fatalf("FileAtRef bytes = %q, want %q", data, content)
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

		_, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("FileAtRef: %v", err)
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

		data, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("FileAtRef: present=%v err=%v", present, err)
		}
		if string(data) != committed {
			t.Fatalf("FileAtRef read the working tree: got %q", data)
		}
	})

	t.Run("no such file", func(t *testing.T) {
		repo := newTestRepo(t)
		_, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("FileAtRef: %v", err)
		}
		if present {
			t.Fatal("a file HEAD does not hold must be absent")
		}
	})

	t.Run("an unborn HEAD is absent, not an error", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "newborn")
		mustRunGit(t, "", "init", repo)

		_, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil {
			t.Fatalf("FileAtRef on an unborn HEAD: %v", err)
		}
		if present {
			t.Fatal("an unborn HEAD checks out nothing")
		}
	})

	t.Run("an oversized blob is present but refused, never truncated", func(t *testing.T) {
		repo := newTestRepo(t)
		writeAndCommit(t, repo, ".atrium.json", strings.Repeat("x", 200))

		data, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", 100)
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

		data, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("FileAtRef: present=%v err=%v", present, err)
		}
		if !strings.Contains(string(data), "\r\n") {
			t.Fatalf("eol=crlf should reach the read bytes; got %q", data)
		}
	})

	t.Run("a filter that expands past the cap is refused, not buffered", func(t *testing.T) {
		// The stored-size pre-check sees the SMALL blob; only the capped stream read
		// bounds what a smudge filter expands it to (git-lfs fetching a huge object
		// is the canonical case — the repo's .gitattributes selects the filter even
		// though the command comes from the user's config). A read that buffered
		// first and measured after would let the repo choose the allocation.
		repo := newTestRepo(t)
		mustRunGit(t, repo, "config", "filter.grow.smudge", "sed -e s/x/xxxxxxxxxxxxxxxxxxxx/g")
		if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte(".atrium.json filter=grow\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRunGit(t, repo, "add", ".gitattributes")
		mustRunGit(t, repo, "commit", "-m", "attrs")
		writeAndCommit(t, repo, ".atrium.json", strings.Repeat("x", 100)) // stores 100, smudges to 2000

		data, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", 500)
		if !present {
			t.Fatal("the file is present; the refusal must say oversized, not absent")
		}
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("an over-cap expansion must be refused with the cap named, got err=%v", err)
		}
		if len(data) != 0 {
			t.Fatalf("no partial bytes may escape an over-cap read, got %d", len(data))
		}

		// Control: the same filter under the cap reads the EXPANDED form — the
		// bytes a checkout produces, which is what the hash pact needs.
		grown, present, err := FileAtRef(context.Background(), repo, "HEAD", ".atrium.json", maxCfg)
		if err != nil || !present {
			t.Fatalf("FileAtRef under the cap: present=%v err=%v", present, err)
		}
		if len(grown) != 2000 {
			t.Fatalf("the filtered form should be 2000 bytes, got %d", len(grown))
		}
	})
}

func TestStartPointPreview(t *testing.T) {
	// The asking-side mirror of resolveStartPoint: the repo-trust prompt reads
	// the file at this ref, so the decision must match what Setup will check out.
	clonePair := func(t *testing.T) (origin, clone string) {
		t.Helper()
		origin = newTestRepo(t)
		clone = filepath.Join(t.TempDir(), "clone")
		mustRunGit(t, "", "clone", origin, clone)
		mustRunGit(t, clone, "config", "user.name", "t")
		mustRunGit(t, clone, "config", "user.email", "t@example.com")
		return origin, clone
	}
	currentBranch := func(t *testing.T, repo string) string {
		t.Helper()
		return CurrentBranchName(context.Background(), repo)
	}

	t.Run("no remote, no explicit base: HEAD", func(t *testing.T) {
		repo := newTestRepo(t)
		if got := StartPointPreview(context.Background(), repo, "", true); got != "HEAD" {
			t.Fatalf("StartPointPreview = %q, want HEAD", got)
		}
	})

	t.Run("behind origin with freshening on: origin/<branch>", func(t *testing.T) {
		origin, clone := clonePair(t)
		writeAndCommit(t, origin, "new.txt", "v2\n")
		mustRunGit(t, clone, "fetch", "origin")

		want := "origin/" + currentBranch(t, clone)
		if got := StartPointPreview(context.Background(), clone, "", true); got != want {
			t.Fatalf("StartPointPreview = %q, want %q (local is behind; Setup will branch from the remote tip)", got, want)
		}
		if got := StartPointPreview(context.Background(), clone, "", false); got != "HEAD" {
			t.Fatalf("with freshening off StartPointPreview = %q, want HEAD", got)
		}
	})

	t.Run("ahead of origin: local wins, never discarding local commits", func(t *testing.T) {
		_, clone := clonePair(t)
		writeAndCommit(t, clone, "local.txt", "local\n")

		if got := StartPointPreview(context.Background(), clone, "", true); got != "HEAD" {
			t.Fatalf("StartPointPreview = %q, want HEAD (local is ahead)", got)
		}
	})

	t.Run("an explicit base resolves like resolveStartPoint", func(t *testing.T) {
		repo := newTestRepo(t)
		base := currentBranch(t, repo)
		mustRunGit(t, repo, "checkout", "-b", "other")

		if got := StartPointPreview(context.Background(), repo, base, false); got != base {
			t.Fatalf("StartPointPreview = %q, want the local base %q", got, base)
		}
		if got := StartPointPreview(context.Background(), repo, "no-such-branch", false); got != "HEAD" {
			t.Fatalf("an unresolvable base must degrade to HEAD, got %q", got)
		}
	})
}
