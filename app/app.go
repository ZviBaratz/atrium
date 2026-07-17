// Package app contains the Bubble Tea program at the heart of Atrium. Its root
// model, home, owns the instance list, the discrete UI states (default / new /
// prompt / help / confirm / rename), and the per-tick poll loop that refreshes
// each session's status and diff; the ui package's components render what home
// orchestrates.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/hints"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

// doubleClickWindow is the maximum delay between two left-clicks on the same
// session row for the second to count as a double-click (attach). Bubble Tea has
// no native double-click event, so it is detected by timing here.
const doubleClickWindow = 400 * time.Millisecond

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
	p := tea.NewProgram(
		h,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
		// Normalize SS3 Home/End (ESC O H/F) that a terminal left in application-cursor
		// mode emits, which bubbletea v1 otherwise mis-decodes into literal "OH"/"OF".
		tea.WithInput(newSS3HomeEndReader(os.Stdin)),
		// Tie the program to the lifecycle context so a SIGTERM (which cancels
		// ctx in main) also stops the TUI loop, not just the subprocesses.
		tea.WithContext(ctx),
	)
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
	// The default file, for sessions that inherit the ambient CLAUDE_CONFIG_DIR
	// (unrouted / catch-all account).
	if err := tmux.EnsureWorktreesRootTrusted(root); err != nil {
		log.WarningLog.Printf("worktrees-root trust skipped: %v", err)
	}
	// Plus each configured Claude account's own config dir: an account-routed
	// session reads trust from $CLAUDE_CONFIG_DIR/.claude.json, so the opt-in
	// wouldn't cover it otherwise (#266). Dedup against the home file, which an
	// account pointing at ~ would otherwise write twice (harmless, but noisy).
	home, _ := os.UserHomeDir()
	homeConfig := filepath.Join(home, ".claude.json")
	for _, acct := range cfg.ClaudeAccounts {
		dir := acct.ResolvedConfigDir()
		if dir == "" || filepath.Join(dir, ".claude.json") == homeConfig {
			continue
		}
		if err := tmux.EnsureAccountWorktreesRootTrusted(dir, root); err != nil {
			log.WarningLog.Printf("worktrees-root trust skipped for account %q: %v", acct.Name, err)
		}
	}
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
	// stateWelcome is the interactive first-launch setup modal: pick a default
	// agent from the ones detected on PATH, then start the first session.
	stateWelcome
	// stateAccounts is the Claude/GitHub account manager modal.
	stateAccounts
	// stateScreensaver is the full-window splash easter egg (backtick from the
	// default state); any key or click returns to stateDefault.
	stateScreensaver
)

type home struct {
	ctx context.Context

	// startWG tracks in-flight new-session Start goroutines so app.Run can join
	// them after p.Run() returns and reconcile a session the Bubble Tea event loop
	// bypassed on signal shutdown / force-quit (#282). Add() runs on the Update
	// goroutine (happens-before app.Run's wait); Done() is deferred inside the
	// start command.
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
	// lostStrikes counts consecutive ticks each instance has been seen with a dead
	// tmux session, debouncing auto-recovery to Paused (see recoverLostInstances).
	lostStrikes map[*session.Instance]int
	// notifier emits the terminal bell / desktop notification when a background
	// session finishes a turn or blocks on a prompt (see app_notify.go, config
	// Notifications). nil disables notification (hand-built test homes).
	notifier *notify.Notifier
	// notifySeen tracks per-instance notification state (first-observation gate to
	// suppress the startup replay of restored statuses, plus per-edge throttle
	// timestamps). An instance absent from the map has not been observed yet, so its
	// first status is never notified — only genuine later transitions are.
	notifySeen map[*session.Instance]*notifyState
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
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

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

	// draggingDivider is true while the user holds the list/preview seam and drags
	// it; motion events then map the cursor column to the split (see handleMouse).
	draggingDivider bool

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
	// queueOverlay manages a session's pending prompt queue (list / cancel).
	queueOverlay *overlay.QueueOverlay
	// cmdLogOverlay shows the recorded tmux/git/gh subprocesses (#372).
	cmdLogOverlay *overlay.CmdLogOverlay
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
	// pendingConfirmBusyLabel is the progress label ("pushing…", "merging PR #12…")
	// for a confirm action that should run off the UI thread. Empty means run the
	// action inline (the legacy synchronous path). Set by confirmAsyncAction.
	pendingConfirmBusyLabel string
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
	// accountsOverlay is the in-TUI Claude/GitHub account manager (stateAccounts).
	// It edits appConfig in place; handleAccountsState persists each change.
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
	// is already in flight, and drives the "Generating name…" hint-bar state.
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

	// Pre-accept Claude's workspace trust for the worktrees root before any
	// session starts (opt-in; best-effort — on failure the trust dialog simply
	// appears per worktree, as it would without the feature). Done once here on
	// the main thread: the trust target is the root, not a per-session path,
	// and session Starts run on background goroutines where concurrent
	// rewrites of ~/.claude.json would race each other.
	maybeTrustWorktreesRoot(appConfig, program)

	// Activate the configured UI theme before any component is constructed, so
	// theme.Current() is correct everywhere it's read (assembleHome's spinner
	// included). The palette and the glyph set (plain vs Nerd-Font) are
	// independent axes.
	theme.Set(appConfig.Theme)
	theme.SetNerdFont(appConfig.GetNerdFont())

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Load saved instances
	instances, err := storage.LoadInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load instances: %w", err)
	}

	return assembleHome(ctx, program, autoYes, version, binName, appConfig, appState, storage, instances), nil
}

func (m *home) Init() tea.Cmd {
	// Upon starting, we want to start the spinner. Whenever we get a spinner.TickMsg, we
	// update the spinner, which sends a new spinner.TickMsg. I think this lasts forever lol.
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		m.armSplashTick(), // idle splash animation, live from the first frame
		tickUpdateMetadataCmd(m.ctx, m.snapshotActiveInstances(), m.list.GetSelectedInstance(), true, m.attachGen), // first tick: full sweep
		m.updateCheckCmd(),   // nil (inert) is fine: tea.Batch skips nil cmds
		m.driftCheckCmd(),    // agent-heuristic drift hint
		m.releaseNotesCmd(),  // nil (inert) is fine: tea.Batch skips nil cmds
		m.startProjectScan(), // nil (disabled) is likewise skipped
	)
}

func (m *home) View() string {
	// The screensaver replaces the whole frame — no list, panes, or hint bar —
	// so it renders (and returns) before the normal layout is even assembled.
	if m.state == stateScreensaver {
		return ui.SplashScreensaver(m.windowWidth, m.windowHeight, m.splashFrame)
	}

	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, m.list.String(), m.tabbedWindow.String())

	parts := []string{listAndPreview}
	// The hint bar and error box each claim a row only when they have something to
	// show; otherwise the last visible component sits flush on the final row with no
	// trailing blank line. (JoinVertical treats an empty string as a blank line, so
	// an unused component must be omitted, not just rendered empty.) menuVisible and
	// menuHeight in updateHandleWindowSizeEvent stay in lockstep so the row the menu
	// occupies here is exactly the row the layout reserved for it.
	if m.menuVisible() {
		parts = append(parts, m.menu.String())
	}
	if m.errBox.HasContent() {
		parts = append(parts, m.errBox.String())
	}
	mainView := lipgloss.JoinVertical(lipgloss.Left, parts...)
	// Scan the frame here, before any overlay composites on top. zone.Scan strips
	// the (zero-width) Mark escapes and records each zone's bounds. Doing it now
	// keeps marker sequences out of overlay.PlaceOverlay, whose column-by-column
	// line splicing could otherwise cut a row's start/end marker pair; bounds stay
	// correct because overlays render at origin and don't shift the content below.
	mainView = zone.Scan(mainView)

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
	} else if m.state == stateCmdLog {
		if m.cmdLogOverlay == nil {
			log.ErrorLog.Printf("command-log overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.cmdLogOverlay.Render(), mainView, true)
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
	}

	return mainView
}
