package git

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// sanitizeBranchName transforms an arbitrary string into a Git branch name friendly string.
// Note: Git branch names have several rules, so this function uses a simple approach
// by allowing only a safe subset of characters.
func sanitizeBranchName(s string) string {
	// Convert to lower-case
	s = strings.ToLower(s)

	// Replace spaces with a dash
	s = strings.ReplaceAll(s, " ", "-")

	// Remove any characters not allowed in our safe subset.
	// Here we allow: letters, digits, dash, underscore, slash, and dot.
	re := regexp.MustCompile(`[^a-z0-9\-_/.]+`)
	s = re.ReplaceAllString(s, "")

	// Replace multiple dashes with a single dash (optional cleanup)
	reDash := regexp.MustCompile(`-+`)
	s = reDash.ReplaceAllString(s, "-")

	// Trim leading and trailing dashes or slashes to avoid issues
	s = strings.Trim(s, "-/")

	return s
}

// checkGHCLI checks if GitHub CLI is installed and configured
func checkGHCLI(ctx context.Context) error {
	// Check if gh is installed
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("GitHub CLI (gh) is not installed. Please install it first")
	}

	// Check if gh is authenticated (may hit the network to validate the token)
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("GitHub CLI is not configured. Please run 'gh auth login' first")
	}

	return nil
}

// IsGitRepo checks if the given path is within a git repository
func IsGitRepo(ctx context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	return cmd.Run() == nil
}

// CurrentBranchName returns the branch HEAD points at in the repo containing path,
// "HEAD" for a detached HEAD (keeping --abbrev-ref's convention), or "" when the path
// is not a git repo (best-effort, like IsGitRepo). `branch --show-current` (rather than
// `rev-parse --abbrev-ref HEAD`) so an unborn HEAD — a fresh init with no commits yet —
// still resolves to its branch name.
func CurrentBranchName(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "HEAD" // --show-current prints nothing when detached
	}
	return branch
}

func findGitRepoRoot(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to find Git repository root from path: %s", path)
	}
	return strings.TrimSpace(string(out)), nil
}

// GetRemoteURL returns the origin remote URL for the repository containing path,
// or "" when there is no origin remote or path is not a git repo (best-effort,
// like CurrentBranchName). Used to route a worktree to a Claude Code account.
func GetRemoteURL(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
