// Package session defines Instance, Atrium's core domain object: one agent =
// one Instance, which lazily composes a tmux session and a git worktree on
// Start. An Instance's Status drives list rendering and daemon behavior, and
// instances are persisted across runs via Storage.
package session

import (
	"github.com/ZviBaratz/atrium/internal/teardown"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/session/transcript"
	"path/filepath"

	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNoWorktree is returned by GetGitWorktree for a direct (non-git) session, which has
// no worktree. Callers that need git use it to fall through to their error path instead
// of dereferencing a nil worktree.
var ErrNoWorktree = errors.New("not available for a direct (non-git) session")

// Status is an instance's lifecycle/activity state. It is persisted in
// state.json, so the variants' numeric values must stay stable (new ones are
// appended).
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

// StatusUrgency returns a session's action-priority rank for the "status" sort
// mode — lower is more urgent and sorts first. It encodes how much the session
// wants the user's attention right now: a blocked prompt outranks a finished-but-
// unseen turn, which outranks an idle session, which outranks one still working.
// unread is the caller's Instance.Unread() (only meaningful for Ready); the value
// is independent of the numeric Status constants so their serialized order can
// keep changing without disturbing this ordering.
func StatusUrgency(s Status, unread bool) int {
	switch s {
	case NeedsInput:
		return 0
	case Ready:
		if unread {
			return 1
		}
		return 2
	case Running:
		return 3
	case Loading:
		return 4
	case Paused:
		return 5
	default:
		return 6
	}
}

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
	// note is an optional freeform annotation surfaced on the session's row
	// (e.g. "blocked on review"). Like displayName it is cosmetic, mutable at
	// any time, and decoupled from the git branch / tmux session.
	note string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
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
	// Prompt is the initial prompt to pass to the instance on startup. It is held until
	// delivery is confirmed (see SendPrompt) and persisted, so a prompt queued but not yet
	// delivered survives a restart and is re-delivered on reload.
	Prompt string
	// PromptQueuedAt records when delivery of Prompt may begin (session creation, or reload
	// for a restored pending prompt). It drives the delivery timeout in promptDeliveryReady
	// so chatty-startup agents that never reach an idle pane don't stall the first message.
	PromptQueuedAt time.Time
	// promptInFlight guards against a second metadata tick dispatching the same queued
	// prompt while a send is still running. Owned by the main thread (set in
	// deliverReadyPrompts, cleared when the send's result is handled), so it never races.
	promptInFlight bool

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// prStatus stores the last fetched pull-request snapshot (number, CI, review
	// state). nil until first computed; transient and never persisted. Read in
	// View and written from the metadata loop, like diffStats.
	prStatus *git.PRStatus

	// baseBranch is the existing branch the session branch is based on (empty = base on HEAD).
	// The session always gets its own branch; baseBranch only chooses the start point.
	baseBranch string

	// direct marks a "direct session": one whose Path is not a git repository. Such a
	// session has no worktree (gitWorktree stays nil), no branch, and no diff — its agent
	// runs directly in Path. Set at construction (NewInstance) or restore (FromInstanceData)
	// and never changes afterwards, so it is read without the lock.
	//
	// Use this (via IsDirect) to test directness, not `gitWorktree == nil`: an unstarted
	// git session also has a nil worktree but is not direct. See worktree() for the full
	// nil-vs-direct distinction.
	direct bool

	// claudeAccount / claudeConfigDir / claudeAccountDefault pin the Claude Code
	// account chosen at creation. claudeConfigDir is injected into the tmux
	// session as CLAUDE_CONFIG_DIR at launch; claudeAccount is the badge label;
	// claudeAccountDefault marks the default/fallback account (dim badge). Set
	// once before Start (or restored by FromInstanceData) and never re-resolved,
	// mirroring Program — the tmux env can only be set at session birth. Read
	// without the lock (creation-fixed, like direct).
	claudeAccount        string
	claudeConfigDir      string
	claudeAccountDefault bool
	// ghConfigDir is the GH_CONFIG_DIR for this session, resolved at creation from
	// config.GHAccounts by the same remote/path routing as claudeConfigDir. It is
	// injected into the tmux session (so the agent's own `gh` and any https
	// credential-helper calls pick the right GitHub account) and onto the
	// Worktree (so Atrium's own `gh` PR subprocesses do too). "" = inherit the
	// ambient gh account. Creation-fixed and read without the lock, like the
	// claude* fields above.
	ghConfigDir string
	// githubTokenEnv are the env var names the routed gh account's token is
	// injected under at launch (config.GHAccount.TokenEnv), forwarded to the tmux
	// session. The token VALUE is resolved at session birth by the tmux layer and
	// never held here or persisted — only these names are. Creation-fixed and read
	// without the lock, like ghConfigDir.
	githubTokenEnv []string

	// modelID is the session's model per its transcript (the newest assistant
	// entry, e.g. "claude-opus-4-7"). Written only on the main thread
	// (SetModelMeta), like diffStats; persisted so paused sessions keep their
	// model chip. "" = not yet known (the UI falls back to the --model flag).
	modelID string
	// modelStamp memoizes the transcript state modelID was extracted from, so
	// the poll goroutine (ComputeModel) can skip unchanged transcripts. Read in
	// the poll goroutine, written on the main thread — serialized by the
	// non-overlapping tick chain, the same contract diffStats relies on. Any
	// second extraction call site (e.g. the daemon) would need a lock here.
	// In-memory only: the first post-restore tick re-extracts once.
	modelStamp transcript.Stamp

	// runtimeMode is the permission mode last detected from the live pane footer
	// (ComputeMode → SetModeMeta), e.g. "auto" after a plan-launched session is
	// switched in-session. Written only on the main thread (like modelID),
	// persisted so paused sessions keep the chip. "" = not yet known (the UI
	// falls back to the --permission-mode flag).
	runtimeMode string

	// baseCtx is the lifecycle context the instance's tmux/git subprocesses derive
	// from; cancelling it (app/daemon shutdown) kills in-flight subprocesses. Set via
	// SetBaseContext (or FromInstanceData) before Start, i.e. before any background
	// goroutine reaches the instance, so it is read without the lock. nil means
	// Background.
	baseCtx context.Context

	// mu guards the live-state fields below (status, started, tmuxSession, gitWorktree),
	// which the background Start() goroutine writes while the metadata-poll goroutines and
	// the UI thread read them. Always access these through the locked accessors
	// (GetStatus/SetStatus/isStarted/tmux/worktree); never hold mu across tmux/git I/O.
	mu sync.RWMutex
	// status is the status of the instance. Guarded by mu.
	status Status

	// unread marks a Ready session the user has not visited since the agent last
	// finished a turn. Set by SetStatus on a transition into Ready; cleared by
	// MarkSeen (attach or selection dwell). Persisted in state.json. Guarded by mu.
	unread bool
	// unreadAt records when unread was last flagged, so the UI can keep a fresh
	// unread visibly bright for at least the dwell duration even when its row is
	// already selected. In-memory only. Guarded by mu.
	unreadAt time.Time
	// suppressNextUnread is a one-shot guard against synthetic lifecycle
	// transitions: restore/recover/resume/detach force status to Running, and the
	// poll that follows settles to Ready without the agent having produced new
	// output. The next into-Ready transition consumes the flag without flagging
	// unread; any non-Ready SetStatus clears it (an observed working phase means
	// the following completion is genuine). Arming sites that write
	// SetStatus(Running) themselves must arm *after* that write, or the write
	// would clear the flag they just set; the post-detach arm instead precedes
	// its poll's async Running write, which is safe — that write clearing the
	// flag is exactly the observed-working rule above. In-memory only. Guarded
	// by mu.
	suppressNextUnread bool

	// The below fields are initialized upon calling Start(). Guarded by mu.

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.Session
	// gitWorktree is the git worktree for the instance.
	gitWorktree *git.Worktree

	// tmuxName is the instance's tmux session name — persisted state, not a
	// derivation. Minted repo-qualified (tmux.QualifiedSessionName) when the
	// session is first created, recorded from the legacy derivation for
	// instances restored from a state.json that predates the field, and
	// re-minted by Rename. Guarded by mu: the background Start() goroutine
	// writes it while the UI thread reads.
	tmuxName string
	// groupKey caches the repo-group key (see GroupKey): computed at most once
	// per instance, possibly via a git subprocess. Guarded by mu (never held
	// across that subprocess).
	groupKey string
	// groupKeyComputeMu serializes the cold-path GroupKey git subprocess so
	// concurrent callers run it at most once. A leaf mutex: taken only on a
	// cache miss for a non-direct, not-yet-started instance, and never nested
	// under mu, so the subprocess never blocks mu-guarded status reads.
	groupKeyComputeMu sync.Mutex
}

// repoGroupKey is the package's hook into git.RepoGroupKey for GroupKey's cold
// path. A var (mirroring git.checkGHCLI) so the dedup test can stub it to count
// cold-path invocations.
var repoGroupKey = git.RepoGroupKey

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		Title:       i.Title,
		DisplayName: i.displayName,
		Note:        i.note,
		Path:        i.Path,
		Branch:      i.Branch,
		Status:      i.GetStatus(),
		Height:      i.Height,
		Width:       i.Width,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   time.Now(),
		Program:     i.Program,
		AutoYes:     i.AutoYes,
		Unread:      i.Unread(),
		Direct:      i.direct,

		ClaudeAccount:        i.claudeAccount,
		ClaudeConfigDir:      i.claudeConfigDir,
		ClaudeAccountDefault: i.claudeAccountDefault,
		GHConfigDir:          i.ghConfigDir,
		GitHubTokenEnv:       i.githubTokenEnv,
		Model:                i.modelID,
		PermissionMode:       i.runtimeMode,
		TmuxName:             i.TmuxSessionName(),

		// Persist an undelivered prompt so it survives a restart and is re-delivered on
		// reload (a delivered prompt has already been cleared, so this is usually empty).
		Prompt:         i.Prompt,
		PromptQueuedAt: i.PromptQueuedAt,
	}

	// Only include worktree data if gitWorktree is initialized
	if wt := i.worktree(); wt != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:         wt.GetRepoPath(),
			WorktreePath:     wt.GetWorktreePath(),
			SessionName:      i.Title,
			BranchName:       wt.GetBranchName(),
			BaseCommitSHA:    wt.GetBaseCommitSHA(),
			BaseRef:          wt.GetBaseRef(),
			IsExistingBranch: wt.IsExistingBranch(),
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

// FromInstanceData rehydrates an Instance from serialized data. It is pure: it
// maps the fields, reconstructs the worktree/diff, and constructs (but does not
// launch or reattach) the tmux Session — so it spawns no subprocesses. The live
// reattach/recovery that used to run here now lives in reattach, which the caller
// (Storage.LoadInstances) invokes next. branchPrefix is the configured
// session-branch prefix, supplied by the caller so bulk restores load config once
// instead of once per instance. ctx is the lifecycle context the instance's
// tmux/git subprocesses (spawned later, by reattach) derive from.
func FromInstanceData(ctx context.Context, data InstanceData, branchPrefix string) (*Instance, error) {
	instance := &Instance{
		baseCtx:     ctx,
		Title:       data.Title,
		displayName: data.DisplayName,
		note:        data.Note,
		Path:        data.Path,
		Branch:      data.Branch,
		status:      data.Status,
		unread:      data.Unread,
		Height:      data.Height,
		Width:       data.Width,
		CreatedAt:   data.CreatedAt,
		UpdatedAt:   data.UpdatedAt,
		Program:     data.Program,
		direct:      data.Direct,

		claudeAccount:        data.ClaudeAccount,
		claudeConfigDir:      data.ClaudeConfigDir,
		claudeAccountDefault: data.ClaudeAccountDefault,
		ghConfigDir:          data.GHConfigDir,
		githubTokenEnv:       data.GitHubTokenEnv,
		modelID:              data.Model,
		runtimeMode:          data.PermissionMode,
		Prompt:               data.Prompt,
	}

	// A pending prompt restored from disk re-enters tick-driven delivery on reload. Restart
	// the delivery-timeout clock from now rather than keeping the (possibly long-past)
	// original queue time, so the timeout measures the post-restart wait, not wall-clock age.
	if instance.Prompt != "" {
		instance.PromptQueuedAt = time.Now()
	}

	// A direct session has no worktree or diff. For a git session, rehydrate both from
	// storage. Restore direct first so every downstream path (Start(false),
	// recoverInPlace) sees the nil worktree and stays on the direct branch.
	if !data.Direct {
		instance.gitWorktree = git.NewWorktreeFromStorage(
			ctx,
			data.Worktree.RepoPath,
			data.Worktree.WorktreePath,
			data.Worktree.SessionName,
			data.Worktree.BranchName,
			data.Worktree.BaseCommitSHA,
			data.Worktree.BaseRef,
			data.Worktree.IsExistingBranch,
			branchPrefix,
		)
		instance.gitWorktree.SetGHConfigDir(instance.ghConfigDir)
		instance.diffStats = &git.DiffStats{
			Added:        data.DiffStats.Added,
			Removed:      data.DiffStats.Removed,
			Content:      data.DiffStats.Content,
			FilesChanged: data.DiffStats.FilesChanged,
			Commits:      data.DiffStats.Commits,
			Behind:       data.DiffStats.Behind,
			Dirty:        data.DiffStats.Dirty,
		}
	}

	// The tmux session name is persisted state. A state.json that predates the
	// field decodes to "" — such a session still lives on the socket under the
	// legacy derived name, so keep deriving and record the result; it persists
	// on the next save and the session keeps its legacy name until deep-renamed.
	var sess *tmux.Session
	if data.TmuxName != "" {
		sess = tmux.NewSessionWithName(ctx, data.TmuxName, data.Title, instance.Program)
	} else {
		sess = tmux.NewSession(ctx, instance.Title, instance.Program)
	}
	sess.SetClaudeConfigDir(instance.claudeConfigDir)
	sess.SetGHConfigDir(instance.ghConfigDir)
	sess.SetGitHubTokenEnv(instance.githubTokenEnv)
	instance.tmuxName = sess.Name()
	instance.tmuxSession = sess

	return instance, nil
}

// reattach brings a rehydrated instance online: it reattaches to a surviving tmux
// session, or recovers in place when that session is wedged or gone. This is the
// live tmux/git IO deliberately kept out of FromInstanceData, so a caller can
// rehydrate an instance without side effects and reattach as a separate step
// (Storage.LoadInstances does both in sequence). A paused instance has no live
// session to reattach — its Session was constructed for a later Resume — so it is
// only marked started.
//
// Precondition: reattach must run during load, before the instance is published to
// the poll loop. It reads tmuxSession and — via the paused branch and
// recoverInPlace — writes started without holding i.mu, which is safe only in that
// single-threaded, pre-publication window.
func (i *Instance) reattach() {
	if i.Paused() {
		i.started = true
		return
	}

	// FromInstanceData mapped the saved Status onto i.status and nothing has changed
	// it since, so it still reflects the status at save time here.
	savedStatus := i.GetStatus()
	sess := i.tmuxSession
	switch {
	case sess.DoesSessionExist():
		// Normal case: the session survived (cs detaches, it doesn't kill), so
		// reattach to it. If the attach (Restore) fails the session is wedged — kill
		// it and recover in place rather than aborting the load of every other
		// session. Start() no longer sets Running itself (that is owned by the
		// caller), so mark a successfully-reattached session Running here;
		// recoverInPlace sets its own status otherwise.
		if err := i.Start(false); err != nil {
			log.ErrorLog.Printf("failed to restore session %s, recovering: %v", i.Title, err)
			if closeErr := sess.Close(); closeErr != nil {
				log.ErrorLog.Printf("failed to close stale session %s: %v", i.Title, closeErr)
			}
			i.recoverInPlace()
		} else {
			i.SetStatus(Running)
			// The Running just written is synthetic (the session was reattached, not
			// observed working), so the first poll's settle to Ready must not flag
			// unread when the session was already idle at save time. A persisted
			// Running means the agent was genuinely working when the app closed — its
			// first Ready is a real completion, so don't arm.
			if savedStatus == Ready {
				i.ArmReadySuppression()
			}
		}
	default:
		// The tmux session is gone — e.g. after a reboot, or the one-time migration
		// to cs's dedicated socket. Don't crash on the failed attach (which
		// previously aborted startup); recover in place.
		i.recoverInPlace()
	}
}

// startResuming relaunches the dead agent in workDir, resuming its prior conversation
// only when one actually exists. It blocks resume *only* when the agent's transcript is
// locatable (claude) AND no session record exists for workDir — the exact case where
// `claude --continue` aborts with "No conversation found to continue!", killing the pane
// and bouncing the session straight back to Paused. Agents without a native-transcript
// adapter (codex/gemini) report supported == false and defer to their own resume probe in
// tmux.resumeCommand, so their behavior is unchanged.
func (i *Instance) startResuming(ts *tmux.Session, workDir string) error {
	resumable, supported := transcript.HasResumable(i.Program, workDir, transcript.Options{Root: i.claudeConfigDir})
	if supported && !resumable {
		return ts.Start(workDir)
	}
	return ts.StartContinue(workDir)
}

// recoverInPlace brings a loaded instance back online after its tmux session
// could not be restored (the session was wedged, or gone entirely). If the
// worktree is intact it recreates the session in place, resuming the agent's
// prior conversation when one exists (startResuming; a fresh start otherwise) and
// marks the instance Running. If the worktree is gone, or the restart fails, it
// degrades to Paused so the branch is preserved and Resume can recover it
// later — a single bad session must never abort loading the rest.
//
// Recreating in place (rather than via Resume) deliberately preserves any
// uncommitted work: Resume would force-recreate the worktree and lose it.
func (i *Instance) recoverInPlace() {
	i.started = true

	wt := i.worktree()
	if wt == nil {
		// Direct session: no worktree to validate. Restart the agent in the real
		// directory; on failure leave it Paused so the user can Resume later.
		if err := i.startResuming(i.tmuxSession, i.Path); err != nil {
			log.ErrorLog.Printf("failed to restart direct session %s in place, leaving paused: %v", i.Title, err)
			i.SetStatus(Paused)
			return
		}
		i.SetStatus(Running)
		// The restarted agent's post-boot idle is a boot artifact, not new output;
		// don't let the first poll's settle to Ready flag unread.
		i.ArmReadySuppression()
		return
	}

	valid, err := wt.IsValidWorktree()
	if err != nil {
		log.ErrorLog.Printf("failed to validate worktree for %s, leaving paused: %v", i.Title, err)
	}
	if err != nil || !valid {
		i.SetStatus(Paused)
		return
	}

	if err := i.startResuming(i.tmuxSession, wt.GetWorktreePath()); err != nil {
		log.ErrorLog.Printf("failed to restart session %s in place, leaving paused: %v", i.Title, err)
		i.SetStatus(Paused)
		return
	}
	i.SetStatus(Running)
	// As above: the post-boot idle settle is not a genuine completion.
	i.ArmReadySuppression()
}

// worktreeCleanup is the seam recreateSession tears the worktree down through on a
// failed (re)launch. A package-level var — matching the git package's own test-seam
// idiom (checkGHCLI/runGitPush/runGHBrowse) — so a test can inject a failing teardown
// and assert the error is wrapped; production always uses (*git.Worktree).Cleanup.
var worktreeCleanup = (*git.Worktree).Cleanup

// recreateSession starts a fresh tmux session for an already-set-up worktree,
// resuming the agent's prior conversation when one exists (startResuming; a fresh
// start otherwise). On failure it tears down the worktree and returns a wrapped
// error. Callers must ensure no session with the same name still exists — Start
// guards against duplicates — so a stale session has to be closed first.
func (i *Instance) recreateSession() error {
	ts := i.tmux()
	wt := i.worktree()
	if err := i.startResuming(ts, i.WorkingDir()); err != nil {
		log.ErrorLog.Print(err)
		// Cleanup git worktree if tmux session creation fails. A direct session has no
		// worktree (wt == nil) and nothing to clean up.
		if wt != nil {
			if cleanupErr := worktreeCleanup(wt); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
		}
		return fmt.Errorf("failed to start new session: %w", err)
	}
	return nil
}

// InstanceOptions are the options for creating a new instance.
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// Branch is an existing branch name to start the session on (empty = new branch from HEAD)
	Branch string
	// Direct creates a direct (non-git) session: the agent runs in Path with no worktree,
	// branch, or diff. Set when Path is not a git repository.
	Direct bool
}

// NewInstance creates a not-yet-started Instance from opts. The tmux session
// and git worktree are only created later, by Start.
func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// Convert path to absolute
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return &Instance{
		Title:      opts.Title,
		status:     Ready,
		Path:       absPath,
		Program:    opts.Program,
		Height:     0,
		Width:      0,
		CreatedAt:  t,
		UpdatedAt:  t,
		baseBranch: opts.Branch,
		direct:     opts.Direct,
	}, nil
}

// RepoName returns the name the instance is grouped under in the list: the git
// repo name for worktree sessions, or the directory base name for direct
// (non-git) sessions.
func (i *Instance) RepoName() (string, error) {
	if !i.isStarted() {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	wt := i.worktree()
	if wt == nil {
		// Direct session: no git repo. Group it by its directory name.
		return filepath.Base(i.Path), nil
	}
	return wt.GetRepoName(), nil
}

// TmuxSessionName returns the instance's persisted tmux session name, or ""
// for an instance that has never been started or restored (the name is minted
// on first Start).
func (i *Instance) TmuxSessionName() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.tmuxName
}

// GroupKey returns the repo-group key the session list files this instance
// under: the repo name for worktree sessions, the directory base name for
// direct ones. Unlike RepoName it also works before Start — resolving the repo
// root from Path — so a just-added Loading instance lands in (and is
// duplicate-checked against) the same group it will join once started. The
// result is computed at most once and cached; mu is never held across the git
// subprocess the cold path may run.
func (i *Instance) GroupKey() string {
	i.mu.RLock()
	cached := i.groupKey
	wt := i.gitWorktree
	direct := i.direct
	i.mu.RUnlock()
	if cached != "" {
		return cached
	}

	// Cheap branches: no subprocess, so the worst a concurrent miss costs is a
	// redundant basename/GetRepoName — not worth serializing.
	switch {
	case wt != nil:
		return i.cacheGroupKey(wt.GetRepoName())
	case direct:
		return i.cacheGroupKey(filepath.Base(i.Path))
	}

	// Cold path: a git subprocess. Serialize on a leaf mutex (never under mu, so
	// the subprocess never blocks status reads) and re-check the cache after
	// acquiring it — a prior holder may have just populated it, collapsing N
	// concurrent callers to a single RepoGroupKey run.
	i.groupKeyComputeMu.Lock()
	defer i.groupKeyComputeMu.Unlock()
	i.mu.RLock()
	cached = i.groupKey
	i.mu.RUnlock()
	if cached != "" {
		return cached
	}
	return i.cacheGroupKey(repoGroupKey(i.baseContext(), i.Path))
}

// cacheGroupKey stores key as the resolved group key under mu and returns it.
// SetPath can clear the cache so a re-pointed instance recomputes.
func (i *Instance) cacheGroupKey(key string) string {
	i.mu.Lock()
	i.groupKey = key
	i.mu.Unlock()
	return key
}

// SetPath sets the repo path for a not-yet-started instance, resolving it to an
// absolute path (mirroring NewInstance). The worktree is created from this path on
// Start, so it must be called before the instance is started.
func (i *Instance) SetPath(path string) error {
	if i.isStarted() {
		return fmt.Errorf("cannot change path after instance has started")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	i.Path = absPath
	// The group key derives from Path; drop a value cached against the old one.
	i.mu.Lock()
	i.groupKey = ""
	i.mu.Unlock()
	return nil
}

// isStarted reports whether Start() has completed, under the read lock.
func (i *Instance) isStarted() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.started
}

// tmux returns the tmux session pointer under the read lock. Callers invoke methods
// on the returned session outside the lock (Session guards its own fields), so
// mu is never held across tmux I/O.
func (i *Instance) tmux() *tmux.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.tmuxSession
}

// worktree returns the git worktree pointer under the read lock. As with tmux(),
// callers run git I/O on the returned worktree outside the lock.
//
// It is nil in exactly two situations: a direct (non-git) session — which never has a
// worktree (see IsDirect) — and a git session before Start has created one. It is NOT
// nil for a paused git session: pause() removes the on-disk worktree directory but
// leaves this pointer intact (and restore rehydrates it from storage), so a paused git
// session still reports worktree() != nil. Consequently `worktree() == nil` is broader
// than IsDirect(): they coincide for every started session, but for an unstarted git
// session worktree() is nil while IsDirect() is false. Test directness with IsDirect();
// use a `worktree() == nil` check only as a nil guard before dereferencing the pointer.
func (i *Instance) worktree() *git.Worktree {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.gitWorktree
}

// IsDirect reports whether this is a direct (non-git) session: one whose Path is not a
// git repository, so it has no worktree, branch, or diff and its agent runs in Path.
func (i *Instance) IsDirect() bool {
	return i.direct
}

// operableGitSession reports whether the instance is a started, non-paused git session
// with a live worktree pointer — i.e. one it is safe to run diff/PR git I/O against.
// It is false for an unstarted, paused, or direct session. This names the intent of the
// otherwise-opaque `!i.isStarted() || i.Paused() || worktree() == nil` guard so callers
// don't conflate "not operable right now" with "is a direct session" (see worktree()).
func (i *Instance) operableGitSession() bool {
	return i.isStarted() && !i.Paused() && i.worktree() != nil
}

// WorkingDir is the directory the agent's tmux session runs in: the isolated worktree
// path for a git session, or Path itself for a direct session (no worktree). The UI
// (e.g. the terminal pane) uses it to host shells in the same cwd as the agent.
func (i *Instance) WorkingDir() string {
	if wt := i.worktree(); wt != nil {
		return wt.GetWorktreePath()
	}
	return i.Path
}

// SetBaseBranch sets the existing branch the session branch will be based on when the
// instance starts. The session still gets its own branch; this only sets the start point.
func (i *Instance) SetBaseBranch(branch string) {
	i.baseBranch = branch
}

// SetBaseContext sets the lifecycle context the instance's tmux/git subprocesses
// derive from (cancelled on app/daemon shutdown). It must be called before Start,
// which constructs the tmux session and git worktree under it.
func (i *Instance) SetBaseContext(ctx context.Context) {
	i.baseCtx = ctx
}

// baseContext returns the lifecycle context subprocesses derive from, defaulting
// to Background for instances constructed without one.
func (i *Instance) baseContext() context.Context {
	if i.baseCtx != nil {
		return i.baseCtx
	}
	return context.Background()
}

// Start brings the instance to life: it creates (or reuses) the tmux session
// and, for non-direct sessions, the git worktree and branch. firstTimeSetup is
// true if this is a new instance; otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	// Create the worktree before the tmux session: the qualified tmux name needs
	// the repo group, which is only certain once the worktree has resolved the
	// repo root.
	if firstTimeSetup && !i.direct {
		// The session always gets its own branch. baseBranch (if set) only chooses the start
		// point it branches off, so i.Branch is the session branch in both cases.
		var gitWorktree *git.Worktree
		var branchName string
		var err error
		if i.baseBranch != "" {
			gitWorktree, branchName, err = git.NewWorktreeFromBase(i.baseContext(), i.Path, i.Title, i.baseBranch)
		} else {
			gitWorktree, branchName, err = git.NewWorktree(i.baseContext(), i.Path, i.Title)
		}
		if err != nil {
			return fmt.Errorf("failed to create git worktree: %w", err)
		}
		// Pin the gh account before publishing the worktree to other goroutines, so
		// the write happens-before any poll-loop read behind i.mu.
		gitWorktree.SetGHConfigDir(i.ghConfigDir)
		i.mu.Lock()
		i.gitWorktree = gitWorktree
		i.mu.Unlock()
		i.Branch = branchName
	}

	i.mu.RLock()
	existing := i.tmuxSession
	i.mu.RUnlock()
	tmuxSession := existing
	if tmuxSession == nil {
		// Mint the session's persisted tmux name: repo-qualified so identical
		// titles in different repo groups never collide on the shared socket.
		// (Restored instances arrive with tmuxSession already injected by
		// FromInstanceData, so they never reach this branch.)
		name := i.TmuxSessionName()
		if name == "" {
			name = tmux.QualifiedSessionName(i.GroupKey(), i.Title)
		}
		tmuxSession = tmux.NewSessionWithName(i.baseContext(), name, i.Title, i.Program)
		tmuxSession.SetClaudeConfigDir(i.claudeConfigDir)
		tmuxSession.SetGHConfigDir(i.ghConfigDir)
		tmuxSession.SetGitHubTokenEnv(i.githubTokenEnv)
	}
	i.mu.Lock()
	i.tmuxSession = tmuxSession
	i.tmuxName = tmuxSession.Name()
	i.mu.Unlock()

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%w (cleanup error: %w)", setupErr, cleanupErr)
			}
		} else {
			i.mu.Lock()
			i.started = true
			i.mu.Unlock()
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session
		if err := tmuxSession.Restore(); err != nil {
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
	} else {
		// Setup git worktree first. wt is the worktree this goroutine just stored above.
		// For a direct session wt is nil: there is nothing to set up, and tmux runs in Path.
		wt := i.worktree()
		if wt != nil {
			if err := wt.Setup(); err != nil {
				setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
				return setupErr
			}
		}

		// Create new session
		if err := tmuxSession.Start(i.WorkingDir()); err != nil {
			// Cleanup git worktree if tmux session creation fails
			if wt != nil {
				if cleanupErr := wt.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
				}
			}
			setupErr = fmt.Errorf("failed to start new session: %w", err)
			return setupErr
		}
	}

	// NOTE: the transition out of Loading is owned by the caller on the main thread,
	// not set here from the background start goroutine, so it can never race with the
	// UI/poll readers. The new-session flow sets Running in the instanceStartedMsg
	// handler; the reattach path (reattach) sets it after Start(false) returns.

	return nil
}

// Kill terminates the instance and cleans up all resources. It is safe to call at
// any point in an instance's lifecycle — including from Start()'s error unwind,
// before started is set, and on a never-started instance — because it only acts on
// the resources that actually exist: the tmux()/worktree() nil checks below no-op
// when a resource was never allocated. It must NOT gate on isStarted(): a failed
// Start() leaves started false yet may already have created the worktree/branch
// (and a partial tmux session), which an early return would leak.
func (i *Instance) Kill() error {
	var tc teardown.Errors

	// Always try to cleanup both resources, even if one fails.
	// Close and Cleanup are themselves teardown paths that log their own
	// failures, so Wrap (not Record) adds return context without re-logging.
	// Clean up tmux session first since it's using the git worktree
	if ts := i.tmux(); ts != nil {
		tc.Wrap("close tmux session", ts.Close())
	}

	// Then clean up git worktree
	if wt := i.worktree(); wt != nil {
		tc.Wrap("cleanup git worktree", wt.Cleanup())
	}

	return tc.Err()
}

// Preview captures the instance's current tmux pane content for the preview
// tab. It returns empty content (not an error) for paused instances and for
// sessions whose tmux pane is missing, so a dead pane degrades gracefully
// instead of escalating to the error box on every refresh.
func (i *Instance) Preview() (string, error) {
	if i.Paused() {
		return "", nil
	}
	// Capture based on whether the tmux session actually exists, not the in-memory
	// `started` flag. A brief window of stale `started` (mid-start, or a missed lifecycle
	// write) must not blank the preview or pin the setup splash while the pane is genuinely
	// live — UpdateContent decides what to show from the captured content.
	//
	// A started session whose tmux pane has died (server restart, the agent process exited,
	// an external kill) would otherwise fail capture every refresh and escalate to the error
	// box. Treat a missing session as empty; the metadata loop detects it via Poll's PaneDead
	// and recovers the instance to Paused.
	ts := i.tmux()
	if ts == nil || !ts.DoesSessionExist() {
		return "", nil
	}
	return ts.CapturePaneContent()
}

// Poll classifies the agent's current pane state. Returns PaneUnknown for a not-yet-started
// instance so callers leave its status untouched.
func (i *Instance) Poll() tmux.PaneState {
	ts := i.tmux()
	if !i.isStarted() || ts == nil {
		return tmux.PaneUnknown
	}
	return ts.Poll()
}

// PollNow classifies the agent's current pane state at face value, skipping the working→idle
// hysteresis, for a one-shot refresh after the poll stream was interrupted (a detach). See
// tmux.Session.PollNow.
func (i *Instance) PollNow() tmux.PaneState {
	ts := i.tmux()
	if !i.isStarted() || ts == nil {
		return tmux.PaneUnknown
	}
	return ts.PollNow()
}

// ApplyPaneState maps a polled pane state onto this instance's status and runs the
// prompt side effects. It returns whether it tapped Enter on an auto-answerable prompt,
// so callers that want to refresh derived state (e.g. the daemon's diff stats) can key
// off it without re-deciding which states auto-answer.
//
// Prompt handling depends on AutoYes: with it on, auto-answer (tap Enter); with it off,
// the session is blocked on the user, so surface NeedsInput. PanePromptManual surfaces
// NeedsInput even under AutoYes — its auto-answer is destructive (claude's plan approval:
// Enter accepts the plan AND enables auto-accept). PaneUnknown (an unreadable or
// not-yet-started pane) and PaneDead (the session is gone) both leave the status
// untouched: a dead session is recovered to Paused separately, debounced by the
// metadata loop's recoverLostInstances, not from here.
func (i *Instance) ApplyPaneState(state tmux.PaneState) (tapped bool) {
	switch state {
	case tmux.PaneWorking:
		i.SetStatus(Running)
	case tmux.PanePrompt:
		if i.AutoYes {
			i.TapEnter()
			return true
		}
		i.SetStatus(NeedsInput)
	case tmux.PanePromptManual:
		i.SetStatus(NeedsInput)
	case tmux.PaneIdle:
		i.SetStatus(Ready)
	case tmux.PaneUnknown, tmux.PaneDead:
	}
	return false
}

// CheckAndHandleTrustPrompt checks for and dismisses the startup gate for programs that
// have one. The adapter guard skips the pane capture entirely for agents with no known
// gates, where there is nothing to dismiss.
func (i *Instance) CheckAndHandleTrustPrompt() bool {
	ts := i.tmux()
	if !i.isStarted() || ts == nil {
		return false
	}
	if len(agent.Resolve(i.Program).Gates) == 0 {
		return false
	}
	return ts.CheckAndHandleTrustPrompt()
}

// IsReadyForPrompt reports whether the agent has finished booting and is past any
// startup gate, so a queued initial prompt can be submitted into its input box.
func (i *Instance) IsReadyForPrompt() bool {
	ts := i.tmux()
	if !i.isStarted() || ts == nil {
		return false
	}
	return ts.IsReadyForPrompt()
}

// AwaitingInput reports whether the agent is rendered with its live input box on screen
// and no startup gate or blocking prompt up — i.e. keystrokes typed now would land in the
// composer. It is the positive readiness signal that gates queued-prompt delivery, stronger
// than IsReadyForPrompt: it additionally confirms the box is present, so a pre-box boot
// frame or a not-yet-painted startup screen that is briefly idle-looking can't be mistaken
// for readiness. Menu-style gates (claude's trust/new-MCP screens render a "❯ 1. …" selector
// that looks like a box) are still excluded by the gate/prompt checks, not by the box check;
// see Session.AwaitingInput.
func (i *Instance) AwaitingInput() bool {
	ts := i.tmux()
	if !i.isStarted() || ts == nil {
		return false
	}
	return ts.AwaitingInput()
}

// MarkPromptSending / PromptSending / ClearPromptSending manage the in-flight guard that
// keeps overlapping metadata ticks from dispatching the same queued prompt twice. All three
// are called only from the main event loop, so the unguarded field never races.

// MarkPromptSending raises the in-flight guard before a queued prompt is dispatched.
func (i *Instance) MarkPromptSending() { i.promptInFlight = true }

// PromptSending reports whether a queued prompt is currently in flight.
func (i *Instance) PromptSending() bool { return i.promptInFlight }

// ClearPromptSending lowers the in-flight guard once a prompt dispatch has settled.
func (i *Instance) ClearPromptSending() { i.promptInFlight = false }

// ClearPrompt retires a queued prompt: it drops the pending text, its timeout clock, and
// the in-flight guard. Called from the main loop once delivery is confirmed (or definitively
// abandoned), so a delivered prompt is never re-sent and stops being a poll target.
func (i *Instance) ClearPrompt() {
	i.Prompt = ""
	i.PromptQueuedAt = time.Time{}
	i.promptInFlight = false
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
func (i *Instance) TapEnter() {
	if !i.isStarted() || !i.AutoYes {
		return
	}
	if err := i.tmux().TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

// ApprovePrompt sends a single Enter to the agent pane to answer a visible
// prompt (tool permission, plan approval) on the user's behalf. Unlike
// TapEnter — the self-gating autoyes path — this is user-initiated, so it
// ignores AutoYes and returns errors instead of logging them. It deliberately
// answers PanePromptManual prompts too: a human keypress is exactly the
// manual confirmation the autoyes NoAutoTap guard preserves. Note that Enter
// selects whatever option the dialog has highlighted — on claude's plan
// dialog the default both accepts the plan and enables auto-accept edits.
func (i *Instance) ApprovePrompt() error {
	ts := i.tmux()
	if !i.isStarted() || i.Paused() || ts == nil {
		return fmt.Errorf("session is not running")
	}
	if err := ts.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}
	return nil
}

// AcceptSuggestion accepts the agent's ghost-text prompt suggestion in the
// idle input box, without attaching: Right (accept) then Enter (send). The
// detection gate lives in the tmux layer on a fresh raw capture
// (tmux.Session.AcceptSuggestion); accepted reports whether anything was
// actually sent, so the caller can distinguish "sent" from "nothing to
// accept" — a normal outcome (non-claude agent, no suggestion showing) that
// must not be treated as an error. Like ApprovePrompt it is user-initiated
// and ignores AutoYes; the autoyes daemon deliberately never calls it.
func (i *Instance) AcceptSuggestion() (accepted bool, err error) {
	ts := i.tmux()
	if !i.isStarted() || i.Paused() || ts == nil {
		return false, fmt.Errorf("session is not running")
	}
	return ts.AcceptSuggestion()
}

// Attach attaches the user's terminal to the instance's tmux session. The
// returned channel closes when the user detaches; consult AttachExitReason and
// AttachKillRequested afterwards for why.
func (i *Instance) Attach() (chan struct{}, error) {
	if !i.isStarted() {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	return i.tmux().Attach(true)
}

// AttachKillRequested reports whether the user pressed the in-session kill key
// (Ctrl+X) during the most recent attach. The app reads this right after the
// attach returns to decide whether to run the kill-confirmation flow.
func (i *Instance) AttachKillRequested() bool {
	ts := i.tmux()
	return ts != nil && ts.KillRequested()
}

// AttachExitReason reports why the most recent Attach ended (a normal detach vs a
// request to cycle to the next/previous sibling session). Meaningful only after the
// channel returned by Attach has closed. A not-yet-started instance never attaches,
// so it reports the default DetachQuit.
func (i *Instance) AttachExitReason() tmux.DetachReason {
	ts := i.tmux()
	if ts == nil {
		return tmux.DetachQuit
	}
	return ts.AttachExitReason()
}

// AttachExitError reports any error encountered while tearing down the most recent
// attach (a failed pty close or Restore). Meaningful only after the channel returned
// by Attach has closed; nil for a clean detach or a not-yet-started instance.
func (i *Instance) AttachExitError() error {
	ts := i.tmux()
	if ts == nil {
		return nil
	}
	return ts.AttachExitError()
}

// SetContext pushes the in-session context-bar strings to the instance's tmux
// session (see tmux.SetContext). It is a no-op for an instance with no live tmux
// session, since there is nothing to render a bar in.
func (i *Instance) SetContext(name, left string) error {
	ts := i.tmux()
	if ts == nil {
		return nil
	}
	return ts.SetContext(name, left)
}

// SetPreviewSize resizes the detached tmux session to match the preview pane,
// so captured content wraps the way it will be displayed. Fails for an
// unstarted or paused instance.
func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.isStarted() || i.Paused() {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmux().SetDetachedSize(width, height)
}

// GetGitWorktree returns the git worktree for the instance
func (i *Instance) GetGitWorktree() (*git.Worktree, error) {
	if !i.isStarted() {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	wt := i.worktree()
	if wt == nil {
		// Direct session: no worktree. Return an error so git-dependent callers take
		// their error path instead of dereferencing nil.
		return nil, ErrNoWorktree
	}
	return wt, nil
}

// GetWorktreePath returns the worktree path for the instance, or empty string if unavailable.
//
// Unlike GetGitWorktree this is deliberately not isStarted-guarded, so it can be called on
// an unstarted git session whose worktree pointer is still nil. The `wt == nil` test is a
// nil guard, not a directness test — keep it (do not swap in IsDirect, which would be false
// for that unstarted git session and let the nil pointer through to a panic). See worktree().
func (i *Instance) GetWorktreePath() string {
	wt := i.worktree()
	if wt == nil {
		return ""
	}
	return wt.GetWorktreePath()
}

// GetRepoPath returns the git repository root for the instance, or empty string if unavailable.
// As with GetWorktreePath, the `wt == nil` check is a nil guard (it also covers an unstarted
// git session), not an IsDirect test — see worktree().
func (i *Instance) GetRepoPath() string {
	wt := i.worktree()
	if wt == nil {
		return ""
	}
	return wt.GetRepoPath()
}

// Started reports whether Start has run (the instance has a tmux session and,
// unless direct, a worktree).
func (i *Instance) Started() bool {
	return i.isStarted()
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.isStarted() {
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
	if !i.isStarted() {
		return fmt.Errorf("cannot deep-rename an instance that has not been started")
	}

	oldTitle := i.Title
	ts := i.tmux()
	wt := i.worktree()

	// Mint the qualified replacement name. This is also the migration point: a
	// session restored under a legacy (unqualified) name adopts a repo-qualified
	// one on its first deep rename. The old name comes from the session itself
	// so rollback is exact even for instances that predate persisted names.
	oldName := ts.Name()
	newName := tmux.QualifiedSessionName(i.GroupKey(), newTitle)

	// 1. Rename the tmux session first: atomic and exactly reversible by name.
	if err := ts.Rename(newTitle, newName); err != nil {
		return fmt.Errorf("failed to rename tmux session: %w", err)
	}

	// 2. Rename the git branch + move the worktree. On failure (incl. its own internal
	// rollback of a half-done branch rename), roll the tmux session back to its old name.
	// A direct session has no worktree, so only the tmux rename (step 1) applies.
	if wt != nil {
		if err := wt.Rename(newTitle); err != nil {
			if rbErr := ts.Rename(oldTitle, oldName); rbErr != nil {
				log.ErrorLog.Printf("failed to roll back tmux rename %q->%q: %v", newTitle, oldTitle, rbErr)
			}
			return fmt.Errorf("failed to rename git worktree: %w", err)
		}
		i.Branch = wt.GetBranchName()
	}

	// 3. Adopt the corrected identity.
	i.Title = newTitle
	i.mu.Lock()
	i.tmuxName = newName
	i.mu.Unlock()
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

// Note returns the freeform annotation shown on the session's row, or "" when unset.
func (i *Instance) Note() string { return i.note }

// SetNote sets the freeform annotation. Whitespace is trimmed; an empty value clears it.
// Like SetDisplayName it works at any time and is independent of the git branch and tmux
// session.
func (i *Instance) SetNote(note string) { i.note = strings.TrimSpace(note) }

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.isStarted() || i.Paused() {
		return "", nil
	}
	return i.tmux().CapturePaneContentWithOptions("-", "-")
}

// ScrollbackSource identifies where scroll-mode content came from, so the UI
// can label the snapshot accordingly.
type ScrollbackSource int

const (
	// ScrollbackTmux is the tmux full-history capture (PreviewFullHistory).
	ScrollbackTmux ScrollbackSource = iota
	// ScrollbackTranscript is the agent program's own session transcript.
	ScrollbackTranscript
)

// ScrollbackContent returns the best available scrollback for scroll mode,
// wrapped to width. Agents that repaint the alternate screen in place (Claude
// Code) leave tmux history structurally empty, so for supported programs the
// session's own transcript is rendered instead; unsupported programs and every
// transcript failure fall back to the tmux capture — never worse than
// PreviewFullHistory alone.
func (i *Instance) ScrollbackContent(width int) (string, ScrollbackSource, error) {
	// Root honors the per-account CLAUDE_CONFIG_DIR (account-routed sessions
	// write transcripts under their own config dir); "" falls through to the
	// process env / ~/.claude.
	text, err := transcript.Render(i.Program, i.WorkingDir(), transcript.Options{Width: width, Root: i.claudeConfigDir})
	if err == nil {
		return text, ScrollbackTranscript, nil
	}
	if !errors.Is(err, transcript.ErrUnsupported) {
		// A supported program whose transcript is unavailable (not written yet,
		// unreadable, …): degrade silently to the tmux capture.
		log.InfoLog.Printf("transcript fallback to tmux capture for %q: %v", i.Title, err)
	}
	content, terr := i.PreviewFullHistory()
	return content, ScrollbackTmux, terr
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.isStarted() || i.Paused() {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmux().SendKeys(keys)
}
