package git

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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

// checkGHCLI checks if GitHub CLI is installed and configured. It is a package
// var so tests can stub the gh-availability gate without a real, authenticated
// gh on PATH.
var checkGHCLI = func(ctx context.Context) error {
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

// localGit runs `git -C dir args...` capped at gitLocalTimeout and returns its
// trimmed stdout. It is the package-level analog of Worktree.runGitCommand for the
// helpers here that hold a context and a path but no *Worktree; deriving the
// timeout and building the command once keeps a local-git invocation defined in a
// single place. Unlike runGitCommand's CombinedOutput, stderr is left out of the
// result so a git diagnostic can't corrupt a parsed value; callers that only care
// whether the command succeeded ignore the string and check err.
func localGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// IsGitRepo checks if the given path is within a git repository
func IsGitRepo(ctx context.Context, path string) bool {
	_, err := localGit(ctx, path, "rev-parse", "--show-toplevel")
	return err == nil
}

// CurrentBranchName returns the branch HEAD points at in the repo containing path,
// "HEAD" for a detached HEAD (keeping --abbrev-ref's convention), or "" when the path
// is not a git repo (best-effort, like IsGitRepo). `branch --show-current` (rather than
// `rev-parse --abbrev-ref HEAD`) so an unborn HEAD — a fresh init with no commits yet —
// still resolves to its branch name.
func CurrentBranchName(ctx context.Context, path string) string {
	branch, err := localGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return ""
	}
	if branch == "" {
		return "HEAD" // --show-current prints nothing when detached
	}
	return branch
}

// BranchNameForSession derives the git branch a session titled title owns:
// sanitizeBranchName(prefix + title). It is the single source of the slug —
// the worktree layer mints it and the new-session form predicts it for the
// duplicate check, so the two can never drift.
func BranchNameForSession(prefix, title string) string {
	return sanitizeBranchName(prefix + title)
}

// LocalBranchExists reports whether branch exists as a local head in the repo
// at repoPath. It is an exact ref lookup (show-ref --verify), deliberately not
// SearchBranches, whose results are capped and merged with origin/ names.
func LocalBranchExists(ctx context.Context, repoPath, branch string) bool {
	_, err := localGit(ctx, repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// RepoGroupKey predicts the repo-group key the session list will file a session
// under when created from path: the repo root's basename when path is inside a
// git repo (even a subdirectory), else the directory's own basename (how direct
// sessions group). Best-effort: any git failure falls back to the basename.
func RepoGroupKey(ctx context.Context, path string) string {
	if root, err := findGitRepoRoot(ctx, path); err == nil {
		return filepath.Base(root)
	}
	return filepath.Base(path)
}

func findGitRepoRoot(ctx context.Context, path string) (string, error) {
	out, err := localGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("failed to find Git repository root from path: %s", path)
	}
	return out, nil
}

// GetRemoteURL returns the origin remote URL for the repository containing path,
// or "" when there is no origin remote or path is not a git repo (best-effort,
// like CurrentBranchName). Used to route a worktree to a Claude Code account.
func GetRemoteURL(ctx context.Context, path string) string {
	out, err := localGit(ctx, path, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return out
}
