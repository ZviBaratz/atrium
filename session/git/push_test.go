package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubGitPush swaps the push seam for a fake and returns a restore func.
func stubGitPush(fn func(context.Context, string, string) error) func() {
	orig := runGitPush
	runGitPush = fn
	return func() { runGitPush = orig }
}

// stubGHBrowse swaps the browse seam for a fake and returns a restore func.
func stubGHBrowse(fn func(context.Context, string, string) error) func() {
	orig := runGHBrowse
	runGHBrowse = fn
	return func() { runGHBrowse = orig }
}

// featWorktree returns a Worktree over a fresh repo checked out on "feat" (a
// deterministic branch, not git's version-dependent default), so commitLocalChanges
// runs against a real worktree while the push seam is swapped for a fake.
func featWorktree(t *testing.T) (*Worktree, string) {
	t.Helper()
	repo := newTestRepo(t)
	mustRunGit(t, repo, "checkout", "-b", "feat")
	return &Worktree{worktreePath: repo, branchName: "feat"}, repo
}

// A clean worktree has nothing to commit, but PushChanges must still push — the
// branch may carry earlier commits that were never pushed.
func TestPushChanges_CleanWorktreeStillPushes(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, repo := featWorktree(t)

	var gotDir, gotBranch string
	calls := 0
	defer stubGitPush(func(_ context.Context, dir, branch string) error {
		calls++
		gotDir, gotBranch = dir, branch
		return nil
	})()

	if err := g.PushChanges("msg", false); err != nil {
		t.Fatalf("PushChanges() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("runGitPush calls = %d, want 1 (clean worktree must still push)", calls)
	}
	if gotBranch != "feat" {
		t.Errorf("push branch = %q, want %q", gotBranch, "feat")
	}
	if gotDir != repo {
		t.Errorf("push dir = %q, want worktree %q", gotDir, repo)
	}
}

// A dirty worktree is committed (no-verify) before the push runs.
func TestPushChanges_CommitsThenPushes(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, repo := featWorktree(t)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	before := revParse(t, repo, "HEAD")

	calls := 0
	defer stubGitPush(func(context.Context, string, string) error { calls++; return nil })()

	if err := g.PushChanges("commit msg", false); err != nil {
		t.Fatalf("PushChanges() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("runGitPush calls = %d, want 1", calls)
	}
	if after := revParse(t, repo, "HEAD"); after == before {
		t.Fatal("PushChanges did not create a commit for the dirty worktree")
	}
	if status := strings.TrimSpace(mustRunGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("worktree still dirty after push: %q", status)
	}
}

// A push failure from the seam propagates out of PushChanges unchanged.
func TestPushChanges_PushFailurePropagates(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, _ := featWorktree(t)

	sentinel := errors.New("failed to push branch feat: rejected (boom)")
	defer stubGitPush(func(context.Context, string, string) error { return sentinel })()

	err := g.PushChanges("msg", false)
	if err == nil {
		t.Fatal("PushChanges() = nil, want the push error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap the seam error", err)
	}
}

// The push authenticates via git (SSH / credential helper), not gh, so an
// unavailable gh must NOT block it: with open=false, PushChanges pushes and
// succeeds even though the gh gate would fail.
func TestPushChanges_PushIsGHIndependent(t *testing.T) {
	defer stubCheckGHCLI(errors.New("gh not configured"))()
	g, _ := featWorktree(t)

	pushed := false
	defer stubGitPush(func(context.Context, string, string) error { pushed = true; return nil })()

	if err := g.PushChanges("msg", false); err != nil {
		t.Fatalf("PushChanges() = %v, want nil (push must not depend on gh)", err)
	}
	if !pushed {
		t.Error("runGitPush did not run; the push must not depend on gh")
	}
}

// With open=true but gh unavailable, the push still succeeds; the browser-open is
// best-effort and self-gates on gh inside OpenBranchURL — so the gate failure is
// logged (not returned) and never reaches the browse seam.
func TestPushChanges_OpenWithoutGHStillPushes(t *testing.T) {
	defer stubCheckGHCLI(errors.New("gh not configured"))()
	g, _ := featWorktree(t)

	pushed := false
	defer stubGitPush(func(context.Context, string, string) error { pushed = true; return nil })()
	browsed := false
	defer stubGHBrowse(func(context.Context, string, string) error { browsed = true; return nil })()

	if err := g.PushChanges("msg", true); err != nil {
		t.Fatalf("PushChanges(open=true) = %v, want nil (browser-open is best-effort)", err)
	}
	if !pushed {
		t.Error("runGitPush did not run")
	}
	if browsed {
		t.Error("runGHBrowse ran despite a failing gh gate in OpenBranchURL")
	}
}

// With open=true, the branch is opened in the browser after a successful push, and
// a browse failure is best-effort: it is logged, not returned.
func TestPushChanges_OpenInvokesBrowse(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, _ := featWorktree(t)
	defer stubGitPush(func(context.Context, string, string) error { return nil })()

	browsed := false
	defer stubGHBrowse(func(_ context.Context, _, branch string) error {
		browsed = true
		if branch != "feat" {
			t.Errorf("browse branch = %q, want feat", branch)
		}
		return errors.New("browser boom") // must NOT fail the push
	})()

	if err := g.PushChanges("msg", true); err != nil {
		t.Fatalf("PushChanges(open=true) error = %v, want nil (browse error is best-effort)", err)
	}
	if !browsed {
		t.Error("runGHBrowse was not invoked for open=true")
	}
}

// A successful push invalidates the PR cache so the next poll reflects a newly
// opened/updated PR.
func TestPushChanges_InvalidatesPRCache(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, _ := featWorktree(t)
	defer stubGitPush(func(context.Context, string, string) error { return nil })()

	g.prCacheMu.Lock()
	g.prCache = PRStatus{HasPR: true, Number: 9, fetchedAt: time.Now()}
	g.prCacheMu.Unlock()

	if err := g.PushChanges("msg", false); err != nil {
		t.Fatalf("PushChanges() error = %v", err)
	}

	g.prCacheMu.Lock()
	cached := g.prCache
	g.prCacheMu.Unlock()
	if cached.HasPR || cached.Number != 0 || !cached.fetchedAt.IsZero() {
		t.Errorf("prCache = %+v, want zeroed after push", cached)
	}
}

// Integration test of the real runGitPush (no network, no gh): a first push creates
// origin/<branch> with upstream tracking; a second push fast-forwards it. The
// seam-swap tests above never exercise the actual `git push` command string.
func TestRunGitPush_CreatesAndFastForwardsRemote(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	g, repo := featWorktree(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	mustRunGit(t, "", "init", "--bare", bare)
	mustRunGit(t, repo, "remote", "add", "origin", bare)

	// First push: origin/feat is created at local HEAD (branch did not exist yet).
	if err := g.PushChanges("first", false); err != nil {
		t.Fatalf("first PushChanges() error = %v", err)
	}
	localHEAD := revParse(t, repo, "HEAD")
	if got := revParse(t, repo, "refs/remotes/origin/feat"); got != localHEAD {
		t.Fatalf("origin/feat = %q after first push, want local HEAD %q", got, localHEAD)
	}

	// A new commit then a second push fast-forwards origin/feat.
	if err := os.WriteFile(filepath.Join(repo, "more.txt"), []byte("more\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.PushChanges("second", false); err != nil {
		t.Fatalf("second PushChanges() error = %v", err)
	}
	newHEAD := revParse(t, repo, "HEAD")
	if newHEAD == localHEAD {
		t.Fatal("second PushChanges did not commit the new file")
	}
	if got := revParse(t, repo, "refs/remotes/origin/feat"); got != newHEAD {
		t.Fatalf("origin/feat = %q after update push, want %q", got, newHEAD)
	}
}

// The real runGitPush folds git's diagnostic into an error that names the branch
// when the push cannot proceed (here: no origin remote configured).
func TestRunGitPush_FailureNamesBranch(t *testing.T) {
	_, repo := featWorktree(t)

	err := runGitPush(context.Background(), repo, "feat")
	if err == nil {
		t.Fatal("runGitPush() = nil, want an error with no origin remote")
	}
	if !strings.Contains(err.Error(), "feat") {
		t.Errorf("error = %q, want it to name the branch", err.Error())
	}
}
