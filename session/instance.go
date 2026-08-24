// Package session defines Instance, Atrium's core domain object: one agent =
// one Instance, which lazily composes a tmux session and a git worktree on
// Start. An Instance's Status drives list rendering and daemon behavior, and
// instances are persisted across runs via Storage.
package session

import (
	"github.com/ZviBaratz/atrium/config"
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
	"slices"
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
	// Paused is if the instance is parked: no agent process, branch preserved. The
	// invariant is "no agent" — that is what the poll loop and the session cap read this
	// state for. Its worktree is usually removed too, because that is what Pause does,
	// but several parks leave one materialized on purpose (see Resume), so nothing may
	// infer from Paused that the directory is gone or that its contents are expendable.
	Paused
	// NeedsInput is if the agent is blocked on a prompt awaiting the user's answer
	// (a tool-permission y/n prompt with AutoYes off). Appended last so previously
	// serialized Status values keep their meaning.
	NeedsInput
	// Pending is when the main turn has ended but the agent still has background work
	// outstanding — sub-agents in flight (#290), or a background shell/monitor the turn
	// left running (tmux.PaneBackground). Either way it is not done. It must
	// render distinctly from Ready ("done, needs you"). Appended after NeedsInput so
	// previously serialized Status values keep their meaning; a restored Pending is
	// overwritten by reattach's synthetic Running and re-derived on the first poll.
	Pending
)

// String renders a Status as a short lowercase word for logs and the status-history
// diagnostic surface. It is deliberately not used for list rendering (a color-coded
// glyph carries the signal there, see ui.stateGlyph).
func (s Status) String() string {
	switch s {
	case Running:
		return "running"
	case Ready:
		return "ready"
	case Loading:
		return "loading"
	case Paused:
		return "paused"
	case NeedsInput:
		return "needs-input"
	case Pending:
		return "pending"
	default:
		return "unknown"
	}
}

// StatusTransition is one entry in an Instance's bounded status-change history: the
// status it moved From, the status it moved To, and when. Recorded by SetStatus on
// every actual change so a transient mislabel — e.g. a session that briefly rendered
// Ready while a background sub-agent was still in flight (#290) — leaves an
// inspectable trace instead of vanishing between polls.
type StatusTransition struct {
	From Status
	To   Status
	At   time.Time
}

// statusHistoryMax bounds the per-instance transition ring buffer. Only real From≠To
// moves are appended (they are already debounced by the poll classifier), so a handful
// of entries spans minutes of activity; the cap only stops a long-lived, flapping
// session from growing the slice without bound.
const statusHistoryMax = 32

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
	case Pending:
		// Still working — a sub-agent is finishing (#290), or a background shell/Monitor the
		// ended turn left running — so it wants the user's attention no more than Running
		// does: rank it alongside, not above, Running. The chip-driven producer rings a
		// turn-end of its own (ApplyPaneState), so a row the user must actually look at is
		// surfaced by the unread bit rather than by this rank.
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
	// CreateRequest is the `atrium new` spool record this session was built for, ""
	// for every other creation path (#716). It is stamped by the create drain between
	// startNewSession returning and Start actually running — both on the update
	// goroutine, and Start does not begin until Bubble Tea runs the boot command the
	// drain returns, so the write does not race the goroutine that reads it.
	//
	// See InstanceData.CreateRequest for what it is for and why it is never cleared.
	CreateRequest string
	// forkSeed, when non-nil, seeds this session's conversation from a checkpoint of
	// another one (#644). It is consumed by the first Start and deliberately not
	// persisted: SaveInstances stores only Started() instances, so a fork that never
	// completed leaves nothing in state.json to resume from — and one that did
	// completed carries the conversation in Program's --resume pin instead, which is
	// the durable form.
	forkSeed *ForkSeed
	// forkPrompt is the turn the fork's print run takes. The forked conversation is
	// materialized by that run, so it is the session's real first prompt rather than
	// a throwaway — it is asked once, headlessly, and the interactive session opens
	// on its answer.
	forkPrompt string
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
	// promptMu guards the queued-prompt state below (promptQueue, promptInFlight).
	// Writers are the main event loop — or, while a tea.Exec attach suspends it, the
	// attach keeper — never both at once; the mutex exists because the metadata tick's
	// cmd goroutines read this state off-thread (pollTargets, collectMetadata)
	// concurrently with those writes.
	promptMu sync.Mutex
	// promptQueue is the FIFO of prompts awaiting delivery to the agent. The head
	// (promptQueue[0]) is the next to deliver; it is held until delivery is confirmed
	// (see SendPrompt) and persisted, so prompts queued but not yet delivered survive a
	// restart and are re-delivered in order on reload. QueuePrompt (the initial/boot
	// prompt) and QueueFollowupPrompt (a quick-send) append; ClearPrompt pops the head.
	// Each entry's queuedAt is its delivery-timeout clock: a boot prompt carries a live
	// clock (promptDeliveryReady's 60s valve, so a chatty startup that never idles can't
	// stall the first message), while a follow-up carries a zero clock — strict
	// idle-only, so it is never force-injected into the middle of the agent's turn.
	promptQueue []queuedPrompt
	// promptInFlight guards against a second dispatcher sending the head prompt while a
	// send is still running (raised by ClaimPrompt, lowered when the send's outcome is
	// settled). It is head-scoped: while it is raised the head cannot change, since only
	// ClearPrompt pops (and only the enqueue methods append, to the tail).
	promptInFlight bool

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// diffContentAt is when the CONTENT half of diffStats (line counts, patch text)
	// was last actually computed. The branch-level half — Commits/Behind/Unpushed/
	// Dirty — is refreshed on every sweep and is not covered by this clock.
	//
	// Guarded by mu, unlike diffStats: the metadata poll reads it from its
	// per-instance background goroutine to decide whether to recompute, while the
	// main loop writes it when a result lands.
	diffContentAt time.Time

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

	// isolateDeps marks a "dependency-isolating" session: one that gets none of the
	// config's link_paths symlinks, so an `npm install` it runs cannot reach the origin
	// checkout or any sibling session (#481). Chosen at session creation, persisted, and
	// pushed onto the Worktree — which is what consults it, on every Setup including the
	// paused→resume recreation. Creation-fixed and read without the lock, like direct.
	//
	// Meaningless for a direct session, which has no worktree and never seeds at all.
	isolateDeps bool

	// claudeAccount / claudeConfigDir / claudeAccountDefault pin the Claude Code
	// account chosen at creation. claudeConfigDir is injected into the tmux
	// session as CLAUDE_CONFIG_DIR at launch; claudeAccount is the badge label;
	// claudeAccountDefault marks the default/fallback account (dim badge).
	//
	// Only claudeConfigDir is truly creation-fixed: set once before Start (or
	// restored by FromInstanceData) and never re-resolved, mirroring Program — the
	// tmux env can only be set at session birth. The other three are LABELS
	// re-derived from that dir by RestampClaudeAccount when config renames the
	// account out from under them (#470). Read without the lock: the labels are
	// written only on the Bubble Tea update goroutine (or before publication).
	claudeAccount        string
	claudeConfigDir      string
	claudeAccountDefault bool
	claudeAccountPool    string // rotation pool this session clusters under; "" = singleton/none
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

	// agyAccount / agyConfigDir pin the Antigravity CLI account chosen at creation.
	// agyConfigDir is used by bwrap to isolate ~/.gemini/antigravity-cli;
	// agyAccount is the name of the resolved route (a label, re-derived from the dir
	// by RestampAgyAccount — same split as the claude* fields above).
	agyAccount   string
	agyConfigDir string

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

	// contextUsage is how many tokens the session's conversation occupied at its
	// newest real assistant turn, plus the model of that same entry (#596).
	// Written only on the main thread (SetUsageMeta), like modelID. Deliberately
	// NOT persisted: unlike the model, a token count is a claim about a live
	// transcript that goes stale the moment the session takes another turn. Zero
	// tokens = no reading, and the chip is absent rather than showing a 0.
	//
	// What "not persisted" costs, stated rather than glossed: a RUNNING session
	// re-derives it on its first post-restore tick, but a PAUSED one never does
	// — ComputeUsage refuses paused sessions, and the poll loop does not even
	// visit them (snapshotActiveInstances excludes paused) — so after an Atrium
	// restart a paused row carries no context chip even though it still carries
	// a model chip, which modelID's persistence does buy. That asymmetry is the
	// accepted trade: a paused session is burning no context, so the number it
	// would show is one nobody can act on, and the alternative is persisting a
	// token count that could be days old and reads as current.
	contextUsage transcript.Usage
	// usageStamp memoizes the transcript state contextUsage was extracted from,
	// with the same cross-thread contract as modelStamp: read in the poll
	// goroutine (ComputeUsage), written on the main thread, serialized by the
	// non-overlapping tick chain.
	usageStamp transcript.Stamp

	// cost is the session's estimated spend across every transcript in its
	// project directory (#392), and costCursor is where its incremental reader
	// resumes. Same cross-thread contract as the pair above: written on the main
	// thread (SetCostMeta), read in the poll goroutine (ComputeCost).
	//
	// Also NOT persisted, but for the opposite reason to contextUsage. A
	// cumulative total does not go stale — it is a fact about bytes on disk, and
	// re-reading them reproduces it exactly — so the argument that retires an old
	// occupancy reading does not apply. What is not persisted is the CURSOR, and
	// the cost of that is one full re-read per session on the first tick after an
	// Atrium restart: 315ms for the largest project directory in the development
	// corpus (64.2MB), on its own poll goroutine, once. Persisting a map of byte
	// offsets to buy that back would put a correctness-critical resume point in
	// state.json, where a stale entry survives every restart; re-deriving it is
	// cheap and cannot be wrong. Revisit if the fleet ever makes the burst visible.
	cost       transcript.Cost
	costCursor transcript.CostCursor

	// endedAsking records that the last turn ended by asking the user something
	// (#571), so a queued follow-up is not delivered as the answer to a question
	// they never saw. Written only on the main thread (SetAskedMeta), like modelID.
	// Deliberately NOT persisted: it is a claim about a live transcript, and a
	// restored session re-derives it on its first post-restore tick that finds the
	// pane SETTLED (endedAskingNow only re-reads there) — which is also what keeps a
	// stale true from outliving the turn that earned it. Starting false is why the
	// gap before that tick is safe: it can only under-hold, never over-hold.
	endedAsking bool
	// askedStamp memoizes the transcript state endedAsking was derived from, with the
	// same cross-thread contract as modelStamp: read in the poll goroutine, written
	// on the main thread, serialized by the non-overlapping tick chain.
	askedStamp transcript.Stamp

	// runtimeMode is the permission mode the poll last resolved — the live pane footer,
	// else the hook record where the footer is silent (ComputeMode → SetModeMeta; see
	// tmux.Session.RuntimePermissionMode) — e.g. "auto" after a plan-launched session is
	// switched in-session. Written only on the main thread (like modelID),
	// persisted so paused sessions keep the chip. "" = not yet known (the UI
	// falls back to the --permission-mode flag).
	runtimeMode string

	// runtimeEffort is the reasoning-effort level claude's hooks last reported for
	// a resolved turn (ComputeEffort → SetEffortMeta), e.g. "low" after a
	// max-launched session is switched in-session with /effort. Written only on the
	// main thread (like runtimeMode), persisted so the chip is right on the first
	// frame after a restart. "" = not yet known (the UI falls back to the --effort
	// flag).
	runtimeEffort string

	// paneFrame is the last successfully captured tmux pane content, paneFrameAt
	// when it was captured, and paneFrameOK whether any capture has ever
	// succeeded. Written by the main loop from a background capture's result and
	// read by the View. See session/paneframe.go for the contract.
	//
	// Guarded by mu, unlike diffStats above, because these three do NOT have a
	// single writer: parking a session clears them from whichever goroutine ran the
	// pause (pause() is reached from an async action and from RecoverLostSession),
	// while the capture chain applies frames on the update thread.
	paneFrame   string
	paneFrameAt time.Time
	paneFrameOK bool
	// paneLive memos the liveness the metadata poll last observed, so the UI can
	// answer "does this session still have a pane?" without forking its own
	// has-session. paneLiveKnown distinguishes "observed dead" from "never polled".
	// Main-loop only, like the frame fields above.
	paneLive      bool
	paneLiveKnown bool

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

	// awaitingSetup marks a session sitting on a one-time startup/trust gate (PaneGate).
	// It reuses the NeedsInput status but lets the row show a "waiting on setup screen"
	// hint so the block is legible instead of looking like an ordinary prompt. Recomputed
	// every poll by ApplyPaneState (set on PaneGate, cleared on every other settled state),
	// so it is in-memory only and never serialized. Guarded by mu.
	awaitingSetup bool

	// setupPhase names what the per-repo setup script is doing right now, or "" when
	// nothing is (#389). It is the row's answer to "why has this been Loading for two
	// minutes" — a string rather than a new Status because Status values are persisted
	// and read by ~15 sites, and this describes a phase of Loading, not a state of its
	// own. In-memory only. Guarded by mu, because Start's goroutine writes it while the
	// renderer reads it.
	setupPhase string
	// setupCancel ends the setup script currently running, or is nil when none is.
	// Published alongside setupPhase and cleared with it, because the two describe the
	// same run. Guarded by mu. See AbortSetupScript for why a script needs a cancel of
	// its own rather than riding the lifecycle context alone.
	setupCancel context.CancelFunc
	// setupErr / setupOutput hold the last setup script's outcome: the error, and the
	// bounded tail of what it printed. Deliberately NOT routed through Start's error
	// return, which would tear the session down (see setupscript.go). In-memory only —
	// a failure describes one run against one materialized worktree. Guarded by mu.
	setupErr    error
	setupOutput string

	// port is the session's managed dev-server port (#389), or 0 when its repo declares
	// no port_range — or declares one that had nothing free. Unlike the setup fields
	// above it IS persisted (InstanceData.Port): a running session's server is bound to
	// this number, so a TUI restart has to re-claim it rather than hand it out again.
	// Guarded by mu; see session/port.go for the lifecycle it is tied to.
	port int
	// portProblem is the report for a session whose range had nothing free, held until
	// the app has a frame to show it on. In-memory only, and cleared as it is shown.
	// Guarded by mu.
	portProblem string

	// termName is the tmux name of the sibling session hosting the terminal tab's shell
	// (ui/terminal.go). OWNED on exactly runName's terms and for the first of its two
	// reasons: minted when the pane first creates a shell, persisted
	// (InstanceData.TermSession), and never re-derived. Deriving it meant a deep rename
	// moved the agent's session and left the shell where it was, so the pane's cache key
	// moved off a live shell — the user's terminal was silently replaced by a fresh one
	// while the old kept running, unreachable by every later reap (#708). Guarded by mu.
	//
	// runName's SECOND reason does not transfer, and must not be repeated here: an empty
	// value carries no safety meaning for the shell. The pane deliberately adopts a live
	// <name>_term it did not start, and kills one it cannot restore, because that is how a
	// shell survives a TUI restart — so it has never treated the mint as a claim of
	// ownership the way StartRunCommand does. What ownership buys the shell is the first
	// half alone: a name that a rename cannot move.
	termName string
	// runName is the tmux name of the sibling session hosting the repo's run_command
	// (#389). It is OWNED, not derived: minted when this session first starts one,
	// persisted (InstanceData.RunSession), and never re-derived.
	//
	// Both halves of that matter. Deriving it meant a deep rename left the running dev
	// server behind under the old name — orphaned, still holding a port, and no longer
	// reachable by the teardown that should have killed it. And an EMPTY value is what
	// says "this session has never hosted one", which is the only thing that stops a
	// teardown from killing a session that merely happens to be named `<us>_run` (a
	// pre-existing session titled "foo.run" sanitizes to exactly that). Guarded by mu.
	runName string
	// runSession is the cached tmux Session for that name, held only once this process
	// has STARTED or adopted it — because Close is what releases the attach pty Start
	// opened, and a Session dropped without it leaks the pty and its client. A probe
	// never populates it: a cached probe object carries the placeholder program, and
	// Start reusing one launched a bare `sh` in place of the dev server. Guarded by mu.
	runSession *tmux.Session
	// runWanted records that the user has a run command started. It IS persisted
	// (InstanceData.RunStarted), because it is what tells a restarted Atrium whether to
	// probe at all, and what tells Resume whether the pause it is undoing had stopped a
	// server. Guarded by mu.
	runWanted bool
	// runLive is the last observed liveness of that session, refreshed by the metadata
	// poll and never persisted — a claim about a process, which a restart cannot inherit.
	// Guarded by mu, unlike paneLive, because the start and stop actions write it from
	// their own goroutine while the renderer reads it.
	runLive bool
	// runGen counts local writes to the run state, so a poll observation computed before
	// one of them can be recognized as stale and discarded. Without it a metadata batch
	// already in flight when the user pressed stop re-asserted a dead server, and no
	// later tick could correct it — the probe only runs while runWanted, which that same
	// stop had just cleared. Guarded by mu.
	runGen uint64
	// runConfigured is whether this session's repository declares a run_command, as of
	// the last poll, with runConfiguredKnown marking that an answer has landed at all.
	// Re-resolved every full sweep rather than memoized for the process: the expensive
	// half is the git fork, which originURL below memoizes instead, so a config edit
	// reaches a running session. Guarded by mu.
	runConfigured      bool
	runConfiguredKnown bool
	// originURL memoizes the repository's origin remote (see originRemote), the one part
	// of routing that costs a subprocess. originURLKnown distinguishes a repo with no
	// origin from one not yet asked. Guarded by mu.
	originURL      string
	originURLKnown bool

	// conversationResumed / conversationKnown record which way the last relaunch
	// went: whether the agent came back into its prior conversation, and whether
	// startResuming could tell at all. Only that function knows — it asks the
	// transcript adapter and then elects `--continue` or a blank launch — and undo's
	// post-restore notice is the one place that has to report it. In-memory only:
	// it describes a single relaunch, not the session. Both guarded by mu, because
	// the relaunch runs off the UI thread that reads them. See ResumedConversation.
	conversationResumed bool
	conversationKnown   bool

	// lastLaunchResumed records whether the launch that started the CURRENT agent process
	// was REWRITTEN to resume: tmux.Session.StartContinue compared the command it ran
	// against the plain program and they differed. That is narrower than "carried a resume
	// flag", deliberately — a program that already pins a conversation (claude --resume
	// <id>, which session/fork.go writes) is launched unchanged and reads false here.
	//
	// It is what makes a crash-at-launch repairable: RepairResumingLaunch relaunches blank
	// only when the command that died is one a blank relaunch would actually change. Set by
	// startResuming on every relaunch and cleared by the blank relaunch it authorises; false
	// for a first Start, which never resumes. In-memory only, and guarded by mu — the
	// relaunch runs off the UI thread and the poll loop reads it from the main one.
	lastLaunchResumed bool

	// unread marks a Ready session the user has not visited since the agent last
	// finished a turn. Set by SetStatus on a transition into Ready; cleared by
	// MarkSeen (attach or selection dwell). Persisted in state.json. Guarded by mu.
	unread bool
	// muted, when set, silences all notifications for this one session (the user
	// toggles it with M). Persisted in state.json so it survives a restart. Guarded
	// by mu.
	muted bool
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

	// statusChangedAt records when status last actually changed (or was first
	// observed via the initial SetStatus). It lets the UI show how long a session
	// has held a state and gives a future reconciliation watchdog a per-status
	// wall-clock to cap against (#290). Persisted, unlike its neighbours here: it
	// round-trips through state.json (see InstanceData.StatusChangedAt), because a
	// session that has been waiting on the user for six hours must still say so
	// after a restart. Guarded by mu.
	statusChangedAt time.Time
	// pendingSource is which of Pending's two producers currently holds this row (see
	// pendingProducer), and pendingSince is when THAT hold began. Zero/pendingNone when
	// the row is not Pending.
	//
	// Neither can be derived from status or statusChangedAt, because both producers write
	// the same Status and recordStatusChange deliberately does not re-stamp when
	// from == to. A session held Pending for 40 minutes by a background chip (never
	// watchdogged, by design) therefore carries a 40-minute-old statusChangedAt; the moment
	// a real sub-agent starts, a shared stamp would fire the watchdog on the first tick of a
	// legitimate run — clearing a live in-flight set and force-committing a false Ready —
	// and the row's elapsed cue would credit the sub-agent with the chip's 40 minutes.
	// Tracking the producer measures each hold from its own start and lets the watchdog cap
	// only the set, which is the thing that can leak.
	//
	// The pair also carries the turn-end edge: a handover INTO pendingBackground is the
	// moment the agent stopped and left work running, which is what setStatusTurnEnded
	// needs and what the status alone cannot express — including the set → chip handover,
	// where the status does not change at all.
	//
	// Neither is persisted. pendingSince is seeded on restore from the persisted
	// statusChangedAt when the restored status is Pending, so the row's elapsed cue survives
	// a restart (see pendingSinceOnRestore); pendingSource is not guessed, and the first poll
	// attributes both. Guarded by mu.
	pendingSource pendingProducer
	pendingSince  time.Time
	// statusDirty marks a statusChangedAt write that has not reached state.json yet.
	// Set by recordStatusChange — i.e. by whichever goroutine observed the change —
	// and cleared once a save carrying it succeeds (see StatusDirty). In-memory only.
	// Guarded by mu.
	statusDirty bool
	// awaitingReattachSettle / reattachSavedStatus carry the status a session was
	// saved in across the synthetic write reattach makes, so the first real
	// observation is measured against what the agent was doing rather than against
	// our placeholder (see setStatusReattached). In-memory only. Guarded by mu.
	awaitingReattachSettle bool
	reattachSavedStatus    Status
	// statusHistory is a bounded ring buffer of recent status transitions (newest
	// last), so a transient mislabel can be diagnosed after the fact rather than
	// lost between polls (#290 observability). In-memory only. Guarded by mu.
	statusHistory []StatusTransition

	// The below fields are initialized upon calling Start(). Guarded by mu.

	started bool
	// startedAt records when the agent was last (re)launched, so a lost-session
	// recovery can tell a crash-moments-after-launch (a bad program/profile) from a
	// long-lived session that later died, and surface an actionable notice. Runtime
	// only, not persisted.
	startedAt time.Time
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
		// Takes i.mu, like the GetStatus above it — safe here because
		// ToInstanceData is never called with the instance lock held.
		StatusChangedAt: i.StatusChangedAt(),
		CreateRequest:   i.CreateRequest,
		Program:         i.Program,
		AutoYes:         i.AutoYes,
		Unread:          i.Unread(),
		Muted:           i.Muted(),
		Direct:          i.direct,
		IsolateDeps:     i.isolateDeps,

		ClaudeAccount:        i.claudeAccount,
		ClaudeConfigDir:      i.claudeConfigDir,
		ClaudeAccountDefault: i.claudeAccountDefault,
		ClaudeAccountPool:    i.claudeAccountPool,
		GHConfigDir:          i.ghConfigDir,
		GitHubTokenEnv:       i.githubTokenEnv,
		AgyAccount:           i.agyAccount,
		AgyConfigDir:         i.agyConfigDir,
		Model:                i.modelID,
		PermissionMode:       i.runtimeMode,
		Effort:               i.runtimeEffort,
		TmuxName:             i.TmuxSessionName(),
		HookName:             i.hookSessionName(),
		Port:                 i.Port(),
		RunStarted:           i.RunWanted(),
		RunSession:           i.RunSessionName(),
		TermSession:          i.TerminalSessionName(),

		// Persist the undelivered prompt queue so it survives a restart and is re-delivered
		// in order on reload (delivered prompts have already been popped, so this is usually
		// empty). The legacy single-prompt fields are no longer written; FromInstanceData
		// still reads them to migrate a state.json that predates the queue.
		PromptQueue: toQueuedPromptData(i.promptQueueSnapshot()),
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
		// Copy to a local before taking its address: &i.diffStats.Unpushed would
		// alias the live struct the poll keeps mutating into the serialized data.
		unpushed := i.diffStats.Unpushed
		data.DiffStats = DiffStatsData{
			Added:        i.diffStats.Added,
			Removed:      i.diffStats.Removed,
			Content:      i.diffStats.Content,
			FilesChanged: i.diffStats.FilesChanged,
			Commits:      i.diffStats.Commits,
			Behind:       i.diffStats.Behind,
			Unpushed:     &unpushed,
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
		// Restored, not re-derived: a session that has been waiting on the user
		// for six hours must still say so after a restart. A zero value (a state
		// file predating the field) is exactly the case recordStatusChange
		// already covers, by stamping on first observation.
		statusChangedAt: data.StatusChangedAt,
		// Seeded from the same stamp, and only for a restored Pending row, so the elapsed
		// cue survives a restart instead of blanking until the first poll. The producer is
		// left unattributed: state.json does not record which one was holding, and guessing
		// "the set" would hand a restored row to the watchdog on a clock it did not earn.
		// The first poll attributes it, restamping as it does for any handover.
		pendingSince: pendingSinceOnRestore(data.Status, data.StatusChangedAt),
		unread:       data.Unread,
		muted:        data.Muted,
		Height:       data.Height,
		Width:        data.Width,
		CreatedAt:    data.CreatedAt,
		UpdatedAt:    data.UpdatedAt,
		Program:      data.Program,
		// Restored so the reconcile can still recognise this row as the one a claim
		// asked for after any number of restarts, not only the one that made it.
		CreateRequest: data.CreateRequest,
		direct:        data.Direct,
		isolateDeps:   data.IsolateDeps,

		claudeAccount:        data.ClaudeAccount,
		claudeConfigDir:      data.ClaudeConfigDir,
		claudeAccountDefault: data.ClaudeAccountDefault,
		claudeAccountPool:    data.ClaudeAccountPool,
		ghConfigDir:          data.GHConfigDir,
		githubTokenEnv:       data.GitHubTokenEnv,
		agyAccount:           data.AgyAccount,
		agyConfigDir:         data.AgyConfigDir,
		modelID:              data.Model,
		runtimeMode:          data.PermissionMode,
		runtimeEffort:        data.Effort,
		port:                 data.Port,
		runWanted:            data.RunStarted,
		runName:              data.RunSession,
		termName:             data.TermSession,
	}

	// Re-reserve the port this session was running on before anything created later in
	// this run can be allocated one. Restoration is the only place that can do it: the
	// registry is process-wide state and starts empty, so a session whose dev server is
	// still bound would otherwise have its port handed straight to the next create.
	instance.claimPersistedPort()

	// Pending prompts restored from disk re-enter tick-driven delivery on reload. The
	// head gets a live clock from now (the agent re-boots on resume, so it needs the 60s
	// valve; restarting the clock also measures the post-restart wait rather than
	// wall-clock age); the rest are follow-ups with strict idle-only delivery. Precedence
	// is strict — read prompt_queue when present, else migrate the legacy single prompt —
	// so a transitional state.json carrying both fields never duplicates the head.
	switch {
	case len(data.PromptQueue) > 0:
		for idx, qp := range data.PromptQueue {
			if idx == 0 {
				instance.QueuePrompt(qp.Text)
			} else {
				instance.QueueFollowupPrompt(qp.Text)
			}
		}
	case data.Prompt != "":
		instance.QueuePrompt(data.Prompt)
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
		// Not just cosmetic on the restore path: Resume calls Setup — and therefore
		// seedLocalPaths — on THIS worktree, not on the one Start built, so without this
		// an isolated session would silently start linking again after the first app
		// restart or the first pause/resume (#481).
		instance.gitWorktree.SetIsolateDeps(instance.isolateDeps)
		// A state.json predating the unpushed field omits it. Resolve that gap
		// conservatively — assume none of the ahead commits are pushed, which is the
		// pre-field behavior — rather than as a literal 0, which would claim nothing
		// is at risk. An active session self-corrects on the next poll; a paused one
		// is never polled, so this value is the one its kill dialog will use.
		unpushed := data.DiffStats.Commits
		if data.DiffStats.Unpushed != nil {
			unpushed = *data.DiffStats.Unpushed
		}
		instance.diffStats = &git.DiffStats{
			Added:        data.DiffStats.Added,
			Removed:      data.DiffStats.Removed,
			Content:      data.DiffStats.Content,
			FilesChanged: data.DiffStats.FilesChanged,
			Commits:      data.DiffStats.Commits,
			Behind:       data.DiffStats.Behind,
			Unpushed:     unpushed,
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
	sess.SetAgyConfigDir(instance.agyConfigDir)
	// Restore the name the surviving agent's hooks are keyed by. It diverges from TmuxName
	// only between a deep rename and the next relaunch — but in exactly that window the
	// session is rebuilt here under the POST-rename name while the agent that outlived the
	// restart still writes to the PRE-rename directory, and reattach (Restore) never re-runs
	// the bake that would re-key it. Empty (a legacy state.json, or a session Atrium never
	// launched) pins the name resolved just above instead of leaving a lazy fallback that a
	// later Rename would move out from under a surviving agent — see SetHookSessionName (#492).
	sess.SetHookSessionName(data.HookName)
	// Bound here as well as in Start, because a restored instance can be relaunched without
	// going through Start at all: recoverInPlace calls the tmux session's Start/StartContinue
	// directly when the pane could not be reattached. Binding the method value rather than a
	// snapshot is the point: Rename rewrites the two facts this renders from (Title, Branch)
	// without touching the tmux session, and Resume/recoverInPlace relaunch without going
	// through Start — so the read has to happen at launch, not here.
	sess.SetSessionBriefFunc(instance.sessionBrief)
	instance.tmuxName = sess.Name()
	instance.tmuxSession = sess

	return instance, nil
}

// paneSurvived reports whether the instance's tmux session is still alive, so the
// loader can reserve a slot for every survivor before rationing relaunches (see
// Storage.LoadInstances and recoveryBudget). A paused instance is never probed: it
// has no live session, only one constructed for a later Resume.
//
// Same pre-publication precondition as reattach — it reads tmuxSession without
// holding i.mu.
func (i *Instance) paneSurvived() bool {
	return !i.Paused() && i.tmuxSession.DoesSessionExist()
}

// reattach brings a rehydrated instance online: it reattaches to a surviving tmux
// session, or recovers in place when that session is wedged or gone. This is the
// live tmux/git IO deliberately kept out of FromInstanceData, so a caller can
// rehydrate an instance without side effects and reattach as a separate step
// (Storage.LoadInstances does both in sequence). A paused instance has no live
// session to reattach — its Session was constructed for a later Resume — so it is
// only marked started.
//
// paneAlive is the caller's paneSurvived() answer, passed in rather than asked again
// here so the caller can count the whole surviving fleet first. budget rations the
// relaunches recovery would perform; nil is unlimited. Recovering a session whose
// pane is gone starts a fresh agent, so past the budget it is parked as Paused
// instead — the way back is r / ctrl+r, itself cap-gated (#463), so the overflow
// cannot be quietly re-oversubscribed. A surviving pane is never refused; it is
// already running.
//
// Precondition: reattach must run during load, before the instance is published to
// the poll loop. It reads tmuxSession and — via the paused branch, parkOverBudget
// and recoverInPlace — writes started without holding i.mu, which is safe only in
// that single-threaded, pre-publication window.
func (i *Instance) reattach(paneAlive bool, budget *recoveryBudget) {
	if i.Paused() {
		i.started = true
		return
	}

	// FromInstanceData mapped the saved Status onto i.status and nothing has changed
	// it since, so it still reflects the status at save time here.
	savedStatus := i.GetStatus()
	sess := i.tmuxSession
	if paneAlive {
		// Normal case: the session survived (cs detaches, it doesn't kill), so
		// reattach to it. If the attach (Restore) fails the session is wedged — kill
		// it and recover in place rather than aborting the load of every other
		// session. That relaunch spends the slot the caller already reserved for this
		// live pane, so it is not asked to spend again — but a recovery that ends
		// Paused never became load, so the slot goes back. Start() no longer sets
		// Running itself (that is owned by the caller), so mark a
		// successfully-reattached session Running here; recoverInPlace sets its own
		// status otherwise.
		if err := i.Start(false); err != nil {
			log.ErrorLog.Printf("failed to restore session %s, recovering: %v", i.Title, err)
			if closeErr := sess.Close(); closeErr != nil {
				log.ErrorLog.Printf("failed to close stale session %s: %v", i.Title, closeErr)
			}
			if !i.recoverInPlace() {
				budget.refund()
			}
		} else {
			// The Running written here is synthetic — the session was reattached, not
			// observed working — so it goes through setStatusReattached, which records
			// no transition. Two things ride on that. The first poll's settle to Ready
			// must not flag unread when the session was already idle at save time
			// (hence the arm below; a persisted Running means the agent was genuinely
			// working when the app closed, so its first Ready is a real completion and
			// must not be armed). And statusChangedAt, just restored from state.json,
			// must survive until that first poll says what the session is really
			// doing — otherwise a six-hour wait reads as having started at launch.
			i.setStatusReattached(Running)
			if savedStatus == Ready {
				i.ArmReadySuppression()
			}
		}
		return
	}

	// The tmux session is gone — e.g. after a reboot, or the one-time migration to
	// cs's dedicated socket. Don't crash on the failed attach (which previously
	// aborted startup); recover in place, which relaunches the agent — so it needs a
	// slot from the budget first, and hands one back that did not end up live.
	if !budget.spend(i) {
		i.parkOverBudget()
		return
	}
	if !i.recoverInPlace() {
		budget.refund()
	}
}

// parkOverBudget parks a session whose agent the host budget cannot afford to
// relaunch, so the user restores it deliberately (r, or ctrl+r for the batch) rather
// than the fleet oversubscribing the host at launch.
//
// The park is a bare status flip, exactly as recoverInPlace's own degradations are —
// deliberately NOT Instance.pause(), which would commit the worktree's uncommitted
// work and remove it. That is what makes this safe for WIP: the worktree stays
// materialized, and Resume reuses a materialized worktree as-is rather than
// re-adding it from the branch (see Resume).
//
// startedAt is deliberately left alone: nothing was launched, so stamping it would
// make DiedAtLaunch report a crash-at-launch for a session that never launched. A
// zero startedAt is the value that predicate is written for.
func (i *Instance) parkOverBudget() {
	i.started = true
	log.InfoLog.Printf("recovery deferred for %q: host session budget reached; parked as paused", i.Title)
	i.SetStatus(Paused)
}

// startResuming relaunches the dead agent in workDir, resuming its prior conversation
// only when one actually exists. It blocks resume *only* when the agent's transcript is
// locatable (claude) AND no session record exists for workDir — the exact case where
// `claude --continue` aborts with "No conversation found to continue!", killing the pane
// and bouncing the session straight back to Paused. Agents without a native-transcript
// adapter (agy/codex/gemini) report supported == false and defer to their own resume
// probe in tmux.resumeCommand — which is a CAPABILITY check, not an existence one, so
// for them a resume flag is applied with nothing having looked for a conversation.
//
// What defends those agents without modelling any vendor's storage is not decided here:
// this records whether the launch carried a resume flag (lastLaunchResumed), and
// RepairResumingLaunch relaunches blank if that launch turns out to have died at birth.
// The detection is left to the poll loop's existing debounce for a measured reason — a
// resume flag with nothing to resume does not fail fast, and the seconds it takes are
// recorded on app.lostSessionLaunchCrashWindow, the window that has to cover them. A
// settle here short enough not to be felt on every resume would miss exactly the failure
// it was added for. Today agy, codex and gemini all survive the flag (driven, recorded
// beside each adapter's Resume), so this guards against a vendor changing its mind
// rather than fixing a live break (#712).
func (i *Instance) startResuming(ts *tmux.Session, workDir string) error {
	// Both branches below create a tmux session, and a session's environment can only
	// be set as it is born — so a relaunch (resume, or an in-place recovery) has to
	// re-apply the repo's session_env here or come back without it.
	i.applySessionEnv(workDir)
	resumable, supported := transcript.HasResumable(i.Program, workDir, transcript.Options{Root: i.claudeConfigDir})
	// Record which way this went before acting on it: undo's post-restore notice
	// has to tell the user whether the conversation came back, and this is the only
	// place that knows. supported == false stays unknown rather than false — the
	// agent's own resume probe decides, out of our sight, and reporting a guess as
	// a fact is the thing the confirmation copy already refuses to do.
	i.noteConversationOutcome(resumable, supported)
	if supported && !resumable {
		i.noteLaunchResumed(false)
		return ts.Start(workDir)
	}
	resuming, err := ts.StartContinue(workDir)
	// Recorded whatever the launch turned out to be, including false: a relaunch that
	// carried no rewrite — no Resume, a probe that failed, an argv the adapter refuses
	// to splice into, or a program already pinning its own conversation — is one a blank
	// retry would run identically, and leaving a stale true here would authorise exactly
	// that.
	i.noteLaunchResumed(resuming)
	return err
}

// noteLaunchResumed records whether the launch just made carried a resume flag.
func (i *Instance) noteLaunchResumed(resuming bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastLaunchResumed = resuming
}

// noteConversationOutcome records what startResuming learned from the transcript
// adapter, so a later restore can report it.
func (i *Instance) noteConversationOutcome(resumable, supported bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.conversationKnown = supported
	i.conversationResumed = supported && resumable
}

// ResumedConversation reports whether the agent's last relaunch came back into its
// prior conversation, and whether that is knowable at all.
//
// known is false in two shapes that must not be confused with "started fresh":
// nothing has relaunched yet, and an agent with no native-transcript adapter
// (agy/codex/gemini), whose own resume probe decides after we have stopped looking.
// A caller that flattens the two would tell the user their conversation was lost
// on the strength of not having looked.
func (i *Instance) ResumedConversation() (resumed, known bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.conversationResumed, i.conversationKnown
}

// recoverInPlace brings a loaded instance back online after its tmux session
// could not be restored (the session was wedged, or gone entirely). If the
// worktree is intact it recreates the session in place, resuming the agent's
// prior conversation when one exists (startResuming; a fresh start otherwise) and
// marks the instance Running. If the worktree is gone, or the restart fails, it
// degrades to Paused so the branch is preserved and Resume can recover it
// later — a single bad session must never abort loading the rest.
//
// Returns whether the instance ended up live (Running). False means it degraded to
// Paused and started no agent, which is what lets the caller hand its recovery slot
// back to the budget (see reattach).
//
// Recreating in place (rather than via Resume) deliberately preserves any
// uncommitted work — but note *why*, because the reason is narrower than "Resume
// loses WIP": Resume force-recreates only a worktree that is no longer valid on
// disk, and a normal pause has removed it by then. Here the worktree is still
// materialized, and re-adding it from the branch (Setup → clearStaleWorktree) would
// discard the WIP; Resume takes the same care for the same reason when it meets a
// still-materialized worktree.
func (i *Instance) recoverInPlace() bool {
	i.started = true
	i.startedAt = time.Now()

	wt := i.worktree()
	if wt == nil {
		// Direct session: no worktree to validate. Restart the agent in the real
		// directory; on failure leave it Paused so the user can Resume later.
		if err := i.startResuming(i.tmuxSession, i.Path); err != nil {
			log.ErrorLog.Printf("failed to restart direct session %s in place, leaving paused: %v", i.Title, err)
			i.SetStatus(Paused)
			return false
		}
		i.SetStatus(Running)
		// The restarted agent's post-boot idle is a boot artifact, not new output;
		// don't let the first poll's settle to Ready flag unread.
		i.ArmReadySuppression()
		return true
	}

	valid, err := wt.IsValidWorktree()
	if err != nil {
		log.ErrorLog.Printf("failed to validate worktree for %s, leaving paused: %v", i.Title, err)
	}
	if err != nil || !valid {
		i.SetStatus(Paused)
		return false
	}

	if err := i.startResuming(i.tmuxSession, wt.GetWorktreePath()); err != nil {
		log.ErrorLog.Printf("failed to restart session %s in place, leaving paused: %v", i.Title, err)
		i.SetStatus(Paused)
		return false
	}
	i.SetStatus(Running)
	// As above: the post-boot idle settle is not a genuine completion.
	i.ArmReadySuppression()
	return true
}

// worktreeCleanup is the seam recreateSession tears the worktree down through on a
// failed (re)launch. A package-level var — matching the git package's own test-seam
// idiom (checkGHCLI/runGitPush/runGHBrowse) — so a test can inject a failing teardown
// and assert the error is wrapped; production always uses (*git.Worktree).Cleanup.
var worktreeCleanup = (*git.Worktree).Cleanup

// recreateSession starts a fresh tmux session for an already-set-up worktree,
// resuming the agent's prior conversation when one exists (startResuming; a fresh
// start otherwise). Callers must ensure no session with the same name still exists —
// Start guards against duplicates — so a stale session has to be closed first.
//
// rollbackWorktree says whether a failed launch should tear the worktree down, and it
// is emphatically not "clean up after yourself": Worktree.Cleanup is the KILL
// teardown — `git worktree remove -f` and `git branch -D`. Pass true only when the
// caller materialized this worktree in the same operation, where the teardown undoes
// its own Setup and the contents came from the branch, so nothing is lost. Pass false
// when the worktree pre-existed the call: it may hold uncommitted work this operation
// did not create, and the branch holds the session's history. A budget-parked session
// (parkOverBudget) is exactly that case, and reaches here on every Resume because its
// tmux session is gone by construction — so getting this wrong would make the load
// shedding introduced for #474 the most destructive path in the program.
func (i *Instance) recreateSession(rollbackWorktree bool) error {
	ts := i.tmux()
	wt := i.worktree()
	if err := i.startResuming(ts, i.WorkingDir()); err != nil {
		log.ErrorLog.Print(err)
		// Undo the worktree this same operation set up, so a failed launch does not
		// leak one. Skipped when we did not create it (see rollbackWorktree) and when
		// there is none at all — a direct session runs in the user's real directory.
		if wt != nil && rollbackWorktree {
			if cleanupErr := worktreeCleanup(wt); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
		}
		return fmt.Errorf("failed to start new session: %w", err)
	}
	// Stamp the relaunch so DiedAtLaunch keeps working across Resume: a typo'd
	// program/profile that crashes moments after launch must stay diagnosable on
	// every resume, not just the first Start (#270). Written under the lock that
	// DiedAtLaunch reads startedAt through.
	i.mu.Lock()
	i.startedAt = time.Now()
	i.mu.Unlock()
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
	// IsolateDeps creates a dependency-isolating session: the config's link_paths are
	// not symlinked into its worktree, so what it installs stays private to it (#481).
	// Ignored for a Direct session, which never seeds anything.
	IsolateDeps bool
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
		Title:       opts.Title,
		status:      Ready,
		Path:        absPath,
		Program:     opts.Program,
		Height:      0,
		Width:       0,
		CreatedAt:   t,
		UpdatedAt:   t,
		baseBranch:  opts.Branch,
		direct:      opts.Direct,
		isolateDeps: opts.IsolateDeps,
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

// hookSessionName returns the tmux name this instance's status-hook artifacts are keyed by
// (InstanceData.HookName), or "" when there is no tmux session yet or it has never been
// launched. It reads through to the tmux Session rather than caching on the Instance: the
// value is frozen inside start(), so the Session is its only writer and a second copy here
// could only ever be stale. See tmux.Session.HookSessionName.
func (i *Instance) hookSessionName() string {
	ts := i.tmux()
	if ts == nil {
		return ""
	}
	return ts.HookSessionName()
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

// DiedAtLaunch reports whether the agent was (re)launched within the last
// `within` — i.e. a lost-session recovery firing now is a crash moments after
// launch (a bad program/profile) rather than a long-running session that died.
// False for a never-started instance (zero startedAt).
func (i *Instance) DiedAtLaunch(within time.Duration) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return !i.startedAt.IsZero() && time.Since(i.startedAt) < within
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

// IsolateDeps reports whether this session is dependency-isolating: its worktree gets
// none of the config's link_paths symlinks, so what it installs stays private to it.
// Chosen at creation and fixed for the session's life (#481).
func (i *Instance) IsolateDeps() bool {
	return i.isolateDeps
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

// sessionBrief collects the facts the injected SessionStart context brief is rendered from
// (#485): who this session is, the origin checkout the worktree came from, the branch it is
// already on, and the tree its siblings live under.
//
// It returns the zero brief — "say nothing" — for a session with no worktree. That is both a
// direct (non-git) session, whose cwd is the user's real checkout with no branch and nothing
// disposable about it, and an unstarted one, which has no facts yet. Every load-bearing
// sentence in the copy is about an Atrium-managed worktree, so a session without one gets no
// SessionStart hook rather than a brief that describes something that does not exist.
//
// The repo path comes from the worktree, not i.Path: i.Path is whatever directory the user
// picked, which may be a subdirectory of the repo, while the worktree holds the resolved root.
func (i *Instance) sessionBrief() tmux.SessionBrief {
	wt := i.worktree()
	if wt == nil {
		return tmux.SessionBrief{}
	}
	root, err := config.WorktreesDir()
	if err != nil {
		// Without the root there is no sibling-worktree warning to give, and ok() rejects a
		// partial brief anyway. Degrade to no brief rather than to a brief with a hole.
		log.ErrorLog.Printf("session brief disabled for %s: cannot resolve worktrees dir: %v", i.Title, err)
		return tmux.SessionBrief{}
	}
	return tmux.SessionBrief{
		Name:          i.Title,
		Origin:        wt.GetRepoPath(),
		Branch:        i.Branch,
		WorktreesRoot: root,
	}
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

// RebindBaseContext points the instance AND its already-constructed tmux/git
// children at ctx. Unlike SetBaseContext (which only affects children built
// later by Start, and so must run before Start), this rebinds live children.
// The children's baseCtx is read lock-free, so this is safe ONLY when no
// goroutine is running against them — i.e. the Start goroutine has joined — and
// the instance is out of every poll set (Started() == false, which
// snapshotActiveInstances/pollSelectedCmd filter on). app.Run uses it during
// shutdown reconciliation (#282) to hand a signal-orphaned session a
// context.WithoutCancel context so Kill's teardown subprocesses aren't
// insta-killed by the cancelled lifecycle context.
func (i *Instance) RebindBaseContext(ctx context.Context) {
	i.baseCtx = ctx
	i.mu.RLock()
	ts, wt := i.tmuxSession, i.gitWorktree
	i.mu.RUnlock()
	if ts != nil {
		ts.SetBaseContext(ctx)
	}
	if wt != nil {
		wt.SetBaseContext(ctx)
	}
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
		// Pin the gh account and the seeding mode before publishing the worktree to
		// other goroutines, so the writes happen-before any poll-loop read behind i.mu.
		gitWorktree.SetGHConfigDir(i.ghConfigDir)
		gitWorktree.SetIsolateDeps(i.isolateDeps)
		i.mu.Lock()
		i.gitWorktree = gitWorktree
		i.mu.Unlock()
		i.Branch = branchName
	}

	// Pin the forked conversation into the launch command before the tmux session is
	// built from it. The id is known up front — it was minted at submit — so this
	// does not wait on the fork run below; what waits is the launch itself, which
	// never happens if that run fails.
	if firstTimeSetup && i.forkSeed != nil {
		i.Program = agent.WithResumeFlag(i.Program, i.forkSeed.NewSessionID)
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
		tmuxSession.SetAgyConfigDir(i.agyConfigDir)
	}

	// Bind the SessionStart brief unconditionally: a restored instance arrives with its tmux
	// session already built (and bound) by FromInstanceData, a first-time one was only just
	// constructed above. Rebinding the same method value is a no-op; what matters is that a
	// Session built anywhere else never reaches start() unbound.
	tmuxSession.SetSessionBriefFunc(i.sessionBrief)

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
			i.startedAt = time.Now()
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
			// The per-repo setup script and session environment (#389), between the
			// worktree existing and the agent seeing it. Here rather than inside Setup
			// for two reasons: the git package has no business running user scripts, and
			// Setup's contract is that anything it returns tears the whole worktree down
			// — which a script that merely could not reach npm must never do. It records
			// its own failure instead; see setupscript.go.
			i.StartRepoEnvironment(i.WorkingDir())
		} else {
			// A direct session has no worktree to install into — its directory is the
			// user's own checkout, already warm and not Atrium's to run `npm ci` in — but
			// it does run somewhere the config can route on, so it still gets the
			// environment.
			i.applySessionEnv(i.WorkingDir())
		}

		// Materialize a forked conversation, between the worktree existing (the fork
		// is filed under the project directory its cwd resolves to, which must be this
		// session's) and the agent being launched to read it.
		//
		// It is a network call, and it is allowed to fail the whole start. That is the
		// point: --resume-session-at is honoured only in print mode, so this run is the
		// only place the truncation can be made to happen *and* be checked. A session
		// that launched anyway would be a working agent seeded from the end of someone
		// else's conversation, with nothing on screen saying so — strictly worse than
		// one that never appeared. The deferred handler above tears the worktree down
		// with the rest, so the refusal leaves nothing behind.
		if i.forkSeed != nil {
			if err := forkConversation(i.baseContext(), i.WorkingDir(), i.claudeConfigDir, *i.forkSeed, i.forkPrompt); err != nil {
				if wt != nil {
					if cleanupErr := wt.Cleanup(); cleanupErr != nil {
						err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
					}
				}
				setupErr = fmt.Errorf("failed to fork the conversation: %w", err)
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

	// The run command's sibling session goes with it (#389). Here rather than in the app
	// layer so that every retire path is covered by construction — single kill, batch
	// kill, the post-merge cleanup offer, and Start's own error unwind all end here.
	tc.Wrap("stop run command", i.StopRunCommand())

	// Then clean up git worktree
	if wt := i.worktree(); wt != nil {
		tc.Wrap("cleanup git worktree", wt.Cleanup())
	}

	// The worktree is gone, so the managed port goes back to its range (#389).
	// Unconditional, and after the teardown above rather than gated on it: a kill whose
	// worktree removal failed still ends the session, and a port held by a registry
	// entry nothing will ever clear is off the range for the life of the process.
	i.releasePort()

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
// Enter accepts the plan AND enables auto-accept). PaneGate (a startup/trust screen) also
// surfaces NeedsInput and is never auto-tapped, with the awaitingSetup flag set so the row
// shows a setup hint. PanePending (main turn ended, background sub-agents still in flight)
// maps to the Pending status via applyPending, which also runs the wall-clock watchdog.
// PaneBackground (main turn ended, a background shell or Monitor it launched still running)
// maps to the same Pending status but bypasses that watchdog, and rings the turn-end once on
// the edge into it — see the arm below for both.
// PaneUnknown (an unreadable or not-yet-started pane) and PaneDead (the session is gone)
// both leave the status untouched: a dead session is recovered to Paused separately,
// debounced by the metadata loop's recoverLostInstances, not from here.
func (i *Instance) ApplyPaneState(state tmux.PaneState) (tapped bool) {
	// A startup gate is never auto-tapped (even under AutoYes): auto-accepting a
	// folder-trust or new-MCP screen is exactly the unsafe action we refuse. Every
	// settled state clears the setup flag so a cleared gate drops the row hint; the
	// keep-prior states (Unknown/Dead) leave both status and flag untouched.
	//
	// Releasing the Pending producer needs no arm here: setStatusLocked drops it on any
	// write of a non-Pending status, and the keep-prior states (Unknown/Dead) write no
	// status at all — which is the behaviour they want anyway, since an unreadable pane is
	// not evidence that the set drained or the chip cleared.
	switch state {
	case tmux.PaneWorking:
		i.setAwaitingSetup(false)
		i.SetStatus(Running)
	case tmux.PaneGate:
		i.setAwaitingSetup(true)
		i.SetStatus(NeedsInput)
	case tmux.PanePrompt:
		i.setAwaitingSetup(false)
		if i.AutoYes {
			i.TapEnter()
			return true
		}
		i.SetStatus(NeedsInput)
	case tmux.PanePromptManual:
		i.setAwaitingSetup(false)
		i.SetStatus(NeedsInput)
	case tmux.PaneIdle:
		i.setAwaitingSetup(false)
		i.SetStatus(Ready)
	case tmux.PanePending:
		i.setAwaitingSetup(false)
		i.applyPending()
	case tmux.PaneBackground:
		// Same status as PanePending, deliberately WITHOUT applyPending's watchdog. That cap
		// backstops one failure — a SubagentStop that never fired, leaving a latched id stuck
		// forever — and a footer chip cannot fail that way: it is re-scraped every poll and
		// gone the moment the work exits. Expiring it would re-commit the exact "done while
		// still working" bug this state exists to fix once the cap elapsed, and a persistent
		// Monitor legitimately runs for the whole session. A dead pane is still caught by
		// tmux liveness (PaneDead) before the pane is ever classified.
		//
		// But the row must not go SILENT for that whole run, which is what dropping the
		// into-Ready edge would do: no finish/asked ding, no unread glyph, skipped by
		// NextUnread, and #571's question hold inert because it releases on Unread(). The
		// handover into this producer is the turn-end — the agent stopped and wrote to the
		// user — so it rings once, here, and the status stays Pending because the work has
		// not finished. A session-length Monitor then rings on every subsequent turn-end too,
		// since each turn's working phase releases the producer and re-enters it.
		i.setAwaitingSetup(false)
		if i.setPendingSource(pendingBackground) {
			i.setStatusTurnEnded(Pending)
		} else {
			i.SetStatus(Pending)
		}
	case tmux.PaneUnknown, tmux.PaneDead:
	}
	return false
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

// queuedPrompt is one entry in an Instance's prompt FIFO: the text to deliver and
// its delivery-timeout clock. A non-zero queuedAt arms promptDeliveryReady's 60s
// valve (for a boot prompt facing a chatty startup); a zero queuedAt means
// strict idle-only (a follow-up that must wait for the agent's turn to finish).
type queuedPrompt struct {
	text     string
	queuedAt time.Time
}

// The queued-prompt state (promptQueue, promptInFlight) forms one small state
// machine: QueuePrompt/QueueFollowupPrompt append a prompt, ClaimPrompt hands the
// head to exactly one sender at a time, and ClearPromptSending/ClearPrompt settle
// the attempt. All of it is promptMu-guarded: the main event loop writes it — or the
// attach keeper does, while a tea.Exec attach suspends the loop — and the metadata
// tick's cmd goroutines read it off-thread.

// Prompt returns the head (next-to-deliver) queued prompt, or "" when the queue is
// empty.
func (i *Instance) Prompt() string {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 {
		return ""
	}
	return i.promptQueue[0].text
}

// PromptQueuedAt returns the head prompt's delivery-timeout clock (zero when the
// queue is empty, or when the head is a follow-up with strict idle-only delivery).
func (i *Instance) PromptQueuedAt() time.Time {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 {
		return time.Time{}
	}
	return i.promptQueue[0].queuedAt
}

// QueueLen returns the number of prompts awaiting delivery (head included).
func (i *Instance) QueueLen() int {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	return len(i.promptQueue)
}

// HasQueuedPrompt reports whether any prompt is awaiting delivery — the row's
// pending-prompt glyph shows exactly while this is true.
func (i *Instance) HasQueuedPrompt() bool {
	return i.QueueLen() > 0
}

// promptQueueSnapshot returns a copy of the queue for persistence, so a caller never
// observes a torn queue mid-mutation.
func (i *Instance) promptQueueSnapshot() []queuedPrompt {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 {
		return nil
	}
	out := make([]queuedPrompt, len(i.promptQueue))
	copy(out, i.promptQueue)
	return out
}

// enqueue appends one prompt to the tail under lock; an empty prompt is a no-op.
func (i *Instance) enqueue(prompt string, queuedAt time.Time) {
	if prompt == "" {
		return
	}
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	i.promptQueue = append(i.promptQueue, queuedPrompt{text: prompt, queuedAt: queuedAt})
}

// SetAgyAccount pins the Antigravity CLI account resolved at creation time.
func (i *Instance) SetAgyAccount(name, configDir string) {
	// These are read lock-free because they are written exactly once before Start.
	i.agyAccount = name
	i.agyConfigDir = configDir
}

// QueuePrompt appends an initial/boot prompt for tick-driven delivery with a live
// delivery-timeout clock (the 60s valve), so a chatty startup that never reaches an
// idle pane can't stall the first message. An empty prompt is a no-op. Used for the
// create-form prompt and the restored head on reload.
func (i *Instance) QueuePrompt(prompt string) {
	i.enqueue(prompt, time.Now())
}

// QueueFollowupPrompt appends a follow-up (quick-send) prompt with a zero clock, so
// promptDeliveryReady delivers it strictly when the agent next idles rather than
// force-injecting it mid-turn. An empty prompt is a no-op.
//
// The zero clock is what makes this safe, not any state of the target: idle-only
// delivery holds whether or not the session has idled before. Quick-send targets
// the selected (necessarily past-Loading) session, but the outbox drain
// (app/outbox_drain.go) also queues here for a session that may still be starting
// up — a prompt for one whose agent never idles simply waits in the queue,
// cancelable, rather than being injected into a startup banner.
func (i *Instance) QueueFollowupPrompt(prompt string) {
	i.enqueue(prompt, time.Time{})
}

// ClaimPrompt atomically claims the head prompt for one delivery attempt: it returns
// ("", false) when the queue is empty or a send is already in flight, otherwise it
// raises the in-flight guard and returns the head text. Collapsing the check-then-act
// into one lock hold is what keeps overlapping dispatchers (metadata ticks, the attach
// keeper) from sending the same prompt twice.
func (i *Instance) ClaimPrompt() (string, bool) {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 || i.promptInFlight {
		return "", false
	}
	i.promptInFlight = true
	return i.promptQueue[0].text, true
}

// PromptSending reports whether the head prompt is currently in flight.
func (i *Instance) PromptSending() bool {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	return i.promptInFlight
}

// ClearPromptSending lowers the in-flight guard once a head dispatch has settled
// softly (pane not ready / unconfirmed), leaving the prompt at the head for a retry.
// It must not touch the head's clock: a deferral is a retry, not a promotion, so a
// boot head keeps accumulating toward its 60s timeout.
func (i *Instance) ClearPromptSending() {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	i.promptInFlight = false
}

// ClearPrompt settles a head delivery attempt: it always lowers the in-flight guard,
// and pops the head only when deliveredText matches it (matched dequeue). The match is
// a double-settle guard — a stale or duplicate settle whose text no longer heads the
// queue leaves the current head (a newer prompt) intact rather than eating it, which
// is exactly the data-loss class this queue exists to prevent. A mismatch is a
// should-never-happen invariant break, so it is logged rather than silently absorbed
// (absorbing it would re-claim and re-deliver the same head forever).
func (i *Instance) ClearPrompt(deliveredText string) {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	i.promptInFlight = false
	if len(i.promptQueue) == 0 {
		return
	}
	if i.promptQueue[0].text != deliveredText {
		log.WarningLog.Printf("ClearPrompt ignored for %q: head %q != settled %q",
			i.Title, i.promptQueue[0].text, deliveredText)
		return
	}
	i.promptQueue = i.promptQueue[1:]
}

// CancelQueuedPrompt removes the queued prompt at idx, but only when it still
// matches expectedText and is not the in-flight head. The text match is the same
// double-settle guard ClearPrompt uses: if a delivery popped the head since the
// UI snapshotted the queue, the stale idx no longer matches and the call is a
// safe no-op instead of cancelling the wrong prompt. idx 0 is cancellable only
// while no send is in flight (an actively-delivering head is locked). Returns
// whether an entry was removed.
func (i *Instance) CancelQueuedPrompt(idx int, expectedText string) bool {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if idx < 0 || idx >= len(i.promptQueue) {
		return false
	}
	if idx == 0 && i.promptInFlight {
		return false
	}
	if i.promptQueue[idx].text != expectedText {
		return false
	}
	i.promptQueue = slices.Delete(i.promptQueue, idx, idx+1)
	return true
}

// QueueView returns a read-only snapshot for the management overlay: head-first
// prompt texts plus whether the head is currently being delivered. Taken under
// one lock so headInFlight can't tear away from the texts it describes.
func (i *Instance) QueueView() (texts []string, headInFlight bool) {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 {
		return nil, false
	}
	texts = make([]string, len(i.promptQueue))
	for idx, qp := range i.promptQueue {
		texts[idx] = qp.text
	}
	return texts, i.promptInFlight
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
// prompt (tool permission, plan approval, selection) on the user's behalf.
// Unlike TapEnter — the self-gating autoyes path — this is user-initiated, so
// it ignores AutoYes and returns errors instead of logging them. It
// deliberately answers PanePromptManual prompts too: a human keypress is
// exactly the manual confirmation the autoyes NoAutoTap guard preserves. Note
// that Enter selects whatever option the dialog has highlighted — on claude's
// plan dialog the default both accepts the plan and enables auto-accept
// edits, and on a selection (AskUserQuestion) it picks the highlighted
// option.
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

// ArmContext records the context strings this session should show and reports
// whether they changed, without touching tmux. Main-loop only (it writes the
// session's context cache); pair it with PushContext on a background goroutine.
func (i *Instance) ArmContext(name, left string) bool {
	ts := i.tmux()
	return ts != nil && ts.ArmContext(name, left)
}

// PushContext writes the armed context strings into tmux. Safe on a background
// goroutine — it reads and writes no cached state.
func (i *Instance) PushContext(name, left string) error {
	ts := i.tmux()
	if ts == nil {
		return nil
	}
	return ts.PushContext(name, left)
}

// ClearContextCache un-arms the context cache after a failed push so the next
// tick retries. Main-loop only, like ArmContext.
func (i *Instance) ClearContextCache() {
	if ts := i.tmux(); ts != nil {
		ts.ClearContextCache()
	}
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

// RenamedIdentity is the identity a completed deep rename has earned but not yet
// adopted: the I/O is done, and these are the fields the main loop must write.
// It exists so Rename can run off the update thread without touching Title or
// Branch — see AdoptRename.
type RenamedIdentity struct {
	Title    string
	Branch   string
	TmuxName string
}

// Rename performs an in-place "deep" rename of a started instance to newTitle: it renames
// the tmux session, then the git branch and worktree directory. Unlike SetDisplayName
// (which only changes the cosmetic label) this fixes the identity everywhere it surfaces —
// git, GitHub/PRs, the worktree path — without killing the running agent. The order
// (tmux → git) keeps rollback exact: a git failure only has to undo the tmux rename
// (reversible by name), never a worktree move that already minted a fresh path.
//
// This is the I/O half only: it runs on a background goroutine (renameIOCmd) and
// deliberately writes NEITHER Title NOR Branch. Those are unguarded fields read by the
// main thread on every render (listRowZoneID keys a row on Title), so writing them here
// would be a data race — the reason the returned identity is applied by AdoptRename on the
// update thread instead. Everything this function does touch (the git/tmux structs) guards
// its own fields.
//
// The renderer is not the only reader, and moving the write to the update thread is not on
// its own enough to make either field safe — the readers on other goroutines have to be
// accounted for one at a time, and are (see AdoptRename). One of them is this function:
// `oldTitle := i.Title` below is an off-thread read of the field AdoptRename writes. It is
// safe for a reason particular to it and not general — the only AdoptRename that can carry
// this instance's title is the one applying THIS call's own result, which by construction
// cannot run until this returns, and the rename dialog's in-flight gate stops a second
// rename overlapping. The reader that had no such reason was TerminalPane.EnsureSession on
// the capture goroutine, which now takes the title as a parameter (frameTarget.termTitle,
// #718).
func (i *Instance) Rename(newTitle string) (RenamedIdentity, error) {
	newTitle = strings.TrimSpace(newTitle)
	if newTitle == "" {
		return RenamedIdentity{}, fmt.Errorf("cannot rename to an empty title")
	}
	if !i.isStarted() {
		return RenamedIdentity{}, fmt.Errorf("cannot deep-rename an instance that has not been started")
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
		return RenamedIdentity{}, fmt.Errorf("failed to rename tmux session: %w", err)
	}

	renamed := RenamedIdentity{Title: newTitle, TmuxName: newName}

	// 2. Rename the git branch + move the worktree. On failure (incl. its own internal
	// rollback of a half-done branch rename), roll the tmux session back to its old name.
	// A direct session has no worktree, so only the tmux rename (step 1) applies.
	if wt != nil {
		if err := wt.Rename(newTitle); err != nil {
			if rbErr := ts.Rename(oldTitle, oldName); rbErr != nil {
				log.ErrorLog.Printf("failed to roll back tmux rename %q->%q: %v", newTitle, oldTitle, rbErr)
			}
			return RenamedIdentity{}, fmt.Errorf("failed to rename git worktree: %w", err)
		}
		renamed.Branch = wt.GetBranchName()
	}
	return renamed, nil
}

// AdoptRename writes the identity a successful Rename earned. Main-loop only, for the
// same single-writer reason as SetDiffStats: Title and Branch are plain fields with no
// mutex, so a second writer would be a data race with no lock to serialise it.
// A zero Branch is left alone — a direct session has no worktree to derive one from, so
// overwriting would blank a field the rename never owned.
//
// This comment used to justify "main-loop only" with "Title is read unguarded by the
// renderer", which is true of the renderer and was never the whole reader set — the
// renderer runs on this same loop, so it is the one reader that cannot race. What makes
// main-loop-only sufficient is that the readers on OTHER goroutines are handed values
// snapshotted here rather than reading the fields: TerminalPane.EnsureSession, on the
// capture goroutine, took the title off the instance until #718 and now receives
// frameTarget.termTitle; app's customCommandSpec carries strings for the same reason.
//
// Not every reader is inside that rule yet, and #718 named the rest rather than converting
// them — the census lives in #719, not here, because it is long, it spans packages, and an
// enumeration in a comment is a claim nothing can hold to the tree. Take it fresh instead:
//
//	grep -rn '\.Title\b\|\.Branch\b\|\.DisplayName()' session/ app/ ui/ --include='*.go' | grep -v _test
//
// Both breadths in that line were bought by getting it wrong. Receiver-agnostic, because
// `i\.Title` finds nothing in app/ or ui/, which spell it `inst.Title` and
// `msg.instance.Title`. And ui/ in the path list, because the reader this whole issue is
// about LIVED there — a recipe scoped to session/ and app/ would have the next maintainer
// audit everything except the package that produced the bug.
//
// It is deliberately noisy, and most of what it returns is on the update thread and fine.
// Ask of each hit which goroutine it is on: the answer is a per-reader argument, never a
// general one —
// a teardown and a run-command sit behind beginAsyncAction's actionInFlight gate, which a
// rename's own I/O sits behind too; a Start goroutine cannot overlap a rename of the same
// instance because Rename refuses one that has not finished starting; Rename's own
// `oldTitle := i.Title` is safe only because the AdoptRename that could race it is the one
// applying that very call's result. Some have no such argument and are merely improbable.
// None of it is a rule a NEW off-thread reader may lean on: snapshot on this thread instead.
//
// It deliberately leaves both sibling names alone: termName and runName are owned rather
// than derived, so the shell and the dev server keep the names they were created under and
// stay reachable by the teardowns that must kill them (#389, #708). Their tmux sessions are
// not renamed on the socket either — the same call hooks make for a stronger reason
// (tmux_rename.go). Nothing here may start chasing them without also moving the sessions.
func (i *Instance) AdoptRename(renamed RenamedIdentity) {
	i.Title = renamed.Title
	if renamed.Branch != "" {
		i.Branch = renamed.Branch
	}
	i.mu.Lock()
	i.tmuxName = renamed.TmuxName
	i.mu.Unlock()
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
