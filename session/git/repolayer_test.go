package git

// repolayer_test.go — #815's seeding side: how a repository's own trusted entries
// combine with the user's, and how a refusal about one of them names which list it
// came from.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/repocfg"
)

// TestUnionSeedEntries pins the layering order, the dedupe, and the provenance
// carried in each entry's warning key. These three are what every filesystem-level
// test downstream depends on, and none of them is observable from the worktree once
// the union has been flattened.
func TestUnionSeedEntries(t *testing.T) {
	t.Run("repo entries lead, global follows", func(t *testing.T) {
		got := unionSeedEntries([]string{"repo-a", "repo-b"}, []string{"glob-a"}, "link_paths")
		want := []string{"repo-a", "repo-b", "glob-a"}
		if len(got) != len(want) {
			t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].rel != w {
				t.Errorf("entry %d = %q, want %q", i, got[i].rel, w)
			}
		}
	})

	t.Run("the same path from both sides is seeded once, the repo's way", func(t *testing.T) {
		// Spelled differently on purpose: the dedupe compares canonical forms, so a
		// slash or a "./" cannot smuggle a second attempt past it — and a second
		// attempt is not harmless, it warns about clobbering the first.
		got := unionSeedEntries([]string{"node_modules"}, []string{"./node_modules/"}, "link_paths")
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1: %+v", len(got), got)
		}
		if !strings.Contains(got[0].kind, repocfg.RepoLocalFileName) {
			t.Errorf("the surviving entry should be the repo's, got kind %q", got[0].kind)
		}
	})

	t.Run("the kind names where the entry came from", func(t *testing.T) {
		got := unionSeedEntries([]string{"repo-a"}, []string{"glob-a"}, "carry_files")
		if got[0].kind != "carry_files ("+repocfg.RepoLocalFileName+")" {
			t.Errorf("repo entry kind = %q", got[0].kind)
		}
		// The global spelling is unchanged, which is what keeps every pre-#815
		// warning reading exactly as it did.
		if got[1].kind != "carry_files" {
			t.Errorf("global entry kind = %q, want the bare key", got[1].kind)
		}
	})

	t.Run("an unusable entry survives to be warned about", func(t *testing.T) {
		// The canonical rule refuses it, but dropping it here would lose the warning:
		// only the leaf function knows which of its guards refused and why.
		got := unionSeedEntries(nil, []string{"../escape", ""}, "carry_files")
		if len(got) != 2 {
			t.Fatalf("got %d entries, want both kept: %+v", len(got), got)
		}
	})
}

// TestSeedWarningNamesTheRepoFile: #477's third complaint. A refusal caused by a
// project's own committed entry used to read as a problem with the global list the
// user maintains by hand, and the two are fixed in different files.
func TestSeedWarningNamesTheRepoFile(t *testing.T) {
	repoPath := newTestRepo(t)
	warnings := captureWarnings(t)

	wt, _, err := NewWorktree(t.Context(), repoPath, "provenance")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	// A carry_files entry naming a DIRECTORY: present in the origin checkout, so
	// carryLocalFile gets past its absence guard and refuses on the shape, which is
	// the one refusal that needs nothing else about the repo to be arranged.
	if err := os.MkdirAll(filepath.Join(repoPath, "adirectory"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wt.SetRepoLocalSeeds(func(string) ([]string, []string) { return []string{"adirectory"}, nil })
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	got := warnings()
	if !strings.Contains(got, "carry_files ("+repocfg.RepoLocalFileName+")") {
		t.Errorf("the warning must name the repo's file as the source, got:\n%s", got)
	}
}

// TestIsolatedSessionSkipsRepoLinkPaths: dependency isolation is a per-SESSION
// choice and it outranks the repository's wishes — a repo declaring link_paths must
// not put a shared symlink into a worktree the user asked to keep private. The
// diagnosis is not skipped with the link (warnIsolatedPathNotIgnored's contract),
// so the entry still has to be named.
func TestIsolatedSessionSkipsRepoLinkPaths(t *testing.T) {
	repoPath := newTestRepo(t)
	makeDepsDir(t, repoPath, "node_modules")
	commitGitignore(t, repoPath, "node_modules")

	wt, _, err := NewWorktree(t.Context(), repoPath, "iso-repo")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	wt.SetIsolateDeps(true)
	wt.SetRepoLocalSeeds(func(string) ([]string, []string) { return nil, []string{"node_modules"} })
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("an isolated session must get no link even from a trusted repo, lstat err = %v", err)
	}
}
