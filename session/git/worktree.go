package git

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"path/filepath"
	"sync"
	"time"
)

// Subprocess budgets. Every git/gh invocation derives a context from the
// worktree's base context (cancelled on app shutdown) capped by one of these,
// so a hung subprocess can never wedge the poll loop or daemon forever.
const (
	// gitLocalTimeout bounds local git operations (status, diff, commit,
	// worktree add/remove, branch queries).
	gitLocalTimeout = 30 * time.Second
	// gitNetworkTimeout bounds network-bound operations (push, fetch,
	// gh repo sync / auth / browse).
	gitNetworkTimeout = 60 * time.Second
)

func getWorktreeDirectory() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(configDir, "worktrees"), nil
}

// GitWorktree manages git worktree operations for a session
type GitWorktree struct {
	// baseCtx is the lifecycle context every git/gh subprocess derives from
	// (with a per-operation timeout). It is set once at construction — before
	// any background goroutine can reach this worktree — and cancelling it
	// (app/daemon shutdown) kills in-flight subprocesses. nil means Background.
	baseCtx context.Context
	// mu guards the fields a deep Rename mutates (worktreePath, branchName, sessionName)
	// against the metadata poll loop, which reads them from a background goroutine. Held
	// for writes only during the in-place field swap, never across a git subprocess.
	mu sync.RWMutex
	// Path to the repository
	repoPath string
	// Path to the worktree
	worktreePath string
	// Name of the session
	sessionName string
	// Branch name for the worktree
	branchName string
	// Base commit hash for the worktree
	baseCommitSHA string
	// baseRef is the ref the session branch is created from (a branch name to base on,
	// or "" to base on HEAD). The session always gets its own branch; baseRef only
	// chooses the start point, so it never conflicts with a branch checked out elsewhere.
	baseRef string
	// isExistingBranch is true if the branch existed before the session was created.
	// When true, the branch will not be deleted on cleanup. Only set for sessions restored
	// from storage that predate the branch-off model; new sessions are always branch-owners.
	isExistingBranch bool
}

func NewGitWorktreeFromStorage(ctx context.Context, repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string, baseRef string, isExistingBranch bool) *GitWorktree {
	return &GitWorktree{
		baseCtx:          ctx,
		repoPath:         repoPath,
		worktreePath:     worktreePath,
		sessionName:      sessionName,
		branchName:       branchName,
		baseCommitSHA:    baseCommitSHA,
		baseRef:          baseRef,
		isExistingBranch: isExistingBranch,
	}
}

// resolveWorktreePaths resolves the repo root and generates a unique worktree path for the given branch name.
func resolveWorktreePaths(ctx context.Context, repoPath string, branchName string) (resolvedRepo string, worktreePath string, err error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		log.ErrorLog.Printf("git worktree path abs error, falling back to repoPath %s: %s", repoPath, err)
		absPath = repoPath
	}

	resolvedRepo, err = findGitRepoRoot(ctx, absPath)
	if err != nil {
		return "", "", err
	}

	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return "", "", err
	}

	worktreePath = filepath.Join(worktreeDir, sanitizeBranchName(branchName))
	worktreePath = worktreePath + "_" + fmt.Sprintf("%x", time.Now().UnixNano())

	return resolvedRepo, worktreePath, nil
}

// NewGitWorktree creates a new GitWorktree instance whose session branch is based on HEAD.
func NewGitWorktree(ctx context.Context, repoPath string, sessionName string) (tree *GitWorktree, branchname string, err error) {
	return newSessionWorktree(ctx, repoPath, sessionName, "")
}

// NewGitWorktreeFromBase creates a new GitWorktree whose session branch is based on baseRef
// (an existing branch to start from). The session still gets its own branch named after the
// session, so cleanup deletes it and baseRef is left untouched; baseRef merely sets the start
// point, which is why it works even when baseRef is checked out in another worktree.
func NewGitWorktreeFromBase(ctx context.Context, repoPath string, sessionName string, baseRef string) (tree *GitWorktree, branchname string, err error) {
	return newSessionWorktree(ctx, repoPath, sessionName, baseRef)
}

// newSessionWorktree builds a GitWorktree that owns a fresh session branch
// (<BranchPrefix><sessionName>) created from baseRef ("" = HEAD).
func newSessionWorktree(ctx context.Context, repoPath string, sessionName string, baseRef string) (*GitWorktree, string, error) {
	cfg := config.LoadConfig()
	branchName := fmt.Sprintf("%s%s", cfg.BranchPrefix, sessionName)
	// Sanitize the final branch name to handle invalid characters from any source
	// (e.g., backslashes from Windows domain usernames like DOMAIN\user)
	branchName = sanitizeBranchName(branchName)

	repoPath, worktreePath, err := resolveWorktreePaths(ctx, repoPath, branchName)
	if err != nil {
		return nil, "", err
	}

	return &GitWorktree{
		baseCtx:      ctx,
		repoPath:     repoPath,
		sessionName:  sessionName,
		branchName:   branchName,
		worktreePath: worktreePath,
		baseRef:      baseRef,
	}, branchName, nil
}

// IsExistingBranch returns whether this worktree uses a pre-existing branch
func (g *GitWorktree) IsExistingBranch() bool {
	return g.isExistingBranch
}

// GetWorktreePath returns the path to the worktree
func (g *GitWorktree) GetWorktreePath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.worktreePath
}

// GetBranchName returns the name of the branch associated with this worktree
func (g *GitWorktree) GetBranchName() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.branchName
}

// GetRepoPath returns the path to the repository
func (g *GitWorktree) GetRepoPath() string {
	return g.repoPath
}

// GetRepoName returns the name of the repository (last part of the repoPath).
func (g *GitWorktree) GetRepoName() string {
	return filepath.Base(g.repoPath)
}

// GetBaseCommitSHA returns the base commit SHA for the worktree
func (g *GitWorktree) GetBaseCommitSHA() string {
	return g.baseCommitSHA
}

// GetBaseRef returns the ref the session branch was based on ("" if branched from HEAD
// or if not persisted for a legacy session).
func (g *GitWorktree) GetBaseRef() string {
	return g.baseRef
}

// baseContext returns the lifecycle context subprocesses derive from,
// defaulting to Background for worktrees constructed without one.
func (g *GitWorktree) baseContext() context.Context {
	if g.baseCtx != nil {
		return g.baseCtx
	}
	return context.Background()
}
