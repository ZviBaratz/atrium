package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// claudeFieldNA is the dim placeholder the claude-only fields (model and
// permission mode) render while the form's effective program is not Claude
// Code; their enabled state is driven together by syncClaudeFieldsEnabled.
const claudeFieldNA = "  n/a — the selected profile is not Claude Code"

// chipRow is the state machine behind the chip-style fields: a horizontal row
// of options with a clamped cursor, focus, and an inert state. By convention
// the first chip is the no-op ("inherit") choice, so selected returns "" for
// it. ModeField is a pure chip row; ModelField layers its free-text custom
// mode on top.
type chipRow struct {
	options  []string
	cursor   int
	focused  bool
	disabled bool
}

// Focus gives the row focus.
func (c *chipRow) Focus() { c.focused = true }

// Blur removes focus from the row.
func (c *chipRow) Blur() { c.focused = false }

// SetDisabled toggles the inert state (the effective program is not claude).
func (c *chipRow) SetDisabled(disabled bool) { c.disabled = disabled }

// Disabled reports whether the row is inert.
func (c *chipRow) Disabled() bool { return c.disabled }

// moveCursor cycles the chips with the arrow keys (Up/Down accepted alongside
// Left/Right, matching the profile picker), clamping at both ends. Every other
// key is a no-op — in particular Esc is never consumed, staying the form's
// close key.
func (c *chipRow) moveCursor(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		if c.cursor > 0 {
			c.cursor--
		}
	case tea.KeyRight, tea.KeyDown:
		if c.cursor < len(c.options)-1 {
			c.cursor++
		}
	}
}

// selected returns the cursor chip, or "" when the row should contribute
// nothing: disabled, or sitting on the first (inherit) chip.
func (c *chipRow) selected() string {
	if c.disabled || c.cursor == 0 {
		return ""
	}
	return c.options[c.cursor]
}

// render renders the chip row (the profile-picker idiom): the cursor chip is
// highlighted when focused, plain when not, every other chip dim, with dim "·"
// separators.
func (c *chipRow) render() string {
	var s strings.Builder
	for i, opt := range c.options {
		label := " " + opt + " "
		switch {
		case i == c.cursor && c.focused:
			s.WriteString(ppSelectedStyle().Render(label))
		case i == c.cursor:
			s.WriteString(label)
		default:
			s.WriteString(mfDimStyle().Render(label))
		}
		if i < len(c.options)-1 {
			s.WriteString(mfDimStyle().Render("·"))
		}
	}
	return s.String()
}
