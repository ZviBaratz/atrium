package git

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/log"
)

// captureWarnings redirects the package's warning logger into a buffer for the
// duration of the test, and returns a func reading what has been written so far.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.WarningLog
	log.WarningLog = stdlog.New(&buf, "", 0)
	t.Cleanup(func() { log.WarningLog = prev })
	return buf.String
}

// setupIsolatedWorktree is setupSessionWorktree for a dependency-isolating session:
// the flag is pushed in before Setup, exactly as Instance.Start and
// FromInstanceData do it, because it is Setup that consults it.
func setupIsolatedWorktree(t *testing.T, repoPath, session string) *Worktree {
	t.Helper()
	wt, _, err := NewWorktree(t.Context(), repoPath, session)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	wt.SetIsolateDeps(true)
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return wt
}

// fingerprintTree hashes every path under root together with its size, mode and
// symlink target — enough to catch an install that added, removed, resized or
// re-permissioned anything. It is what "the origin checkout is byte-identical
// afterward" (#481 criterion 1) is asserted with.
func fingerprintTree(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, info.Mode(), info.Size())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "-> %s\x00", target)
		case info.Mode().IsRegular():
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// The headline behavior, and its mirror. A dependency-isolating session receives
// NOTHING at a link_paths entry — not a symlink and not an empty directory — so the
// worktree looks exactly like a fresh checkout in which nobody ran `npm install`,
// which is the state a repo's setup_script is already written against. An ordinary
// session in the same repo is unchanged (#481 criterion 2): still symlinked, still
// paying nothing.
//
// Asserting "nothing" rather than "an empty directory" is deliberate. An empty
// node_modules satisfies the `[ -d node_modules ] || npm ci` an idempotent setup
// script is commonly written as, which would skip the install and leave the session
// running against a dead tree with no signal.
func TestSetup_IsolatedSessionGetsNoLink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		isolated bool
	}{
		{"isolated", true},
		{"shared", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := newTestRepo(t)
			const rel = "node_modules"
			commitGitignore(t, repoPath, rel)
			makeDepsDir(t, repoPath, rel)
			writeLinkConfig(t, []string{rel})

			var wt *Worktree
			if tc.isolated {
				wt = setupIsolatedWorktree(t, repoPath, "isolate-"+tc.name)
			} else {
				wt = setupSessionWorktree(t, repoPath, "isolate-"+tc.name)
			}
			dst := filepath.Join(wt.GetWorktreePath(), rel)

			info, err := os.Lstat(dst)
			if !tc.isolated {
				if err != nil {
					t.Fatalf("an ordinary session must still be linked: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("an ordinary session must still get a symlink, got mode %v", info.Mode())
				}
				return
			}
			if err == nil {
				t.Fatalf("an isolated session must get nothing at %q, found mode %v", rel, info.Mode())
			}
			if !os.IsNotExist(err) {
				t.Fatalf("lstat %q: %v", dst, err)
			}
		})
	}
}

// #481 criterion 1, as far as a unit test can carry it: with no symlink there is no
// path by which a write inside the worktree can reach the origin tree. The real
// `npm install` is the manual half of this; here the install is simulated by writing
// the tree the agent would have created, and the origin is fingerprinted on both
// sides.
func TestIsolatedSessionInstallLeavesOriginByteIdentical(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	origin := makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	before := fingerprintTree(t, origin)

	wt := setupIsolatedWorktree(t, repoPath, "isolate-install")
	dst := filepath.Join(wt.GetWorktreePath(), rel)

	// What `npm install` does: create the directory and populate it. Every one of
	// these writes would have landed in the origin checkout under a symlink.
	if err := os.MkdirAll(filepath.Join(dst, "typescript"), 0755); err != nil {
		t.Fatalf("simulate install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "marker.txt"), []byte("upgraded"), 0644); err != nil {
		t.Fatalf("simulate install: %v", err)
	}

	if after := fingerprintTree(t, origin); after != before {
		t.Fatalf("the origin checkout changed under an isolated session: %s -> %s", before, after)
	}
	// And the private tree really is private: a real directory, not a link back out.
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("private tree missing: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the session's own tree must be a real directory, got mode %v", info.Mode())
	}
	got, err := os.ReadFile(filepath.Join(origin, "marker.txt"))
	if err != nil {
		t.Fatalf("read origin marker: %v", err)
	}
	if string(got) != "installed" {
		t.Fatalf("origin marker = %q, want %q — the install wrote through to the origin", got, "installed")
	}
}

// Isolation is about link_paths only. carry_files are already per-session copies, so
// an isolated session must still get its local config — losing it would be a silent
// second behavior change riding on the same flag.
func TestSetup_IsolatedSessionStillCarriesFiles(t *testing.T) {
	repoPath := newTestRepo(t)
	const carried = ".claude/settings.local.json"
	addGitignoredFile(t, repoPath, carried, `{"hooks":{}}`)
	writeCarryConfig(t, []string{carried})

	wt := setupIsolatedWorktree(t, repoPath, "isolate-carry")

	got, err := os.ReadFile(filepath.Join(wt.GetWorktreePath(), carried))
	if err != nil {
		t.Fatalf("an isolated session must still receive its carried files: %v", err)
	}
	if string(got) != `{"hooks":{}}` {
		t.Fatalf("carried content = %q", got)
	}
}

// The #167 guard, held for the isolated case too: the diff intent-adds every
// untracked path on each 500ms tick, so anything seeding leaves behind that reads as
// untracked would run `add -N` for the life of the session. Skipping an entry must
// leave the worktree as clean as linking one does — including for a nested entry,
// whose parent directories linking would have created.
func TestSetup_IsolatedSessionLeavesNothingUntracked(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "container/agent-runner/node_modules"
	commitGitignore(t, repoPath, "node_modules")
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupIsolatedWorktree(t, repoPath, "isolate-untracked")
	wtPath := wt.GetWorktreePath()

	if _, err := os.Lstat(filepath.Join(wtPath, "container")); !os.IsNotExist(err) {
		t.Errorf("skipping a nested entry must not create its parent directories, lstat err = %v", err)
	}
	if paths := wt.untrackedPathsToIntentAdd(wtPath); len(paths) != 0 {
		t.Errorf("an isolated session's worktree must leave nothing untracked, got %q", paths)
	}
}

// The flag rides the Worktree and Setup consults it every time, so a pause/resume
// cycle — which removes the worktree and re-materializes it — must not quietly
// restore the link. This is the git-layer half of the invariant; the session-layer
// half (that a resumed instance's restored Worktree is told at all) is guarded in
// session/storage_test.go.
func TestSetup_IsolationSurvivesPauseResume(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel)
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupIsolatedWorktree(t, repoPath, "isolate-resume")
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove (pause): %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup (resume): %v", err)
	}

	if _, err := os.Lstat(filepath.Join(wt.GetWorktreePath(), rel)); !os.IsNotExist(err) {
		t.Fatalf("resume must not restore the link for an isolated session, lstat err = %v", err)
	}
}

// A dir-only ignore pattern ("node_modules/") is the trap linkLocalPath must refuse,
// because git stores a symlink as a file and the pattern would not cover it — so the
// link would land in pause's `git add .`. Isolation removes the trap rather than
// inheriting it: what ends up at that path is a real directory, which the pattern
// does match, so the entry is skipped without a warning and the tree the session
// installs stays out of the index.
func TestIsolatedSessionIsUnaffectedByTheDirOnlyIgnoreTrap(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	commitGitignore(t, repoPath, rel+"/") // the pattern linkLocalPath refuses
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	wt := setupIsolatedWorktree(t, repoPath, "isolate-dironly")
	wtPath := wt.GetWorktreePath()

	// The session installs its own tree, as an agent would.
	if err := os.MkdirAll(filepath.Join(wtPath, rel), 0755); err != nil {
		t.Fatalf("simulate install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, rel, "marker.txt"), []byte("private"), 0644); err != nil {
		t.Fatalf("simulate install: %v", err)
	}

	// What pause does.
	mustRunGit(t, wtPath, "add", ".")
	if staged := mustRunGit(t, wtPath, "ls-files", "-s"); strings.Contains(staged, rel) {
		t.Fatalf("the session's private tree leaked into a pause commit:\n%s", staged)
	}
}

// Skipping the link must not also skip the diagnosis. linkLocalPath refuses an entry
// git would not ignore in the worktree, because the symlink would land in pause's
// `git add .`; an isolated session creates no symlink, but it is the one session
// guaranteed to FILL that path — so an unignored entry there is a whole dependency
// tree committed onto the branch and into any PR cut from it.
//
// The ignore rule exists in the origin checkout here but has not been committed, so it
// never reaches the session's base — the case linkLocalPath's own comment names, and
// the one a session created off an older base hits without doing anything unusual.
func TestIsolatedSessionWarnsWhenTheDepPathIsNotIgnored(t *testing.T) {
	repoPath := newTestRepo(t)
	const rel = "node_modules"
	// Written but deliberately NOT committed: nothing carries it to the worktree.
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(rel+"\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	makeDepsDir(t, repoPath, rel)
	writeLinkConfig(t, []string{rel})

	warnings := captureWarnings(t)
	wt := setupIsolatedWorktree(t, repoPath, "isolate-unignored")

	got := warnings()
	if !strings.Contains(got, rel) || !strings.Contains(got, "not ignored") {
		t.Fatalf("expected a warning naming %q as unignored, got:\n%s", rel, got)
	}

	// And the hazard the warning is about is real, not theoretical: what the session
	// installs at that path is staged by exactly what pause runs.
	wtPath := wt.GetWorktreePath()
	if err := os.MkdirAll(filepath.Join(wtPath, rel), 0755); err != nil {
		t.Fatalf("simulate install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, rel, "marker.txt"), []byte("private"), 0644); err != nil {
		t.Fatalf("simulate install: %v", err)
	}
	mustRunGit(t, wtPath, "add", ".")
	if staged := mustRunGit(t, wtPath, "ls-files", "-s"); !strings.Contains(staged, rel) {
		t.Fatalf("expected the unignored tree to be staged (the reason for the warning), got:\n%s", staged)
	}
}

// The complement, and the reason the probe is slash-terminated: a dir-only rule
// ("node_modules/") is a perfectly good ignore for the real directory an isolated
// session ends up with, even though linkLocalPath must refuse it for a symlink. A
// probe copied verbatim from the shared path would warn here — on the commonest
// .gitignore spelling in the ecosystem — about a session that is entirely fine.
func TestIsolatedSessionDoesNotWarnForADirOnlyIgnoreRule(t *testing.T) {
	for _, pattern := range []string{"node_modules", "node_modules/"} {
		t.Run(pattern, func(t *testing.T) {
			repoPath := newTestRepo(t)
			const rel = "node_modules"
			commitGitignore(t, repoPath, pattern)
			makeDepsDir(t, repoPath, rel)
			writeLinkConfig(t, []string{rel})

			warnings := captureWarnings(t)
			setupIsolatedWorktree(t, repoPath, "isolate-quiet")
			if got := warnings(); got != "" {
				t.Fatalf("expected no warning for an ignored path, got:\n%s", got)
			}
		})
	}
}
