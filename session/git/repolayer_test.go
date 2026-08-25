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

// TestRepoSeedEntryCannotEscapeThroughASymlink is #815's trust boundary at its
// sharpest point, and the one the lexical guards could not hold.
//
// The setup is ordinary, not contrived: users keep gitignored convenience symlinks
// in their checkouts (a pnpm store, .venv → /opt/..., deps → somewhere shared). A
// trusted repo commits the .gitignore rule AND the seed entry, so every lexical
// check passes — "deps/secret.txt" is a repo-relative path — while os.Stat in the
// ORIGIN follows the link out of the repo entirely. The check-ignore probe a
// comment here once named as the guard for this cannot see it either: it runs in
// the WORKTREE, where `deps` does not exist, and `deps/` in .gitignore still makes
// it exit 0.
func TestRepoSeedEntryCannotEscapeThroughASymlink(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("id_rsa"), 0600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// Prove the premise rather than assuming it: if check-ignore ever stopped
	// answering "ignored" for a path under a missing ignored directory, this test
	// would still pass while guarding nothing.
	repoPath := newTestRepo(t)
	writeCarryConfig(t, nil)
	commitGitignore(t, repoPath, "deps/", "shared")
	// TWO symlinks, and they must not nest: a first draft used one path for both
	// entries, which put the carry entry underneath the link entry — so
	// dropCarriesUnderLinks refused the carry and the escape guard was never
	// reached. The carry assertion passed for the wrong reason, and a mutation
	// removing that guard survived.
	if err := os.Symlink(outside, filepath.Join(repoPath, "deps")); err != nil {
		t.Fatalf("symlink deps: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repoPath, "shared")); err != nil {
		t.Fatalf("symlink shared: %v", err)
	}

	warnings := captureWarnings(t)
	wt, _, err := NewWorktree(t.Context(), repoPath, "escape")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	wt.SetRepoLocalSeeds(func(string) ([]string, []string) {
		return []string{"deps/secret.txt"}, []string{"shared"}
	})
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "deps", "secret.txt")); err == nil {
		t.Error("a repo's carry_files entry followed a symlink out of the repo and copied a file the trust dialog never named")
	}
	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "shared")); err == nil {
		t.Error("a repo's link_paths entry handed the agent a writable symlink to a tree outside the repo")
	}
	if got := warnings(); !strings.Contains(got, "outside this repository") {
		t.Errorf("the refusal must say what it refused and why, got:\n%s", got)
	}
}

// TestGlobalSeedEntryMayStillLeaveTheRepo is the other side of that asymmetry, and
// it is deliberate rather than an oversight. The user's own link_paths pointing at a
// shared package store outside the checkout is documented, working configuration —
// linkLocalPath Lstats rather than Stats precisely so it survives — and #815 must
// not narrow it. Without this control, tightening the repo side by tightening
// BOTH would look like a pass.
func TestGlobalSeedEntryMayStillLeaveTheRepo(t *testing.T) {
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "pkg"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	repoPath := newTestRepo(t)
	writeLinkConfig(t, []string{"store"})
	commitGitignore(t, repoPath, "store")
	if err := os.Symlink(outside, filepath.Join(repoPath, "store")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	wt, _, err := NewWorktree(t.Context(), repoPath, "shared-store")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "store")); err != nil {
		t.Errorf("the user's own link_paths entry must still reach a store outside the repo: %v", err)
	}
}

// TestRepoCarryCannotSuppressAUserLink: ordering alone cannot resolve a carry entry
// nested under a link entry, and the collision is silent. Carrying first has
// os.MkdirAll create a real directory at the link's path, so the never-clobber guard
// finds something there and skips the link with one log line — a repo declaring
// `carry_files: ["node_modules/.x"]` would cost the user their whole linked
// node_modules in every session in that repo, which is precisely the suppression
// union semantics exist to prevent.
func TestRepoCarryCannotSuppressAUserLink(t *testing.T) {
	repoPath := newTestRepo(t)
	writeLinkConfig(t, []string{"node_modules"})
	makeDepsDir(t, repoPath, "node_modules")
	commitGitignore(t, repoPath, "node_modules")
	if err := os.WriteFile(filepath.Join(repoPath, "node_modules", ".pkg-lock"), []byte("x"), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	warnings := captureWarnings(t)
	wt, _, err := NewWorktree(t.Context(), repoPath, "collide")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	wt.SetRepoLocalSeeds(func(string) ([]string, []string) {
		return []string{"node_modules/.pkg-lock"}, nil
	})
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	// The user's link survived, as a link — not as a directory holding one file.
	info, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "node_modules"))
	if err != nil {
		t.Fatalf("the user's link_paths entry was suppressed by a repo carry entry: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("node_modules is a %v, not a symlink: the repo's carry entry pre-created the directory and the link was silently skipped", info.Mode().Type())
	}
	if got := warnings(); !strings.Contains(got, "link_paths symlinks") {
		t.Errorf("the refused carry must name the collision, got:\n%s", got)
	}
}

// TestSharedEntryKeepsTheUsersProvenance: a path the user declares TOO is not
// repo-authored, however the dedupe orders it — and the consequence is a refusal, not
// a label.
//
// unionSeedEntries puts the repo's entries first, so a path both sides declare is
// emitted once, from the repo's pass. Marking that single entry repo-authored hands it
// to seedSourceEscapes, which refuses any repo entry whose source resolves outside the
// checkout. The user's own carry of a symlinked shared file is documented, working
// configuration; it must not stop working because a repo happened to name the same
// path — and the overlap is the LIKELY case, since a repo names the same local-config
// files its developers already name.
//
// TestGlobalSeedEntryMayStillLeaveTheRepo is the no-overlap control and cannot see
// this: with nothing on the repo side the entry is user-authored by construction, so
// it passes whether or not provenance transfers.
func TestSharedEntryKeepsTheUsersProvenance(t *testing.T) {
	outside := t.TempDir()
	shared := filepath.Join(outside, "shared.env")
	if err := os.WriteFile(shared, []byte("TOKEN=1"), 0600); err != nil {
		t.Fatalf("write shared: %v", err)
	}

	repoPath := newTestRepo(t)
	// The user declares it in their OWN global list.
	writeCarryConfig(t, []string{"shared.env"})
	commitGitignore(t, repoPath, "shared.env")
	if err := os.Symlink(shared, filepath.Join(repoPath, "shared.env")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	warnings := captureWarnings(t)
	wt, _, err := NewWorktree(t.Context(), repoPath, "shared-entry")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	// And the repo declares the SAME path. This is the whole fixture: drop this line
	// and the test degenerates into the no-overlap control above.
	wt.SetRepoLocalSeeds(func(string) ([]string, []string) {
		return []string{"shared.env"}, nil
	})
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = wt.Cleanup() })

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), "shared.env")); err != nil {
		t.Errorf("the user's own carry_files entry was refused because a repo also named it: %v", err)
	}
	if got := warnings(); strings.Contains(got, "outside this repository") {
		t.Errorf("the user's own entry was refused and the repo blamed for it:\n%s", got)
	}
}
