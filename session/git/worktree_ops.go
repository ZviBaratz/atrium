package git

import (
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Setup creates a new worktree for the session
func (g *GitWorktree) Setup() error {
	// Ensure worktrees directory exists early (can be done in parallel with branch check)
	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return err
	}

	// When basing on a chosen branch, always branch off it into a fresh session branch
	// (setupNewWorktree force-recreates the session branch from the start point). We must
	// not fall into the reuse path below, which would ignore baseRef.
	if g.baseRef != "" {
		return g.setupNewWorktree()
	}

	// HEAD-based session: if the session branch already exists (e.g. a leftover from a
	// previous run with the same title), reuse it rather than wiping it.
	_, err = g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName))
	if err == nil {
		return g.setupFromExistingBranch()
	}
	return g.setupNewWorktree()
}

// setupFromExistingBranch creates a worktree from an existing branch
func (g *GitWorktree) setupFromExistingBranch() error {
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
			return fmt.Errorf("failed to create worktree from remote branch %s: %w", g.branchName, err)
		}
		return nil
	}

	// Create a new worktree from the existing local branch
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", g.worktreePath, g.branchName); err != nil {
		return fmt.Errorf("failed to create worktree from branch %s: %w", g.branchName, err)
	}

	return nil
}

// setupNewWorktree creates a new worktree on a fresh session branch, started from g.baseRef
// (an existing branch to base on) or HEAD when baseRef is empty.
func (g *GitWorktree) setupNewWorktree() error {
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
func (g *GitWorktree) resolveStartPoint() (string, error) {
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
func (g *GitWorktree) Cleanup() error {
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
func (g *GitWorktree) Remove() error {
	// Remove the worktree using git command
	if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// Prune removes all working tree administrative files and directories
func (g *GitWorktree) Prune() error {
	if _, err := g.runGitCommand(g.repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}

// CleanupWorktrees removes all worktrees and their associated branches
func CleanupWorktrees() error {
	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return fmt.Errorf("failed to read worktree directory: %w", err)
	}

	// Get a list of all branches associated with worktrees
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Parse the output to extract branch names
	worktreeBranches := make(map[string]string)
	currentWorktree := ""
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			currentWorktree = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branchPath := strings.TrimPrefix(line, "branch ")
			// Extract branch name from refs/heads/branch-name
			branchName := strings.TrimPrefix(branchPath, "refs/heads/")
			if currentWorktree != "" {
				worktreeBranches[currentWorktree] = branchName
			}
		}
	}

	for _, entry := range entries {
		if entry.IsDir() {
			worktreePath := filepath.Join(worktreesDir, entry.Name())

			// Delete the branch associated with this worktree if found
			for path, branch := range worktreeBranches {
				if strings.Contains(path, entry.Name()) {
					// Delete the branch
					deleteCmd := exec.Command("git", "branch", "-D", branch)
					if err := deleteCmd.Run(); err != nil {
						// Log the error but continue with other worktrees
						log.ErrorLog.Printf("failed to delete branch %s: %v", branch, err)
					}
					break
				}
			}

			// Remove the worktree directory
			_ = os.RemoveAll(worktreePath)
		}
	}

	// You have to prune the cleaned up worktrees.
	cmd = exec.Command("git", "worktree", "prune")
	_, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}

	return nil
}
