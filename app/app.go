// Package app contains the Bubble Tea program at the heart of Atrium. Its root
// model, home, owns the instance list, the discrete UI states (default / new /
// prompt / help / confirm / rename), and the per-tick poll loop that refreshes
// each session's status and diff; the ui package's components render what home
// orchestrates.
package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
	"golang.org/x/term"
)

// doubleClickWindow is the maximum delay between two left-clicks on the same
// session row for the second to count as a double-click (attach). Bubble Tea has
// no native double-click event, so it is detected by timing here.
const doubleClickWindow = 400 * time.Millisecond

// Run is the main entrypoint into the application.
func Run(ctx context.Context, program string, autoYes bool) error {
	// Initialize the global bubblezone manager before the first render. The list
	// and tab views Mark() rows/tabs via the package-level manager, which panics
	// ("manager not initialized") until NewGlobal() is called. Idempotent, so it
	// coexists with the test init()s that also call it.
	zone.NewGlobal()
	p := tea.NewProgram(
		newHome(ctx, program, autoYes),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
		// Tie the program to the lifecycle context so a SIGTERM (which cancels
		// ctx in main) also stops the TUI loop, not just the subprocesses.
		tea.WithContext(ctx),
	)
	_, err := p.Run()
	if ctx.Err() != nil {
		// Signal-driven shutdown: Bubble Tea reports the kill as an error
		// (ErrProgramKilled), but for us it is a clean exit.
		return nil
	}
	return err
}

// copyToClipboard writes text to the system clipboard. It is a package var so tests
// can substitute a fake without touching the host clipboard.
var copyToClipboard = clipboard.WriteAll

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
	if err := tmux.EnsureWorktreesRootTrusted(root); err != nil {
		log.WarningLog.Printf("worktrees-root trust skipped: %v", err)
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
)

type home struct {
	ctx context.Context

	// -- Storage and Configuration --

	program string
	autoYes bool

	// storage is the interface for saving/loading data to/from the app's state
	storage *session.Storage
	// lostStrikes counts consecutive ticks each instance has been seen with a dead
	// tmux session, debouncing auto-recovery to Paused (see recoverLostInstances).
	lostStrikes map[*session.Instance]int
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

	// -- State --

	// state is the current discrete state of the application
	state state
	// lastClickTitle / lastClickAt track the previous left-click on a session row
	// so a second click on the same row within doubleClickWindow is treated as a
	// double-click (attach). Bubble Tea has no native double-click event.
	lastClickTitle string
	lastClickAt    time.Time
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
	// textOverlay displays text information
	textOverlay *overlay.TextOverlay
	// confirmationOverlay displays confirmation modals
	confirmationOverlay *overlay.ConfirmationOverlay
	// pendingConfirmAction is the action to run if the confirmation overlay is
	// confirmed. It is executed on the main loop and its returned message is fed
	// back through Update so errors surface in the error box.
	pendingConfirmAction tea.Cmd
	// renameOverlay handles editing a session's display label
	renameOverlay *overlay.RenameOverlay
	// settingsOverlay is the in-TUI configuration panel. It edits appConfig in
	// place; applySettingChange persists and live-applies each change.
	settingsOverlay *overlay.SettingsOverlay
	// renameTarget is the instance the rename overlay was opened for. It is captured
	// when the overlay opens so the new label lands on the right session even if the
	// list selection moves while the overlay is open (e.g. during async auto-naming).
	renameTarget *session.Instance
	// generatingName guards against launching a second auto-name request while one
	// is already in flight, and drives the "Generating name…" hint-bar state.
	generatingName bool

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

func newHome(ctx context.Context, program string, autoYes bool) *home {
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
	// theme.Current() is correct everywhere it's read. Set once, never mutated.
	theme.Set(appConfig.Theme)

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		fmt.Printf("Failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	h := &home{
		ctx: ctx,
		spinner: spinner.New(spinner.WithSpinner(spinner.Spinner{
			Frames: theme.Current().Glyphs.SpinnerFrames,
			FPS:    theme.Current().Glyphs.SpinnerFPS,
		})),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(ctx)),
		errBox:       ui.NewErrBox(),
		storage:      storage,
		lostStrikes:  make(map[*session.Instance]int),
		appConfig:    appConfig,
		program:      program,
		autoYes:      autoYes,
		state:        stateDefault,
		appState:     appState,
		listRatio:    appState.GetListRatio(),
	}
	h.list = ui.NewList(&h.spinner)
	// With the always-on hint bar enabled, the bar already carries the first-run
	// keys; suppress the list's centered empty hint so guidance isn't duplicated.
	h.list.SetShowEmptyHint(!appConfig.GetHintBar())

	// Load saved instances
	instances, err := storage.LoadInstances(ctx)
	if err != nil {
		fmt.Printf("Failed to load instances: %v\n", err)
		os.Exit(1)
	}

	// Add loaded instances to the list
	for _, instance := range instances {
		// Call the finalizer immediately.
		h.list.AddInstance(instance)()
		if autoYes {
			instance.AutoYes = true
		}
	}
	// Restore folded groups only after every instance is loaded — AddInstance auto-expands the
	// group it inserts into, so applying persisted folds earlier would be undone by the loop.
	h.list.SetCollapsedRepos(appState.GetCollapsedRepos())

	return h
}

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// The session list takes listRatio of the width (default 30%); the preview pane
	// takes the rest. listRatio is user-adjustable with < / > (clamped in appState).
	// A zero value means the home was built without seeding the ratio (e.g. a struct
	// literal in tests); fall back to the persisted/default value so the list never
	// collapses to nothing.
	if m.listRatio <= 0 {
		m.listRatio = m.appState.GetListRatio()
	}
	listWidth := int(float32(msg.Width) * float32(m.listRatio))
	tabsWidth := msg.Width - listWidth

	m.windowWidth, m.windowHeight = msg.Width, msg.Height

	// The hint bar is contextual (see menuVisible): it claims a row only during the
	// inline interactions where it carries unique information, and the panes reclaim
	// that row during plain navigation and behind overlays. The error box likewise
	// takes a row only while an error is showing. Whichever rows are claimed, the
	// composed frame is always exactly msg.Height tall and never floats in a
	// centered band; transitions that flip menuVisible call recomputeLayout.
	menuHeight := 0
	if m.menuVisible() {
		menuHeight = 1
	}
	errHeight := 0
	if m.errBox.HasError() {
		errHeight = 1
	}
	contentHeight := max(1, msg.Height-menuHeight-errHeight)
	m.errBox.SetSize(int(float32(msg.Width)*0.9), errHeight)

	m.tabbedWindow.SetSize(tabsWidth, contentHeight)
	m.list.SetSize(listWidth, contentHeight)

	if m.textInputOverlay != nil {
		// Pass the full terminal height: the create form sizes its own sections to fit (and the
		// plain prompt overlay applies its own fraction), so it needs to know the real height
		// rather than a pre-scaled slice of it.
		m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), msg.Height)
	}
	if m.textOverlay != nil {
		m.textOverlay.SetWidth(int(float32(msg.Width) * 0.6))
	}
	if m.settingsOverlay != nil {
		// Pass the full terminal size: the panel caps its own width and windows
		// its rows to fit short terminals.
		m.settingsOverlay.SetSize(msg.Width, msg.Height)
	}
	if m.confirmationOverlay != nil {
		// The dialog keeps its classic width on normal terminals and shrinks with
		// narrow ones; it was the one overlay excluded from resize handling.
		m.confirmationOverlay.SetWidth(confirmWidth(msg.Width))
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, menuHeight)
}

// menuVisible reports whether the hint bar should occupy a row. Inline
// interactions always get it (stateFilter shows its accept/clear cue, and a
// background name generation its progress). Modal overlays
// (prompt/rename/confirm/help/info) render their own instructions, so the bar
// behind them would be a redundant strip. Plain navigation shows the always-on
// hint line unless the user turned it off (hint_bar in config.json), which
// restores the chrome-free interface.
func (m *home) menuVisible() bool {
	switch m.state {
	case stateFilter:
		return true
	case statePrompt, stateRename, stateConfirm, stateHelp, stateInfo, stateSettings:
		return false
	default: // stateDefault (and the empty list)
		return m.generatingName || m.appConfig.GetHintBar()
	}
}

// recomputeLayout re-runs the size calculation off the cached terminal size. Use
// it when something other than a resize changes the vertical budget — e.g. an
// error appearing or clearing toggles whether the error box claims a row, or a
// state transition flips menuVisible.
func (m *home) recomputeLayout() {
	if m.windowWidth == 0 || m.windowHeight == 0 {
		return
	}
	m.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: m.windowWidth, Height: m.windowHeight})
}

// applySettingChange persists the config after the settings panel changed the
// given row, then live-applies whatever that field controls. Fields without a
// case here are read live at their point of use (auto_attach, max_sessions,
// kill_double_tap_confirm) or only consumed by later operations (branch_prefix,
// default_program on the next session; daemon_poll_interval on the next daemon
// run), so persisting is all they need.
func (m *home) applySettingChange(key string) tea.Cmd {
	if err := config.SaveConfig(m.appConfig); err != nil {
		return m.handleError(err)
	}
	switch key {
	case "theme":
		// Styles read theme.Current() lazily at render time, so swapping the
		// palette plus a forced repaint restyles the whole UI in place.
		theme.Set(m.appConfig.Theme)
		return tea.Sequence(tea.ClearScreen, tea.WindowSize())
	case "hint_bar":
		// Mirror the newHome seeding: the list shows its inline key hint only
		// when the always-on bar is off.
		if m.list != nil {
			m.list.SetShowEmptyHint(!m.appConfig.GetHintBar())
		}
		m.recomputeLayout() // the bar claims or releases its row
	case "session_context_bar", "tmux_config_override":
		// Re-render the managed tmux conf so sessions started from now on pick
		// the change up; live sessions keep their current status line (tmux only
		// reads the config when a server starts).
		if err := tmux.Init(m.appConfig.TmuxConfigOverride, m.appConfig.GetSessionContextBar()); err != nil {
			return m.handleError(err)
		}
	case "auto_yes":
		// In-TUI auto-accept is driven by each instance's AutoYes flag (the
		// daemon only runs while the TUI is closed — main.go stops it before
		// app.Run and relaunches it on exit from the persisted config).
		m.autoYes = m.appConfig.AutoYes
		if m.list != nil {
			for _, inst := range m.list.GetInstances() {
				inst.AutoYes = m.appConfig.AutoYes
			}
		}
	}
	return nil
}

// listRatioStep is how much each < / > press shifts the list/preview split.
const listRatioStep = 0.05

// adjustListRatio nudges the list/preview split by delta, persists the clamped
// value, re-pushes sizes to every pane, and refreshes the preview at its new width.
// appState owns the clamp, so the stored and live values stay in lockstep.
func (m *home) adjustListRatio(delta float64) tea.Cmd {
	if err := m.appState.SetListRatio(m.listRatio + delta); err != nil {
		return m.handleError(err)
	}
	m.listRatio = m.appState.GetListRatio()
	m.recomputeLayout()
	return m.instanceChanged()
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
		tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()),
	)
}

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		if msg.gen == m.noticeGen {
			if m.menu != nil {
				m.menu.ClearNotice()
			}
			m.errBox.Clear()
			m.recomputeLayout() // reclaim the error row; panes grow back by one
		}
	case previewTickMsg:
		m.markSeenAfterDwell(time.Now())
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
	case autoNameDoneMsg:
		m.generatingName = false
		if msg.err != nil {
			// The progress row goes away and we return to plain navigation; surface the
			// failure and leave the name untouched rather than applying a junk fallback.
			m.menu.SetState(ui.StateDefault)
			m.recomputeLayout() // the progress bar gave up its row; panes reclaim it
			return m, m.handleError(msg.err)
		}
		// Offer the generated name through the existing rename overlay so the user
		// can confirm or edit it before it commits.
		m.renameTarget = msg.instance
		m.renameOverlay = overlay.NewRenameOverlay(msg.name)
		m.state = stateRename
		m.recomputeLayout() // the progress bar gave up its row; the overlay self-documents
		return m, nil
	case metadataUpdateDoneMsg:
		if recoverLostInstances(msg.results, m.lostStrikes) {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				log.ErrorLog.Printf("failed to persist recovered sessions: %v", err)
			}
		}
		for _, r := range msg.results {
			// Skip instances that were paused while metadata was being computed, or
			// that were just recovered to Paused above because their session died.
			if r.sessionLost || r.instance.Paused() {
				continue
			}
			applyPaneState(r.instance, r.state)
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
		}
		m.pushSessionContexts()
		cmds := deliverReadyPrompts(msg.results)
		cmds = append(cmds, tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()))
		return m, tea.Batch(cmds...)
	case instancePolledMsg:
		// An off-cadence single-instance refresh (selection change / detach). Apply the
		// state but do NOT reschedule the metadata tick — that chain is owned by
		// metadataUpdateDoneMsg above; touching it here would spawn a second tick loop.
		if msg.instance.GetStatus() != session.Paused {
			applyPaneState(msg.instance, msg.state)
		}
		return m, nil
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		// Mouse wheel is routed by what it hovers, only in the default state
		// (overlays own the screen otherwise, mirroring the left-click gate
		// below). Over the session list it moves the selection like ↑/↓; over
		// the right tabbed pane it scrolls the active tab; anywhere else (menu /
		// hint bar / error rows) it is ignored. Zones are resolved against the
		// frame scanned in View(); before the first scan both InBounds checks
		// return false, so the wheel does nothing.
		if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
			if m.state != stateDefault {
				return m, nil
			}
			// Over the list: move the selection, regardless of the selected
			// instance's state (paused / nil), exactly like the keyboard paths.
			if m.list.InPanelBounds(msg) {
				if msg.Button == tea.MouseButtonWheelUp {
					m.list.Up()
				} else {
					m.list.Down()
				}
				return m, m.instanceChanged()
			}
			// Over the right tabbed pane: scroll the active tab. A nil or
			// paused selection has nothing to scroll.
			if m.tabbedWindow.InBounds(msg) {
				selected := m.list.GetSelectedInstance()
				if selected == nil || selected.Paused() {
					return m, nil
				}
				if msg.Button == tea.MouseButtonWheelUp {
					m.tabbedWindow.ScrollUp()
				} else {
					m.tabbedWindow.ScrollDown()
				}
				return m, nil
			}
			return m, nil
		}
		// Left-click selects a session row, switches the active tab, or (on a quick
		// second click of the same row) attaches. Only in the default state — when
		// an overlay is up the rows behind it still have recorded bounds, so a click
		// there must be ignored. Click regions are resolved against the frame
		// scanned in View().
		if msg.Button == tea.MouseButtonLeft && m.state == stateDefault {
			if inst := m.list.InstanceAtZone(msg); inst != nil {
				m.list.SelectInstance(inst)
				// A second click on the same row within doubleClickWindow attaches,
				// mirroring Enter, via the tea.Exec attach path (attachExec). The first
				// click already selected the row, so it is the current selection.
				now := time.Now()
				if m.lastClickTitle == inst.Title && now.Sub(m.lastClickAt) <= doubleClickWindow {
					m.lastClickTitle = ""
					if inst.Paused() || inst.GetStatus() == session.Loading || !inst.TmuxAlive() {
						return m, m.instanceChanged()
					}
					if m.tabbedWindow.IsInTerminalTab() {
						return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
					}
					// inst is the current selection, so list.Attach targets it;
					// killTarget carries it for the ctrl-x in-session kill flow.
					return m, m.attachExec(m.list.Attach, inst)
				}
				m.lastClickTitle = inst.Title
				m.lastClickAt = now
				return m, m.instanceChanged()
			}
			// A click on a repo-group header toggles its fold, mirroring ←/→.
			// Persist the new collapsed set exactly like the keyboard paths do.
			if key, ok := m.list.HeaderAtZone(msg); ok {
				if m.list.ClickHeader(key) {
					if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
						return m, m.handleError(err)
					}
					return m, m.instanceChanged()
				}
				return m, nil
			}
			if idx, ok := m.tabbedWindow.TabAtZone(msg); ok {
				m.tabbedWindow.SetActiveTab(idx)
				return m, m.instanceChanged()
			}
		}
		return m, nil
	case branchSearchDebounceMsg:
		// Debounce timer fired — check if this is still the current filter version
		if m.textInputOverlay == nil {
			return m, nil
		}
		if msg.version != m.textInputOverlay.BranchFilterVersion() {
			return m, nil // stale, a newer debounce is pending
		}
		return m, m.runBranchSearch(msg.filter, msg.version)
	case branchSearchResultMsg:
		if m.textInputOverlay != nil {
			if msg.err {
				m.textInputOverlay.SetBranchSearchError(msg.version)
			} else {
				m.textInputOverlay.SetBranchResults(msg.branches, msg.version)
			}
		}
		return m, nil
	case targetValidityDebounceMsg:
		// Debounce timer fired — only check if this is still the current target.
		if m.textInputOverlay == nil || msg.path != m.newSessionPath {
			return m, nil
		}
		return m, m.runValidityCheck(msg.path)
	case targetValidityResultMsg:
		// Apply only if the result is for the still-current target, so a stale check
		// (the user has navigated on) can't clobber the indicator.
		if m.textInputOverlay != nil && msg.path == m.newSessionPath {
			m.textInputOverlay.SetTargetValidity(msg.valid, msg.direct, msg.headBranch)
			// A confirmed git target gets one background fetch per form-session, so its
			// branch list reflects current remote refs. The verdict (not the path change)
			// is the trigger: filesystem browsing through non-repos never fetches.
			if msg.valid && !msg.direct && !m.fetchedPaths[msg.path] {
				if m.fetchedPaths == nil {
					m.fetchedPaths = make(map[string]bool)
				}
				m.fetchedPaths[msg.path] = true
				return m, m.runBranchFetch(msg.path)
			}
		}
		return m, nil
	case branchFetchDoneMsg:
		// A background fetch finished. If its path is still the current target, re-run
		// the branch search so newly-fetched refs appear without retyping the filter; a
		// completion for an abandoned path is dropped. (SetResults' version check still
		// guards against the user having typed during the search itself.)
		if m.textInputOverlay == nil || msg.path != m.newSessionPath {
			return m, nil
		}
		return m, m.runBranchSearch(m.textInputOverlay.BranchFilter(), m.textInputOverlay.BranchFilterVersion())
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		// First launch ever: show the one-time welcome once the size is known.
		m.maybeShowWelcome()
		return m, nil
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action
		return m, m.instanceChanged()
	case attachFinishedMsg:
		// A tea.Exec terminal attach returned (the user detached, or it failed to
		// start). tea.Exec's RestoreTerminal has already repainted the frame; refine
		// the layout and selection-derived panes from here.
		m.state = stateDefault
		if msg.err != nil {
			return m, m.handleError(msg.err)
		}
		// The user was watching this session until a moment ago, so if the agent
		// finished while attached, the poll below settles a stale Running to Ready —
		// a synthetic transition that must not flag unread. An agent still working
		// at detach is observed as Running first, which clears the suppression, so a
		// later genuine completion flags normally. Armed before BOTH detach paths:
		// the sibling-cycle early return below and the normal fresh poll.
		if msg.killTarget != nil {
			msg.killTarget.ArmReadySuppression()
		}
		// Honor an in-session kill (Ctrl+X) requested before detach. killTarget is the
		// attached instance (nil for the terminal tab, which has no kill key); keep
		// tea.WindowSize() so the confirmation overlay redraws at the correct
		// dimensions after the full-screen attach (confirmKill only mutates state).
		if msg.killTarget != nil && msg.killTarget.AttachKillRequested() {
			return m, tea.Batch(tea.WindowSize(), m.confirmKill(msg.killTarget))
		}
		// A sibling-cycle key (Ctrl+PgUp/PgDn) detaches with a direction; re-attach the
		// neighbouring session in the repo group, keeping cycling inside Atrium's model.
		// killTarget is the session just detached (nil for the terminal tab, which has
		// no cycle keys).
		if msg.killTarget != nil {
			if next := m.cycleTarget(msg.killTarget); next != nil {
				m.list.SelectInstance(next)
				m.pushOneContext(next)
				return m, m.attachExec(next.Attach, next)
			}
		}
		// Polling stalled while attached, so the smoothing state is stale: refresh the
		// selected session at face value (fresh) rather than letting a stale "running" on a
		// now-idle agent linger — and re-run through the hysteresis — until it settles. Pin
		// the poll tracker to the current selection first so instanceChanged's own
		// (hysteresis) poll doesn't also fire for the same instance.
		selected := m.list.GetSelectedInstance()
		m.lastStatusPollSelection = selected
		return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), pollSelectedCmd(selected, true))
	case infoMsg:
		// An action requested a dismissible info modal (e.g. an actionable resume
		// error). Unlike handleError's transient box, this persists until dismissed.
		return m, m.showInfo(string(msg))
	case instanceStartedMsg:
		// Select the instance that just started (or failed)
		m.list.SelectInstance(msg.instance)

		if msg.err != nil {
			m.list.Kill()
			return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
		}

		// Own the Loading -> Running transition here, on the main thread. Start()
		// deliberately no longer sets Running from its background goroutine (that
		// raced the UI/poll readers and could leave the session stuck on the
		// "Setting up workspace..." splash); this message arrives after Start()
		// completed, so the write is race-free. applyPaneState refines it to
		// Ready/NeedsInput on later ticks.
		msg.instance.SetStatus(session.Running)

		// Save after successful start
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			return m, m.handleError(err)
		}
		m.recordRecentPath(msg.instance.Path)
		// First successful session start retires the one-time welcome. This is the single
		// chokepoint every start (inline `n` and the `N` form) funnels through, so the
		// welcome re-shows on every launch until the user has actually created a session —
		// a dismissal alone no longer burns it (see showHelpScreen). Best-effort persist.
		if seen := m.appState.GetHelpScreensSeen(); seen&(helpTypeWelcome{}.mask()) == 0 {
			if err := m.appState.SetHelpScreensSeen(seen | helpTypeWelcome{}.mask()); err != nil {
				log.WarningLog.Printf("failed to persist welcome-seen state: %v", err)
			}
		}
		if m.autoYes {
			msg.instance.AutoYes = true
		}

		// A prompt from the N form is delivered later by the metadata tick loop,
		// once the agent is past its startup/trust screen and ready for input
		// (see deliverReadyPrompts). Sending here races the agent's boot and lands
		// keystrokes in the trust dialog instead of the input box.
		m.menu.SetState(ui.StateDefault)

		if m.shouldAutoOpen(msg.instance) {
			// Drop straight into the new session, mirroring the KeyEnter attach path.
			// Attach msg.instance directly rather than via m.list.Attach(): a background
			// instanceStartedMsg from another freshly-created session could have moved
			// the list selection by now. The attach runs through tea.Exec, which hands
			// the terminal to tmux and repaints on detach; post-detach handling — an
			// in-session Ctrl+X kill request, keyed on msg.instance since the selection
			// may have drifted, or a sibling-cycle request — lands in the
			// attachFinishedMsg handler.
			return m, m.attachExec(msg.instance.Attach, msg.instance)
		}

		return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
		return m, m.handleError(err)
	}
	return m, tea.Quit
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	// Ctrl+L forces a full repaint. The alt-screen renderer updates incrementally and
	// never erases lines, so it desyncs (leaving accumulating ghost rows) if the terminal
	// ever renders a line wider than measured — e.g. a font lacking a combined emoji glyph.
	// theme.SanitizeWidth prevents the known cases; this is the universal manual-redraw
	// escape hatch for any residual artifact, in any state.
	if msg.String() == "ctrl+l" {
		return m, tea.ClearScreen
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateInfo {
		return m.handleInfoState(msg)
	}

	if m.state == statePrompt {
		// Handle cancel via ctrl+c before delegating to the overlay
		if msg.String() == "ctrl+c" {
			return m, m.cancelPromptOverlay()
		}

		// Use the new TextInputOverlay component to handle all key events
		shouldClose, branchFilterChanged := m.textInputOverlay.HandleKeyPress(msg)

		// Check if the form was submitted or canceled
		if shouldClose {
			if m.textInputOverlay.IsCanceled() {
				return m, m.cancelPromptOverlay()
			}

			if !m.textInputOverlay.IsSubmitted() {
				m.textInputOverlay = nil
				m.state = stateDefault
				return m, nil
			}

			prompt := m.textInputOverlay.GetValue()

			// The new-session form creates the instance only now, on submit, so no row
			// appears in the list while the user is still filling it in.
			if m.textInputOverlay.IsCreateForm() {
				return m, m.createSessionFromForm(prompt)
			}

			// Quick-send overlay: fire the message at the selected running session and drop
			// straight back to the list (no new-session help — the session is already up).
			selected := m.list.GetSelectedInstance()
			if selected == nil {
				m.textInputOverlay = nil
				m.state = stateDefault
				return m, nil
			}
			if err := selected.SendPrompt(prompt); err != nil {
				return m, m.handleError(err)
			}
			m.textInputOverlay = nil
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)
			return m, tea.Sequence(tea.WindowSize(), m.instanceChanged())
		}

		// If the target directory changed in the picker, re-scope the branch search to
		// the new repo: invalidate in-flight results for the old repo, then schedule a
		// fresh (debounced) search with the current branch filter.
		if newPath := m.textInputOverlay.GetSelectedPath(); newPath != "" && newPath != m.newSessionPath {
			m.newSessionPath = newPath
			// Re-scope the branch search and (debounced, off the hot path) re-check the
			// new target's state (directory? git repo or direct session?). The check is
			// async because filesystem browsing changes the selected path almost every
			// keystroke, and a synchronous git subprocess per keystroke would stutter the
			// UI. Reset the indicator to "unknown" up front so the previous path's verdict
			// isn't asserted for the new path during the debounce window; the async
			// result re-sets it.
			m.textInputOverlay.ClearTargetValidity()
			version := m.textInputOverlay.InvalidateBranchSearch()
			return m, tea.Batch(
				m.scheduleBranchSearch(m.textInputOverlay.BranchFilter(), version),
				m.scheduleValidityCheck(newPath),
			)
		}

		// Schedule a debounced branch search if the filter changed
		if branchFilterChanged {
			filter := m.textInputOverlay.BranchFilter()
			version := m.textInputOverlay.BranchFilterVersion()
			return m, m.scheduleBranchSearch(filter, version)
		}

		return m, nil
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
		if shouldClose {
			confirmed := m.confirmationOverlay.Confirmed
			action := m.pendingConfirmAction
			m.state = stateDefault
			m.confirmationOverlay = nil
			m.pendingConfirmAction = nil
			if confirmed && action != nil {
				// Run the action here, on the main loop, because it mutates shared
				// model state (list, terminals); a tea.Cmd would run it in a
				// goroutine and race Update. Feed only the resulting message back
				// through the runtime so a returned error reaches the error box.
				resultMsg := action()
				return m, func() tea.Msg { return resultMsg }
			}
			return m, nil
		}
		return m, nil
	}

	// Handle rename state. This must run before the global q/ctrl+c quit handling below so
	// those keys edit (or cancel) the label instead of quitting the app.
	if m.state == stateRename {
		shouldClose := m.renameOverlay.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}

		submitted := m.renameOverlay.IsSubmitted()
		value := m.renameOverlay.Value()
		deep := m.renameOverlay.IsDeep()
		// Apply to the instance the overlay was opened for, not the currently
		// selected one — they can differ if the selection moved while the overlay
		// was open (notably during async auto-naming).
		target := m.renameTarget
		m.renameOverlay = nil
		m.renameTarget = nil
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)

		if submitted && target != nil {
			if deep {
				if err := m.deepRename(target, value); err != nil {
					return m, m.handleError(err)
				}
			} else {
				target.SetDisplayName(value)
				if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
					return m, m.handleError(err)
				}
			}
		}
		return m, m.instanceChanged()
	}

	// Handle settings state. Like the other overlay states, this must run before
	// the global quit handling so q/esc and printable keys reach the panel. The
	// overlay mutates appConfig in place and reports which row changed; persisting
	// and live-applying that change is applySettingChange's job.
	if m.state == stateSettings {
		closed, changedKey := m.settingsOverlay.HandleKeyPress(msg)
		var cmds []tea.Cmd
		if changedKey != "" {
			if cmd := m.applySettingChange(changedKey); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if closed {
			m.settingsOverlay = nil
			m.state = stateDefault
			m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row
			cmds = append(cmds, tea.WindowSize())
		}
		return m, tea.Batch(cmds...)
	}

	// Handle filter state. This must run before the global quit handling so that printable keys
	// and Esc update the filter instead of quitting. The list holds the query (single source of
	// truth); note that letter keys must reach the default case, so we cannot reserve "j"/"k"
	// (vim navigation elsewhere) as commit keys — they have to be typeable into the query.
	if m.state == stateFilter {
		switch msg.String() {
		case "esc":
			// Esc clears the filter and returns to default.
			m.list.ClearFilter()
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)
			m.recomputeLayout() // the hint bar gave up its row; panes reclaim it
			return m, m.instanceChanged()
		case "enter", "down":
			// Accept the current query and move focus to the filtered list.
			m.list.SetFilterActive(false)
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)
			m.recomputeLayout() // the hint bar gave up its row; panes reclaim it
			return m, m.instanceChanged()
		case "backspace", "ctrl+h":
			if q := m.list.FilterQuery(); q != "" {
				// Remove the last rune (handles multi-byte correctly).
				runes := []rune(q)
				m.list.SetFilter(string(runes[:len(runes)-1]))
			}
			return m, m.instanceChanged()
		default:
			// Append printable characters to the filter query.
			if len(msg.Runes) > 0 {
				m.list.SetFilter(m.list.FilterQuery() + string(msg.Runes))
			}
			return m, m.instanceChanged()
		}
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
		// If in preview tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode() {
			// Use the selected instance from the list
			selected := m.list.GetSelectedInstance()
			err := m.tabbedWindow.ResetPreviewToNormalMode(selected)
			if err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		// If in terminal tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode() {
			m.tabbedWindow.ResetTerminalToNormalMode()
			return m, m.instanceChanged()
		}
		// A committed filter (typed with /, accepted with Enter) is still
		// narrowing the list; Esc clears it, the expected escape hatch.
		if m.list.FilterQuery() != "" {
			m.list.ClearFilter()
			return m, m.instanceChanged()
		}
	}

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeySettings:
		m.state = stateSettings
		m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
		m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
		return m, tea.WindowSize()
	case keys.KeyPrompt:
		// The full entry point: focus starts on the project picker.
		return m, m.openCreateForm(false)
	case keys.KeyNew:
		// The quick entry point: the same form, focused on the title, so
		// "n → type a name → ⌃S" creates a session in the contextual repo.
		return m, m.openCreateForm(true)
	case keys.KeyQuickSend:
		// Open a compose box to fire an ad-hoc message at the selected running session
		// without attaching. Only meaningful when the agent is up and accepting input;
		// other states explain the guard instead of silently swallowing the key.
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.Paused() {
			return m, m.handleInfoNotice("session is paused — press r to resume before sending")
		}
		if !selected.Started() || selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		m.state = statePrompt
		m.textInputOverlay = overlay.NewQuickSendOverlay("Send to " + selected.DisplayName())
		return m, tea.WindowSize()
	case keys.KeyCopyBranch:
		// Yank the selected session's branch name to the system clipboard for handoff
		// to a PR, a teammate, or a git command. Both outcomes are acknowledged on the
		// hint row: without a toast, success and failure were indistinguishable from
		// the keyboard.
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.Branch == "" {
			return m, m.handleInfoNotice("no branch to copy yet")
		}
		if err := copyToClipboard(selected.Branch); err != nil {
			return m, m.handleError(fmt.Errorf("copy branch: %w", err))
		}
		return m, m.handleInfoNotice(fmt.Sprintf("branch '%s' copied", selected.Branch))
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp()
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown()
		return m, m.instanceChanged()
	case keys.KeyShrinkList:
		return m, m.adjustListRatio(-listRatioStep)
	case keys.KeyGrowList:
		return m, m.adjustListRatio(+listRatioStep)
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyShiftTab:
		m.tabbedWindow.ToggleReverse()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyTabPreview, keys.KeyTabDiff, keys.KeyTabTerminal:
		// Direct tab jump by number, complementing Tab/Shift+Tab cycling. The
		// three KeyNames are consecutive, so the offset from KeyTabPreview is the
		// tab index (PreviewTab/DiffTab/TerminalTab are likewise 0/1/2).
		m.tabbedWindow.SetActiveTab(int(name - keys.KeyTabPreview))
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		return m, m.confirmKill(m.list.GetSelectedInstance())
	case keys.KeyFilter:
		// Resume editing a committed query rather than resetting it — re-pressing
		// / to refine a filter should not force retyping it. Esc still clears.
		m.list.SetFilterActive(true)
		m.state = stateFilter
		m.menu.SetState(ui.StateFilter)
		m.recomputeLayout() // the hint bar now claims a row; shrink the panes to fit
		return m, m.instanceChanged()
	case keys.KeyRename:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		m.renameTarget = selected
		m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName())
		m.state = stateRename
		return m, nil
	case keys.KeyAutoName:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if m.generatingName {
			return m, m.handleInfoNotice("already generating a name")
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		// The model call (and the full diff it needs) happen in the background Cmd so
		// the UI stays responsive; only the instance and prompt are captured here.
		m.generatingName = true
		m.menu.SetState(ui.StateGeneratingName)
		m.recomputeLayout() // the progress bar now claims a row; shrink the panes to fit
		return m, runAutoNameCmd(m.ctx, selected, selected.Prompt)
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		// A direct (non-git) session has nothing to push. Fail fast rather than prompting
		// for confirmation and only then erroring. (The menu also hides this action.)
		if selected.IsDirect() {
			return m, m.handleError(fmt.Errorf("push is not available for a direct (non-git) session"))
		}

		// Create the push action as a tea.Cmd
		pushAction := func() tea.Msg {
			// Default commit message with timestamp
			commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s", selected.DisplayName(), time.Now().Format(time.RFC822))
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err = worktree.PushChanges(commitMsg, true); err != nil {
				return err
			}
			return nil
		}

		// Show confirmation modal
		message := fmt.Sprintf("Push changes from session '%s'?", selected.DisplayName())
		return m, m.confirmAction(message, pushAction)
	case keys.KeyPause:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}

		// A direct (non-git) session has no worktree to free and runs in the user's
		// real directory, so pausing it would only detach a still-running agent.
		// Warn instead of pausing. (The menu also hides this action for direct sessions.)
		if selected.IsDirect() {
			return m, m.handleError(fmt.Errorf("pause is not available for a direct (non-git) session; it runs in place with no worktree to free"))
		}

		// Pause: commit changes and free the worktree. The branch name is copied to
		// the clipboard inside Pause(); the always-on hint bar carries the reminder.
		if err := selected.Pause(); err != nil {
			return m, m.handleError(err)
		}
		m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
		return m, m.instanceChanged()
	case keys.KeyMoveUp:
		if m.list.MoveUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveDown:
		if m.list.MoveDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupUp:
		if m.list.MoveGroupUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupDown:
		if m.list.MoveGroupDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapse:
		if m.list.Collapse() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyExpand:
		if m.list.Expand() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapseAll:
		if m.list.ToggleCollapseAll() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		if !selected.Paused() {
			return m, m.handleInfoNotice("session is already running — only paused sessions resume")
		}
		return m, m.resumeSelected(selected)
	case keys.KeyEnter, keys.KeyAttachToggle:
		// KeyAttachToggle (ctrl+q) mirrors the in-session detach key
		// (session/tmux/tmux.go): on the list it attaches the selected session,
		// making ctrl+q a symmetric attach/detach toggle. It funnels through the
		// same guards as enter.
		if m.list.NumInstances() == 0 {
			return m, nil
		}
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.Paused() {
			return m, m.handleInfoNotice("session is paused — press r to resume")
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		if !selected.TmuxAlive() {
			return m, m.handleInfoNotice("session has no live terminal — resume it or kill it")
		}
		// Attach to the session (or its terminal tab) via tea.Exec, which hands the
		// terminal to tmux and repaints on detach; the hint bar carries the ctrl-q
		// detach reminder. Post-detach handling lands in the attachFinishedMsg handler.
		if m.tabbedWindow.IsInTerminalTab() {
			// The terminal tab has no in-session kill key, so no kill target.
			return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
		}
		return m, m.attachExec(m.list.Attach, selected)
	default:
		return m, nil
	}
}

// cycleTarget returns the sibling to re-attach when an in-session cycle key
// (Ctrl+PgUp/PgDn) ended the attach, or nil for a normal detach. Cycling stays
// inside Atrium's model — each hop is a real detach+attach, correctly sized via the
// existing attach path. (A tmux switch-client would avoid the repaint but mis-sizes
// panes here, since every session permanently holds its own pty client.)
// SiblingInGroup returns attached itself when there is no other attachable sibling,
// making a stray cycle key a harmless re-attach.
func (m *home) cycleTarget(attached *session.Instance) *session.Instance {
	switch attached.AttachExitReason() {
	case tmux.DetachNext:
		return m.list.SiblingInGroup(attached, +1)
	case tmux.DetachPrev:
		return m.list.SiblingInGroup(attached, -1)
	}
	return nil
}

// pushSessionContexts refreshes the in-session context bar for every live session.
// SetContext caches per session, so an unchanged tick costs only string comparisons
// rather than tmux subprocesses. No-op when the feature is disabled.
func (m *home) pushSessionContexts() {
	if !m.appConfig.GetSessionContextBar() {
		return
	}
	for _, inst := range m.list.GetInstances() {
		m.pushOneContext(inst)
	}
}

// pushOneContext composes and pushes the context bar for a single session, skipping
// sessions that have no live tmux pane to render it in (unstarted, paused, dead).
func (m *home) pushOneContext(inst *session.Instance) {
	if !m.appConfig.GetSessionContextBar() || !inst.Started() || inst.Paused() || !inst.TmuxAlive() {
		return
	}
	name, left := ui.ComposeSessionContext(inst, ui.RepoKey(inst))
	if err := inst.SetContext(name, left); err != nil {
		log.WarningLog.Printf("failed to push session context for %q: %v", inst.Title, err)
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
// deepRename renames the selected instance's title, git branch, worktree directory, and tmux
// session to value, then clears the cosmetic label so the list shows the corrected name. It
// rejects an empty title or one already used by another instance (Title is the storage key).
// Runs synchronously on the main event loop — the rename is a handful of instant subprocesses,
// and the git/tmux structs guard the fields the background poll loop reads.
func (m *home) deepRename(selected *session.Instance, value string) error {
	if value == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	for _, inst := range m.list.GetInstances() {
		if inst != selected && inst.Title == value {
			return fmt.Errorf("a session named %q already exists", value)
		}
	}
	if err := selected.Rename(value); err != nil {
		return err
	}
	selected.SetDisplayName("")
	return m.storage.SaveInstances(m.list.GetInstances())
}

// attachCommand adapts a blocking tmux attach into a tea.ExecCommand so Bubble
// Tea releases the terminal before the attach and restores+repaints it after —
// on the event loop, via execMsg, which is the framework's supported path for a
// blocking terminal takeover. (Calling ReleaseTerminal/RestoreTerminal directly
// from inside Update blocks the event loop for the whole attach and leaves the
// renderer/input reader wedged.) Run also puts stdin in raw mode for the
// duration: ReleaseTerminal restores cooked mode, where Ctrl+Q (ASCII 17 = XON)
// is swallowed by IXON flow control and never reaches the detach reader. The
// Set* methods are no-ops because the attach copies os.Stdin/os.Stdout directly
// rather than through the streams Bubble Tea would inject.
type attachCommand struct {
	attach func() (chan struct{}, error)
}

func (a attachCommand) Run() error {
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if oldState, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		} else {
			// Stay in cooked mode where IXON swallows Ctrl+Q, so detach won't work and
			// the attach looks like a hang. Log a breadcrumb (to the file, not the
			// tmux-owned terminal) instead of failing silently.
			log.WarningLog.Printf("failed to set raw mode for attach; Ctrl+Q detach may not work: %v", err)
		}
	}
	ch, err := a.attach()
	if err != nil {
		return err
	}
	<-ch
	return nil
}

func (a attachCommand) SetStdin(io.Reader)  {}
func (a attachCommand) SetStdout(io.Writer) {}
func (a attachCommand) SetStderr(io.Writer) {}

// attachExec hands the terminal to a tmux attach via tea.Exec and reports the
// outcome as an attachFinishedMsg once the user detaches. killTarget is the
// attached instance whose in-session Ctrl+X kill request the handler should honor
// on detach, or nil when the attach has no kill key (the terminal tab).
func (m *home) attachExec(attach func() (chan struct{}, error), killTarget *session.Instance) tea.Cmd {
	// Attaching is the strongest form of visiting: clear the unread state before
	// handing the terminal over. killTarget is nil only for the terminal tab,
	// which the selection dwell covers instead.
	if killTarget != nil {
		killTarget.MarkSeen()
	}
	return tea.Exec(attachCommand{attach: attach}, func(err error) tea.Msg {
		return attachFinishedMsg{err: err, killTarget: killTarget}
	})
}

func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}

	// Refresh the newly-selected session's status immediately rather than waiting for the
	// next 500ms metadata tick. instanceChanged also fires on every 100ms preview tick, so
	// gate on an actual selection change (a detach resets the tracker to nil to force a
	// refresh) to avoid polling 10×/s.
	if selected != m.lastStatusPollSelection {
		m.lastStatusPollSelection = selected
		m.selectedSince = time.Now()
		return pollSelectedCmd(selected, false)
	}
	return nil
}

// readDwell is how long a row must stay selected — and its unread state visible —
// before the selection counts as a read. Long enough that cursor travel and a
// just-landed result don't self-clear; short enough that glancing at the preview does.
const readDwell = 1500 * time.Millisecond

// markSeenAfterDwell clears the selected instance's unread state once the user has
// demonstrably seen it: the row has been selected for readDwell (the preview pane
// shows its live content) AND the unread flag itself is at least readDwell old (a
// reply landing on an already-selected row stays bright long enough to register).
// Gated on stateDefault because the 100ms preview tick fires in every UI state,
// including overlays that occlude the preview.
func (m *home) markSeenAfterDwell(now time.Time) {
	if m.state != stateDefault {
		return
	}
	sel := m.list.GetSelectedInstance()
	if sel == nil || !sel.Unread() {
		return
	}
	// Zero selectedSince means instanceChanged hasn't stamped a selection yet
	// (the first tick runs this before it): no dwell has been observed, and the
	// zero value must not read as "selected ~forever" — that would wipe a
	// restored unread bit (whose unreadAt is also zero) ~100ms after launch.
	if m.selectedSince.IsZero() {
		return
	}
	if now.Sub(m.selectedSince) < readDwell || now.Sub(sel.UnreadAt()) < readDwell {
		return
	}
	sel.MarkSeen()
}

// hideErrMsg implements tea.Msg and clears the transient toast (menu notice or
// error box). gen identifies which toast the timer belongs to: a stale timer's
// message must not clear a newer toast.
type hideErrMsg struct {
	gen int
}

// previewTickMsg implements tea.Msg and triggers a preview update
type previewTickMsg struct{}

type instanceChangedMsg struct{}

// attachFinishedMsg is delivered after a tea.Exec terminal attach returns (the
// user detached or the attach errored). It carries the attach error, if any, and
// the attached instance so the post-detach handler can surface an error and honor
// an in-session Ctrl+X kill request. killTarget is nil for the terminal tab, which
// has no kill key.
type attachFinishedMsg struct {
	err        error
	killTarget *session.Instance
}

// infoMsg requests a dismissible information modal carrying actionable text.
// Confirmation-action callbacks return it to surface a message that must persist
// until the user dismisses it, instead of the auto-hiding transient error box.
type infoMsg string

type instanceStartedMsg struct {
	instance *session.Instance
	err      error
}

// shouldAutoOpen reports whether a freshly started session should be attached
// automatically. It is gated by the auto_attach config flag and skipped when the
// instance carries an initial prompt (delivered asynchronously by the metadata tick,
// which is paused while attached). The Started/TmuxAlive guards avoid attaching a
// session that did not come up — and, because Started() short-circuits before
// TmuxAlive() (which dereferences tmuxSession), keep unstarted instances (e.g. in
// tests) off both the panic and the attach path.
func (m *home) shouldAutoOpen(inst *session.Instance) bool {
	return m.appConfig.GetAutoAttach() && inst.Prompt == "" && inst.Started() && inst.TmuxAlive()
}

// branchSearchDebounceMsg fires after the debounce interval to trigger a search.
type branchSearchDebounceMsg struct {
	filter  string
	version uint64
}

// branchSearchResultMsg carries search results back to Update. err marks a failed
// search (e.g. the target is not a git repo) so the picker can clear its loading state
// and show an error hint instead of spinning forever.
type branchSearchResultMsg struct {
	branches []string
	version  uint64
	err      bool
}

const branchSearchDebounce = 150 * time.Millisecond

// scheduleBranchSearch returns a debounced tea.Cmd: sleeps, then triggers a search message.
func (m *home) scheduleBranchSearch(filter string, version uint64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return branchSearchDebounceMsg{filter: filter, version: version}
	}
}

// branchFetchDoneMsg signals that a background `git fetch` for a candidate target repo
// finished (successfully or not), keyed by the path it fetched so a completion for a
// path the user has navigated away from can be dropped.
type branchFetchDoneMsg struct {
	path string
}

// runBranchFetch returns a tea.Cmd that fetches the repo's remote refs in the background
// and reports completion. FetchBranches is best-effort (errors are ignored — offline or
// remoteless repos simply keep their local view), so completion always re-triggers a
// search via the branchFetchDoneMsg handler.
func (m *home) runBranchFetch(path string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		git.FetchBranches(ctx, path)
		return branchFetchDoneMsg{path: path}
	}
}

// targetValidityDebounceMsg fires after the debounce interval to trigger an async
// state check (targetValidity) of the chosen target path.
type targetValidityDebounceMsg struct {
	path string
}

// targetValidityResultMsg carries the target-state check result back to Update, keyed by
// the path it was computed for so a stale result (the user has since moved on) is dropped.
// headBranch is the resolved name of the branch HEAD points at (only for git targets),
// shown in the branch picker's default base option.
type targetValidityResultMsg struct {
	path          string
	valid, direct bool
	headBranch    string
}

// scheduleValidityCheck returns a debounced tea.Cmd mirroring scheduleBranchSearch: it
// sleeps, then asks for an async target-state check. Debouncing keeps targetValidity's
// git subprocess off the keystroke hot path while the user types/browses a path.
func (m *home) scheduleValidityCheck(path string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return targetValidityDebounceMsg{path: path}
	}
}

// runValidityCheck returns a tea.Cmd that runs targetValidity in the background and
// reports the result tagged with the path it was computed for.
func (m *home) runValidityCheck(path string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		valid, direct, head := targetValidity(ctx, path)
		return targetValidityResultMsg{path: path, valid: valid, direct: direct, headBranch: head}
	}
}

// runBranchSearch returns a tea.Cmd that performs the git search in the background.
// It searches the current new-session target repo (m.newSessionPath), captured at call
// time so it reflects the directory chosen in the picker rather than the process cwd.
func (m *home) runBranchSearch(filter string, version uint64) tea.Cmd {
	target := m.newSessionPath
	ctx := m.ctx
	return func() tea.Msg {
		if target == "" {
			var err error
			if target, err = os.Getwd(); err != nil {
				return nil
			}
		}
		branches, err := git.SearchBranches(ctx, target, filter)
		if err != nil {
			log.WarningLog.Printf("branch search failed: %v", err)
			return branchSearchResultMsg{version: version, err: true}
		}
		return branchSearchResultMsg{branches: branches, version: version}
	}
}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance       *session.Instance
	state          tmux.PaneState
	readyForPrompt bool
	// sessionLost is set when a started, non-paused instance's tmux pane no longer
	// exists. The main thread recovers it to Paused (see recoverLostInstances).
	sessionLost bool
	diffStats   *git.DiffStats
}

// applyPaneState maps a polled pane state onto an instance's status. Prompt handling
// depends on AutoYes: with it on, auto-answer (TapEnter is a no-op otherwise); with it
// off the session is blocked on the user, so surface NeedsInput rather than a spinner.
// PaneUnknown (an unreadable pane) leaves the status untouched.
func applyPaneState(inst *session.Instance, state tmux.PaneState) {
	switch state {
	case tmux.PaneWorking:
		inst.SetStatus(session.Running)
	case tmux.PanePrompt:
		if inst.AutoYes {
			inst.TapEnter()
		} else {
			inst.SetStatus(session.NeedsInput)
		}
	case tmux.PaneIdle:
		inst.SetStatus(session.Ready)
	case tmux.PaneUnknown:
	}
}

// instancePolledMsg carries the result of an off-cadence poll of a single instance,
// triggered when the selection changes or a session is detached. It refreshes that one
// instance's status immediately instead of waiting up to a full 500ms metadata tick —
// which is why an idle session no longer lingers as "running" right after you switch to
// it or step out of it.
type instancePolledMsg struct {
	instance *session.Instance
	state    tmux.PaneState
}

// pollSelectedCmd polls a single instance off the UI thread for an immediate status
// refresh. Returns nil for a session that can't be polled; Poll itself also yields
// PaneUnknown for a dead session, which applyPaneState ignores.
//
// fresh selects PollNow over Poll: use it after a detach, where the tick stream was stalled
// while attached so the hysteresis state is stale and a face-value snapshot is correct. A
// live selection change uses the hysteresis-respecting Poll (the tick loop kept the monitor
// current).
func pollSelectedCmd(inst *session.Instance, fresh bool) tea.Cmd {
	if inst == nil || !inst.Started() || inst.Paused() {
		return nil
	}
	return func() tea.Msg {
		if fresh {
			return instancePolledMsg{instance: inst, state: inst.PollNow()}
		}
		return instancePolledMsg{instance: inst, state: inst.Poll()}
	}
}

// sendPromptCmd submits a queued initial prompt to an instance off the UI thread,
// so the SendKeys→Enter pause inside SendPrompt does not block rendering.
func sendPromptCmd(instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		if err := instance.SendPrompt(prompt); err != nil {
			log.ErrorLog.Printf("failed to send queued prompt: %v", err)
		}
		return nil
	}
}

// deliverReadyPrompts submits each ready instance's queued prompt and returns the
// commands that perform the sends. The prompt is cleared synchronously here so it
// is dispatched at most once, even if a later tick also reports the instance ready.
func deliverReadyPrompts(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range results {
		if r.readyForPrompt && r.instance.Prompt != "" {
			prompt := r.instance.Prompt
			r.instance.Prompt = ""
			r.instance.PromptQueuedAt = time.Time{}
			cmds = append(cmds, sendPromptCmd(r.instance, prompt))
		}
	}
	return cmds
}

// promptDeliveryTimeout bounds how long a queued startup prompt waits for the pane
// to fall idle before it is delivered anyway. It is comfortably longer than a typical
// agent boot (including slow MCP server init) yet short enough that a genuinely stalled
// boot does not feel hung. The clock starts when the prompt is queued (session creation),
// so it also covers worktree setup, not just the agent's own startup.
const promptDeliveryTimeout = 60 * time.Second

// promptDeliveryReady decides whether a queued startup prompt may be delivered now.
//
// gateReady is Instance.IsReadyForPrompt(): the agent has rendered and is past any
// one-time startup gate (claude's trust-folder / "new MCP server" screen, or the
// non-claude docs-url screen). This is a hard precondition the timeout never bypasses —
// keystrokes sent while a gate is up are consumed by the gate dialog, not the agent's
// input box, so the prompt would be lost.
//
// Normally we also wait for the pane to leave PaneWorking to avoid the post-trust
// "loading" transition window. But a chatty agent that writes continuously on boot can
// stay PaneWorking indefinitely and stall the first message forever; once the prompt has
// been queued longer than promptDeliveryTimeout we drop only that busy check. A zero
// queuedAt disables the timeout (the prompt was queued without a timestamp), falling back
// to the strict idle-pane requirement.
func promptDeliveryReady(state tmux.PaneState, gateReady bool, queuedAt, now time.Time) bool {
	if !gateReady {
		return false
	}
	if state != tmux.PaneWorking {
		return true
	}
	return !queuedAt.IsZero() && now.Sub(queuedAt) > promptDeliveryTimeout
}

// lostSessionRecoverThreshold is how many consecutive ticks an instance must be seen
// with a dead tmux session before it is recovered to Paused. Recovery commits any WIP
// and removes the worktree, so a single transient `tmux has-session` miss (server
// blip, load spike) must not trigger it — require confirmation across ticks.
const lostSessionRecoverThreshold = 2

// recoverLostInstances moves instances whose tmux session has died (flagged
// sessionLost by the metadata tick) into Paused, so they stop being polled and can be
// brought back with Resume. It debounces using strikes (a per-instance count of
// consecutive dead observations, owned by the caller): a session is only recovered
// after lostSessionRecoverThreshold consecutive misses; any live observation resets
// the count. Returns whether any instance was recovered so the caller can persist.
// Runs on the main thread — the only place model state may be mutated.
func recoverLostInstances(results []instanceMetaResult, strikes map[*session.Instance]int) (recovered bool) {
	for _, r := range results {
		if !r.sessionLost || r.instance.Paused() {
			delete(strikes, r.instance) // alive (or already paused): clear any prior strikes
			continue
		}
		strikes[r.instance]++
		if strikes[r.instance] < lostSessionRecoverThreshold {
			continue // not yet confirmed dead; re-check next tick
		}
		delete(strikes, r.instance)
		if err := r.instance.RecoverLostSession(); err != nil {
			log.ErrorLog.Printf("failed to recover lost session %q: %v", r.instance.Title, err)
		}
		recovered = true
	}
	return recovered
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

// autoNameDoneMsg is sent when a background name generation completes. instance
// identifies which session the name was generated for, so the result lands on the
// right one even if the selection moved meanwhile.
type autoNameDoneMsg struct {
	instance *session.Instance
	name     string
	err      error
}

// runAutoNameCmd returns a Cmd that generates a display name in a background
// goroutine (the agent subprocess can take a few seconds) so the UI stays
// responsive. The session's own agent does the naming when it supports
// headless one-shot prompting (see session.GenerateName).
func runAutoNameCmd(ctx context.Context, instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		// Compute the full diff here, off the UI thread. The cached stats are often the
		// lightweight numstat form (Content empty) — that's all that's kept for a
		// session unless it is the selected one during a diff poll — which would starve
		// the namer of signal and yield a confabulated name. ComputeDiff is
		// goroutine-safe; fall back to the cached stats if it can't run (e.g. paused).
		stats := instance.ComputeDiff()
		if stats == nil || stats.Content == "" {
			if cached := instance.GetDiffStats(); cached != nil {
				stats = cached
			}
		}
		name, err := session.GenerateName(ctx, instance.Program, prompt, stats)
		return autoNameDoneMsg{instance: instance, name: name, err: err}
	}
}

// snapshotActiveInstances returns the currently active (started, not paused)
// instances. Called on the main thread so the filtering doesn't race with
// state mutations.
func (m *home) snapshotActiveInstances() []*session.Instance {
	var out []*session.Instance
	for _, inst := range m.list.GetInstances() {
		if inst.Started() && !inst.Paused() {
			out = append(out, inst)
		}
	}
	return out
}

// tickUpdateMetadataCmd returns a self-chaining Cmd that sleeps 500ms, then performs
// expensive metadata I/O (tmux capture, git diff) in parallel background goroutines.
// Because it only re-schedules after completing, overlapping ticks are impossible.
// The active instances slice should be snapshotted on the main thread via
// snapshotActiveInstances() before being passed here.
//
// Only the selected instance gets a full diff (with Content); the rest get a
// lightweight numstat-only summary. This keeps per-instance memory bounded
// since the diff pane only ever renders the selected one.
func tickUpdateMetadataCmd(active []*session.Instance, selected *session.Instance) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)

		if len(active) == 0 {
			return metadataUpdateDoneMsg{}
		}

		results := make([]instanceMetaResult, len(active))
		var wg sync.WaitGroup
		for idx, inst := range active {
			wg.Add(1)
			go func(i int, instance *session.Instance) {
				defer wg.Done()
				r := &results[i]
				r.instance = instance
				// A started session whose tmux pane has died would fail every probe
				// (capture, diff) and flood the log/error box. Detect it once here
				// (read-only) and skip polling; the main thread recovers it to Paused.
				if instance.Started() && !instance.Paused() && !instance.TmuxAlive() {
					r.sessionLost = true
					return
				}
				r.state = instance.Poll()
				// Only probe readiness while a prompt is actually queued (a brief
				// window after a new session), so the extra pane capture is rare.
				if instance.Prompt != "" {
					r.readyForPrompt = promptDeliveryReady(
						r.state, instance.IsReadyForPrompt(),
						instance.PromptQueuedAt, time.Now())
				}
				if instance == selected {
					r.diffStats = instance.ComputeDiff()
				} else {
					r.diffStats = instance.ComputeDiffNumstat()
				}
			}(idx, inst)
		}
		wg.Wait()

		return metadataUpdateDoneMsg{results: results}
	}
}

// errToastDuration is how long the transient error box stays before auto-hiding.
const errToastDuration = 5 * time.Second

// handleError surfaces an error in the UI. Short, single-line errors get a
// transient toast (auto-hidden after errToastDuration): when the always-on hint
// bar is up, the toast rides the bar's reserved row so the layout never shifts;
// otherwise it falls back to the error box's own row. An error that a one-line
// toast cannot actually convey — multi-line, or wider than the row can show
// (e.g. a failed push's git output) — is routed to the persistent info modal
// instead, but only from stateDefault: in any overlay state (e.g. a form
// validation error) switching to stateInfo would clobber the open overlay, so
// those always use the toast.
func (m *home) handleError(err error) tea.Cmd {
	if m.state == stateDefault && !m.errBox.Fits(err) {
		return m.showInfo(err.Error()) // showInfo logs the message itself
	}
	log.ErrorLog.Printf("%v", err)
	if m.menuVisible() && m.menu != nil {
		m.menu.SetNotice(err.Error(), ui.NoticeError)
	} else {
		m.errBox.SetError(err)
		m.recomputeLayout() // give the error its row; panes shrink by one
	}
	return m.scheduleNoticeHide()
}

// handleInfoNotice flashes a neutral acknowledgment ("branch copied") on the
// hint bar's reserved row. Unlike errors, info is chrome: when the user runs
// without the hint bar there is no reserved row to ride, so the notice is
// dropped rather than claiming one.
func (m *home) handleInfoNotice(text string) tea.Cmd {
	if !m.menuVisible() || m.menu == nil {
		return nil
	}
	m.menu.SetNotice(text, ui.NoticeInfo)
	return m.scheduleNoticeHide()
}

// scheduleNoticeHide stamps the just-shown toast with a fresh generation and
// returns the command that clears it after errToastDuration. The generation
// keeps an older toast's timer from clearing a newer toast early.
func (m *home) scheduleNoticeHide() tea.Cmd {
	m.noticeGen++
	gen := m.noticeGen
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(errToastDuration):
		}

		return hideErrMsg{gen: gen}
	}
}

// resumeSelected resumes a paused instance and persists the new running state
// (Resume itself only mutates in-memory status, so without this a crash before
// the next save would leave the session stamped Paused). When resume is blocked
// because the session branch is checked out in the BASE repo — the common result
// of the Checkout action — it offers to detach the base repo and retry. When the
// branch is held by a sibling worktree it surfaces a dismissible modal naming the
// holder rather than auto-touching another live worktree.
func (m *home) resumeSelected(selected *session.Instance) tea.Cmd {
	err := selected.Resume()
	if err == nil {
		if serr := m.storage.SaveInstances(m.list.GetInstances()); serr != nil {
			log.WarningLog.Printf("failed to persist resumed instance %s: %v", selected.Title, serr)
		}
		return tea.WindowSize()
	}

	// Only a branch-busy failure is recoverable; surface anything else as-is.
	var busy *git.BranchCheckedOutError
	if !errors.As(err, &busy) {
		return m.handleError(err)
	}

	wt, gerr := selected.GetGitWorktree()
	if gerr != nil {
		return m.handleError(err)
	}
	heldByBase, herr := wt.IsBranchHeldByBaseRepo()
	if herr != nil || !heldByBase {
		// Held by a sibling worktree (or undeterminable): report where it lives in
		// a dismissible modal; never auto-detach another live worktree.
		return m.showInfo(err.Error())
	}

	message := fmt.Sprintf("Branch '%s' is checked out in the main repo. Detach it and resume?", wt.GetBranchName())
	action := func() tea.Msg {
		if derr := wt.DetachBranchInBaseRepo(); derr != nil {
			// e.g. the dirty-repo refusal — show it in a modal the user can read.
			return infoMsg(derr.Error())
		}
		if rerr := selected.Resume(); rerr != nil {
			return rerr
		}
		if serr := m.storage.SaveInstances(m.list.GetInstances()); serr != nil {
			log.WarningLog.Printf("failed to persist resumed instance %s: %v", selected.Title, serr)
		}
		return instanceChangedMsg{}
	}
	return m.confirmAction(message, action)
}

// showInfo displays an actionable message in a dismissible modal (reusing the
// TextOverlay the help screen uses). Unlike handleError's 3-second box, it stays
// until the user presses a key — appropriate for errors that require the user to
// read and act (e.g. "branch is checked out at <path>"). It reuses m.textOverlay,
// which is safe because only one modal state is active at a time.
func (m *home) showInfo(text string) tea.Cmd {
	log.ErrorLog.Printf("%s", text)
	m.textOverlay = overlay.NewTextOverlay(text)
	m.state = stateInfo
	return nil
}

// handleInfoState dismisses the info modal on any key press.
func (m *home) handleInfoState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.textOverlay.HandleKeyPress(msg) {
		m.state = stateDefault
		return m, tea.Sequence(
			tea.WindowSize(),
			func() tea.Msg {
				m.menu.SetState(ui.StateDefault)
				return nil
			},
		)
	}
	return m, nil
}

// newSessionFormOverlay builds the unified new-session form (title, project, optional
// profile, branch, prompt) shared by both creation flows. It also reports whether the
// seeded target is a git repo, so openCreateForm can gate the open-time branch plumbing
// without re-running the git checks.
func (m *home) newSessionFormOverlay() (_ *overlay.TextInputOverlay, isGit bool) {
	ov := overlay.NewSessionCreateOverlay(m.appConfig.GetProfiles(), m.candidateRepoPaths())
	// Seed the initial validity so the picker can flag the default target before the user
	// navigates: a non-git default directory shows the direct-session hint (and an inert
	// branch section), not a block.
	valid, direct, head := targetValidity(m.ctx, m.newSessionPath)
	ov.SetTargetValidity(valid, direct, head)
	return ov, valid && !direct
}

// openCreateForm opens the unified new-session form — the single creation flow
// behind both `n` (focusTitle, for "type a name and go") and `N` (project picker
// first). The session itself is not created (and no list row appears) until the
// form is submitted. The contextual target is derived up front and, when it is a
// git repo, a background fetch kicked off so branches are current by the time the
// user reaches the branch field.
func (m *home) openCreateForm(focusTitle bool) tea.Cmd {
	if limit := m.appConfig.GetMaxSessions(); limit > 0 && m.list.NumInstances() >= limit {
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", limit))
	}

	m.newSessionPath = m.defaultNewSessionPath()
	target := m.newSessionPath

	m.state = statePrompt
	ov, isGit := m.newSessionFormOverlay()
	m.textInputOverlay = ov
	if focusTitle {
		m.textInputOverlay.FocusTitle()
	}

	// Branch plumbing only applies to a git target: seed the fetched-once set and kick
	// the background fetch plus the initial (undebounced) branch search. A non-git
	// target's branch section is inert, so there is nothing to fetch or list — and a
	// later path change onto a git repo triggers its own verdict-driven fetch (every
	// other candidate is fetched when, and if, it is confirmed as git while selected).
	m.fetchedPaths = map[string]bool{}
	cmds := []tea.Cmd{tea.WindowSize()}
	if isGit {
		m.fetchedPaths[target] = true
		cmds = append(cmds,
			m.runBranchFetch(target),
			m.runBranchSearch("", m.textInputOverlay.BranchFilterVersion()))
	}
	return tea.Batch(cmds...)
}

// createSessionFromForm validates the submitted new-session form, creates the session,
// adds it to the list, and starts it in the background with the entered prompt. On a
// validation error it leaves the overlay open (clearing the submitted flag) and surfaces
// the error so the user can correct the offending field.
func (m *home) createSessionFromForm(prompt string) tea.Cmd {
	ov := m.textInputOverlay

	title := ov.GetTitle()
	if title == "" {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("title cannot be empty"))
	}

	path := ov.GetSelectedPath()
	if path == "" {
		path = m.newSessionPath
	}
	// A non-git directory becomes a direct session (agent runs in place, no worktree).
	valid, direct, _ := targetValidity(m.ctx, path)
	if !valid {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("%q is not a directory", path))
	}

	program := m.program
	if p := ov.GetSelectedProgram(); p != "" {
		program = p
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    path,
		Program: program,
		Direct:  direct,
	})
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
	}
	instance.SetBaseContext(m.ctx)

	// Create the list row only now, on submit. AddInstance may insert it mid-list under its
	// repo group, so select it by identity.
	finalizer := m.list.AddInstance(instance)
	m.list.SelectInstance(instance)
	if branch := ov.GetSelectedBranch(); branch != "" {
		instance.SetBaseBranch(branch)
	}
	instance.Prompt = prompt
	instance.PromptQueuedAt = time.Now()
	instance.SetStatus(session.Loading)
	finalizer()

	m.textInputOverlay = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)

	startCmd := func() tea.Msg {
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err}
	}
	return tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd)
}

// targetValidity reports whether path is a usable new-session target and, if so,
// whether it would be a direct (non-git) session. For a git target it also resolves
// headBranch — the branch HEAD points at — for the branch picker's default base label.
// Both the inline (`n`) and form (`N`) flows use it to drive the picker's inline hint
// and to set the Direct flag.
func targetValidity(ctx context.Context, path string) (valid, direct bool, headBranch string) {
	if !config.DirExists(path) {
		return false, false, ""
	}
	if !git.IsGitRepo(ctx, path) {
		return true, true, ""
	}
	return true, false, git.CurrentBranchName(ctx, path)
}

// defaultNewSessionPath returns the contextual target repo for a new session: the
// highlighted session's repo, falling back to the current working directory. The
// empty string is returned only if there is no repo context at all (no highlighted
// session and cwd is unavailable).
func (m *home) defaultNewSessionPath() string {
	if selected := m.list.GetSelectedInstance(); selected != nil && selected.Path != "" {
		return selected.Path
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// candidateRepoPaths returns the deduped candidate target paths for the directory
// picker: the current target first, then existing sessions' repos, then recently-used
// project directories, then cwd.
func (m *home) candidateRepoPaths() []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	add(m.newSessionPath)
	for _, inst := range m.list.GetInstances() {
		add(inst.Path)
	}
	for _, p := range m.appState.GetRecentPaths() {
		// Skip recent paths that no longer exist so deleted/moved repos don't clutter
		// the picker or error only when selected.
		if !config.DirExists(p) {
			continue
		}
		add(p)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	return paths
}

// recordRecentPath records a newly-started session's repo path in the MRU list. It is
// best-effort: a persistence error is logged but does not interrupt the session flow.
func (m *home) recordRecentPath(path string) {
	if err := m.appState.AddRecentPath(path); err != nil {
		log.WarningLog.Printf("failed to record recent path %q: %v", path, err)
	}
}

// cancelPromptOverlay cancels the prompt overlay.
func (m *home) cancelPromptOverlay() tea.Cmd {
	m.textInputOverlay = nil
	m.state = stateDefault
	return tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// confirmKill shows the kill-confirmation overlay for inst and stashes the
// teardown action. inst need not be the selected instance: the in-session kill
// key (Ctrl+X) and the auto-open path target a specific session regardless of
// the current list selection, so the action keys on inst (and KillInstance)
// rather than on whatever happens to be selected when the user confirms.
func (m *home) confirmKill(inst *session.Instance) tea.Cmd {
	if inst == nil || inst.GetStatus() == session.Loading {
		return nil
	}

	killAction := func() tea.Msg {
		// Refuse to kill only when the branch is checked out in the primary repo
		// itself (deleting it would strand the user's main checkout on a dangling
		// branch). A live session's branch is always checked out in the session's
		// OWN worktree, so we must NOT use IsBranchCheckedOut here — that any-worktree
		// check would refuse every running session. IsBranchHeldByBaseRepo is the
		// base-repo-only predicate. This is a teardown path: if the worktree or its
		// repo is unreachable — e.g. the user renamed/removed the project directory —
		// fail open and proceed, otherwise an orphaned session can never be deleted.
		// A direct (non-git) session has no branch or worktree, so skip the base-repo
		// branch check entirely — calling GetGitWorktree would only log a misleading
		// "cannot resolve worktree" warning for a session that never had one.
		if !inst.IsDirect() {
			if worktree, err := inst.GetGitWorktree(); err != nil {
				log.WarningLog.Printf("kill %s: cannot resolve worktree, proceeding: %v", inst.Title, err)
			} else if heldByBase, cerr := worktree.IsBranchHeldByBaseRepo(); cerr != nil {
				log.WarningLog.Printf("kill %s: cannot verify branch checkout, proceeding: %v", inst.Title, cerr)
			} else if heldByBase {
				return fmt.Errorf("branch for %s is checked out in the main repo; switch it away before deleting", inst.DisplayName())
			}
		}

		// Clean up terminal session for this instance
		m.tabbedWindow.CleanupTerminalForInstance(inst.Title)

		// Delete from storage first
		if err := m.storage.DeleteInstance(inst.Title); err != nil {
			return err
		}

		// Then kill the instance
		m.list.KillInstance(inst)
		return instanceChangedMsg{}
	}

	message := fmt.Sprintf("Kill session '%s'?", inst.DisplayName())
	cmd := m.confirmAction(message, killAction)
	// Kill is the one destructive confirmation, so it alone wears the danger
	// border (the default is accent); confirmAction created m.confirmationOverlay
	// synchronously above.
	m.confirmationOverlay.SetBorderColor(theme.Current().Palette.Danger)
	// Opt-in: a second press of the kill key confirms the dialog, so Ctrl+X Ctrl+X
	// kills in one motion. Scoped to the kill dialog (other confirmations still
	// require 'y').
	if m.appConfig.GetKillDoubleTapConfirm() {
		m.confirmationOverlay.SetConfirmAltKey(keys.KillKey)
	}
	return cmd
}

// confirmWidth is the confirmation dialog's width for the given terminal
// width: the classic 50 columns when they fit, shrinking with the terminal
// (border + a margin) on narrow ones so the box never spills off-screen. A
// zero terminal width (startup, tests) keeps the default.
func confirmWidth(termWidth int) int {
	const preferred = 50
	if termWidth <= 0 {
		return preferred
	}
	return max(20, min(preferred, termWidth-4))
}

// confirmAction shows a confirmation modal and stores the action to execute on
// confirm. The action is run (and its result dispatched) by the stateConfirm key
// handler, not here, so its returned message — including any error — flows through
// Update instead of being discarded.
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm
	m.pendingConfirmAction = action

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	m.confirmationOverlay.SetWidth(confirmWidth(m.windowWidth))

	return nil
}

func (m *home) View() string {
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
	if m.errBox.HasError() {
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
	} else if m.state == stateSettings {
		if m.settingsOverlay == nil {
			log.ErrorLog.Printf("settings overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.settingsOverlay.Render(), mainView, true)
	}

	return mainView
}
