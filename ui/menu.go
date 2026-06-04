package ui

import (
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
)

// Hint-bar styles read the active theme at render time: keys in primary text,
// descriptions dim, separators faint, progress in accent.
func keyStyle() lipgloss.Style      { return theme.Current().FgStyle() }
func descStyle() lipgloss.Style     { return theme.Current().DimStyle() }
func sepStyle() lipgloss.Style      { return theme.Current().FaintStyle() }
func progressStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

var separator = " · "

// MenuState represents different states the menu can be in
type MenuState int

const (
	// StateDefault is the hint bar shown when a session is selected.
	StateDefault MenuState = iota
	// StateEmpty is the hint bar shown when no sessions exist.
	StateEmpty
	// StateNewInstance is the bar shown while naming a new session inline.
	StateNewInstance
	// StatePrompt is the bar shown while a text-input overlay is up. Those
	// overlays self-document and the bar is hidden behind them (menuVisible),
	// so this renders only the submit cue, for the callers that still set it.
	StatePrompt
	// StateFilter is the bar shown while typing an incremental filter query.
	StateFilter
	// StateGeneratingName is shown while a session name is being generated in the
	// background; the hint bar reports progress instead of the usual options.
	StateGeneratingName
)

// defaultHintKeys are the high-value bindings the always-on bar surfaces during
// plain navigation with a session selected. Deliberately few: the bar is a
// reminder that keys exist (and that ? lists them all), not a reference card.
var defaultHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyQuickSend, keys.KeyKill, keys.KeyHelp}

// emptyHintKeys are the bindings surfaced when no sessions exist yet.
var emptyHintKeys = []keys.KeyName{keys.KeyNew, keys.KeyPrompt, keys.KeyHelp, keys.KeyQuit}

// Menu is the bottom hint bar: a single line of the most useful keybindings for
// the current UI state, with ? as the doorway to the full cheatsheet.
type Menu struct {
	height, width int
	state         MenuState
	hasInstance   bool
	activeTab     int

	// newInstanceHint is the target repo shown while naming a new session.
	newInstanceHint string
}

// NewMenu returns a Menu in the empty state.
func NewMenu() *Menu {
	return &Menu{state: StateEmpty}
}

// SetState updates the menu state.
func (m *Menu) SetState(state MenuState) {
	m.state = state
}

// SetNewInstanceHint sets the target-repo hint shown while naming a new session.
func (m *Menu) SetNewInstanceHint(repo string) {
	m.newInstanceHint = repo
}

// SetInstance records whether a session is selected, which decides between the
// default and empty hint sets. Special states (NewInstance, Prompt, Filter,
// GeneratingName) persist across the periodic instanceChanged ticks.
func (m *Menu) SetInstance(instance *session.Instance) {
	m.hasInstance = instance != nil
	if m.state == StateDefault || m.state == StateEmpty {
		if m.hasInstance {
			m.state = StateDefault
		} else {
			m.state = StateEmpty
		}
	}
}

// SetActiveTab updates the currently active tab; the panes with a scroll mode
// (diff/terminal) add a scroll hint to the default bar.
func (m *Menu) SetActiveTab(tab int) {
	m.activeTab = tab
}

// SetSize sets the width of the window. The menu will be centered horizontally within this width.
func (m *Menu) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// renderHintLine renders a flat "key desc · key desc" line for the given bindings.
func renderHintLine(names []keys.KeyName) string {
	var s strings.Builder
	for i, k := range names {
		binding := keys.GlobalkeyBindings[k]
		if i > 0 {
			s.WriteString(sepStyle().Render(separator))
		}
		s.WriteString(keyStyle().Render(binding.Help().Key))
		s.WriteString(" ")
		s.WriteString(descStyle().Render(binding.Help().Desc))
	}
	return s.String()
}

func (m *Menu) String() string {
	var line string
	switch m.state {
	case StateGeneratingName:
		// While generating a name, the bar shows a single status line.
		line = progressStyle().Render("✨ Generating name…")
	case StateNewInstance:
		line = renderHintLine([]keys.KeyName{keys.KeySubmitName})
		// While naming a new session, show which repo it will be created in.
		if m.newInstanceHint != "" {
			line += sepStyle().Render(separator) +
				keyStyle().Render("in ") + descStyle().Render(m.newInstanceHint)
		}
	case StatePrompt:
		line = renderHintLine([]keys.KeyName{keys.KeySubmitName})
	case StateFilter:
		line = keyStyle().Render("enter") + " " + descStyle().Render("accept") +
			sepStyle().Render(separator) +
			keyStyle().Render("esc") + " " + descStyle().Render("clear")
	case StateEmpty:
		line = renderHintLine(emptyHintKeys)
	default: // StateDefault
		hints := defaultHintKeys
		if m.activeTab == DiffTab || m.activeTab == TerminalTab {
			hints = append(append([]keys.KeyName{}, hints...), keys.KeyShiftUp)
		}
		line = renderHintLine(hints)
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, line)
}
