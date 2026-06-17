package git

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Setup creates a new worktree for the session
func (g *Worktree) Setup() error {
	// Ensure worktrees directory exists early (can be done in parallel with branch check)
	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return err
	}

	// The session always gets its own branch. baseRef only selects the start point at first
	// creation; once the branch exists it holds the session's committed work (including the
	// WIP commit pause makes), so resume must reuse it rather than `branch -D` it away and
	// rebuild from baseRef — which silently discarded that work for base-branch sessions
	// (#146). Branch existence is the discriminator: creation never collides because the
	// new-session form blocks a title whose branch slug already exists (app/app_session.go),
	// so a pre-existing branch here means a resume of a base-branch or HEAD-based session.
	var setupErr error
	if _, refErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName)); refErr == nil {
		setupErr = g.setupFromExistingBranch()
	} else {
		setupErr = g.setupNewWorktree()
	}
	if setupErr != nil {
		return setupErr
	}

	// The worktree is materialized; carry configured gitignored files from the
	// origin checkout into it (best-effort, never an error — see carry.go).
	g.carryLocalFiles()
	return nil
}

// setupFromExistingBranch creates a worktree from an existing branch
func (g *Worktree) setupFromExistingBranch() error {
	// Directory already created in Setup(), skip duplicate creation

	// Clean up any existing worktree first
	_, _ = g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath) // Ignore error if worktree doesn't exist
	// If the directory is still there (orphaned, not registered with git), drop it so `git worktree add` won't fail.
	_ = os.RemoveAll(g.worktreePath)

	// Check if the local branch exists
	_, localErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName))
	if localErr != nil {
		// Local branch doesn't exist — check if remote tracking branch exists
		_, remoteErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", g.branchName))
		if remoteErr != nil {
			return fmt.Errorf("branch %s not found locally or on remote", g.branchName)
		}
		// Create a local tracking branch via worktree add -b
		if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, fmt.Sprintf("origin/%s", g.branchName)); err != nil {
			if busyErr := g.busyBranchError(err); busyErr != nil {
				return busyErr
			}
			return fmt.Errorf("failed to create worktree from remote branch %s: %w", g.branchName, err)
		}
		return nil
	}

	// Create a new worktree from the existing local branch
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", g.worktreePath, g.branchName); err != nil {
		// Defense in depth: the Resume pre-check frees the branch first, but the
		// branch can become busy again between that check and here (another
		// session/manual checkout). Translate git's raw "already used by
		// worktree" output into the same friendly, path-named message.
		if busyErr := g.busyBranchError(err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to create worktree from branch %s: %w", g.branchName, err)
	}

	return nil
}

// busyBranchError returns a *BranchCheckedOutError when err is git's "branch
// already used by another worktree" failure, or nil otherwise. It shares the
// typed error the Resume pre-check returns so the app layer detects both origins
// with a single errors.As — including the path-less fallback, which the app
// recovers via IsBranchHeldByBaseRepo regardless.
func (g *Worktree) busyBranchError(err error) error {
	path, busy := busyBranchHolder(err)
	if !busy {
		return nil
	}
	return &BranchCheckedOutError{Branch: g.branchName, Path: path}
}

// busyBranchHolder scans a git error for the "already used by worktree" /
// "already checked out" signatures (wording varies across git versions) and
// returns the worktree path git named, plus whether the error was a busy-branch
// conflict at all. A marker match with an unparseable path yields ("", true).
func busyBranchHolder(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	for _, marker := range []string{"is already used by worktree at '", "is already checked out at '"} {
		idx := strings.Index(msg, marker)
		if idx < 0 {
			continue
		}
		rest := msg[idx+len(marker):]
		if end := strings.IndexByte(rest, '\''); end >= 0 {
			return rest[:end], true
		}
		return "", true
	}
	return "", false
}

// setupNewWorktree creates a new worktree on a fresh session branch, started from g.baseRef
// (an existing branch to base on) or HEAD when baseRef is empty.
func (g *Worktree) setupNewWorktree() error {
	// Clean up any existing worktree first
	_, _ = g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath) // Ignore error if worktree doesn't exist
	// If the directory is still there (orphaned, not registered with git), drop it so `git worktree add` won't fail.
	_ = os.RemoveAll(g.worktreePath)

	// Clean up any existing branch using git CLI (much faster than go-git PlainOpen)
	_, _ = g.runGitCommand(g.repoPath, "branch", "-D", g.branchName) // Ignore error if branch doesn't exist

	// Resolve the start point. Branching off a ref (rather than checking it out) succeeds
	// even when that ref is checked out in another worktree, which is the whole point.
	startPoint, err := g.resolveStartPoint()
	if err != nil {
		return err
	}

	output, err := g.runGitCommand(g.repoPath, "rev-parse", startPoint)
	if err != nil {
		return fmt.Errorf("failed to resolve start point %s: %w", startPoint, err)
	}
	g.baseCommitSHA = strings.TrimSpace(output)

	// Create a new worktree on its own branch from the start point. Starting from a commit
	// (rather than the current worktree) gives the session a clean slate without inheriting
	// uncommitted changes.
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, startPoint); err != nil {
		return fmt.Errorf("failed to create worktree on branch %s from %s: %w", g.branchName, startPoint, err)
	}

	return nil
}

// resolveStartPoint returns the ref to branch the session off. When baseRef is empty this is
// HEAD; otherwise it is the local branch baseRef, falling back to its remote-tracking
// counterpart origin/<baseRef> when no local branch exists.
func (g *Worktree) resolveStartPoint() (string, error) {
	if g.baseRef == "" {
		if _, err := g.runGitCommand(g.repoPath, "rev-parse", "--verify", "HEAD"); err != nil {
			if strings.Contains(err.Error(), "fatal: ambiguous argument 'HEAD'") ||
				strings.Contains(err.Error(), "fatal: not a valid object name") ||
				strings.Contains(err.Error(), "fatal: HEAD: not a valid object name") {
				return "", fmt.Errorf("this appears to be a brand new repository: please create an initial commit before creating an instance")
			}
			return "", fmt.Errorf("failed to get HEAD commit hash: %w", err)
		}
		return "HEAD", nil
	}

	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.baseRef)); err == nil {
		return g.baseRef, nil
	}
	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", g.baseRef)); err == nil {
		return fmt.Sprintf("origin/%s", g.baseRef), nil
	}
	return "", fmt.Errorf("base branch %q not found locally or on remote", g.baseRef)
}

// Cleanup removes the worktree and associated branch
func (g *Worktree) Cleanup() error {
	var errs []error

	// Check if worktree path exists before attempting removal
	if _, err := os.Stat(g.worktreePath); err == nil {
		// Remove the worktree using git command
		if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
			// The git removal can fail when the repo itself is unreachable — e.g.
			// the user renamed or deleted the project directory the session was
			// created from. Fall back to deleting the directory outright so an
			// orphaned worktree is never left behind, guarding the path to the
			// managed worktrees/ tree so a bug can't RemoveAll something arbitrary.
			if rmErr := removeOrphanedWorktreeDir(g.worktreePath); rmErr != nil {
				errs = append(errs, err, rmErr)
			} else {
				log.WarningLog.Printf("git worktree remove failed for %s, removed directory directly: %v", g.worktreePath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		// Only append error if it's not a "not exists" error
		errs = append(errs, fmt.Errorf("failed to check worktree path: %w", err))
	}

	// Delete the branch using git CLI, but skip if this is a pre-existing branch
	if !g.isExistingBranch {
		if _, err := g.runGitCommand(g.repoPath, "branch", "-D", g.branchName); err != nil {
			// Only log if it's not a "branch not found" error
			if !strings.Contains(err.Error(), "not found") {
				errs = append(errs, fmt.Errorf("failed to remove branch %s: %w", g.branchName, err))
			}
		}
	}

	// Prune the worktree to clean up any remaining references
	if err := g.Prune(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return g.combineErrors(errs)
	}

	return nil
}

// removeOrphanedWorktreeDir deletes worktreePath, but only when it lives under the
// managed worktrees/ tree. The containment check is a safety belt: Cleanup calls
// this as a fallback when git can no longer manage the worktree, and we never want
// an unexpected path to turn into a recursive delete of something important.
func removeOrphanedWorktreeDir(worktreePath string) error {
	root, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to resolve worktrees directory: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("failed to resolve worktrees directory: %w", err)
	}
	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("failed to resolve worktree path: %w", err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove worktree path outside managed tree: %s", absPath)
	}
	if err := os.RemoveAll(absPath); err != nil {
		return fmt.Errorf("failed to remove orphaned worktree directory %s: %w", absPath, err)
	}
	return nil
}

// Remove removes the worktree but keeps the branch
func (g *Worktree) Remove() error {
	// Remove the worktree using git command
	if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// Prune removes all working tree administrative files and directories
func (g *Worktree) Prune() error {
	if _, err := g.runGitCommand(g.repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}

// parseWorktreeList parses `git worktree list --porcelain` output into a map of
// worktree-path → branch-name. Detached-HEAD worktrees map to an empty branch.
func parseWorktreeList(output string) map[string]string {
	result := make(map[string]string)
	current := ""
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch ") && current != "":
			branchPath := strings.TrimPrefix(line, "branch ")
			result[current] = strings.TrimPrefix(branchPath, "refs/heads/")
		}
	}
	return result
}

// resolvePath returns the symlink-resolved absolute path, falling back to the
// cleaned input when resolution fails (e.g. the path no longer exists). It lets
// the worktree-prefix check below match even when git reports a path through a
// different symlink than getWorktreeDirectory() returns — e.g. macOS resolves
// the temp dir /var/... to /private/var/....
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// uniqueNonEmptyStrings returns the input with empty strings and duplicates
// removed, preserving first-seen order.
func uniqueNonEmptyStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// CleanupWorktrees removes every worktree managed by atrium and its associated
// session branch. repoPaths must be the git repository roots that had active
// sessions: each git command runs with `git -C <repoPath>` so cleanup succeeds
// even when the caller's working directory is not a git repository (e.g.
// `atrium reset` from a home directory) or when sessions span multiple repos.
//
// The order is dictated by git: `git worktree list` only reports a worktree's
// branch while it is still registered, so branches are collected first; and
// `git branch -D` refuses to delete a branch checked out in a live worktree, so
// the directories are removed and pruned (detaching the branches) before the
// branches are finally deleted.
func CleanupWorktrees(ctx context.Context, repoPaths []string) error {
	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read worktree directory: %w", err)
	}

	worktreePrefix := resolvePath(worktreesDir) + string(filepath.Separator)
	repos := uniqueNonEmptyStrings(repoPaths)

	// Collect the session branch of every worktree that lives under our managed
	// worktrees directory, remembering which repo owns it. Worktree directories
	// are nested under a branch-prefix subdir, so match by path prefix rather
	// than by top-level directory name.
	type repoBranch struct{ repo, branch string }
	var branchesToDelete []repoBranch
	for _, repoPath := range repos {
		listCtx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
		output, err := exec.CommandContext(listCtx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
		cancel()
		if err != nil {
			log.ErrorLog.Printf("failed to list worktrees for repo %s: %v", repoPath, err)
			continue
		}
		for wtPath, branch := range parseWorktreeList(string(output)) {
			if branch == "" || !strings.HasPrefix(resolvePath(wtPath), worktreePrefix) {
				continue
			}
			branchesToDelete = append(branchesToDelete, repoBranch{repo: repoPath, branch: branch})
		}
	}

	// Remove the physical worktree directories before pruning and deleting
	// branches, so git no longer treats the branches as checked out.
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := removeOrphanedWorktreeDir(filepath.Join(worktreesDir, entry.Name())); err != nil {
			log.ErrorLog.Printf("failed to remove worktree dir %s: %v", entry.Name(), err)
		}
	}

	// Prune git's internal worktree tracking now that the directories are gone.
	for _, repoPath := range repos {
		pruneCtx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
		if err := exec.CommandContext(pruneCtx, "git", "-C", repoPath, "worktree", "prune").Run(); err != nil {
			log.ErrorLog.Printf("failed to prune worktrees for repo %s: %v", repoPath, err)
		}
		cancel()
	}

	// Finally delete the session branches; they are no longer checked out.
	for _, rb := range branchesToDelete {
		delCtx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
		if err := exec.CommandContext(delCtx, "git", "-C", rb.repo, "branch", "-D", rb.branch).Run(); err != nil {
			log.ErrorLog.Printf("failed to delete branch %s in %s: %v", rb.branch, rb.repo, err)
		}
		cancel()
	}

	return nil
}
