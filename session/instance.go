package session

import (
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"path/filepath"

	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

type Status int

const (
	// Running is the status when the instance is running and claude is working.
	Running Status = iota
	// Ready is if the claude instance is ready to be interacted with (waiting for user input).
	Ready
	// Loading is if the instance is loading (if we are starting it up or something).
	Loading
	// Paused is if the instance is paused (worktree removed but branch preserved).
	Paused
	// NeedsInput is if the agent is blocked on a prompt awaiting the user's answer
	// (a tool-permission y/n prompt with AutoYes off). Appended last so previously
	// serialized Status values keep their meaning.
	NeedsInput
)

// Instance is a running instance of claude code.
type Instance struct {
	// Title is the title of the instance. It is the stable identifier used as the storage
	// key and to seed the git branch and tmux session names at creation, so it never changes
	// once the instance has started.
	Title string
	// displayName is an optional, purely cosmetic label shown in the list in place of Title.
	// Unlike Title it can be changed at any time because it is decoupled from the git branch,
	// worktree, and tmux session. Empty means "show Title".
	displayName string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
	// Status is the status of the instance.
	Status Status
	// Program is the program to run in the instance.
	Program string
	// Height is the height of the instance.
	Height int
	// Width is the width of the instance.
	Width int
	// CreatedAt is the time the instance was created.
	CreatedAt time.Time
	// UpdatedAt is the time the instance was last updated.
	UpdatedAt time.Time
	// AutoYes is true if the instance should automatically press enter when prompted.
	AutoYes bool
	// Prompt is the initial prompt to pass to the instance on startup
	Prompt string

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// baseBranch is the existing branch the session branch is based on (empty = base on HEAD).
	// The session always gets its own branch; baseBranch only chooses the start point.
	baseBranch string

	// The below fields are initialized upon calling Start().

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.TmuxSession
	// gitWorktree is the git worktree for the instance.
	gitWorktree *git.GitWorktree
}

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		Title:       i.Title,
		DisplayName: i.displayName,
		Path:        i.Path,
		Branch:      i.Branch,
		Status:      i.Status,
		Height:      i.Height,
		Width:       i.Width,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   time.Now(),
		Program:     i.Program,
		AutoYes:     i.AutoYes,
	}

	// Only include worktree data if gitWorktree is initialized
	if i.gitWorktree != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:         i.gitWorktree.GetRepoPath(),
			WorktreePath:     i.gitWorktree.GetWorktreePath(),
			SessionName:      i.Title,
			BranchName:       i.gitWorktree.GetBranchName(),
			BaseCommitSHA:    i.gitWorktree.GetBaseCommitSHA(),
			BaseRef:          i.gitWorktree.GetBaseRef(),
			IsExistingBranch: i.gitWorktree.IsExistingBranch(),
		}
	}

	// Only include diff stats if they exist
	if i.diffStats != nil {
		data.DiffStats = DiffStatsData{
			Added:        i.diffStats.Added,
			Removed:      i.diffStats.Removed,
			Content:      i.diffStats.Content,
			FilesChanged: i.diffStats.FilesChanged,
			Commits:      i.diffStats.Commits,
			Behind:       i.diffStats.Behind,
			Dirty:        i.diffStats.Dirty,
		}
	}

	return data
}

// FromInstanceData creates a new Instance from serialized data
func FromInstanceData(data InstanceData) (*Instance, error) {
	instance := &Instance{
		Title:       data.Title,
		displayName: data.DisplayName,
		Path:        data.Path,
		Branch:      data.Branch,
		Status:      data.Status,
		Height:      data.Height,
		Width:       data.Width,
		CreatedAt:   data.CreatedAt,
		UpdatedAt:   data.UpdatedAt,
		Program:     data.Program,
		gitWorktree: git.NewGitWorktreeFromStorage(
			data.Worktree.RepoPath,
			data.Worktree.WorktreePath,
			data.Worktree.SessionName,
			data.Worktree.BranchName,
			data.Worktree.BaseCommitSHA,
			data.Worktree.BaseRef,
			data.Worktree.IsExistingBranch,
		),
		diffStats: &git.DiffStats{
			Added:        data.DiffStats.Added,
			Removed:      data.DiffStats.Removed,
			Content:      data.DiffStats.Content,
			FilesChanged: data.DiffStats.FilesChanged,
			Commits:      data.DiffStats.Commits,
			Behind:       data.DiffStats.Behind,
			Dirty:        data.DiffStats.Dirty,
		},
	}

	if instance.Paused() {
		instance.started = true
		instance.tmuxSession = tmux.NewTmuxSession(instance.Title, instance.Program)
	} else {
		sess := tmux.NewTmuxSession(instance.Title, instance.Program)
		instance.tmuxSession = sess
		switch {
		case sess.DoesSessionExist():
			// Normal case: the session survived (cs detaches, it doesn't kill),
			// so reattach to it.
			if err := instance.Start(false); err != nil {
				return nil, err
			}
		default:
			// The tmux session is gone — e.g. after a reboot, or the one-time
			// migration to cs's dedicated socket. Don't crash on the failed
			// attach (which previously aborted startup). If the worktree is
			// intact, restart the session in place: this preserves uncommitted
			// work (Resume would force-recreate the worktree and lose it) and
			// keeps the instance usable. If the worktree is also gone, leave it
			// Paused so the branch is preserved and Resume can recover it.
			if valid, err := instance.gitWorktree.IsValidWorktree(); err == nil && valid {
				// The agent process died with the tmux session, so resume its prior
				// conversation rather than starting blank (no-op for non-claude agents).
				if err := sess.StartContinue(instance.gitWorktree.GetWorktreePath()); err != nil {
					return nil, err
				}
				instance.started = true
				instance.SetStatus(Running)
			} else {
				instance.started = true
				instance.SetStatus(Paused)
			}
		}
	}

	return instance, nil
}

// Options for creating a new instance
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// If AutoYes is true, then
	AutoYes bool
	// Branch is an existing branch name to start the session on (empty = new branch from HEAD)
	Branch string
}

func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// Convert path to absolute
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return &Instance{
		Title:      opts.Title,
		Status:     Ready,
		Path:       absPath,
		Program:    opts.Program,
		Height:     0,
		Width:      0,
		CreatedAt:  t,
		UpdatedAt:  t,
		AutoYes:    false,
		baseBranch: opts.Branch,
	}, nil
}

func (i *Instance) RepoName() (string, error) {
	if !i.started {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	return i.gitWorktree.GetRepoName(), nil
}

// SetPath sets the repo path for a not-yet-started instance, resolving it to an
// absolute path (mirroring NewInstance). The worktree is created from this path on
// Start, so it must be called before the instance is started.
func (i *Instance) SetPath(path string) error {
	if i.started {
		return fmt.Errorf("cannot change path after instance has started")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	i.Path = absPath
	return nil
}

func (i *Instance) SetStatus(status Status) {
	i.Status = status
}

// SetBaseBranch sets the existing branch the session branch will be based on when the
// instance starts. The session still gets its own branch; this only sets the start point.
func (i *Instance) SetBaseBranch(branch string) {
	i.baseBranch = branch
}

// firstTimeSetup is true if this is a new instance. Otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	var tmuxSession *tmux.TmuxSession
	if i.tmuxSession != nil {
		// Use existing tmux session (useful for testing)
		tmuxSession = i.tmuxSession
	} else {
		// Create new tmux session
		tmuxSession = tmux.NewTmuxSession(i.Title, i.Program)
	}
	i.tmuxSession = tmuxSession

	if firstTimeSetup {
		// The session always gets its own branch. baseBranch (if set) only chooses the start
		// point it branches off, so i.Branch is the session branch in both cases.
		var gitWorktree *git.GitWorktree
		var branchName string
		var err error
		if i.baseBranch != "" {
			gitWorktree, branchName, err = git.NewGitWorktreeFromBase(i.Path, i.Title, i.baseBranch)
		} else {
			gitWorktree, branchName, err = git.NewGitWorktree(i.Path, i.Title)
		}
		if err != nil {
			return fmt.Errorf("failed to create git worktree: %w", err)
		}
		i.gitWorktree = gitWorktree
		i.Branch = branchName
	}

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%w (cleanup error: %w)", setupErr, cleanupErr)
			}
		} else {
			i.started = true
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session
		if err := tmuxSession.Restore(); err != nil {
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
	} else {
		// Setup git worktree first
		if err := i.gitWorktree.Setup(); err != nil {
			setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
			return setupErr
		}

		// Create new session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
			}
			setupErr = fmt.Errorf("failed to start new session: %w", err)
			return setupErr
		}
	}

	i.SetStatus(Running)

	return nil
}

// Kill terminates the instance and cleans up all resources
func (i *Instance) Kill() error {
	if !i.started {
		// If instance was never started, just return success
		return nil
	}

	var errs []error

	// Always try to cleanup both resources, even if one fails
	// Clean up tmux session first since it's using the git worktree
	if i.tmuxSession != nil {
		if err := i.tmuxSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close tmux session: %w", err))
		}
	}

	// Then clean up git worktree
	if i.gitWorktree != nil {
		if err := i.gitWorktree.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup git worktree: %w", err))
		}
	}

	return i.combineErrors(errs)
}

// combineErrors combines multiple errors into a single error
func (i *Instance) combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple cleanup errors occurred:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return fmt.Errorf("%s", errMsg)
}

func (i *Instance) Preview() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	// A started session whose tmux pane has died (server restart, the agent
	// process exited, an external kill) would fail capture every refresh and
	// escalate to the error box. Treat a missing session as empty; the metadata
	// loop detects it via TmuxAlive() and recovers the instance to Paused.
	if !i.TmuxAlive() {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContent()
}

func (i *Instance) HasUpdated() (updated bool, hasPrompt bool) {
	if !i.started {
		return false, false
	}
	return i.tmuxSession.HasUpdated()
}

// Poll classifies the agent's current pane state. Returns PaneUnknown for a not-yet-started
// instance so callers leave its status untouched.
func (i *Instance) Poll() tmux.PaneState {
	if !i.started {
		return tmux.PaneUnknown
	}
	return i.tmuxSession.Poll()
}

// CheckAndHandleTrustPrompt checks for and dismisses the trust prompt for supported programs.
func (i *Instance) CheckAndHandleTrustPrompt() bool {
	if !i.started || i.tmuxSession == nil {
		return false
	}
	program := i.Program
	if !strings.HasSuffix(program, tmux.ProgramClaude) &&
		!strings.HasSuffix(program, tmux.ProgramAider) &&
		!strings.HasSuffix(program, tmux.ProgramGemini) {
		return false
	}
	return i.tmuxSession.CheckAndHandleTrustPrompt()
}

// IsReadyForPrompt reports whether the agent has finished booting and is past any
// startup gate, so a queued initial prompt can be submitted into its input box.
func (i *Instance) IsReadyForPrompt() bool {
	if !i.started || i.tmuxSession == nil {
		return false
	}
	return i.tmuxSession.IsReadyForPrompt()
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
func (i *Instance) TapEnter() {
	if !i.started || !i.AutoYes {
		return
	}
	if err := i.tmuxSession.TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

func (i *Instance) Attach() (chan struct{}, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	return i.tmuxSession.Attach()
}

func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmuxSession.SetDetachedSize(width, height)
}

// GetGitWorktree returns the git worktree for the instance
func (i *Instance) GetGitWorktree() (*git.GitWorktree, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	return i.gitWorktree, nil
}

// GetWorktreePath returns the worktree path for the instance, or empty string if unavailable
func (i *Instance) GetWorktreePath() string {
	if i.gitWorktree == nil {
		return ""
	}
	return i.gitWorktree.GetWorktreePath()
}

// GetRepoPath returns the git repository root for the instance, or empty string if unavailable
func (i *Instance) GetRepoPath() string {
	if i.gitWorktree == nil {
		return ""
	}
	return i.gitWorktree.GetRepoPath()
}

func (i *Instance) Started() bool {
	return i.started
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.started {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.Title = title
	return nil
}

// Rename performs an in-place "deep" rename of a started instance to newTitle: it renames
// the tmux session, then the git branch and worktree directory, then updates Title and the
// rendered Branch field. Unlike SetDisplayName (which only changes the cosmetic label) this
// fixes the identity everywhere it surfaces — git, GitHub/PRs, the worktree path — without
// killing the running agent. The order (tmux → git) keeps rollback exact: a git failure only
// has to undo the tmux rename (reversible by name), never a worktree move that already minted
// a fresh path. Title/Branch are written here on the main thread; no background reader touches
// them, so they need no lock (the git/tmux structs guard their own fields).
func (i *Instance) Rename(newTitle string) error {
	newTitle = strings.TrimSpace(newTitle)
	if newTitle == "" {
		return fmt.Errorf("cannot rename to an empty title")
	}
	if !i.started {
		return fmt.Errorf("cannot deep-rename an instance that has not been started")
	}

	oldTitle := i.Title

	// 1. Rename the tmux session first: atomic and exactly reversible by name.
	if err := i.tmuxSession.Rename(newTitle); err != nil {
		return fmt.Errorf("failed to rename tmux session: %w", err)
	}

	// 2. Rename the git branch + move the worktree. On failure (incl. its own internal
	// rollback of a half-done branch rename), roll the tmux session back to its old name.
	if err := i.gitWorktree.Rename(newTitle); err != nil {
		if rbErr := i.tmuxSession.Rename(oldTitle); rbErr != nil {
			log.ErrorLog.Printf("failed to roll back tmux rename %q->%q: %v", newTitle, oldTitle, rbErr)
		}
		return fmt.Errorf("failed to rename git worktree: %w", err)
	}

	// 3. Adopt the corrected identity.
	i.Title = newTitle
	i.Branch = i.gitWorktree.GetBranchName()
	return nil
}

// DisplayName returns the cosmetic label shown for the instance, falling back to Title when
// no custom label has been set.
func (i *Instance) DisplayName() string {
	if i.displayName != "" {
		return i.displayName
	}
	return i.Title
}

// SetDisplayName sets the cosmetic display label. Unlike SetTitle it works at any time
// (even after the instance has started) because the label is decoupled from the git branch
// and tmux session. Whitespace is trimmed; an empty value clears the label so the name
// reverts to Title.
func (i *Instance) SetDisplayName(name string) {
	i.displayName = strings.TrimSpace(name)
}

func (i *Instance) Paused() bool {
	return i.Status == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	return i.tmuxSession.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch.
// It copies the branch name to the clipboard so the user can check it out elsewhere.
func (i *Instance) Pause() error {
	return i.pause(true)
}

// RecoverLostSession transitions an instance whose tmux pane has died (server
// restart, agent exit, external kill) into Paused, so the metadata loop stops
// polling it and the user can bring it back with Resume. It reuses the Pause path —
// committing any uncommitted work and removing the worktree — but does not copy the
// branch to the clipboard, since the user did not initiate the transition.
func (i *Instance) RecoverLostSession() error {
	return i.pause(false)
}

// pause stops the tmux session and removes the worktree, preserving the branch.
func (i *Instance) pause(copyBranchToClipboard bool) error {
	if !i.started {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("instance is already paused")
	}

	var errs []error

	// If the worktree is orphaned (path or .git missing), git cannot operate
	// on it. Skip dirty check and Remove, prune any lingering metadata, then
	// transition to Paused so the user can recover via Resume.
	if valid, err := i.gitWorktree.IsValidWorktree(); err != nil {
		errs = append(errs, fmt.Errorf("failed to validate worktree: %w", err))
		log.ErrorLog.Print(err)
	} else if !valid {
		log.WarningLog.Printf("worktree at %s is orphaned; skipping dirty check and remove",
			i.gitWorktree.GetWorktreePath())
		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}
		// Drop any leftover directory so a future Resume's `git worktree add` won't conflict.
		if err := os.RemoveAll(i.gitWorktree.GetWorktreePath()); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove orphaned worktree directory: %w", err))
			log.ErrorLog.Print(err)
		}
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
		}
		i.SetStatus(Paused)
		if copyBranchToClipboard {
			_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
		}
		return i.combineErrors(errs)
	}

	// Check if there are any changes to commit
	if dirty, err := i.gitWorktree.IsDirty(); err != nil {
		errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
		log.ErrorLog.Print(err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub)
		commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s (paused)", i.Title, time.Now().Format(time.RFC822))
		if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
			errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
			log.ErrorLog.Print(err)
			// Return early if we can't commit changes to avoid corrupted state
			return i.combineErrors(errs)
		}
	}

	// Detach from tmux session instead of closing to preserve session output
	if err := i.tmuxSession.DetachSafely(); err != nil {
		errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
		log.ErrorLog.Print(err)
		// Continue with pause process even if detach fails
	}

	// Check if worktree exists before trying to remove it
	if _, err := os.Stat(i.gitWorktree.GetWorktreePath()); err == nil {
		// Remove worktree but keep branch
		if err := i.gitWorktree.Remove(); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove git worktree: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}

		// Only prune if remove was successful
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}
	}

	i.SetStatus(Paused)
	if copyBranchToClipboard {
		_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
	}

	if err := i.combineErrors(errs); err != nil {
		log.ErrorLog.Print(err)
		return err
	}
	return nil
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.started {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if i.Status != Paused {
		return fmt.Errorf("can only resume paused instances")
	}

	// Check if branch is checked out
	if checked, err := i.gitWorktree.IsBranchCheckedOut(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to check if branch is checked out: %w", err)
	} else if checked {
		return fmt.Errorf("cannot resume: branch is checked out, please switch to a different branch")
	}

	// Setup git worktree
	if err := i.gitWorktree.Setup(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}

	// Check if tmux session still exists from pause, otherwise create new one
	if i.tmuxSession.DoesSessionExist() {
		// Session exists, just restore PTY connection to it
		if err := i.tmuxSession.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// If restore fails, fall back to creating new session
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				log.ErrorLog.Print(err)
				// Cleanup git worktree if tmux session creation fails
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
					log.ErrorLog.Print(err)
				}
				return fmt.Errorf("failed to start new session: %w", err)
			}
		}
	} else {
		// The tmux session is gone, so the agent process died with it: resume its prior
		// conversation rather than starting blank (no-op for non-claude agents).
		if err := i.tmuxSession.StartContinue(i.gitWorktree.GetWorktreePath()); err != nil {
			log.ErrorLog.Print(err)
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
			return fmt.Errorf("failed to start new session: %w", err)
		}
	}

	i.SetStatus(Running)
	return nil
}

// UpdateDiffStats updates the git diff statistics for this instance
func (i *Instance) UpdateDiffStats() error {
	if !i.started {
		i.diffStats = nil
		return nil
	}

	if i.Status == Paused {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	stats := i.gitWorktree.Diff()
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			return nil
		}
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	return nil
}

// ComputeDiff runs the expensive git diff I/O and returns the result without
// mutating instance state. Safe to call from a background goroutine.
func (i *Instance) ComputeDiff() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.Diff()
}

// ComputeDiffNumstat runs a lightweight git diff --numstat and returns only the
// added/removed line counts (Content is left empty). Safe to call from a
// background goroutine. Use this for instances whose full diff content is not
// currently needed so we avoid keeping large diffs in memory.
func (i *Instance) ComputeDiffNumstat() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.DiffNumstat()
}

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// SendPrompt sends a prompt to the tmux session
func (i *Instance) SendPrompt(prompt string) error {
	if !i.started {
		return fmt.Errorf("instance not started")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if err := i.tmuxSession.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}

	// Brief pause to prevent carriage return from being interpreted as newline
	time.Sleep(100 * time.Millisecond)
	if err := i.tmuxSession.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}

	return nil
}

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContentWithOptions("-", "-")
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.TmuxSession) {
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmuxSession.SendKeys(keys)
}
