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
// of options with a wrapping cursor, focus, and an inert state. By convention
// the first chip is the no-op ("default") choice, so selected returns "" for
// it. ModeField is a pure chip row; ModelField layers its free-text custom
// mode on top.
type chipRow struct {
	options  []string
	labels   []string // display labels; nil = use options as-is (len must equal len(options) when set)
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

// wrapIndex moves cur by delta within [0,n), wrapping at both ends. A
// non-positive n (no options) returns 0, keeping callers panic-free since
// "% 0" would panic where the old clamp checks were silently safe.
func wrapIndex(cur, delta, n int) int {
	if n <= 0 {
		return 0
	}
	return ((cur+delta)%n + n) % n
}

// moveCursor cycles the chips with the arrow keys (Up/Down accepted alongside
// Left/Right, matching the profile picker), wrapping at both ends so one keypress
// reaches the opposite end. Every other key is a no-op — in particular Esc is
// never consumed, staying the form's close key.
func (c *chipRow) moveCursor(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		c.cursor = wrapIndex(c.cursor, -1, len(c.options))
	case tea.KeyRight, tea.KeyDown:
		c.cursor = wrapIndex(c.cursor, +1, len(c.options))
	}
}

// selected returns the cursor chip, or "" when the row should contribute
// nothing: disabled, or sitting on the first (default) chip.
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
		displayLabel := opt
		if c.labels != nil {
			displayLabel = c.labels[i]
		}
		label := " " + displayLabel + " "
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
