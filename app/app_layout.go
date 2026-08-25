package app

// Layout, window-size, and live settings application for the home model.

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// A zero ratio means the home was built without seeding it (e.g. a struct
	// literal in tests); fall back to the persisted/default value so the list
	// never collapses to nothing.
	if m.listRatio <= 0 {
		m.listRatio = m.appState.GetListRatio()
	}
	regions := m.computeRegions(msg.Width)

	m.windowWidth, m.windowHeight = msg.Width, msg.Height

	// Whichever rows computeBudget claims, the composed frame is always exactly
	// msg.Height tall and never floats in a centered band; transitions that flip
	// menuVisible call recomputeLayout.
	budget := m.computeBudget(msg.Height)
	m.errBox.SetSize(int(float32(msg.Width)*0.9), budget.err)

	m.tabbedWindow.SetSize(regions.tabs, budget.body)
	m.list.SetSize(regions.list, budget.body)

	// Each overlay's resize policy is its surfaceSpecs entry's size: the
	// overlay's own SizeSpec resolved against the terminal, applied to the
	// entry's target field. The walk runs every entry, not just the current
	// state's: a still-armed overlay from another state is resized exactly as
	// the old per-pointer blocks resized it — sizing follows the FIELD, not the
	// state (which is also why one entry covers the shared textOverlay).
	for st := stateDefault; st < numStates; st++ {
		entry := surfaceSpecs[st].size
		if entry.target == nil {
			continue
		}
		if r := entry.target(m); r != nil {
			r.SetSize(entry.spec.Fit(msg.Width, msg.Height))
		}
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, budget.menu)
}

// menuVisible reports whether the hint bar should occupy a row. Inline
// interactions always get it (stateFilter shows its accept/clear cue, and a
// background name generation its progress). Modal overlays
// (prompt/rename/confirm/help/info) render their own instructions, so the bar
// behind them would be a redundant strip. Plain navigation shows the always-on
// hint line; with it turned off (hint_bar in config.json) the row stays reserved
// but renders blank (Menu.quiet) instead of disappearing, so a transient notice
// can ride it without shifting the layout (#438). Which state keeps the row is
// each surfaceSpecs entry's barVisible bit, with its reason beside it.
func (m *home) menuVisible() bool {
	return surfaceSpecs[m.state].barVisible
}

// paneContentHeight is how many rows the list/preview panes occupy: the body
// slice of computeBudget's partition at the cached terminal height. The panes
// start at row topBannerHeight(), so their screen span is [topBannerHeight(),
// topBannerHeight()+paneContentHeight()) — the divider Y-bound in handleMouse
// is offset by the banner accordingly.
func (m *home) paneContentHeight() int {
	return m.computeBudget(m.windowHeight).body
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

// refreshSettingsRepoLayer hands the settings panel what the SELECTED session's
// repository adds to the repo-layerable rows (#815), so those rows can say the
// value shown is not the effective value there.
//
// The selected session is what "this repo" means while browsing the list, and its
// resolution is one the poll sweep has already done — so this reads a mutex-guarded
// pair of slices and forks nothing. That matters twice over: the panel's render path
// may not touch the filesystem, and a fresh assessment here would be git on the
// update thread, which is the debt #857 already tracks for the create path.
//
// nil whenever there is nothing to ask: no session selected (an empty list, the
// welcome screen), a direct session, or one whose worktree has not been materialized
// since the app started, so no resolution has run. The panel renders nil as "unknown"
// rather than "the repo adds nothing" — the difference matters for a paused session
// in a repo that really does declare a list.
func (m *home) refreshSettingsRepoLayer() {
	if m.settingsOverlay == nil || m.list == nil {
		return
	}
	inst := m.list.GetSelectedInstance()
	if inst == nil {
		m.settingsOverlay.SetRepoLayer(nil)
		return
	}
	carry, link, resolved := inst.RepoLocalSeeds()
	if !resolved || (len(carry) == 0 && len(link) == 0) {
		// An empty resolution is as informative as no resolution for this surface —
		// there is nothing to annotate either way — so it takes the same nil rather
		// than a struct every row has to test for emptiness.
		m.settingsOverlay.SetRepoLayer(nil)
		return
	}
	m.settingsOverlay.SetRepoLayer(&overlay.RepoLayer{
		Repo:       inst.GetRepoPath(),
		CarryFiles: carry,
		LinkPaths:  link,
	})
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
// double_tap_confirm, image_preview, notify_throttle_seconds,
// diff_refresh_seconds) or only consumed by later operations (branch_prefix;
// daemon_poll_interval on the next daemon run), so persisting is all they need.
//
// Those three are named in that list deliberately rather than left to fall
// through, because nothing here guards the omission: no test asserts that every
// row key is handled or knowingly unhandled, so a field that needed an arm and
// did not get one would simply not apply until relaunch. image_preview is
// resolved when the overlay opens (openImagePreview → kittyEligible), so the next
// image obeys the new value and there is nothing to restyle;
// notify_throttle_seconds is read per notification edge in maybeNotify and
// diff_refresh_seconds per tick in diffContentFloor, so the next edge and the
// next tick already carry the new value.
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
		//
		// Set, not applyThemeSelection: the themes directory was re-read when this panel
		// OPENED (reloadUserThemes), so the name being saved here is already registered and
		// re-reading would only repeat the work — per keypress, since cycling a row calls
		// this on every left/right press, and with it a second buffering of every refusal
		// into a modal the user would meet on closing the panel.
		//
		// And NOT ApplyThemeAtLaunch, whose extra step is SetScheme(initialScheme()): that
		// reads COLORFGBG, which is silent on most terminals, and would overwrite a
		// polarity OSC 11 had already detected. Saving glyph_set would then flip a
		// correctly-detected light terminal to the dark default, with no re-query to
		// correct it (applySchemeQueryCmd fires for the theme row alone).
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
	case "context_warn_percent", "context_danger_percent":
		// Both keys re-seed the pair, not just the one that moved: the accessors hold
		// warn ≤ danger, so raising danger can also raise the warn band it was
		// capping, and pushing only the edited half would leave the renderer with the
		// stale other one until the next restart.
		if m.list != nil {
			m.list.SetContextThresholds(m.appConfig.GetContextWarnPercent(), m.appConfig.GetContextDangerPercent())
		}
	case "pending_watchdog_minutes":
		// The cap lives in the session package, which owns the reconciliation, and is
		// read on each applyPending rather than captured — so the next Pending poll
		// uses the new value with nothing here to re-arm.
		session.SetPendingWatchdog(m.appConfig.PendingWatchdogOverride())
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
	cols := m.listCols(m.windowWidth)
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
