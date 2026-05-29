package ui

import (
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/ZviBaratz/atrium/session"

	"github.com/charmbracelet/lipgloss"
)

// Hint-bar styles read the active theme at render time: keys in primary text,
// descriptions dim, the primary action group in accent, separators faint.
func keyStyle() lipgloss.Style         { return theme.Current().FgStyle() }
func descStyle() lipgloss.Style        { return theme.Current().DimStyle() }
func sepStyle() lipgloss.Style         { return theme.Current().FaintStyle() }
func actionGroupStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }
func menuStyle() lipgloss.Style        { return lipgloss.NewStyle() }

var separator = " • "
var verticalSeparator = " │ "

// MenuState represents different states the menu can be in
type MenuState int

const (
	StateDefault MenuState = iota
	StateEmpty
	StateNewInstance
	StatePrompt
	// StateGeneratingName is shown while a session name is being generated in the
	// background; the hint bar reports progress instead of the usual options.
	StateGeneratingName
)

type Menu struct {
	options       []keys.KeyName
	height, width int
	state         MenuState
	instance      *session.Instance
	activeTab     int

	// newInstanceHint is the target repo shown while naming a new session.
	newInstanceHint string

	// keyDown is the key which is pressed. The default is -1.
	keyDown keys.KeyName
}

var defaultMenuOptions = []keys.KeyName{keys.KeyNew, keys.KeyPrompt, keys.KeyHelp, keys.KeyQuit}
var newInstanceMenuOptions = []keys.KeyName{keys.KeySubmitName}
var promptMenuOptions = []keys.KeyName{keys.KeySubmitName}

func NewMenu() *Menu {
	return &Menu{
		options:   defaultMenuOptions,
		state:     StateEmpty,
		activeTab: 0,
		keyDown:   -1,
	}
}

func (m *Menu) Keydown(name keys.KeyName) {
	m.keyDown = name
}

func (m *Menu) ClearKeydown() {
	m.keyDown = -1
}

// SetState updates the menu state and options accordingly
func (m *Menu) SetState(state MenuState) {
	m.state = state
	m.updateOptions()
}

// SetNewInstanceHint sets the target-repo hint shown while naming a new session.
func (m *Menu) SetNewInstanceHint(repo string) {
	m.newInstanceHint = repo
}

// SetInstance updates the current instance and refreshes menu options
func (m *Menu) SetInstance(instance *session.Instance) {
	m.instance = instance
	// Only change the state if we're not in a special state (NewInstance, Prompt,
	// or GeneratingName) — those persist across the periodic instanceChanged ticks.
	if m.state != StateNewInstance && m.state != StatePrompt && m.state != StateGeneratingName {
		if m.instance != nil {
			m.state = StateDefault
		} else {
			m.state = StateEmpty
		}
	}
	m.updateOptions()
}

// SetActiveTab updates the currently active tab
func (m *Menu) SetActiveTab(tab int) {
	m.activeTab = tab
	m.updateOptions()
}

// updateOptions updates the menu options based on current state and instance
func (m *Menu) updateOptions() {
	switch m.state {
	case StateEmpty:
		m.options = defaultMenuOptions
	case StateDefault:
		if m.instance != nil {
			// When there is an instance, show that instance's options
			m.addInstanceOptions()
		} else {
			// When there is no instance, show the empty state
			m.options = defaultMenuOptions
		}
	case StateNewInstance:
		m.options = newInstanceMenuOptions
	case StatePrompt:
		m.options = promptMenuOptions
	case StateGeneratingName:
		m.options = nil
	}
}

func (m *Menu) addInstanceOptions() {
	// Loading instances only get minimal options
	if m.instance != nil && m.instance.Status == session.Loading {
		m.options = []keys.KeyName{keys.KeyNew, keys.KeyHelp, keys.KeyQuit}
		return
	}

	// Instance management group
	options := []keys.KeyName{keys.KeyNew, keys.KeyKill, keys.KeyRename}

	// Action group
	actionGroup := []keys.KeyName{keys.KeyEnter, keys.KeySubmit}
	if m.instance.Status == session.Paused {
		actionGroup = append(actionGroup, keys.KeyResume)
	} else {
		actionGroup = append(actionGroup, keys.KeyCheckout)
	}

	// Navigation group (when in diff tab)
	if m.activeTab == DiffTab || m.activeTab == TerminalTab {
		actionGroup = append(actionGroup, keys.KeyShiftUp)
	}

	// System group
	systemGroup := []keys.KeyName{keys.KeyTab, keys.KeyHelp, keys.KeyQuit}

	// Combine all groups
	options = append(options, actionGroup...)
	options = append(options, systemGroup...)

	m.options = options
}

// SetSize sets the width of the window. The menu will be centered horizontally within this width.
func (m *Menu) SetSize(width, height int) {
	m.width = width
	m.height = height
}

func (m *Menu) String() string {
	// While generating a name, the hint bar shows a single status line rather than
	// the usual option groups.
	if m.state == StateGeneratingName {
		msg := actionGroupStyle().Render("✨ Generating name…")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}

	var s strings.Builder

	// Define group boundaries
	groups := []struct {
		start int
		end   int
	}{
		{0, 3}, // Instance management group (n, D, R)
		{3, 6}, // Action group (enter, submit, pause/resume)
		{7, 9}, // System group (tab, help, q)
	}

	for i, k := range m.options {
		binding := keys.GlobalkeyBindings[k]

		var (
			localActionStyle = actionGroupStyle()
			localKeyStyle    = keyStyle()
			localDescStyle   = descStyle()
		)
		if m.keyDown == k {
			localActionStyle = localActionStyle.Underline(true)
			localKeyStyle = localKeyStyle.Underline(true)
			localDescStyle = localDescStyle.Underline(true)
		}

		var inActionGroup bool
		switch m.state {
		case StateEmpty:
			// For empty state, the action group is the first group
			inActionGroup = i <= 1
		default:
			// For other states, the action group is the second group
			inActionGroup = i >= groups[1].start && i < groups[1].end
		}

		if inActionGroup {
			s.WriteString(localActionStyle.Render(binding.Help().Key))
			s.WriteString(" ")
			s.WriteString(localActionStyle.Render(binding.Help().Desc))
		} else {
			s.WriteString(localKeyStyle.Render(binding.Help().Key))
			s.WriteString(" ")
			s.WriteString(localDescStyle.Render(binding.Help().Desc))
		}

		// Add appropriate separator
		if i != len(m.options)-1 {
			isGroupEnd := false
			for _, group := range groups {
				if i == group.end-1 {
					s.WriteString(sepStyle().Render(verticalSeparator))
					isGroupEnd = true
					break
				}
			}
			if !isGroupEnd {
				s.WriteString(sepStyle().Render(separator))
			}
		}
	}

	// While naming a new session, show which repo it will be created in.
	if m.state == StateNewInstance && m.newInstanceHint != "" {
		s.WriteString(sepStyle().Render(verticalSeparator))
		s.WriteString(keyStyle().Render("in "))
		s.WriteString(descStyle().Render(m.newInstanceHint))
	}

	centeredMenuText := menuStyle().Render(s.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, centeredMenuText)
}
