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

// MaxBranchSearchResults is the maximum number of branches returned by SearchBranches.
const MaxBranchSearchResults = 50

// FetchBranches fetches and prunes remote-tracking branches (best-effort, won't fail if offline).
func FetchBranches(ctx context.Context, repoPath string) {
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fetch", "--prune")
	_ = cmd.Run()
}

// SearchBranches searches for branches whose name contains filter (case-insensitive),
// ordered by most recently updated first. Returns at most MaxBranchSearchResults.
// If filter is empty, returns all branches up to the limit.
func SearchBranches(ctx context.Context, repoPath, filter string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "-a",
		"--sort=-committerdate",
		"--format=%(refname:short)")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %s (%w)", output, err)
	}

	seen := make(map[string]bool)
	var branches []string
	lower := strings.ToLower(filter)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "HEAD") {
			continue
		}
		name := strings.TrimPrefix(line, "origin/")
		if seen[name] {
			continue
		}
		seen[name] = true
		if filter != "" && !strings.Contains(strings.ToLower(name), lower) {
			continue
		}
		branches = append(branches, name)
		if len(branches) >= MaxBranchSearchResults {
			break
		}
	}
	return branches, nil
}

// runGitCommand executes a local git command and returns any error. The command
// runs under the worktree's base context capped at gitLocalTimeout: every caller
// is a local operation, so the timeout is derived here rather than at each of the
// ~30 call sites. Network-bound commands (push, gh) do not go through this funnel —
// they build their own exec.CommandContext with gitNetworkTimeout.
func (g *Worktree) runGitCommand(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(g.baseContext(), gitLocalTimeout)
	defer cancel()
	baseArgs := []string{"-C", path}
	cmd := exec.CommandContext(ctx, "git", append(baseArgs, args...)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git command failed: %s (%w)", output, err)
	}

	return string(output), nil
}

// PushChanges commits and pushes changes in the worktree to the remote branch
func (g *Worktree) PushChanges(commitMessage string, open bool) error {
	if err := checkGHCLI(g.baseContext()); err != nil {
		return err
	}

	// Check if there are any changes to commit
	isDirty, err := g.IsDirty()
	if err != nil {
		return fmt.Errorf("failed to check for changes: %w", err)
	}

	if isDirty {
		// Stage all changes
		if _, err := g.runGitCommand(g.worktreePath, "add", "."); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to stage changes: %w", err)
		}

		// Create commit
		if _, err := g.runGitCommand(g.worktreePath, "commit", "-m", commitMessage, "--no-verify"); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to commit changes: %w", err)
		}
	}

	// Each network subprocess gets its own gitNetworkTimeout budget derived from
	// the worktree's base context.
	ctx, cancel := context.WithTimeout(g.baseContext(), gitNetworkTimeout)
	defer cancel()

	// First push the branch to remote to ensure it exists
	pushCmd := exec.CommandContext(ctx, "gh", "repo", "sync", "--source", "-b", g.branchName)
	pushCmd.Dir = g.worktreePath
	if err := pushCmd.Run(); err != nil {
		// If sync fails, try creating the branch on remote first
		fallbackCtx, fallbackCancel := context.WithTimeout(g.baseContext(), gitNetworkTimeout)
		defer fallbackCancel()
		gitPushCmd := exec.CommandContext(fallbackCtx, "git", "push", "-u", "origin", g.branchName)
		gitPushCmd.Dir = g.worktreePath
		if pushOutput, pushErr := gitPushCmd.CombinedOutput(); pushErr != nil {
			log.ErrorLog.Print(pushErr)
			return fmt.Errorf("failed to push branch: %s (%w)", pushOutput, pushErr)
		}
	}

	// Now sync with remote
	syncCtx, syncCancel := context.WithTimeout(g.baseContext(), gitNetworkTimeout)
	defer syncCancel()
	syncCmd := exec.CommandContext(syncCtx, "gh", "repo", "sync", "-b", g.branchName)
	syncCmd.Dir = g.worktreePath
	if output, err := syncCmd.CombinedOutput(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to sync changes: %s (%w)", output, err)
	}

	// Open the branch in the browser
	if open {
		if err := g.OpenBranchURL(); err != nil {
			// Just log the error but don't fail the push operation
			log.ErrorLog.Printf("failed to open branch URL: %v", err)
		}
	}

	// The commit graph changed; refresh the ahead/behind counts and the dirty flag on the next tick.
	g.invalidateStatsCache()
	// The push may have just opened or updated the PR; re-poll on the next tick.
	g.invalidatePRCache()
	return nil
}

// CommitChanges commits changes locally without pushing to remote
func (g *Worktree) CommitChanges(commitMessage string) error {
	// Check if there are any changes to commit
	isDirty, err := g.IsDirty()
	if err != nil {
		return fmt.Errorf("failed to check for changes: %w", err)
	}

	if isDirty {
		// Stage all changes
		if _, err := g.runGitCommand(g.worktreePath, "add", "."); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to stage changes: %w", err)
		}

		// Create commit (local only)
		if _, err := g.runGitCommand(g.worktreePath, "commit", "-m", commitMessage, "--no-verify"); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to commit changes: %w", err)
		}

		// The commit graph changed; refresh the ahead/behind counts and the dirty flag on the next tick.
		g.invalidateStatsCache()
	}

	return nil
}

// IsDirty checks if the worktree has uncommitted changes
func (g *Worktree) IsDirty() (bool, error) {
	output, err := g.runGitCommand(g.worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("failed to check worktree status: %w", err)
	}
	return len(output) > 0, nil
}

// IsValidWorktree reports whether the worktree path exists and contains a
// .git entry, i.e. git can still recognize it as a working tree.
// Returns (false, nil) if the worktree is orphaned (path or .git missing).
func (g *Worktree) IsValidWorktree() (bool, error) {
	if _, err := os.Stat(g.worktreePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat worktree path: %w", err)
	}
	if _, err := os.Stat(filepath.Join(g.worktreePath, ".git")); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat worktree .git: %w", err)
	}
	return true, nil
}

// BranchCheckedOutError reports that the session branch is already checked out
// in another worktree (the base repo or a sibling), which blocks recreating the
// session worktree on resume. It is a typed error so callers can recognise the
// busy-branch case with errors.As — far more durable than substring-matching the
// message across package boundaries. Path names the holding worktree when git
// revealed it, and is "" when only the conflict (not the holder) is known.
type BranchCheckedOutError struct {
	Branch string
	Path   string
}

func (e *BranchCheckedOutError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("cannot resume: branch %q is checked out at %s", e.Branch, e.Path)
	}
	return fmt.Sprintf("cannot resume: branch %q is already checked out elsewhere", e.Branch)
}

// BranchCheckoutPath returns the path of the worktree (the base repo or any
// sibling) that currently has g.branchName checked out, or "" if the branch is
// free. It parses `git worktree list --porcelain` (via parseWorktreeList) so it
// sees ALL worktrees, not just the base repo; detached-HEAD and bare records
// carry no branch line and therefore never match.
func (g *Worktree) BranchCheckoutPath() (string, error) {
	output, err := g.runGitCommand(g.repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("failed to list worktrees: %w", err)
	}
	for path, branch := range parseWorktreeList(output) {
		if branch == g.branchName {
			return path, nil
		}
	}
	return "", nil
}

// IsBranchCheckedOut reports whether the instance branch is checked out anywhere
// (the base repo or a sibling worktree). It is a thin wrapper over
// BranchCheckoutPath; callers that need the location should use that directly.
func (g *Worktree) IsBranchCheckedOut() (bool, error) {
	path, err := g.BranchCheckoutPath()
	if err != nil {
		return false, err
	}
	return path != "", nil
}

// IsBranchHeldByBaseRepo reports whether g.branchName is checked out in the base
// repo itself (as opposed to a sibling worktree). This distinguishes the
// auto-recoverable case (detach the base repo) from a branch held by another
// live worktree, which must not be touched automatically.
func (g *Worktree) IsBranchHeldByBaseRepo() (bool, error) {
	path, err := g.BranchCheckoutPath()
	if err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	// git prints canonical absolute paths; resolvePath (EvalSymlinks+Clean) lets
	// the comparison hold even when repoPath reaches the same tree through a
	// different symlink (e.g. macOS /var vs /private/var temp dirs).
	return resolvePath(path) == resolvePath(g.repoPath), nil
}

// DetachBranchInBaseRepo detaches the base repo's HEAD from g.branchName at its
// current commit, freeing the branch so the session worktree can re-check it
// out. It refuses when the base repo has uncommitted changes, to avoid stranding
// the user's work on a detached HEAD. Callers should confirm the branch is
// actually held by the base repo (IsBranchHeldByBaseRepo) before calling.
func (g *Worktree) DetachBranchInBaseRepo() error {
	// Use the base repo's own working tree for the dirty check — IsDirty inspects
	// the session worktree, which is the wrong target here.
	status, err := g.runGitCommand(g.repoPath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("failed to check base repo status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("base repo has uncommitted changes; commit or stash in %s and retry", g.repoPath)
	}
	// `switch --detach` with no ref detaches at the current HEAD commit.
	if _, err := g.runGitCommand(g.repoPath, "switch", "--detach"); err != nil {
		return fmt.Errorf("failed to detach base repo from branch %s: %w", g.branchName, err)
	}
	return nil
}

// OpenBranchURL opens the branch URL in the default browser
func (g *Worktree) OpenBranchURL() error {
	// Check if GitHub CLI is available
	if err := checkGHCLI(g.baseContext()); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(g.baseContext(), gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "browse", "--branch", g.branchName)
	cmd.Dir = g.worktreePath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to open branch URL: %w", err)
	}
	return nil
}
