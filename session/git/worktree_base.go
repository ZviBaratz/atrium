package git

// Base-branch freshening for new sessions: fetch the base branch from origin so a
// session starts off the latest remote tip rather than a stale local branch, and —
// when the user opts in — fast-forward the local base branch to match. Everything
// here is strictly best-effort: it runs only on first creation (setupNewWorktree,
// guarded by updateBaseOnCreate) and never fails session creation. Failures and
// skips are logged, never surfaced, because the create flow usually drops straight
// into the attached session where a toast could not be seen.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// updateBaseRef fetches the session's base branch from origin (so resolveStartPoint
// can branch off the freshest remote tip) and, when fastForwardLocalBase is set,
// advances the local base branch to match. It stays silent — matching pre-feature
// behavior — when there is nothing to update: a detached/unborn HEAD or a repo with
// no origin remote. Any network or git failure logs and returns so creation
// proceeds from whatever local state exists.
func (g *Worktree) updateBaseRef() {
	name := g.baseBranchName()
	if name == "" {
		return // detached HEAD, unborn repo, or unresolved current branch
	}
	if GetRemoteURL(g.baseContext(), g.repoPath) == "" {
		return // local-only repo: nothing to fetch, and not an error
	}

	ctx, cancel := context.WithTimeout(g.baseContext(), baseFetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "fetch", "origin", name)
	start := time.Now()
	err := cmd.Run()
	recordCmd(cmd, g.sessionName, start, nil, err)
	if err != nil {
		// Info, not warning: this is a best-effort freshen that always falls back to
		// the local base, and the common cause is benign — a local-only base branch
		// that was never pushed (routine in a stacked-branch workflow), not just an
		// offline remote. The session is still created correctly from local.
		log.InfoLog.Printf("base update: could not fetch origin %s, using local base: %v", name, err)
		return
	}

	if g.fastForwardLocalBase {
		g.fastForwardBase(name)
	}
}

// baseBranchName resolves the branch the session is based on: the explicit baseRef
// (with any re-entry "origin/" prefix stripped), or the base repo's current branch
// when basing off HEAD. Returns "" for a detached HEAD or when the branch cannot be
// resolved.
func (g *Worktree) baseBranchName() string {
	if g.baseRef != "" {
		return strings.TrimPrefix(g.baseRef, "origin/")
	}
	if branch := CurrentBranchName(g.baseContext(), g.repoPath); branch != "" && branch != "HEAD" {
		return branch
	}
	return ""
}

// freshenRef returns "origin/<name>" when the session should start from the remote
// tip — origin/<name> exists and the local branch is either absent or an ancestor of
// it (behind or equal). It returns "" when there is nothing fresher than local (no
// remote-tracking ref, or local is ahead/diverged), so the caller falls back to its
// historical start point and leaves baseRef untouched. This is what prevents a fresh
// session from ever discarding local-only commits.
func (g *Worktree) freshenRef(name string) string {
	remote := "origin/" + name
	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", "refs/remotes/"+remote); err != nil {
		return "" // no remote-tracking ref to prefer
	}
	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", "refs/heads/"+name); err != nil {
		return remote // local branch absent: the remote tip is the only/freshest base
	}
	// Local present: prefer the remote only when local is an ancestor of it (behind
	// or equal), never when it carries commits the remote lacks (ahead/diverged).
	if _, err := g.runGitCommand(g.repoPath, "merge-base", "--is-ancestor", name, remote); err == nil {
		return remote
	}
	return ""
}

// fastForwardBase advances the local <name> branch to origin/<name> when that is a
// clean fast-forward (local strictly behind). It moves the ref directly when the
// branch is not checked out anywhere, or runs git merge --ff-only in the holding
// worktree when it is checked out and the tree is clean. Dirty, ahead, diverged, and
// already-current cases are left untouched; the user's working tree is never forced
// or stashed. Only reached behind the fastForwardLocalBase opt-in.
func (g *Worktree) fastForwardBase(name string) {
	remote := "origin/" + name
	// Strict fast-forward only: local must be a proper ancestor of the remote.
	if _, err := g.runGitCommand(g.repoPath, "merge-base", "--is-ancestor", name, remote); err != nil {
		return // local missing, ahead, or diverged — nothing to fast-forward
	}
	localSHA, _ := g.runGitCommand(g.repoPath, "rev-parse", name)
	remoteSHA, _ := g.runGitCommand(g.repoPath, "rev-parse", remote)
	if strings.TrimSpace(localSHA) == strings.TrimSpace(remoteSHA) {
		return // already up to date (is-ancestor is also true when equal)
	}

	holder, err := g.checkoutPathForBranch(name)
	if err != nil {
		log.WarningLog.Printf("base update: could not determine if %s is checked out: %v", name, err)
		return
	}
	if holder == "" {
		// Not checked out anywhere: a pure ref move disturbs no index or working tree.
		if _, err := g.runGitCommand(g.repoPath, "update-ref", "refs/heads/"+name, remote); err != nil {
			log.WarningLog.Printf("base update: failed to fast-forward local %s: %v", name, err)
		}
		return
	}
	// Checked out: never touch a dirty tree; otherwise fast-forward it in place.
	status, err := g.runGitCommand(holder, "status", "--porcelain")
	if err != nil {
		log.WarningLog.Printf("base update: status check for %s failed: %v", name, err)
		return
	}
	if strings.TrimSpace(status) != "" {
		log.InfoLog.Printf("base update: %s has uncommitted changes at %s; left as-is", name, holder)
		return
	}
	if _, err := g.runGitCommand(holder, "merge", "--ff-only", remote); err != nil {
		log.WarningLog.Printf("base update: ff-only merge of %s failed: %v", name, err)
	}
}

// checkoutPathForBranch returns the worktree path that has branch name checked out,
// or "" when no worktree holds it. It mirrors BranchCheckoutPath but for an arbitrary
// branch rather than this session's own branch.
func (g *Worktree) checkoutPathForBranch(name string) (string, error) {
	output, err := g.runGitCommand(g.repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("failed to list worktrees: %w", err)
	}
	for path, branch := range parseWorktreeList(output) {
		if branch == name {
			return path, nil
		}
	}
	return "", nil
}
