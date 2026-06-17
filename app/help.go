package app

import (
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type helpText interface {
	// toContent returns the help UI content.
	toContent() string
	// hint returns the dismiss hint the overlay pins below the content (it
	// must stay visible while the content scrolls, so it is not part of
	// toContent).
	hint() string
	// mask returns the bit mask used to track which one-time screens have been
	// seen (persisted in app state). Screens with alwaysShow ignore it.
	mask() uint32
}

// helpTypeGeneral is the on-demand cheatsheet (opened with '?').
type helpTypeGeneral struct{}

// helpTypeWelcome is the one-time welcome shown on first launch ever.
type helpTypeWelcome struct{}

// Help styles read the active theme at render time.
func helpTitleStyle() lipgloss.Style  { return theme.Current().PurpleStyle().Bold(true).Underline(true) }
func helpHeaderStyle() lipgloss.Style { return theme.Current().CyanStyle().Bold(true) }
func helpKeyStyle() lipgloss.Style    { return theme.Current().AttentionStyle().Bold(true) }
func helpDescStyle() lipgloss.Style   { return theme.Current().FgStyle() }
func helpDimStyle() lipgloss.Style    { return theme.Current().DimStyle() }

// helpRow formats a "key   description" line with the key column padded to a
// fixed width so descriptions align.
func helpRow(key, desc string) string {
	const keyCol = 12
	pad := keyCol - runewidth.StringWidth(key)
	if pad < 1 {
		pad = 1
	}
	return helpKeyStyle().Render(key) + strings.Repeat(" ", pad) + helpDescStyle().Render(desc)
}

func (h helpTypeGeneral) toContent() string {
	g := theme.Current().Glyphs
	legend := helpDimStyle().Render(
		theme.Current().SuccessStyle().Render(g.Ready) + " ready    " +
			theme.Current().AttentionStyle().Render(g.Waiting) + " waiting on you    " +
			theme.Current().BadgeStyle().Render(" "+g.AutoBadge+"AUTO ") + " auto-accepting",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		helpTitleStyle().Render("Atrium — Keys"),
		"",
		helpHeaderStyle().Render("Navigate"),
		helpRow("↑/k ↓/j", "move selection"),
		helpRow("tab / shift-tab", "next / prev pane"),
		helpRow("1 / 2 / 3", "jump to preview / diff / terminal"),
		helpRow("shift-↑↓", "scroll the active pane"),
		helpRow("< / >", "shrink / grow the session list"),
		helpRow("esc", "exit scroll mode / clear filter"),
		"",
		helpHeaderStyle().Render("Manage"),
		helpRow("n", "new session (form, name first)"),
		helpRow("N", "new session (form, project first)"),
		helpRow("R", "rename session (label only)"),
		helpRow("A", "auto-name session (via its agent)"),
		helpRow("/", "filter sessions"),
		"",
		helpHeaderStyle().Render("Handoff"),
		helpRow("↵/o", "attach to the selected session"),
		helpRow("ctrl-q", "toggle attach/detach (detach when in, attach from the list)"),
		helpRow("ctrl-x", "kill the selected/attached session (twice to confirm)"),
		helpRow("ctrl-pgup/pgdn", "in a session: cycle to prev / next session in the repo group"),
		helpRow("s", "send a message (without attaching)"),
		helpRow("a", "approve the agent's prompt (enter picks its default); on idle claude, accept the suggested prompt"),
		helpRow("p", "pause: commit changes + free the worktree"),
		helpRow("ctrl-p", "pause all active sessions in the current view"),
		helpRow("P", "commit & push branch"),
		helpRow("c", "create a PR for the pushed branch (gh)"),
		helpRow("m", "merge the session's PR (squash)"),
		helpRow("w", "open the session's PR in the browser"),
		helpRow("r", "resume a paused session"),
		helpRow("ctrl-r", "resume all paused sessions in the current view"),
		helpRow("y", "copy branch name to clipboard"),
		helpRow("f", "copy/open URLs & paths from the preview"),
		"",
		helpHeaderStyle().Render("Groups"),
		helpRow("J / K", "reorder within a repo group"),
		helpRow("{ / }", "move a whole group up / down"),
		helpRow("← / →", "collapse / expand group"),
		helpRow("Z", "collapse / expand all"),
		"",
		helpHeaderStyle().Render("Other"),
		helpRow("?", "toggle this cheatsheet"),
		helpRow(",", "settings"),
		helpRow("ctrl-l", "force a full redraw of the screen"),
		helpRow("q", "quit"),
		"",
		legend,
	)
}

func (h helpTypeWelcome) toContent() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		helpTitleStyle().Render("Welcome to Atrium"),
		"",
		helpDescStyle().Render("Run multiple coding agents in parallel — each in its own"),
		helpDescStyle().Render("git worktree and tmux session, managed from one place."),
		"",
		helpRow("n", "start your first session"),
		helpRow("?", "show all keys, any time"),
	)
}

func (h helpTypeGeneral) hint() string { return "press any key to close" }
func (h helpTypeWelcome) hint() string { return "press any key to begin" }

func (h helpTypeGeneral) mask() uint32 { return 1 }

// helpTypeWelcome uses bit 4; bits 1-3 belonged to retired teaching modals.
func (h helpTypeWelcome) mask() uint32 { return 1 << 4 }

// showHelpScreen displays a help overlay. The cheatsheet (helpTypeGeneral) always
// shows on demand; one-time screens (welcome) show until their seen bit is set.
// Crucially, the bit is NOT set here on render — the welcome's bit is set only on
// the first successful session start (see the instanceStartedMsg handler), so a
// stray keypress that dismisses the welcome no longer burns it for good; it
// re-shows each launch until the user has actually created a session. onDismiss is
// retained for compatibility but is now always nil.
func (m *home) showHelpScreen(helpType helpText, onDismiss func()) (tea.Model, tea.Cmd) {
	var alwaysShow bool
	switch helpType.(type) {
	case helpTypeGeneral:
		alwaysShow = true
	}

	flag := helpType.mask()

	if alwaysShow || (m.appState.GetHelpScreensSeen()&flag) == 0 {
		m.textOverlay = overlay.NewTextOverlay(helpType.toContent())
		m.textOverlay.SetHint(helpType.hint())
		m.textOverlay.OnDismiss = onDismiss
		m.state = stateHelp
		// Size the overlay now rather than waiting for the next resize; no-op
		// before the first WindowSizeMsg (the overlay then renders unwindowed).
		m.recomputeLayout()
		return m, nil
	}

	if onDismiss != nil {
		onDismiss()
	}
	return m, nil
}

// maybeShowWelcome shows the one-time welcome overlay on first launch ever.
func (m *home) maybeShowWelcome() {
	if m.welcomeChecked {
		return
	}
	m.welcomeChecked = true
	m.showHelpScreen(helpTypeWelcome{}, nil)
}

// handleHelpState handles key events when in help state
func (m *home) handleHelpState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The overlay scrolls on navigation keys while its content overflows;
	// any other key closes it.
	if m.textOverlay.HandleKeyPress(msg) {
		return m.closeTextOverlay()
	}
	return m, nil
}

// closeTextOverlay dismisses the modal text overlay (help or info) and
// restores the default state. Shared by every dismissal path: any-key from
// the help and info states, and a click outside the box.
func (m *home) closeTextOverlay() (tea.Model, tea.Cmd) {
	m.textOverlay.Dismiss()
	m.state = stateDefault
	return m, tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// textOverlayContains reports whether the screen cell (x, y) falls inside the
// rendered modal box. PlaceOverlay centers the overlay on the composed frame,
// and the frame is exactly windowWidth×windowHeight (an invariant pinned by
// TestViewFitsTerminalBounds and TestHelpOverlayFitsShortTerminal), so the
// same centering math reproduces the box's on-screen rectangle.
func (m *home) textOverlayContains(x, y int) bool {
	box := m.textOverlay.Render()
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	left := max(0, (m.windowWidth-w)/2)
	top := max(0, (m.windowHeight-h)/2)
	return x >= left && x < left+w && y >= top && y < top+h
}
