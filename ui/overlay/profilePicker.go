package overlay

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ProfilePicker is an embeddable component for selecting a profile.
// It displays a horizontal selector with left/right arrow navigation.
type ProfilePicker struct {
	profiles []config.Profile
	cursor   int
	focused  bool
	width    int
}

// NewProfilePicker creates a new profile picker with the given profiles.
// The first profile is selected by default.
func NewProfilePicker(profiles []config.Profile) *ProfilePicker {
	return &ProfilePicker{
		profiles: profiles,
	}
}

// Focus gives the profile picker focus.
func (pp *ProfilePicker) Focus() {
	pp.focused = true
}

// Blur removes focus from the profile picker.
func (pp *ProfilePicker) Blur() {
	pp.focused = false
}

// SetWidth sets the rendering width.
func (pp *ProfilePicker) SetWidth(w int) {
	pp.width = w
}

// HandleKeyPress processes a key event. Returns true if consumed. Up/Down are accepted
// alongside Left/Right so navigation is ↑/↓ everywhere in the form, even though this picker
// renders horizontally. The cursor wraps at both ends so one keypress reaches the opposite end.
func (pp *ProfilePicker) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		pp.cursor = wrapIndex(pp.cursor, -1, len(pp.profiles))
		return true
	case tea.KeyRight, tea.KeyDown:
		pp.cursor = wrapIndex(pp.cursor, +1, len(pp.profiles))
		return true
	}
	return false
}

// GetSelectedProfile returns the currently selected profile, or the zero
// Profile when the picker holds none (callers construct pickers only for
// non-empty profile lists, but a safety guard must not itself panic).
func (pp *ProfilePicker) GetSelectedProfile() config.Profile {
	if len(pp.profiles) == 0 {
		return config.Profile{}
	}
	if pp.cursor < 0 || pp.cursor >= len(pp.profiles) {
		return pp.profiles[0]
	}
	return pp.profiles[pp.cursor]
}

// HasMultiple returns true if there is more than one profile to choose from.
func (pp *ProfilePicker) HasMultiple() bool {
	return len(pp.profiles) > 1
}

func ppLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }
func ppSelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().Palette.Accent).
		Foreground(theme.Current().Palette.Bg)
}
func ppDimStyle() lipgloss.Style { return theme.Current().DimStyle() }

// Render renders the profile picker.
func (pp *ProfilePicker) Render() string {
	var s strings.Builder
	s.WriteString(ppLabelStyle().Render("Profile"))

	if pp.HasMultiple() && pp.focused {
		s.WriteString(ppDimStyle().Render("  ↑↓ to change"))
	}
	s.WriteString("\n\n")

	for i, p := range pp.profiles {
		// Prefix each option with its agent's identity glyph (same glyph the
		// session list shows) so picking among same-named profiles is visual.
		glyph, _ := theme.Current().AgentGlyph(string(agent.Resolve(p.Program).Key))
		label := " " + glyph + " " + p.Name + " "
		if i == pp.cursor && pp.focused {
			s.WriteString(ppSelectedStyle().Render(label))
		} else if i == pp.cursor {
			s.WriteString(label)
		} else {
			s.WriteString(ppDimStyle().Render(label))
		}
		if i < len(pp.profiles)-1 {
			s.WriteString(ppDimStyle().Render(" | "))
		}
	}

	return s.String()
}
