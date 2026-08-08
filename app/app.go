// Package app contains the Bubble Tea program at the heart of Atrium. Its root
// model, home, owns the instance list, the discrete UI states (default / new /
// prompt / help / confirm / rename), and the per-tick poll loop that refreshes
// each session's status and diff; the ui package's components render what home
// orchestrates.
package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/hints"
	"github.com/ZviBaratz/atrium/internal/memo"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	zone "github.com/lrstanley/bubblezone/v2"
)

// doubleClickWindow is the maximum delay between two left-clicks on the same
// session row for the second to count as a double-click (attach). Bubble Tea has
// no native double-click event, so it is detected by timing here.
const doubleClickWindow = 400 * time.Millisecond

// scanFrame registers each marked zone's bounds and strips the marker escapes from
// a finished frame. A package var rather than a direct zone.Scan call so a
// benchmark can isolate its cost and a test can count how often it runs; see the
// call site in viewContent. Production is always zone.Scan.
var scanFrame = zone.Scan

// frameKey is the finished frame's components, before they are stacked. Each part
// carries a presence flag beside it because an ABSENT component and an EMPTY one
// are different frames: JoinVertical renders "" as a blank line, so the hint bar
// reserving a quiet row (#438) must not compare equal to no hint bar at all.
type frameKey struct {
	banner    string
	hasBanner bool
	body      string
	menu      string
	hasMenu   bool
	errBox    string
	hasErr    bool
}

// joinFrame stacks the components into the frame. A pure function of the key —
// which is what lets frameCached memoize the join and the scan together.
func joinFrame(k frameKey) string {
	parts := make([]string, 0, 4)
	if k.hasBanner {
		parts = append(parts, k.banner)
	}
	parts = append(parts, k.body)
	if k.hasMenu {
		parts = append(parts, k.menu)
	}
	if k.hasErr {
		parts = append(parts, k.errBox)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// frameCached stacks the frame and scans it, memoized on the components, because
// an idle Atrium hands it the same ones over and over: Bubble Tea calls View()
// after every message (tea.go:880), and at idle the preview tick and the metadata
// tick produce ~12 of those a second — ~22 once the pane-capture chain has
// something to capture (armFrameCapture), ~32 once the spinner loop joins them too
// (armSpinnerTick). Measured on a cold 14-session build, the stack and the scan
// together are ~21% of its time — 13 points the join, 8 the scan — and the scan
// alone is over half its allocated bytes.
//
// Reusing the previous output is safe, and the reason is worth stating because it
// is not obvious: a zone's ID lives INSIDE the pre-scan string, as the ANSI marker
// pair zone.Mark wrote. So two frames that compare equal here carry the same
// markers at the same offsets, and the bounds the previous scan registered still
// describe them exactly. A different zone, or a moved one, is necessarily a
// different component string and misses the memo. bubblezone's own doc for Scan
// blesses this ("some users may cache generated views").
//
// It covers the join rather than only the scan (#565) because equal components
// necessarily join to an equal frame: a separate memo over the joined string would
// hit exactly when this one does, leaving the inner one unreachable on its own
// terms while its tests went on claiming to exercise it.
//
// What the memo deliberately does NOT do is skip the scan for a frame that merely
// looks similar. Equality is byte equality on every component.
func (m *home) frameCached(k frameKey) string {
	return m.frameMemo.Get(k, func() string { return scanFrame(joinFrame(k)) })
}

// noColorProfile is the colour profile NO_COLOR selects. Named rather than
// inlined so the choice between Ascii and NoTTY — which is the difference between
// a monochrome UI and a flat one — is one identifier a test can pin.
//
// Ascii drops every colour form, including escapes Atrium hand-wrote into the
// frame (the overlay fade's SGR rewrite), while leaving bold, italic and
// underline intact. NoTTY strips those too: measured through colorprofile.Writer
// and again through ultraviolet's ConvertStyle, which is the path the renderer
// actually takes.
//
// One hazard if this is ever revisited: tea.go's CapabilityMsg handler upgrades
// the profile to TrueColor on an "RGB"/"Tc" reply WITHOUT checking whether it was
// pinned here. That is dormant only because Atrium never calls
// tea.RequestCapability. Anything that starts calling it has to gate on
// theme.Mono() as well.
func noColorProfile() colorprofile.Profile { return colorprofile.Ascii }

// programOptions builds the Bubble Tea program options.
//
// tea.WithContext ties the program to the lifecycle context so a SIGTERM (which
// cancels ctx in main) also stops the TUI loop, not just the subprocesses.
//
// The colour profile is pinned only under NO_COLOR, and it reads theme.Mono()
// rather than the environment: main.go resolves the variable once, before
// tmux.Init, because the managed tmux config is rendered there and tmux — not
// Bubble Tea — draws the in-session status band. Reading the variable here
// instead would leave that band coloured for every session started this run.
func programOptions(ctx context.Context) []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if theme.Mono() {
		opts = append(opts, tea.WithColorProfile(noColorProfile()))
	}
	return opts
}

// Run is the main entrypoint into the application. version is the build-stamped
// binary version ("dev" when unstamped); it gates the startup update check and
// names the current release in hints. binName is the invoked binary's basename,
// used verbatim in user-facing hints.
func Run(ctx context.Context, program string, autoYes bool, version, binName string) error {
	// Initialize the global bubblezone manager before the first render. The list
	// and tab views Mark() rows/tabs via the package-level manager, which panics
	// ("manager not initialized") until NewGlobal() is called. Idempotent, so it
	// coexists with the test init()s that also call it.
	zone.NewGlobal()
	h, err := newHome(ctx, program, autoYes, version, binName)
	if err != nil {
		return err
	}
	// Alt screen, focus reporting and mouse capture are no longer options here:
	// Bubble Tea v2 takes them as fields on the View this model returns every
	// frame, so they live in View() alongside the content they apply to. The
	// SS3 Home/End input shim is gone too — v2's decoder reads ESC O H/F natively,
	// which is the whole of what that wrapper existed to work around.
	//
	p := tea.NewProgram(h, programOptions(ctx)...)
	_, err = p.Run()
	// The event loop has exited. On signal shutdown it returned on ctx.Done()
	// without dispatching Update, and the force-quit escape exits with a session
	// still Loading — either way an in-flight Start was never persisted or torn
	// down. Reconcile it here (adopt-or-teardown) so it isn't orphaned (#282).
	h.reconcileInFlightStarts(ctx)
	if ctx.Err() != nil {
		// Signal-driven shutdown: Bubble Tea reports the kill as an error
		// (ErrProgramKilled), but for us it is a clean exit.
		return nil
	}
	return err
}

// maybeTrustWorktreesRoot pre-accepts Claude's workspace trust for the
// worktrees root when the opt-in trust_worktrees_root flag is on and any
// configured program resolves to claude (the launch program or any profile —
// sessions can be created from either). Programs stored on persisted instances
// are deliberately not consulted: a stored claude session whose program no
// longer matches the config is rare, the miss only re-surfaces Claude's own
// dialog, and the gate self-corrects as soon as claude is configured again.
// Strictly best-effort: every failure is a warning, never an error, because
// the fallback is just Claude's own trust dialog.
func maybeTrustWorktreesRoot(cfg *config.Config, program string) {
	if !cfg.GetTrustWorktreesRoot() {
		return
	}
	claudeConfigured := tmux.IsClaude(program)
	for _, p := range cfg.GetProfiles() {
		claudeConfigured = claudeConfigured || tmux.IsClaude(p.Program)
	}
	if !claudeConfigured {
		return
	}
	root, err := config.WorktreesDir()
	if err != nil {
		log.WarningLog.Printf("worktrees-root trust skipped: %v", err)
		return
	}
	for _, dir := range claudeTrustDirs(cfg) {
		if err := tmux.EnsureWorktreesRootTrustedIn(dir, root); err != nil {
			log.WarningLog.Printf("worktrees-root trust skipped for %s: %v", dir, err)
		}
	}
}

// claudeTrustDirs lists every Claude config dir a session might read workspace
// trust from, ambient first, deduped: the dir an unrouted / catch-all session
// inherits, plus each configured account's own dir (an account-routed session
// reads trust from its $CLAUDE_CONFIG_DIR/.claude.json, so the opt-in would not
// cover it otherwise — #266).
//
// The ambient entry comes from config.AmbientClaudeConfigDir, claude's own rule of
// $CLAUDE_CONFIG_DIR-then-home. Resolving it as $HOME unconditionally was #359:
// with that variable exported — from a shell profile, or by the enclosing Claude
// Code session when Atrium is launched from one — claude reads trust from
// $CLAUDE_CONFIG_DIR/.claude.json while Atrium pre-accepted it in ~/.claude.json,
// and the opt-in silently did nothing. #488's probe confirmed both halves on a
// live claude 2.1.220: trust written to $HOME with CLAUDE_CONFIG_DIR set leaves
// the dialog up, and written to $CLAUDE_CONFIG_DIR it does not appear.
//
// Dedupe is by CLEANED path, so an account whose config_dir is the ambient dir
// spelled with a trailing slash is still one write, not two. Clean is kept away
// from the ""-means-unresolvable sentinel: filepath.Clean("") is ".", which would
// turn "no dir" into a cwd-relative one.
//
// A relative dir that survives to here (a hand-written config_dir, or a relative
// $CLAUDE_CONFIG_DIR) is deliberately NOT filtered out: the writer refuses it with
// an error, which the caller logs to the warning log, so the misconfiguration
// leaves a trace instead of vanishing at this step. Dropping it here would silence
// even that — the trust write is best-effort, so the log is the only channel it has.
func claudeTrustDirs(cfg *config.Config) []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		if seen[clean] {
			return
		}
		seen[clean] = true
		dirs = append(dirs, clean)
	}
	add(config.AmbientClaudeConfigDir())
	for _, acct := range cfg.ClaudeAccounts {
		add(acct.ResolvedConfigDir())
	}
	return dirs
}

type state int

const (
	stateDefault state = iota
	// statePrompt is the state when a text-input overlay is up (the new-session
	// form, quick-send compose).
	statePrompt
	// stateHelp is the state when a help screen is displayed.
	stateHelp
	// stateConfirm is the state when a confirmation modal is displayed.
	stateConfirm
	// stateRename is the state when the user is editing a session's display label.
	stateRename
	// stateQueue is the state when the pending-prompt management overlay is up.
	stateQueue
	// stateCmdLog is the state when the command-log overlay is up (the tmux/git/gh
	// subprocesses Atrium has run — #372).
	stateCmdLog
	// stateFilter is the state when the user is typing an incremental filter query
	// to narrow the session list by DisplayName / Branch.
	stateFilter
	// stateInfo is the state when a dismissible information modal is displayed
	// (an actionable error that must persist until the user reads and dismisses it,
	// rather than auto-vanishing like the transient error box).
	stateInfo
	// stateSettings is the state when the settings panel is open for viewing and
	// editing the persistent configuration.
	stateSettings
	// stateHints is the state when hint (fingers) mode overlays the preview
	// pane with copy/open labels; every key routes to hint selection.
	stateHints
	// stateVisual is multi-select ("visual") mode: space marks/unmarks the
	// highlighted session and a lifecycle action (pause/resume/kill) applies to
	// the marked set; esc clears the marks and exits.
	stateVisual
	// stateDiffComment is the diff-tab line-cursor mode (#383): j/k move the cursor
	// over code lines, enter opens the composer for the current line, esc exits. The
	// diff pane is frozen on a snapshot while this state is active.
	stateDiffComment
	// stateWelcome is the interactive first-launch setup modal: pick a default
	// agent from the ones detected on PATH, then start the first session.
	stateWelcome
	// stateAccounts is the Claude/GitHub/Antigravity account manager modal.
	stateAccounts
	// stateScreensaver is the full-window splash easter egg (backtick from the
	// default state); any key or click returns to stateDefault.
	stateScreensaver
	// stateHistory is the prompt-history picker, opened from an empty prompt field
	// (create form or quick-send) to reuse a previously-submitted prompt (#388).
	stateHistory
	// stateCommandPalette is the fuzzy-over-every-action picker (#374): type what
	// you want to do, enter runs it against the current selection.
	stateCommandPalette
	// stateCustomCommands is the leader-key menu over the user's own verbs from
	// config.json's custom_commands section (#375): each row's own key runs it.
	stateCustomCommands
	// stateCheckpoints is the checkpoint timeline for a Claude session (#385): the
	// native file-history checkpoints read off Claude's transcript. Read-only — its
	// one action attaches, because only Claude's own Esc-Esc can rewind.
	stateCheckpoints
	// stateImagePreview shows an agent-produced image — a screenshot, a plot — in
	// a centred overlay, opened by uppercase-hinting its path (#398). Read-only;
	// esc closes.
	stateImagePreview

	// numStates counts the states above and must stay last. It exists so a test can
	// walk the enum rather than hand-listing it: TestEveryBarHidingStateRestoresTheFrame
	// requires every state that hides the hint bar to be driven or exempted, and a new
	// state added without one fails there instead of shipping unchecked.
	numStates
)

type home struct {
	ctx context.Context

	// startWG tracks in-flight session lifecycle goroutines — new-session Starts
	// and, since the kill split, teardowns — so app.Run can join them after
	// p.Run() returns and reconcile work the Bubble Tea event loop bypassed on
	// signal shutdown / force-quit (#282). A teardown belongs here for the same
	// reason a start does: quitting midway through one can strand a half-removed
	// worktree. Add() runs on the Update goroutine (happens-before app.Run's
	// wait); Done() is deferred inside the command.
	startWG sync.WaitGroup

	// -- Storage and Configuration --

	program string
	autoYes bool
	// version is the build-stamped binary version ("dev" when unstamped); it
	// gates the startup update check and names the current release in hints.
	version string
	// binName is how the user invoked the binary ("atrium" or the "atr"
	// alias); update hints quote it so the suggested command actually exists
	// in the user's shell. Empty (tests) falls back to "atrium".
	binName string
	// pendingUpdateNotice buffers a one-shot update notice that arrived while
	// the hint bar couldn't render it (a modal overlay was open); the preview
	// tick re-delivers it. Empty when nothing is pending.
	pendingUpdateNotice string
	// pendingAgentNotice buffers a one-shot agent detection notice that arrived while
	// the hint bar couldn't render it; the preview tick re-delivers it. Empty when
	// nothing is pending.
	pendingAgentNotice string
	// pendingReleaseNotes buffers a one-shot "what's new" overlay that arrived
	// while another modal owned the screen; the preview tick flushes it once the
	// screen is free. nil when nothing is pending.
	pendingReleaseNotes *releaseNotesFetchedMsg
	// pendingLaunchCrash buffers a lost-session crash-at-launch modal that fired
	// while an overlay (form, rename, confirm, prompt) owned the screen; the
	// preview tick flushes it once the screen is free, so background recovery
	// never clobbers what the user was doing. nil when nothing is pending.
	pendingLaunchCrash *lostRecovery

	// storage is the interface for saving/loading data to/from the app's state
	storage *session.Storage
	// lastStatusPersist coalesces the saves that follow status changes (see
	// flushStatusPersist). Zero means no such save has happened yet, so the first
	// change writes immediately. What is still owed is held by the instances
	// themselves, as a dirty bit — see session.Instance.StatusDirty.
	lastStatusPersist time.Time
	// lostStrikes counts consecutive ticks each instance has been seen with a dead
	// tmux session, debouncing auto-recovery to Paused (see recoverLostInstances).
	lostStrikes map[*session.Instance]int

	// outboxPoisoned holds spool files whose unlink failed, so a message that
	// cannot be removed is not re-delivered on every tick for the rest of the run.
	// Deliberately in-memory only: the next launch should re-try the file rather
	// than inherit a verdict from a transient failure.
	outboxPoisoned map[string]bool
	// notifier emits the terminal bell / desktop notification when a background
	// session finishes a turn or blocks on a prompt (see app_notify.go, config
	// Notifications). nil disables notification (hand-built test homes).
	notifier *notify.Notifier
	// osChromeTitle and osChromeProgress are the fleet's presence in the terminal's
	// OS chrome — window title and OSC 9;4 taskbar progress (see refreshOSChrome,
	// config OSChrome). Recomputed once per metadata tick and read by View, which
	// hands them to Bubble Tea. Their zero values ("" and no bar) mean "no chrome",
	// so a hand-built test home renders none without needing a guard.
	osChromeTitle    string
	osChromeProgress tea.ProgressBarState
	// notifySeen tracks per-instance notification state (first-observation gate to
	// suppress the startup replay of restored statuses, plus per-edge throttle
	// timestamps). An instance absent from the map has not been observed yet, so its
	// first status is never notified — only genuine later transitions are.
	notifySeen map[*session.Instance]*notifyState
	// focused reports whether Atrium's terminal window currently has focus, tracked
	// via tea.FocusMsg/BlurMsg (WithReportFocus). Its zero value is false, so a
	// terminal that never reports focus — no DECSET 1004 — is never treated as
	// focused and notifies exactly as before (see maybeNotify, config
	// NotifyWhenFocused). While focused, background sessions stay silent.
	focused bool
	// metadataTick counts metadata poll cycles. Non-selected sessions are only fully
	// swept every metadataFullSweepEvery ticks (see tickUpdateMetadataCmd); the counter
	// drives that cadence.
	metadataTick uint64
	// splashClock is the empty-state splash's animation clock in nominal
	// 60fps frame units, advanced by the dedicated splash tick (see
	// handleSplashTick) in steps sized to the actual tick interval — so an
	// ATRIUM_SPLASH_FPS override changes smoothness, never speed. The floor
	// is pushed to the panes that render the field (splashFrame mirrors it
	// for tests). It only ever advances while the splash is on screen, so an
	// overlay freezes the field exactly where it was. Zero value is fine.
	splashClock float64
	splashFrame int
	// splashTicking marks a live splash tick loop, so the 100ms preview tick
	// (which re-arms the animation whenever the idle splash is on screen)
	// never starts a second one. The loop clears it as it dies.
	splashTicking bool
	// frameMemo memoizes the frame stack and the bubblezone scan across identical
	// frames (see frameCached). Main-thread only, like the rest of the render path.
	frameMemo memo.Cache[frameKey]
	// spinnerTicking marks a live spinner tick loop, mirroring splashTicking: the
	// 100ms preview tick and the metadata apply both re-arm it whenever a row starts
	// spinning, and neither may start a second loop. The handler clears it as it dies.
	spinnerTicking bool
	// attachGen counts terminal attaches. It is bumped by attachCommand.Run — on the
	// suspended event-loop goroutine, so it is still main-thread state — once an
	// attach succeeds. Pane-state capture cmds (metadata tick, detach sweep,
	// selection poll) stamp the generation they were created under, and their
	// results are dropped when an attach ran in between: the attach keeper may have
	// advanced the very dialog a pre-attach capture observed, so replaying its
	// PanePrompt at detach would TapEnter whatever is up NOW — e.g. accept a
	// PanePromptManual plan-approval screen that auto-yes deliberately never
	// answers. One accepted edge: a capture that STARTS after the bump (a detach
	// sweep racing an immediately-following auto-open attach) carries the current
	// generation, so it can still apply a capture taken concurrently with that next
	// attach's keeper — the next attach's own bump retires it one attach later.
	attachGen uint64
	// retiring marks instances whose teardown is in flight. The row still exists
	// during the async window, so without this the metadata poll would observe the
	// dying pane and "recover" it to Paused with a notice that both lies and hides
	// the kill's own progress row. Main-thread only, like lostStrikes.
	//
	// A mark is armed when a teardown is CONFIRMED, never when its dialog opens:
	// an entry here suppresses recovery for as long as it exists, so a mark left
	// behind by a cancelled dialog would exempt a perfectly live session from
	// lost-session recovery for the rest of the run. See armTeardown.
	retiring map[*session.Instance]bool
	// frameInFlight marks a dispatched pane capture whose paneFrameMsg has not come
	// back yet. It is the whole no-overlap guarantee for the capture chain: exactly
	// one is ever in flight, so an unresponsive tmux server parks one goroutine
	// instead of accumulating a new one every 100ms. See app_frames.go.
	frameInFlight bool
	// frameQuiet is the run of identical captures observed for the pane being
	// watched, which is what lets resolveFrameTarget stop paying for a pane that
	// has stopped moving. Main-thread only, like frameInFlight. See framegate.go.
	frameQuiet quietRun
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState
	// accountSync carries what the startup re-stamp pass changed (#470) from the
	// IO-free constructor to flushAccountStamps, which persists it and zeroes this.
	accountSync accountStampSync
	// pendingAccountSync accumulates what the accounts panel's edits re-stamped, until
	// it closes and the list they rearranged is visible again. Accumulated rather than
	// rendered per edit, so one visit's several renames become one honest notice.
	pendingAccountSync accountStampSync

	// -- State --

	// state is the current discrete state of the application
	state state
	// lastClickInstance / lastClickAt track the previous left-click on a session
	// row so a second click on the same row within doubleClickWindow is treated as
	// a double-click (attach). Pointer identity, not Title: titles are only unique
	// per repo group, and a removed instance can't be returned by InstanceAtZone,
	// so the pointer can't go stale. Bubble Tea has no native double-click event.
	lastClickInstance *session.Instance
	lastClickAt       time.Time
	// newSessionPath is the target repo path for the session currently being created.
	// It defaults to the contextual repo (the highlighted session's repo, else cwd) and
	// can be re-pointed via the directory picker in the new-session overlay. It scopes the
	// branch search and is applied to the instance before Start.
	newSessionPath string
	// fetchedPaths tracks which repo paths have had a background `git fetch` during the
	// current new-session form, so each confirmed-git target is fetched at most once per
	// form-session (re-pointing the picker back and forth doesn't spam the network).
	// Reset in openCreateForm, seeded with the initial target when it is a git repo.
	fetchedPaths map[string]bool
	// newSessionGroup is the repo-group key of the current new-session target — the
	// scope of the duplicate-title check. Set when the form opens, updated from the
	// async validity check as the directory picker moves, re-derived at submit.
	newSessionGroup string
	// titleBranchExists / titleBranchName hold the latest async branch-existence
	// verdict for the form's title (an orphan branch from a killed session would
	// make Start fail late). Display-only — submit re-verifies synchronously.
	titleBranchExists bool
	titleBranchName   string

	// scannedRepos is the latest completed background repo scan (most-recently-
	// active first), seeded from the persisted cache at startup so the first
	// form-open is populated instantly; lastScanAt is when it was produced.
	// scanGen versions scans so a superseded result is dropped, and
	// scanInFlight gates to one walk at a time.
	scannedRepos []string
	lastScanAt   time.Time
	scanGen      uint64
	scanInFlight bool

	// welcomeChecked guards the one-time first-launch welcome so it is only
	// attempted once per process (its seen-bit handles persistence across runs).
	welcomeChecked bool

	// windowWidth/windowHeight cache the last terminal size so the layout can be
	// recomputed off a synthesized size event — e.g. when an error appears or
	// clears and the panes must give up or reclaim the error box's row.
	windowWidth, windowHeight int

	// listRatio is the live fraction of width given to the session list (the rest
	// goes to the preview pane). Adjusted with < / > and persisted via appState.
	listRatio float64

	// layoutIndex is the active named layout preset (an index into layoutPresets;
	// see app_presets.go) — the base the layout key cycles from and < / > override.
	// layoutPrev is the preset active just before the current one, so Esc can back
	// out of focus to it. layoutCustom marks listRatio as a manual override of the
	// active preset's own ratio. All three are seeded from appState and persisted
	// by SetLayout, so the chosen preset survives relaunch (as listRatio does).
	layoutIndex  int
	layoutPrev   int
	layoutCustom bool

	// draggingDivider is true while the user holds the list/preview seam and drags
	// it; motion events then map the cursor column to the split (see handleMouse).
	draggingDivider bool

	// composingDiffComment is true while the diff-comment composer overlay is up
	// (#383): it routes handlePromptState to the diff-comment submit/cancel and
	// returns to stateDiffComment (not stateDefault) when the composer closes.
	composingDiffComment bool

	// -- UI Components --

	// list displays the list of instances
	list *ui.List
	// menu displays the bottom menu
	menu *ui.Menu
	// tabbedWindow displays the tabbed window with preview and diff panes
	tabbedWindow *ui.TabbedWindow
	// errBox displays error messages when the hint bar isn't there to carry them
	// (hint_bar off, or an overlay state hides the bar).
	errBox *ui.ErrBox
	// noticeGen stamps the most recent transient toast (menu notice or error
	// box); hideErrMsg carries the stamp so a stale timer can't clear a newer
	// toast early.
	noticeGen int
	// global spinner instance. we plumb this down to where it's needed
	spinner spinner.Model
	// textInputOverlay handles text input with state
	textInputOverlay *overlay.TextInputOverlay
	// promptHistoryOverlay is the reuse picker over State.PromptHistory, opened from
	// an empty prompt field; on select it inserts into textInputOverlay (#388).
	promptHistoryOverlay *overlay.PromptHistoryOverlay
	// queueOverlay manages a session's pending prompt queue (list / cancel).
	queueOverlay *overlay.QueueOverlay
	// cmdLogOverlay shows the recorded tmux/git/gh subprocesses (#372).
	cmdLogOverlay *overlay.CmdLogOverlay
	// checkpointOverlay lists a Claude session's native checkpoints (#385).
	checkpointOverlay *overlay.CheckpointOverlay
	// imageOverlay shows an agent-produced image in Atrium's own chrome (#398).
	imageOverlay *overlay.ImageOverlay
	// checkpointTarget is the session the open timeline belongs to — deliberately
	// not the live selection, so an arriving load result can be matched against the
	// session that asked for it and a result for any other one dropped.
	checkpointTarget *session.Instance
	// checkpointCancel stops the in-flight enumeration. The read is an unbounded
	// whole-file scan, so its lifetime belongs to the overlay that asked for it, not
	// to the process: closing the box, or reloading over it, must not leave a
	// goroutine decoding JSON for a result nothing will use.
	checkpointCancel context.CancelFunc
	// commandPaletteOverlay is the fuzzy-over-every-action picker (#374).
	commandPaletteOverlay *overlay.CommandPaletteOverlay
	// paletteRows is what the open palette's row indices mean: the overlay reports
	// a chosen index, and this says which action that index runs. Rebuilt on every
	// open (an action's availability is a function of the moment) and dropped on
	// close, so a stale index can never resolve to an action.
	paletteRows []paletteRow
	// customCommandsOverlay is the leader-key menu over custom_commands (#375).
	customCommandsOverlay *overlay.CustomCommandsOverlay
	// customCommandRows is what the open menu's row indices mean, in the same
	// rebuilt-per-open / dropped-on-close shape as paletteRows: a row's
	// availability is a function of the moment, so a stale index must not resolve.
	customCommandRows []customCommandRow
	// customCommands are the validated custom commands, and customCommandProblems
	// the entries validation refused. Both are computed once at construction — the
	// config cannot change under a running TUI, and a menu that revalidated on every
	// open would report a problem at a moment the user cannot connect to a cause.
	customCommands []customcmd.Command
	// pendingCustomCommandProblems buffers the startup report for the problems
	// above until the screen is free, in the shape pendingLaunchCrash uses. Nil once
	// it has been shown, so the 100ms preview tick cannot reopen it forever.
	pendingCustomCommandProblems []customcmd.Problem
	// pendingRepoScriptProblems is the same buffer for the repo_scripts entries
	// validation refused (#389), in the same shape and flushed by the same tick. A
	// refused entry is dropped rather than applied, so without this the whole symptom
	// is a setup script that silently never runs.
	pendingRepoScriptProblems []repocfg.Problem
	// pendingKeybindingProblems is the same buffer for the keybindings overrides
	// validation refused (#376). A refused override leaves its action on the default
	// key, which looks exactly like the config file not having been read at all.
	pendingKeybindingProblems []keys.Problem
	// pendingDeferredRecovery buffers the startup report for sessions whose agent the
	// host session budget would not let the load relaunch (#474), until there is a
	// frame to toast on. Zeroed once it has been shown, in the shape
	// pendingCustomCommandProblems uses, so the preview tick cannot re-toast it forever.
	pendingDeferredRecovery session.DeferredRecovery
	// pendingEarlierRecovery is the same report for a park an EARLIER process made —
	// the autoyes daemon's, arriving through internal/parkreport and already reconciled
	// against this load's fleet (#622). Held separately because it is toasted in
	// different words (earlierParkNotice: a park made hours ago must not be reported in
	// the present tense) and its delivery unlinks the spool file. Never non-empty at the
	// same time as the field above — pendingParkReports, which fills both, reads the
	// spool only when this load deferred nothing.
	pendingEarlierRecovery session.DeferredRecovery
	// runningCustomCommand is the key of the custom command currently running in the
	// background, or "". It serializes them: ui.BusyBackground is a single shared
	// slot, so a second concurrent run would make the progress row name one command
	// while another finishes and clears it.
	runningCustomCommand string
	// queueTarget is the instance the queue overlay was opened for; a cancel acts
	// on it even if the selection moves (mirrors renameTarget).
	queueTarget *session.Instance
	// stashedDraft keeps a dirty new-session form across Escape so reopening with
	// n/N restores it — the full live overlay, every field, within this run. It is
	// also mirrored to state.json (title/prompt/project only; see config.SessionDraft)
	// so a deliberate non-destructive cancel survives a crash or quit: the next bare
	// n/N rebuilds the form from that on-disk copy when no in-run stash exists.
	stashedDraft *overlay.TextInputOverlay
	// textOverlay displays text information
	textOverlay *overlay.TextOverlay
	// confirmationOverlay displays confirmation modals
	confirmationOverlay *overlay.ConfirmationOverlay
	// pendingConfirmAction is the action to run if the confirmation overlay is
	// confirmed. When pendingConfirmBusyLabel is empty it runs inline on the main
	// loop (kill and other list-mutating confirms); when the label is set it runs
	// off the UI thread via beginAsyncAction. Either way its returned message is fed
	// back through Update so errors surface in the error box.
	pendingConfirmAction tea.Cmd
	// undoRefused holds the journal records this run has already been refused, so
	// the undo key steps past them instead of re-offering the same refusal every
	// press. In memory on purpose: a relaunch re-offers them, because the reason
	// (a moved repository, a reclaimed name) may have gone away in the meantime.
	undoRefused map[string]bool
	// pendingConfirmBusyLabel is the progress label ("pushing…", "merging PR #12…")
	// for a confirm action that should run off the UI thread. Empty means run the
	// action inline (the legacy synchronous path). Set by confirmAction.
	pendingConfirmBusyLabel string
	// pendingConfirmArm is bookkeeping the staged action needs applied on the update
	// thread the moment it is CONFIRMED — never when the dialog merely opens. A
	// teardown uses it to mark its instances retiring and join startWG (armTeardown);
	// doing that at dialog-open time leaks both on every cancel, because a declined
	// confirmation drops its action without ever running it. Reset by confirmAction,
	// so a dialog that stages none cannot inherit the previous one's.
	pendingConfirmArm func()
	// hostCap is the host-derived soft session cap (config.DefaultSessionCap() at
	// construction). It is the Limit used when max_sessions is unset; a field rather
	// than a live call so tests can pin it independent of the runner's CPU count.
	hostCap int
	// readIdentity reads the Claude login recorded in a config dir, for the
	// launch-time identity gate (see account_identity.go). A field for the same
	// reason as hostCap: tests pin an identity without needing a real config dir.
	// Nil falls back to config.ReadAccountIdentity, so a home built anywhere still
	// verifies rather than silently skipping the check.
	readIdentity config.IdentityReadFunc
	// pendingOverCap holds the fully-resolved creation staged behind a host-capacity
	// confirmation: crossing the soft cap opens a confirm whose acceptance emits
	// proceedOverCapMsg, and Update then spawns this plan. Nil when no confirm is
	// pending; consumed (set nil) when the plan is spawned.
	pendingOverCap *spawnPlan
	// pendingExhausted holds a creation staged behind an all-members-rate-limited
	// confirmation (see confirmAllExhausted). Its account is already pinned to the
	// soonest-to-reset member. Nil when no such confirm is pending.
	pendingExhausted *spawnPlan
	// quitRequested records that the user asked to quit while a session was still
	// Loading. handleQuit defers the exit (a Loading session isn't yet persisted,
	// so quitting would drop it); handleInstanceStarted re-invokes handleQuit once
	// the in-flight Start completes, which quits once nothing is Loading. Touched
	// only on the Update goroutine, so it needs no synchronization.
	quitRequested bool
	// renameOverlay handles editing a session's display label
	renameOverlay *overlay.RenameOverlay
	// settingsOverlay is the in-TUI configuration panel. It edits appConfig in
	// place; applySettingChange persists and live-applies each change.
	settingsOverlay *overlay.SettingsOverlay

	// settingsRail remembers which settings category was current, so reopening the panel
	// returns to it within a run. The overlay is reconstructed on every ',', so the memory has
	// to live out here. Deliberately not persisted to state.json (spec §7).
	//
	// It is a *int because a plain int's zero value is 0 — the All settings entry, the one rail
	// entry spec §4 explicitly excludes as the landing. nil means "not opened yet this run", so
	// the first ',' gets railDefaultIndex().
	settingsRail *int

	// noticeSettingKey is the settings row the transient notice currently on screen points
	// at, so the ',' that notice advertises lands on that row instead of the remembered
	// category. Armed with the notice and cleared when it goes, so an unrelated ',' minutes
	// later is unaffected. See settingNotice.
	noticeSettingKey string

	// pendingConfirmSettingKey is the settings row the open confirmation dialog's copy
	// promises ',' will open — the host-capacity dialog and nothing else today. Empty means
	// ',' is inert in stateConfirm, which is what keeps the key from silently cancelling an
	// unrelated confirmation.
	pendingConfirmSettingKey string
	// accountsOverlay is the in-TUI Claude/GitHub/Antigravity account manager
	// (stateAccounts). It edits appConfig in place; handleAccountsState persists each change.
	accountsOverlay *overlay.AccountsOverlay
	// welcomeOverlay is the interactive first-run setup modal (stateWelcome).
	welcomeOverlay *overlay.WelcomeOverlay
	// pathWarned guards the one-shot startup warning that the effective program
	// is not installed, so it fires at most once per launch.
	pathWarned bool
	// renameTarget is the instance the rename overlay was opened for. It is captured
	// when the overlay opens so the new label lands on the right session even if the
	// list selection moves while the overlay is open (e.g. during async auto-naming).
	renameTarget *session.Instance
	// generatingName guards against launching a second auto-name request while one
	// is already in flight; the row itself is now a background busy line.
	generatingName bool
	// actionInFlight is true while a confirm/pause/resume action runs off the UI
	// thread (see beginAsyncAction). It drives the StateBusy progress row, gates
	// re-entry, and makes handleKeyPress swallow mutating keys until the action's
	// completion message lands. Touched only on the Update loop, so it needs no
	// synchronization.
	actionInFlight bool

	// smartDispatchSeededTitle is the deterministic placeholder title the async form
	// opened with. The routing call's (better) title replaces it only while the field
	// still equals this — i.e. the user hasn't typed their own.
	smartDispatchSeededTitle string

	// hintScreen is the frozen, labeled capture hint mode is acting on.
	// hintTyped is the entered label prefix, and hintOpenVariant records
	// whether any hint character was typed uppercase (selecting copy+open).
	hintScreen      *hints.Screen
	hintTyped       string
	hintOpenVariant bool

	// lastStatusPollSelection is the instance instanceChanged last fired an immediate
	// status poll for. instanceChanged runs on every 100ms preview tick, so we only
	// re-poll when the selection actually changes (or when a detach resets this to nil),
	// not 10×/s — which would also perturb the tick-based idle hysteresis.
	lastStatusPollSelection *session.Instance

	// selectedSince records when the current selection was last changed. The
	// read-dwell (markSeenAfterDwell) requires the row to have been selected this
	// long before clearing its unread state, so cursor travel through rows never
	// marks them seen. Zero until instanceChanged first stamps it (~the first
	// preview tick); markSeenAfterDwell treats the zero value as "no dwell yet",
	// never as a dwell long since passed.
	selectedSince time.Time
}

// newHome loads the persisted config, state, and instances from disk — the IO
// the TUI is built on — then hands them to assembleHome, which builds the root
// model with no further IO. Splitting the two keeps assembleHome a pure,
// unit-testable constructor and lets a load failure return an error (surfaced by
// Run) instead of an os.Exit from inside the constructor.
func newHome(ctx context.Context, program string, autoYes bool, version, binName string) (*home, error) {
	// Load application config
	appConfig := config.LoadConfig()

	// Resolve the agent OOM margin once, before any session is constructed (instance
	// restore below and every later new/resume all read it through newSession), so
	// each agent pane raises its Linux oom_score_adj above the shared tmux server's
	// and the kernel OOM killer sheds one recoverable session instead of the server
	// (every session). Off Linux / when disabled this resolves to a harmless no-op.
	tmux.SetAgentOOMMargin(appConfig.GetAgentOOMMargin())

	// Pre-accept Claude's workspace trust for the worktrees root before any
	// session starts (opt-in; best-effort — on failure the trust dialog simply
	// appears per worktree, as it would without the feature). Done once here on
	// the main thread: the trust target is the root, not a per-session path,
	// and session Starts run on background goroutines where concurrent
	// rewrites of a shared .claude.json would race each other.
	maybeTrustWorktreesRoot(appConfig, program)

	// Activate the configured UI theme before any component is constructed, so
	// theme.Current() is correct everywhere it's read (assembleHome's spinner
	// included). The palette and the glyph set (plain vs Nerd-Font) are
	// independent axes.
	theme.Set(appConfig.GetTheme())
	theme.SetGlyphSet(appConfig.GetGlyphSet())
	// The detection ladder's lower rung, read once, for terminals that will never
	// answer the OSC 11 query Init also sends. It sits here so the ladder's order is
	// visible in one place: Init's query outranks this if an answer arrives.
	theme.SetScheme(initialScheme())

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Load saved instances. A session whose tmux session is gone is *relaunched* by
	// this call, so it is rationed by the host session budget; deferred names the
	// sessions it left parked rather than oversubscribing the host at launch (#474).
	instances, deferred, err := storage.LoadInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load instances: %w", err)
	}

	h := assembleHome(ctx, program, autoYes, version, binName, appConfig, appState, storage, instances)
	// Buffered rather than surfaced here: newHome runs before the program starts, so
	// there is no frame to toast on yet. The preview tick flushes it, in the shape
	// every other startup notice uses. Set after assembleHome so that constructor
	// keeps its IO-free, fixed-argument shape.
	// A park an earlier process made reaches us only through the spool, because it is
	// already persisted as an ordinary Paused row and this load therefore refuses nothing
	// on its account (#622). pendingParkReports owns which of the two buffers is filled.
	h.pendingDeferredRecovery, h.pendingEarlierRecovery = pendingParkReports(deferred, instances, time.Now())
	// Write back any account identities assembleHome healed (#470). Doing it here
	// keeps that constructor IO-free, and persisting eagerly is what lets `atrium ls`
	// and the daemon — separate processes that read the stored rows raw — agree with
	// what the list shows.
	h.flushAccountStamps()
	return h, nil
}

func (m *home) Init() tea.Cmd {
	// The spinner loop self-chains: each spinner.TickMsg advances the frame and
	// returns the next one. It runs only while some row is actually drawing a spinner
	// (armSpinnerTick), because every message it delivers costs a full frame rebuild.
	return tea.Batch(
		m.armSpinnerTick(), // only if a row is already Running/Loading; revived by the tick
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		m.armFrameCapture(0), // pane-capture chain: the only place tmux content is read
		m.armSplashTick(),    // idle splash animation, live from the first frame
		tickUpdateMetadataCmd(m.ctx, m.snapshotActiveInstances(), m.list.GetSelectedInstance(), true, m.attachGen, m.usagePolicy()), // first tick: full sweep
		m.updateCheckCmd(),   // nil (inert) is fine: tea.Batch skips nil cmds
		m.driftCheckCmd(),    // agent-heuristic drift hint
		m.agentCheckCmd(),    // background agent CLI detection
		m.releaseNotesCmd(),  // nil (inert) is fine: tea.Batch skips nil cmds
		m.startProjectScan(), // nil (disabled) is likewise skipped
		m.sweepUndoCmd(),     // release undo records past their horizon
		m.requestSchemeCmd(), // OSC 11: nil unless theme: auto, and nil is fine here
	)
}

// sweepUndoCmd expires the undo journal off the render path. A data dir left
// closed for a month has records to release, and this is the one moment we know
// the sweep has not run in a while.
func (m *home) sweepUndoCmd() tea.Cmd {
	return func() tea.Msg {
		m.sweepUndoJournal(time.Now())
		return nil
	}
}

// View is the whole frame plus the terminal state it should be rendered under.
//
// Bubble Tea v2 made the second half declarative. Alt screen, focus reporting and
// mouse capture used to be program options fixed at launch (and, for the mouse, a
// command sent again whenever the setting changed); they are now fields recomputed
// with the content on every frame, so there is exactly one place each is decided
// and no way for the terminal's state to drift out of step with the model's.
//
// The content itself is unchanged — viewContent below is the v1 View body.
func (m *home) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	// Report terminal focus/blur (DECSET 1004) so notifications can stay silent
	// while the user is watching the fleet. Atrium's TUI runs in the real terminal
	// (not inside its private tmux server), so these events reach us directly; a
	// terminal that ignores 1004 simply never sends them, and an unknown focus is
	// treated as "not focused" — never permanent silence.
	v.ReportFocus = true
	// v.KeyboardEnhancements is deliberately left at its zero value, and the omission
	// is a decision rather than an oversight (#396) — TestView_RequestsNoKeyboardEnhancements
	// pins it.
	//
	// The enhancement Atrium actually wants, key disambiguation, is not in this struct:
	// Bubble Tea requests it unconditionally and cannot be talked out of it, so
	// shift+enter already arrives on a terminal that speaks the protocol. Every field
	// here only ADDS flags on top of that, and Atrium needs none of them.
	//
	// ReportEventTypes is the one with a concrete hazard. It makes the terminal report
	// releases and repeats; every handler here matches the concrete tea.KeyPressMsg, so
	// today a release would be silently DROPPED rather than double-dispatched. The
	// hazard is that it is latent: tea.KeyMsg is an interface both KeyPressMsg and
	// KeyReleaseMsg satisfy, with identical String(), so the first handler written
	// against it turns one physical keystroke into two dispatches — and nothing about
	// writing that handler would look wrong.
	//
	// The other three change how ordinary keys are DELIVERED — as escape codes, with
	// alternate codes, with associated text — and this app dispatches on msg.String()
	// end to end. Nothing here has measured them against that, and requesting an
	// unmeasured input path buys nothing, so the zero value is the whole argument.
	// (ReportAssociatedText is inert on its own; Bubble Tea's own field doc says it
	// only has an effect with ReportAllKeysAsEscapeCodes.)
	//
	// Mouse capture is opt-out (config `mouse`, default on). With it off we never
	// enable cell-motion reporting, so every mouse event stays with the terminal and
	// its native select-to-copy works unmodified — the fix for terminals whose
	// selection the capture would otherwise hijack (#397).
	if m.appConfig.GetMouse() {
		v.MouseMode = tea.MouseModeCellMotion
	}
	// The fleet in the terminal's own chrome (config `os_chrome`, see refreshOSChrome
	// for the derivation and chrome for what the strings mean). Declaring them here
	// rather than writing the escapes ourselves is what makes them self-clearing: the
	// renderer emits an empty title and a reset bar whenever it releases the terminal
	// — to a tmux attach, at quit, and on a signal shutdown — so no stale "5 running"
	// can outlive the frame that claimed it.
	v.WindowTitle = m.osChromeTitle
	// A fresh ProgressBar every frame, deliberately. The renderer keeps the pointer it
	// is handed and diffs the value behind it, so handing back one stored pointer and
	// mutating it in place would compare the new state against itself and find no
	// change — freezing the bar, skipping its reset at exit, and (because the
	// whole-view equality check reads through the same pointer) dropping entire frames
	// whenever the content happened not to move.
	v.ProgressBar = tea.NewProgressBar(m.osChromeProgress, 0)
	return v
}

func (m *home) viewContent() string {
	// The screensaver replaces the whole frame — no list, panes, or hint bar —
	// so it renders (and returns) before the normal layout is even assembled.
	if m.state == stateScreensaver {
		return ui.SplashScreensaver(m.windowWidth, m.windowHeight, m.splashFrame)
	}

	// In focus mode the list is hidden and the tabbed window fills the full
	// terminal width; every other preset joins the list and preview side by side.
	// Omit the list entirely (rather than render it at zero width) so no sliver of
	// panel border remains — updateHandleWindowSizeEvent already gave the tabbed
	// window the whole width to match.
	// Rendered once and reused: this is the single most expensive thing Atrium
	// builds (#565), and the two branches below used to call it twice, the second
	// call discarding the first's frame in every layout but focus mode.
	tabbed := m.tabbedWindow.String()
	listAndPreview := tabbed
	if !m.listHidden() {
		listAndPreview = lipgloss.JoinHorizontal(lipgloss.Top, m.list.String(), tabbed)
	}

	// The auto-accept safety banner pins the top row while armed (#378); its row is
	// reserved by topBannerHeight in the layout budget, so prepending it here keeps
	// the frame exactly windowHeight tall.
	k := frameKey{body: listAndPreview}
	if m.autoYesArmed() {
		k.banner, k.hasBanner = m.autoYesBanner(m.windowWidth), true
	}
	// The hint bar claims a row whenever menuVisible (in plain navigation, always —
	// blank when the bar is off, #438); the error box claims one only while a notice is
	// showing. A component that claims no row is omitted entirely: JoinVertical treats
	// an empty string as a blank line, so an unused component must be dropped, not
	// rendered empty — whereas the reserved-but-quiet menu row renders a real blank
	// line on purpose. That is why each component carries a presence flag into
	// frameKey rather than relying on "" to mean absent. menuVisible and menuHeight in
	// updateHandleWindowSizeEvent stay in lockstep so the row the menu occupies here
	// is exactly the row the layout reserved for it.
	if m.menuVisible() {
		k.menu, k.hasMenu = m.menu.String(), true
	}
	if m.errBox.HasContent() {
		k.errBox, k.hasErr = m.errBox.String(), true
	}
	// Stack and scan the frame here, before any overlay composites on top. scanFrame
	// strips the (zero-width) Mark escapes and records each zone's bounds. Doing it
	// now keeps marker sequences out of overlay.PlaceOverlay, whose column-by-column
	// line splicing could otherwise cut a row's start/end marker pair; bounds stay
	// correct because overlays render at origin and don't shift the content below.
	//
	// The scan is indirected through a package var purely so it can be measured and
	// counted: zone.Scan is a rune-by-rune walk of the whole frame that also pushes
	// one ZoneInfo per zone across a channel to a lock-taking worker, and it used to
	// run on every one of the ~32 frames a second an idle Atrium built (#546) — hence
	// frameCached, which now skips it, and the join, for the identical ones. A
	// benchmark cannot isolate its share, and a test cannot assert it was skipped,
	// without a seam. Production always uses zone.Scan.
	mainView := m.frameCached(k)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true)
	} else if m.state == stateHelp || m.state == stateInfo {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true)
	} else if m.state == stateConfirm {
		if m.confirmationOverlay == nil {
			log.ErrorLog.Printf("confirmation overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.confirmationOverlay.Render(), mainView, true)
	} else if m.state == stateRename {
		if m.renameOverlay == nil {
			log.ErrorLog.Printf("rename overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.renameOverlay.Render(), mainView, true)
	} else if m.state == stateQueue {
		if m.queueOverlay == nil {
			log.ErrorLog.Printf("queue overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.queueOverlay.Render(), mainView, true)
	} else if m.state == stateHistory {
		if m.promptHistoryOverlay == nil {
			log.ErrorLog.Printf("prompt history overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.promptHistoryOverlay.Render(), mainView, true)
	} else if m.state == stateCmdLog {
		if m.cmdLogOverlay == nil {
			log.ErrorLog.Printf("command-log overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.cmdLogOverlay.Render(), mainView, true)
	} else if m.state == stateCheckpoints {
		if m.checkpointOverlay == nil {
			log.ErrorLog.Printf("checkpoint overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.checkpointOverlay.Render(), mainView, true)
	} else if m.state == stateCommandPalette {
		if m.commandPaletteOverlay == nil {
			log.ErrorLog.Printf("command palette overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.commandPaletteOverlay.Render(), mainView, true)
	} else if m.state == stateCustomCommands {
		if m.customCommandsOverlay == nil {
			log.ErrorLog.Printf("custom commands overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.customCommandsOverlay.Render(), mainView, true)
	} else if m.state == stateSettings {
		if m.settingsOverlay == nil {
			log.ErrorLog.Printf("settings overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.settingsOverlay.Render(), mainView, true)
	} else if m.state == stateWelcome {
		if m.welcomeOverlay == nil {
			log.ErrorLog.Printf("welcome overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.welcomeOverlay.Render(), mainView, true)
	} else if m.state == stateAccounts {
		if m.accountsOverlay == nil {
			log.ErrorLog.Printf("accounts overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.accountsOverlay.Render(), mainView, true)
	} else if m.state == stateImagePreview {
		if m.imageOverlay == nil {
			log.ErrorLog.Printf("image overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.imageOverlay.Render(), mainView, true)
	}

	return mainView
}
