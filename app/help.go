package app

import (
	"github.com/ZviBaratz/atrium/log"
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
		helpRow("shift-↑↓", "scroll the active pane"),
		helpRow("< / >", "shrink / grow the session list"),
		helpRow("esc", "exit scroll mode"),
		"",
		helpHeaderStyle().Render("Manage"),
		helpRow("n", "new session (inline)"),
		helpRow("N", "new session with a prompt"),
		helpRow("R", "rename session (label only)"),
		helpRow("A", "auto-name session (via claude)"),
		helpRow("/", "filter sessions"),
		helpRow("D", "kill session"),
		"",
		helpHeaderStyle().Render("Handoff"),
		helpRow("↵/o", "attach to the selected session"),
		helpRow("ctrl-q", "toggle attach/detach (detach when in, attach from the list)"),
		helpRow("ctrl-x", "kill the session you're attached to"),
		helpRow("→", "send a message (without attaching)"),
		helpRow("c", "checkout: commit changes + pause"),
		helpRow("p", "commit & push branch"),
		helpRow("r", "resume a paused session"),
		helpRow("y", "copy branch name to clipboard"),
		"",
		helpHeaderStyle().Render("Groups"),
		helpRow("J / K", "reorder within a repo group"),
		helpRow("{ / }", "move a whole group up / down"),
		helpRow("space", "collapse / expand group"),
		helpRow("Z", "collapse / expand all"),
		"",
		helpHeaderStyle().Render("Other"),
		helpRow("?", "toggle this cheatsheet"),
		helpRow("q", "quit"),
		"",
		legend,
		"",
		helpDimStyle().Render("press any key to close"),
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
		"",
		helpDimStyle().Render("press any key to begin"),
	)
}

func (h helpTypeGeneral) mask() uint32 { return 1 }

// helpTypeWelcome uses bit 4; bits 1-3 belonged to retired teaching modals.
func (h helpTypeWelcome) mask() uint32 { return 1 << 4 }

// showHelpScreen displays a help overlay. The cheatsheet (helpTypeGeneral)
// always shows on demand; one-time screens (welcome) show only until their seen
// bit is set. onDismiss is retained for compatibility but is now always nil.
func (m *home) showHelpScreen(helpType helpText, onDismiss func()) (tea.Model, tea.Cmd) {
	var alwaysShow bool
	switch helpType.(type) {
	case helpTypeGeneral:
		alwaysShow = true
	}

	flag := helpType.mask()

	if alwaysShow || (m.appState.GetHelpScreensSeen()&flag) == 0 {
		if err := m.appState.SetHelpScreensSeen(m.appState.GetHelpScreensSeen() | flag); err != nil {
			log.WarningLog.Printf("Failed to save help screen state: %v", err)
		}

		m.textOverlay = overlay.NewTextOverlay(helpType.toContent())
		m.textOverlay.OnDismiss = onDismiss
		m.state = stateHelp
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
	// Any key press will close the help overlay
	shouldClose := m.textOverlay.HandleKeyPress(msg)
	if shouldClose {
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
