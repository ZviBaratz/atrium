package git

import (
	"context"
	"fmt"
	"strings"
)

// Retention refs: the primitives that make a kill reversible.
//
// Cleanup runs `git branch -D`, after which the session's commits are reachable
// from nothing and `git gc` — which the user's own work in the project repo
// triggers — prunes them past its two-week grace period. Recording the head SHA
// is therefore not enough; the objects have to stay *reachable*. Pointing a ref
// at the branch tip before teardown does that, and putting it outside refs/heads
// keeps it out of `git branch`, `git push` and `git fetch`, so a killed session
// leaves no visible clutter behind. Expiry is then just deleting the ref.
//
// Everything here runs through runGitCommand or localGit rather than building its
// own exec: those funnels apply the local timeout and record into the command log
// (cmdlog), and a second git-exec path is exactly how the command log grows a
// hole.

// RetainBranch points ref at this worktree's branch head and returns the SHA it
// captured, so the caller can tell later whether a branch that still exists is
// still the one that was killed.
//
// It must run before Cleanup: afterwards the branch is gone and there is nothing
// left to name. A direct session has no repository and never calls this.
func (g *Worktree) RetainBranch(ref string) (string, error) {
	branch := g.GetBranchName()
	out, err := g.runGitCommand(g.repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("resolve branch %s for retention: %w", branch, err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("branch %s has no head to retain", branch)
	}
	if _, err := g.runGitCommand(g.repoPath, "update-ref", ref, sha); err != nil {
		return "", fmt.Errorf("retain branch %s at %s: %w", branch, ref, err)
	}
	return sha, nil
}

// RefExists resolves ref in the repository at repoPath, reporting the SHA it
// points at.
//
// `show-ref --verify`, not `rev-parse --verify`: rev-parse walks git's shorthand
// lookup order, so a partial name resolves against refs/, refs/tags/, refs/heads/
// and so on. This is the gate a restore trusts before recreating a branch, and it
// must answer for the exact refname it was handed — never for a tag that happens
// to share the tail. (The rest of this package uses show-ref --verify for the same
// reason.)
//
// Package-level, like CleanupWorktrees: at undo and expiry time the session is
// gone, so there is no live Worktree to hang this off.
func RefExists(ctx context.Context, repoPath, ref string) (string, bool) {
	out, err := localGit(ctx, repoPath, "show-ref", "--verify", "--hash", ref)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// BranchTip reports what branch points at in repoPath, and whether it exists at
// all. The two answers are different questions for a restore: an absent branch
// can be recreated, while one that exists but has moved means the tree a
// `worktree add` would materialize is not the tree that was killed.
func BranchTip(ctx context.Context, repoPath, branch string) (string, bool) {
	return RefExists(ctx, repoPath, "refs/heads/"+branch)
}

// CreateBranchAt recreates branch at whatever ref points to. It never forces:
// by the time an undo runs, the name may belong to a session the user started
// since, and moving that branch would silently detach its work.
func CreateBranchAt(ctx context.Context, repoPath, branch, ref string) error {
	if _, err := localGit(ctx, repoPath, "branch", branch, ref); err != nil {
		return fmt.Errorf("recreate branch %s at %s: %w", branch, ref, err)
	}
	return nil
}

// DeleteRef drops a retention ref, releasing its objects to the next gc.
//
// A ref that is already gone is not an error, which matters because expiry runs
// repeatedly over the same journal and a partially completed sweep must be safe
// to re-run. That idempotence comes from `update-ref -d` itself — deleting an
// absent ref exits 0 — so there is no existence pre-check here; guarding it would
// only add a subprocess per swept entry. TestDeleteRefIsIdempotent pins the
// behavior we are relying on.
func DeleteRef(ctx context.Context, repoPath, ref string) error {
	if _, err := localGit(ctx, repoPath, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("delete ref %s: %w", ref, err)
	}
	return nil
}
