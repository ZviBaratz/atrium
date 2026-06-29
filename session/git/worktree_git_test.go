package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenameMutableFields_ConcurrentReadsAreLocked is a -race guard for the snapshot
// protection the write ops (IsDirty, PushChanges, OpenBranchURL) and Diff rely on:
// worktreePath/branchName must be read under g.mu, never as a bare field, so a
// concurrent deep Rename swapping those fields can't tear the read. The reader loop
// spins on the locked accessors throughout Rename's field swap; a bare read would
// trip the race detector here.
func TestRenameMutableFields_ConcurrentReadsAreLocked(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, _, err := NewWorktree(context.Background(), repoPath, "race-before")
	if err != nil {
		t.Fatalf("NewWorktree error = %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = wt.GetWorktreePath() // shares the RLock snapshotWorktreePath uses
				_ = wt.GetBranchName()
			}
		}
	}()

	renameErr := wt.Rename("race-after")
	close(stop)
	<-done

	if renameErr != nil {
		t.Fatalf("Rename() error = %v", renameErr)
	}
	// IsDirty now reads the path through the same locked snapshot.
	if _, err := wt.IsDirty(); err != nil {
		t.Fatalf("IsDirty() after rename error = %v", err)
	}
}

// BranchCheckoutPath must report the base repo when the branch is checked out there.
func TestBranchCheckoutPath_BaseRepo(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	mustRunGit(t, repoPath, "switch", "feat") // base HEAD now on feat

	g := &Worktree{repoPath: repoPath, branchName: "feat"}

	path, err := g.BranchCheckoutPath()
	if err != nil {
		t.Fatalf("BranchCheckoutPath() error = %v", err)
	}
	if resolvePath(path) != resolvePath(repoPath) {
		t.Fatalf("BranchCheckoutPath() = %q, want base repo %q", path, repoPath)
	}
	if held, err := g.IsBranchHeldByBaseRepo(); err != nil || !held {
		t.Fatalf("IsBranchHeldByBaseRepo() = %v, %v; want true, nil", held, err)
	}
	if checked, err := g.IsBranchCheckedOut(); err != nil || !checked {
		t.Fatalf("IsBranchCheckedOut() = %v, %v; want true, nil", checked, err)
	}
}

// BranchCheckoutPath must report a sibling worktree (not the base repo) when the
// branch is checked out there, and IsBranchHeldByBaseRepo must be false.
func TestBranchCheckoutPath_SiblingWorktree(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat") // base stays on its default branch

	sibling := filepath.Join(t.TempDir(), "sibling")
	mustRunGit(t, repoPath, "worktree", "add", sibling, "feat")

	g := &Worktree{repoPath: repoPath, branchName: "feat"}

	path, err := g.BranchCheckoutPath()
	if err != nil {
		t.Fatalf("BranchCheckoutPath() error = %v", err)
	}
	if resolvePath(path) != resolvePath(sibling) {
		t.Fatalf("BranchCheckoutPath() = %q, want sibling %q", path, sibling)
	}
	if held, err := g.IsBranchHeldByBaseRepo(); err != nil || held {
		t.Fatalf("IsBranchHeldByBaseRepo() = %v, %v; want false, nil", held, err)
	}
	if checked, err := g.IsBranchCheckedOut(); err != nil || !checked {
		t.Fatalf("IsBranchCheckedOut() = %v, %v; want true, nil", checked, err)
	}
}

// A live session's branch lives in its OWN worktree (not the base repo). Killing
// it must be allowed: IsBranchHeldByBaseRepo is the kill guard's predicate and must
// be false here, even though IsBranchCheckedOut (the old, buggy predicate) is true.
func TestSessionOwnWorktreeIsKillable(t *testing.T) {
	repoPath := newTestRepo(t) // base repo stays on its default branch
	sessionWT := filepath.Join(t.TempDir(), "session")
	mustRunGit(t, repoPath, "worktree", "add", "-b", "session/test", sessionWT)

	g := &Worktree{repoPath: repoPath, branchName: "session/test", worktreePath: sessionWT}

	if held, err := g.IsBranchHeldByBaseRepo(); err != nil || held {
		t.Fatalf("IsBranchHeldByBaseRepo() = %v, %v; want false, nil (kill must be allowed)", held, err)
	}
	if checked, err := g.IsBranchCheckedOut(); err != nil || !checked {
		t.Fatalf("IsBranchCheckedOut() = %v, %v; want true, nil (old predicate wrongly blocked)", checked, err)
	}
}

// A branch that exists but is not checked out anywhere is free.
func TestBranchCheckoutPath_Free(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat") // exists, never checked out

	g := &Worktree{repoPath: repoPath, branchName: "feat"}

	if path, err := g.BranchCheckoutPath(); err != nil || path != "" {
		t.Fatalf("BranchCheckoutPath() = %q, %v; want \"\", nil", path, err)
	}
	if checked, err := g.IsBranchCheckedOut(); err != nil || checked {
		t.Fatalf("IsBranchCheckedOut() = %v, %v; want false, nil", checked, err)
	}
	if held, err := g.IsBranchHeldByBaseRepo(); err != nil || held {
		t.Fatalf("IsBranchHeldByBaseRepo() = %v, %v; want false, nil", held, err)
	}
}

// A detached-HEAD base repo emits no branch line, so its former branch reads free.
func TestBranchCheckoutPath_DetachedBaseRepo(t *testing.T) {
	repoPath := newTestRepo(t)
	defaultBranch := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--show-current"))
	mustRunGit(t, repoPath, "branch", "feat")
	mustRunGit(t, repoPath, "switch", "--detach")

	// The branch the base repo just detached from must not be reported as held.
	g := &Worktree{repoPath: repoPath, branchName: defaultBranch}
	if path, err := g.BranchCheckoutPath(); err != nil || path != "" {
		t.Fatalf("BranchCheckoutPath(%q) = %q, %v; want \"\", nil", defaultBranch, path, err)
	}
	// And an unrelated existing branch is likewise free.
	g.branchName = "feat"
	if path, err := g.BranchCheckoutPath(); err != nil || path != "" {
		t.Fatalf("BranchCheckoutPath(feat) = %q, %v; want \"\", nil", path, err)
	}
}

// Defense in depth: when the branch is busy in a sibling worktree, Setup's
// `git worktree add` must surface the friendly, path-named message rather than
// the raw "git command failed" output.
func TestSetup_BusyBranchFriendlyError(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	sibling := filepath.Join(t.TempDir(), "sibling")
	mustRunGit(t, repoPath, "worktree", "add", sibling, "feat")

	tempHome := os.Getenv("HOME") // newTestRepo sandboxed HOME
	g := &Worktree{
		repoPath:         repoPath,
		worktreePath:     filepath.Join(tempHome, ".claude-squad", "worktrees", "sess-busy"),
		branchName:       "feat",
		isExistingBranch: true,
	}

	err := g.Setup()
	if err == nil {
		t.Fatal("Setup() succeeded, want busy-branch error")
	}
	// The app layer recognises the busy-branch case with errors.As, so the type —
	// not just the wording — is the contract that must hold across the boundary.
	var busy *BranchCheckedOutError
	if !errors.As(err, &busy) {
		t.Fatalf("Setup() error = %q, want a *BranchCheckedOutError", err.Error())
	}
	if busy.Path == "" {
		t.Errorf("BranchCheckedOutError.Path is empty, want the sibling worktree path")
	}
	if !strings.Contains(err.Error(), "is checked out at") {
		t.Fatalf("Setup() error = %q, want it to mention 'is checked out at'", err.Error())
	}
	if strings.Contains(err.Error(), "git command failed") {
		t.Fatalf("Setup() error leaked raw git output: %q", err.Error())
	}
}

// Recovery: when the base repo holds the branch, Setup fails busy; detaching the
// base repo frees it and a subsequent Setup succeeds on that branch.
func TestDetachBranchInBaseRepo_FreesBranch(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	mustRunGit(t, repoPath, "switch", "feat") // base repo now holds feat

	tempHome := os.Getenv("HOME")
	g := &Worktree{
		repoPath:         repoPath,
		worktreePath:     filepath.Join(tempHome, ".claude-squad", "worktrees", "sess-recover"),
		branchName:       "feat",
		isExistingBranch: true,
	}

	// Sanity: Setup is blocked while the base repo holds the branch.
	if err := g.Setup(); err == nil || !strings.Contains(err.Error(), "is checked out at") {
		t.Fatalf("Setup() = %v, want busy-branch error before detach", err)
	}

	if err := g.DetachBranchInBaseRepo(); err != nil {
		t.Fatalf("DetachBranchInBaseRepo() error = %v", err)
	}
	if cur := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--show-current")); cur != "" {
		t.Fatalf("base repo still on branch %q after detach, want detached HEAD", cur)
	}
	if path, err := g.BranchCheckoutPath(); err != nil || path != "" {
		t.Fatalf("BranchCheckoutPath() = %q, %v after detach; want \"\", nil", path, err)
	}

	if err := g.Setup(); err != nil {
		t.Fatalf("Setup() after detach error = %v, want success", err)
	}
	if valid, err := g.IsValidWorktree(); err != nil || !valid {
		t.Fatalf("IsValidWorktree() = %v, %v; want true, nil", valid, err)
	}
	if cur := mustRunGit(t, g.worktreePath, "branch", "--show-current"); cur != "feat\n" {
		t.Fatalf("session worktree branch = %q, want %q", cur, "feat\n")
	}
}

// DetachBranchInBaseRepo must refuse when the base repo has uncommitted changes,
// to avoid stranding the user's work on a detached HEAD.
func TestDetachBranchInBaseRepo_RefusesDirty(t *testing.T) {
	repoPath := newTestRepo(t)
	mustRunGit(t, repoPath, "branch", "feat")
	mustRunGit(t, repoPath, "switch", "feat")

	if err := os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	g := &Worktree{repoPath: repoPath, branchName: "feat"}
	err := g.DetachBranchInBaseRepo()
	if err == nil {
		t.Fatal("DetachBranchInBaseRepo() succeeded on a dirty base repo, want refusal")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("DetachBranchInBaseRepo() error = %q, want it to mention 'uncommitted'", err.Error())
	}
	// The base repo must be left untouched (still on feat).
	if cur := strings.TrimSpace(mustRunGit(t, repoPath, "branch", "--show-current")); cur != "feat" {
		t.Fatalf("base repo branch = %q after refused detach, want 'feat'", cur)
	}
}
