package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubGHPRWeb swaps the open-PR seam for a fake and returns a restore func.
func stubGHPRWeb(fn func(context.Context, string, string) error) func() {
	orig := runGHPRWeb
	runGHPRWeb = fn
	return func() { runGHPRWeb = orig }
}

// stubCheckGHCLI bypasses the real `gh auth status` gate so these tests stay
// hermetic (no installed/authenticated gh required).
func stubCheckGHCLI(err error) func() {
	orig := checkGHCLI
	checkGHCLI = func(context.Context) error { return err }
	return func() { checkGHCLI = orig }
}

// OpenPRURL must resolve the PR by the session branch and run gh from the origin
// repo (repoPath), which survives a paused session whose worktree is gone.
func TestOpenPRURL_OpensByBranchFromRepoPath(t *testing.T) {
	defer stubCheckGHCLI(nil)()

	var gotDir, gotBranch string
	defer stubGHPRWeb(func(_ context.Context, dir, branch string) error {
		gotDir, gotBranch = dir, branch
		return nil
	})()

	wt := &Worktree{repoPath: "/base/repo", branchName: "zvi/feat"}
	if err := wt.OpenPRURL(); err != nil {
		t.Fatalf("OpenPRURL: unexpected error: %v", err)
	}
	if gotBranch != "zvi/feat" {
		t.Errorf("branch = %q, want %q", gotBranch, "zvi/feat")
	}
	if gotDir != "/base/repo" {
		t.Errorf("dir = %q, want repoPath %q", gotDir, "/base/repo")
	}
}

// A gh failure is wrapped with the branch so the notice is actionable.
func TestOpenPRURL_WrapsSeamError(t *testing.T) {
	defer stubCheckGHCLI(nil)()
	defer stubGHPRWeb(func(context.Context, string, string) error {
		return errors.New("boom")
	})()

	wt := &Worktree{repoPath: "/base/repo", branchName: "zvi/feat"}
	err := wt.OpenPRURL()
	if err == nil {
		t.Fatal("OpenPRURL: expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "zvi/feat") || !strings.Contains(got, "boom") {
		t.Errorf("error = %q, want it to name the branch and wrap the cause", got)
	}
}

// A failed gh-availability gate short-circuits before the seam runs.
func TestOpenPRURL_GHGateBlocks(t *testing.T) {
	defer stubCheckGHCLI(errors.New("gh not configured"))()

	seamRan := false
	defer stubGHPRWeb(func(context.Context, string, string) error {
		seamRan = true
		return nil
	})()

	wt := &Worktree{repoPath: "/base/repo", branchName: "zvi/feat"}
	if err := wt.OpenPRURL(); err == nil {
		t.Fatal("OpenPRURL: expected the gh gate to block")
	}
	if seamRan {
		t.Error("the open seam must not run when the gh gate fails")
	}
}
