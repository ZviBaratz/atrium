package app

// Layout, window-size, and live settings application for the home model.

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

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
	// Focus preset: the list is hidden (View omits it), so hand its whole column
	// to the tabbed window. Every other preset keeps the listRatio split.
	if m.listHidden() {
		listWidth = 0
	}
	tabsWidth := msg.Width - listWidth

	m.windowWidth, m.windowHeight = msg.Width, msg.Height

	// The hint bar is contextual (see menuVisible): during plain navigation it always
	// claims a row — blank when hint_bar is off, so a transient notice rides it with no
	// reflow (#438) — and the panes reclaim that row only behind overlays. The error
	// box likewise takes a row only while a notice is showing. Whichever rows are
	// claimed, the composed frame is always exactly msg.Height tall and never floats in
	// a centered band; transitions that flip menuVisible call recomputeLayout.
	menuHeight := 0
	if m.menuVisible() {
		menuHeight = 1
	}
	errHeight := 0
	if m.errBox.HasContent() {
		errHeight = 1
	}
	// Kept in lockstep with paneContentHeight, which recomputes this same budget
	// for the divider's Y-bound; menuHeight/errHeight are needed here anyway to
	// size the menu and error rows. topBannerHeight reserves the auto-accept safety
	// banner's row at the top when armed (#378).
	contentHeight := max(1, msg.Height-m.topBannerHeight()-menuHeight-errHeight)
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
		// Pass the full terminal size: the overlay hugs its content width and
		// windows its lines to fit short terminals.
		m.textOverlay.SetSize(msg.Width, msg.Height)
	}
	if m.settingsOverlay != nil {
		// Pass the full terminal size: the panel caps its own width and windows
		// its rows to fit short terminals.
		m.settingsOverlay.SetSize(msg.Width, msg.Height)
	}
	if m.accountsOverlay != nil {
		// Pass the full terminal size: the panel caps its own width and windows
		// its rows to fit short terminals.
		m.accountsOverlay.SetSize(msg.Width, msg.Height)
	}
	if m.confirmationOverlay != nil {
		// The dialog keeps its classic width on normal terminals and shrinks with
		// narrow ones; it was the one overlay excluded from resize handling.
		m.confirmationOverlay.SetWidth(confirmWidth(msg.Width))
	}
	if m.welcomeOverlay != nil {
		// Same idiom as the confirmation dialog: keep the authored width on normal
		// terminals, shrink so the box never spills off a narrow one.
		m.welcomeOverlay.SetWidth(welcomeWidth(msg.Width))
	}
	if m.queueOverlay != nil {
		w := int(float32(msg.Width) * 0.6)
		if w > 80 {
			w = 80
		}
		m.queueOverlay.SetWidth(w)
	}
	if m.promptHistoryOverlay != nil {
		w := int(float32(msg.Width) * 0.6)
		if w > 80 {
			w = 80
		}
		m.promptHistoryOverlay.SetWidth(w)
	}
	if m.cmdLogOverlay != nil {
		// The command log benefits from width (argv) and height (many rows), so it
		// takes a larger share than the queue overlay, capped for very wide terminals.
		w := int(float32(msg.Width) * 0.85)
		if w > 120 {
			w = 120
		}
		h := int(float32(msg.Height) * 0.85)
		if h > 44 {
			h = 44
		}
		m.cmdLogOverlay.SetSize(w, h)
	}
	if m.commandPaletteOverlay != nil {
		// Three columns wide (key, verb, prose) and as many rows as it can get:
		// the palette's whole value is seeing a lot of the keymap at once. Capped
		// like the command log so a very wide terminal doesn't stretch the prose
		// column past comfortable reading.
		w := int(float32(msg.Width) * 0.85)
		if w > 100 {
			w = 100
		}
		// The share is of the *box*, border and padding included, so it is the room
		// the palette may occupy rather than the room it fills and then overruns.
		// The +3 keeps the rendered size where it was before that was true.
		h := int(float32(msg.Height)*0.85) + 3
		if h > 43 {
			h = 43
		}
		if h > msg.Height {
			h = msg.Height
		}
		m.commandPaletteOverlay.SetSize(w, h)
	}
	if m.customCommandsOverlay != nil {
		// Narrower than the palette: two columns (key, description) of user-authored
		// prose, where the palette has three of generated text. Capped for the same
		// reason — a very wide terminal should not stretch a one-line description
		// across the screen. The share is of the box, border and padding included.
		w := int(float32(msg.Width) * 0.7)
		if w > 80 {
			w = 80
		}
		// No `h > msg.Height` clamp, unlike the palette above: a 0.7 share capped at 30
		// cannot exceed the height it was taken from. That guard is necessary there
		// because of the palette's `+3`, and copying it here would read as load-bearing
		// while doing nothing.
		h := int(float32(msg.Height) * 0.7)
		if h > 30 {
			h = 30
		}
		m.customCommandsOverlay.SetSize(w, h)
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
// hint line; with it turned off (hint_bar in config.json) the row stays reserved
// but renders blank (Menu.quiet) instead of disappearing, so a transient notice
// can ride it without shifting the layout (#438).
func (m *home) menuVisible() bool {
	switch m.state {
	case stateFilter, stateVisual, stateDiffComment:
		// These inline interactions teach their gestures on the bar, so it stays
		// even when the always-on hint bar is turned off.
		return true
	case statePrompt, stateRename, stateQueue, stateCmdLog, stateCommandPalette, stateCustomCommands, stateConfirm, stateHelp, stateInfo, stateSettings, stateWelcome, stateAccounts, stateHistory:
		return false
	default: // stateDefault (and the empty list)
		// The bottom row is always reserved during plain navigation, so a transient
		// notice never resizes the frame (#438). generatingName / actionInFlight are
		// subsumed here — they still drive the menu's StateGeneratingName / StateBusy
		// content. With hint_bar off the row renders blank (Menu.quiet, seeded from the
		// setting), giving a still chrome-free frame a notice can ride without a shift.
		return true
	}
}

// welcomeWidth clamps the first-run welcome modal's box width so it never spills
// off a narrow terminal, keeping its authored width on normal ones. Mirrors
// confirmWidth; the welcome's copy is written a little wider than the dialog.
func welcomeWidth(termWidth int) int {
	const preferred = 54
	if termWidth <= 0 {
		return preferred
	}
	return max(20, min(preferred, termWidth-4))
}

// paneContentHeight is how many rows the list/preview panes occupy: the full
// terminal height minus the auto-accept banner row (topBannerHeight, at the top when
// armed) and the contextual hint-bar and error rows (see menuVisible), which View
// reserves in lockstep with the layout. The panes start at row topBannerHeight(), so
// their screen span is [topBannerHeight(), topBannerHeight()+paneContentHeight()) —
// the divider Y-bound in handleMouse is offset by the banner accordingly.
func (m *home) paneContentHeight() int {
	menuHeight := 0
	if m.menuVisible() {
		menuHeight = 1
	}
	errHeight := 0
	if m.errBox.HasContent() {
		errHeight = 1
	}
	return max(1, m.windowHeight-m.topBannerHeight()-menuHeight-errHeight)
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

// refreshSettingsClusteringGate hands the settings panel the list's own answer to "are account
// clusters currently visible?", which the panel cannot derive: the gate counts distinct cluster
// keys over live sessions, and settingRow predicates only see the config.
func (m *home) refreshSettingsClusteringGate() {
	if m.settingsOverlay == nil || m.list == nil {
		return
	}
	m.settingsOverlay.SetAccountClusteringVisible(m.list.AccountClusteringVisible())
}

// applySplashConfig pushes the `splash` key's two halves into ui: whether the
// idle panes animate at all (config.SplashOff, #316) and which pattern they draw.
// ui takes both normalized, so it needs no config import.
//
// One function rather than two adjacent calls at each site, because the pair has
// no guard: a future third caller that seeded the variant and forgot the enable
// flag would leave the animation running for a user who turned it off, and no
// test would say so. Called from newHome and from applySettingChange.
func applySplashConfig(cfg *config.Config) {
	ui.SetSplashEnabled(cfg.SplashEnabled())
	ui.SetSplashVariant(cfg.GetSplash())
}

// applySettingChange persists the config after the settings panel changed the
// given row — or, for "profiles", after its record editor changed the profile
// list — then live-applies whatever that field controls. Fields without a case
// here are read live at their point of use (auto_attach, max_sessions,
// kill_double_tap_confirm) or only consumed by later operations (branch_prefix;
// daemon_poll_interval on the next daemon run), so persisting is all they need.
func (m *home) applySettingChange(key string) tea.Cmd {
	if err := config.SaveConfig(m.appConfig); err != nil {
		return m.handleError(err)
	}
	switch key {
	case "default_program", "profiles":
		// m.program is the create form's fallback launch command, resolved once at startup from
		// GetProgram(). With no variant picker — zero or one profile — it IS the command a new
		// session runs, so a changed default, or an edited profile the default names, must
		// re-resolve it or the form keeps launching the previous command until relaunch.
		//
		// Running sessions are untouched by design: session.Instance.Program stores its own
		// resolved command and is never re-derived, so a profile edit cannot reach a session
		// that already exists.
		m.program = m.appConfig.GetProgram()
		m.stashedDraft = nil
		// A stashed create-form draft snapshotted GetProfiles() and m.program when it was built,
		// and VariantPicker replays each profile's Program verbatim — so a restored draft would
		// offer a renamed-away profile and launch its OLD command. Drop it so the next open
		// rebuilds from live config. handleAccountsState does exactly this, for exactly this
		// reason.
	case "theme", "glyph_set":
		// Styles read theme.Current() lazily at render time, so swapping the
		// palette / glyph set plus a forced repaint restyles the whole UI in place.
		theme.Set(m.appConfig.GetTheme())
		theme.SetGlyphSet(m.appConfig.GetGlyphSet())
		// The spinner snapshots its frames at construction (assembleHome), so a
		// rung change that alters them (ascii's |/-\ vs the Braille dots) would not
		// show until relaunch. The list holds &m.spinner, so re-seeding the frames
		// here re-frames the running spinner in place.
		m.spinner.Spinner = spinner.Spinner{
			Frames: theme.Current().Glyphs.SpinnerFrames,
			FPS:    theme.Current().Glyphs.SpinnerFPS,
		}
		// The in-session bar's BAND lives in tmux, not in this frame: its colours
		// are a server option baked in when the server started, so restyling the
		// TUI alone leaves every attached pane's header on the old band while its
		// text (pushed per tick as #[fg=...] markup) moves. Push both halves.
		//
		// Only for a palette change: this case is shared with glyph_set, which moves
		// no colour, and the push costs a conf rewrite plus validateConfig's probe
		// server. applyBarStyleCmd returns nil for anything else, so the Batch below
		// degrades to the repaint alone.
		//
		// applySchemeQueryCmd is the same shape for the same reason, and it is the
		// fourth query point: the palette selection may have just BECOME `auto`, and
		// this is the only site where the gate that suppressed every earlier query is
		// itself what changed. See scheme.go.
		return tea.Sequence(tea.ClearScreen, tea.Batch(
			tea.RequestWindowSize,
			m.applyBarStyleCmd(key),
			m.applySchemeQueryCmd(key),
		))
	case "model_indicator":
		// Mirror the newHome seeding; the renderer takes the normalized mode
		// string so ui needs no config import.
		if m.list != nil {
			m.list.SetModelIndicator(m.appConfig.GetModelIndicator())
		}
	case "effort_indicator":
		if m.list != nil {
			m.list.SetEffortIndicator(m.appConfig.GetEffortIndicator())
		}
	case "permission_indicator":
		if m.list != nil {
			m.list.SetPermissionIndicator(m.appConfig.GetPermissionIndicator())
		}
	case "context_indicator":
		if m.list != nil {
			m.list.SetContextIndicator(m.appConfig.GetContextIndicator())
		}
	case "os_chrome":
		// Recompute now rather than waiting a tick: enabling shows the current fleet
		// on the next frame, and disabling zeroes the title and bar, which the
		// renderer turns into an empty title and a cleared bar.
		m.refreshOSChrome(false)
	case "splash":
		// With zero sessions the splash repaints in place, so cycling the enum
		// previews each pattern — and the off rung — behind the panel. The
		// animation loop itself is gated in splashAnimating, which the 100ms
		// preview tick re-reads, so turning it back on revives motion within a
		// tick without anything here re-arming it.
		applySplashConfig(m.appConfig)
	case "project_search_roots", "project_search_depth":
		// The scan's scope changed under a live TUI. Switching it off must also
		// retire the results already held: assembleHome gates the persisted cache
		// on the same condition at launch, because "a cache written before the
		// user disabled the scan must not keep surfacing" (#120) — but that gate
		// only runs at construction, and the panel can now flip the key without a
		// relaunch. A still-enabled scope keeps its results (the best answer until
		// the new scope's walk lands); either way the scan is marked stale so the
		// next form-open re-walks rather than serving the old scope for a full
		// projectScanTTL.
		m.lastScanAt = time.Time{}
		if m.appConfig.GetProjectSearchDepth() <= 0 {
			m.scannedRepos = nil
			// Best-effort, like the write in handleProjectScanDone: a failed clear
			// only costs a stale first paint on the next launch, which that gate
			// discards anyway while the scan stays off.
			if err := m.appState.ClearScannedRepos(); err != nil {
				log.WarningLog.Printf("failed to clear the repo-scan cache: %v", err)
			}
		}
	case "session_sort":
		// Re-order the list under the new mode immediately; the list takes the
		// normalized mode string so ui needs no config import. Selection is
		// preserved by identity.
		if m.list != nil {
			m.list.SetSortMode(m.appConfig.GetSessionSort())
		}
	case "group_mode":
		// Re-group the list under the new mode immediately; the list takes the
		// normalized mode string so ui needs no config import. Selection is
		// preserved by identity.
		if m.list != nil {
			m.list.SetGroupMode(m.appConfig.GetGroupMode())
		}
		// Re-ask the list: the mode change alters accountGrouped(), which is half the gate, so
		// the panel's copy must be recomputed rather than re-read — otherwise a row that has
		// just started clustering two accounts still shows "nothing to cluster".
		m.refreshSettingsClusteringGate()
	case "hint_bar":
		// The row is always reserved (menuVisible stays true in stateDefault); hint_bar
		// only toggles the bar between its hints and a blank line, so the row count no
		// longer changes on toggle — the panes don't resize either. Update the flag and
		// repaint.
		m.menu.SetQuiet(!m.appConfig.GetHintBar())
		m.recomputeLayout()
	case "mouse":
		// Nothing to command. View() sets MouseMode from the config on every frame,
		// so the next render after this change already carries the new mode and
		// Bubble Tea reconciles the terminal to it. Under v1 this had to send
		// EnableMouseCellMotion/DisableMouse by hand; the declarative View is what
		// removed the second place the setting had to be applied.
		return nil
	case "session_context_bar", "tmux_config_override":
		// Re-render the managed tmux conf so sessions started from now on pick
		// the change up; live sessions keep their current status line (tmux only
		// reads the config when a server starts).
		if err := tmux.Init(m.appConfig.TmuxConfigOverride, m.appConfig.GetSessionContextBar()); err != nil {
			return m.handleError(err)
		}
	case "agent_oom_margin":
		// Re-sync the process-wide margin. Each session applies the current value at
		// launch, so any session the user relaunches after this change (pause → resume,
		// or a pane recreate) picks it up; a session whose agent is already running keeps
		// its launched oom_score_adj until it is next relaunched (the kernel sets it once,
		// at exec).
		tmux.SetAgentOOMMargin(m.appConfig.GetAgentOOMMargin())
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

// barStyleApplier is the tmux bar push, as a package var so tests can substitute
// a recorder instead of shelling to a real server — the same seam idiom as
// tmuxAvailable in app_session.go.
//
// Two writes, because they cover two different populations: RewriteManagedConfig
// fixes the sessions started later, whose server will read the config file, and
// ApplyBarStyle fixes the ones running right now (status-style is server-global,
// so that is one subprocess for the fleet). Skipping either leaves the fleet's
// bars disagreeing depending on when each session was created.
//
// Both failures are logged, not surfaced: the bar is cosmetic, the user just asked
// for a theme, and the most common failure is simply that no tmux server is
// running yet.
//
// This runs on a tea.Cmd goroutine, so every global it reaches has to be safe off
// the update thread — RewriteManagedConfig republishes tmux's config state (atomic,
// swapped by rename), and both calls resolve the band's colours through
// barStyleColours, which reads theme.Current() and theme.Mono() (both atomic
// loads). Adding a global to that resolution means adding it to this list. A
// tea.Cmd is a goroutine: moving a startup-only call into one re-scopes everything
// it touches, and the seam below is exactly what hides that from the race detector.
//
// #394 Stage E's theme.CurrentScheme() is deliberately NOT on that list: a detected
// flip reaches the band through theme.Current(), which compose() has already folded
// the scheme into on the update thread. Detection widened who calls this — the
// scheme handler now does, alongside the settings panel — without widening what it
// reads.
var barStyleApplier = func(ctx context.Context, contextBar bool) {
	if err := tmux.RewriteManagedConfig(contextBar); err != nil {
		log.WarningLog.Printf("failed to rewrite managed tmux config after theme change: %v", err)
	}
	if err := tmux.ApplyBarStyle(ctx, cmd.MakeExecutor()); err != nil {
		log.WarningLog.Printf("failed to restyle live session bars after theme change: %v", err)
	}
}

// applyBarStyleCmd restyles every live session's in-pane status band for the
// theme that is now active, off the update thread.
//
// Nil unless key is "theme": the caller's case also fires for glyph_set, which
// changes no colour, and the push is not free (a conf rewrite plus validateConfig's
// throwaway probe server).
//
// Nil when the context bar is off: there is no band to restyle, and the managed
// conf that arm rewrites (see the "session_context_bar" case) carries the theme
// anyway.
func (m *home) applyBarStyleCmd(key string) tea.Cmd {
	if key != "theme" {
		return nil
	}
	return m.barStylePushCmd()
}

// barStylePushCmd is the push itself, carrying only the gate both callers share: no
// context bar means no band to restyle, and the managed conf the "session_context_bar"
// arm rewrites carries the theme anyway.
//
// Split from applyBarStyleCmd because detection reaches it too (applyDetectedScheme
// in scheme.go), and a detected dark->light flip is not a settings row. Passing the
// literal "theme" from there would have satisfied the gate while making the doc
// comment above — which explains that gate purely as "the caller's case also fires
// for glyph_set" — false about one of its two callers.
func (m *home) barStylePushCmd() tea.Cmd {
	if !m.appConfig.GetSessionContextBar() {
		return nil
	}
	contextBar := m.appConfig.GetSessionContextBar()
	ctx := m.ctx
	return func() tea.Msg {
		barStyleApplier(ctx, contextBar)
		return nil
	}
}

// listColStep is how many terminal columns each < / > press shifts the split.
// A whole-column step gives predictable, exact control at any terminal width;
// the mouse drag (handleMouse) covers larger jumps.
const listColStep = 1

// adjustListRatio nudges the list/preview split by delta and applies it as a
// custom override of the active preset (setCustomRatio persists the clamped
// value and re-pushes sizes), so fine-tuning the split complements the preset
// cycle instead of fighting it.
func (m *home) adjustListRatio(delta float64) tea.Cmd {
	return m.setCustomRatio(m.listRatio + delta)
}

// adjustListCols nudges the split by whole columns: it converts the current ratio
// to a column count at the live width, steps it, and converts back, so a press
// always moves the divider exactly delta columns regardless of terminal width.
// Before the first size event (windowWidth == 0) there is no column basis, so it
// falls back to a fixed ratio nudge.
func (m *home) adjustListCols(delta int) tea.Cmd {
	// A home that hasn't taken its first size event yet may carry a zero ratio
	// (a struct literal in tests, or pre-seed); fall back to the persisted value
	// so a nudge grows/shrinks from the real split rather than from nothing.
	if m.listRatio <= 0 {
		m.listRatio = m.appState.GetListRatio()
	}
	if m.windowWidth <= 0 {
		return m.adjustListRatio(float64(delta) * 0.02)
	}
	cols := int(float32(m.windowWidth) * float32(m.listRatio))
	// Center the target column (+0.5) so the layout's int(width*ratio) truncation
	// lands squarely on cols+delta instead of on a boundary a float32 rounding
	// error could snap back to cols, which would make a step silently stick.
	ratio := (float64(cols+delta) + 0.5) / float64(m.windowWidth)
	return m.setCustomRatio(ratio)
}

// repaintAfterAttach forces a hard repaint after a full-screen tea.Exec attach
// returns control to the app. tea.Exec's RestoreTerminal only does a soft repaint
// (using the diff cache), which leaves the frame stale or blank if the OS/terminal
// didn't preserve the alternate screen perfectly (tmux often clobbers it). This
// issues a tea.ClearScreen to flush the diff cache, then re-emits a WindowSizeMsg
// so components reflow and re-render completely. Any additional cmds are batched
// with the repaint.
//
// It also re-asks the terminal for its background colour. Detection was blind for the
// whole attach — tea.Exec suspended the loop and tmux owned the terminal, so neither
// an OSC 11 reply nor a focus event could reach us — and this is the one moment we
// know that. Nil unless theme: auto.
func (m *home) repaintAfterAttach(cmds ...tea.Cmd) tea.Cmd {
	return tea.Sequence(
		tea.ClearScreen,
		tea.Batch(append(cmds,
			func() tea.Msg {
				return tea.WindowSizeMsg{Width: m.windowWidth, Height: m.windowHeight}
			},
			m.requestSchemeCmd(),
		)...),
	)
}
