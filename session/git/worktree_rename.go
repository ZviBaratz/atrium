package git

import (
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
)

// Rename renames the session branch and moves the worktree dir to match newSessionName.
// On success it swaps branchName/worktreePath/sessionName under the write-lock so the
// metadata poll loop never tears a read. On a failed move it rolls back the branch rename
// so the worktree is left fully intact. For a paused/orphaned worktree (no dir on disk) it
// skips the move and only recomputes the stored worktreePath so a later Resume's
// `git worktree add` lands at the path matching the corrected branch.
func (g *GitWorktree) Rename(newSessionName string) error {
	cfg := config.LoadConfig()
	newBranch := sanitizeBranchName(fmt.Sprintf("%s%s", cfg.BranchPrefix, newSessionName))
	if newBranch == "" {
		return fmt.Errorf("new session name %q produces an empty branch name", newSessionName)
	}

	g.mu.RLock()
	oldBranch := g.branchName
	oldPath := g.worktreePath
	g.mu.RUnlock()

	if newBranch == oldBranch {
		// Nothing to do at the git layer (the title/label may still differ).
		return nil
	}

	// Reject if the target branch already exists; never force-clobber another session's branch.
	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", newBranch)); err == nil {
		return fmt.Errorf("a branch named %q already exists", newBranch)
	}

	// Resolve the new worktree path up front (locals only; fields are swapped at the end).
	_, newPath, err := resolveWorktreePaths(g.repoPath, newBranch)
	if err != nil {
		return fmt.Errorf("failed to resolve new worktree path: %w", err)
	}

	// 1. Rename the branch. git is worktree-aware: it updates the HEAD of the worktree that
	// has this branch checked out, so the running agent stays on the renamed branch.
	if _, err := g.runGitCommand(g.repoPath, "branch", "-m", oldBranch, newBranch); err != nil {
		return fmt.Errorf("failed to rename branch %q to %q: %w", oldBranch, newBranch, err)
	}

	// 2. Move the worktree directory, unless it's orphaned (e.g. a paused session whose dir
	// was removed). A same-filesystem move is an O(1) rename(2) the running process survives.
	if valid, verr := g.IsValidWorktree(); verr == nil && valid {
		if _, err := g.runGitCommand(g.repoPath, "worktree", "move", oldPath, newPath); err != nil {
			// Roll back the branch rename so the session is left fully intact.
			if _, rbErr := g.runGitCommand(g.repoPath, "branch", "-m", newBranch, oldBranch); rbErr != nil {
				log.ErrorLog.Printf("failed to roll back branch rename %q->%q: %v", newBranch, oldBranch, rbErr)
			}
			return fmt.Errorf("failed to move worktree %q to %q: %w", oldPath, newPath, err)
		}
	}

	// 3. Swap the in-memory fields atomically for the concurrent metadata poll loop.
	g.mu.Lock()
	g.branchName = newBranch
	g.sessionName = newSessionName
	g.worktreePath = newPath
	g.mu.Unlock()

	return nil
}
