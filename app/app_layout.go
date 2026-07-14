package app

// Layout, window-size, and live settings application for the home model.

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
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
	tabsWidth := msg.Width - listWidth

	m.windowWidth, m.windowHeight = msg.Width, msg.Height

	// The hint bar is contextual (see menuVisible): it claims a row only during the
	// inline interactions where it carries unique information, and the panes reclaim
	// that row during plain navigation and behind overlays. The error box likewise
	// takes a row only while a notice is showing. Whichever rows are claimed, the
	// composed frame is always exactly msg.Height tall and never floats in a
	// centered band; transitions that flip menuVisible call recomputeLayout.
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
	// size the menu and error rows.
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
	case stateFilter, stateVisual:
		// Both inline interactions teach their gestures on the bar, so it stays
		// even when the always-on hint bar is turned off.
		return true
	case statePrompt, stateRename, stateQueue, stateConfirm, stateHelp, stateInfo, stateSettings, stateWelcome, stateAccounts:
		return false
	default: // stateDefault (and the empty list)
		// generatingName and actionInFlight each force the bar visible so their
		// progress row shows even when the always-on hint bar is turned off.
		return m.generatingName || m.actionInFlight || m.appConfig.GetHintBar()
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
// terminal height minus the contextual hint-bar and error rows (see menuVisible),
// which View reserves at the bottom in lockstep with the layout. The panes start
// at row 0, so this is also the exclusive lower Y-bound of the draggable divider
// (handleMouse) — a press below it lands on the menu/error strip, not the seam.
func (m *home) paneContentHeight() int {
	menuHeight := 0
	if m.menuVisible() {
		menuHeight = 1
	}
	errHeight := 0
	if m.errBox.HasError() {
		errHeight = 1
	}
	return max(1, m.windowHeight-menuHeight-errHeight)
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
	case "theme", "nerd_font":
		// Styles read theme.Current() lazily at render time, so swapping the
		// palette / glyph set plus a forced repaint restyles the whole UI in place.
		theme.Set(m.appConfig.Theme)
		theme.SetNerdFont(m.appConfig.GetNerdFont())
		return tea.Sequence(tea.ClearScreen, tea.WindowSize())
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
	case "splash":
		// Mirror the newHome seeding; ui takes the normalized name so it needs
		// no config import. With zero sessions the splash repaints in place, so
		// cycling the enum previews each pattern behind the panel.
		ui.SetSplashVariant(m.appConfig.GetSplash())
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

// listColStep is how many terminal columns each < / > press shifts the split.
// A whole-column step gives predictable, exact control at any terminal width;
// the mouse drag (handleMouse) covers larger jumps.
const listColStep = 1

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
	if err := m.appState.SetListRatio(ratio); err != nil {
		return m.handleError(err)
	}
	m.listRatio = m.appState.GetListRatio()
	m.recomputeLayout()
	return m.instanceChanged()
}
