package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmationOverlay represents a confirmation dialog overlay
type ConfirmationOverlay struct {
	// Whether the overlay has been dismissed
	Dismissed bool
	// Confirmed reports whether the overlay was dismissed by confirming (vs cancelling)
	Confirmed bool
	// Message to display in the overlay
	message string
	// Width of the overlay
	width int
	// Custom confirm key (defaults to 'y')
	ConfirmKey string
	// ConfirmAltKey is an optional second key that also confirms. Empty means
	// unused. Set, for example, to the kill chord so pressing it again confirms a
	// kill dialog (double-tap to kill).
	ConfirmAltKey string
	// Custom cancel key (defaults to 'n')
	CancelKey string
	// Custom styling options
	borderColor lipgloss.Color
}

// NewConfirmationOverlay creates a new confirmation dialog overlay with the given message
func NewConfirmationOverlay(message string) *ConfirmationOverlay {
	return &ConfirmationOverlay{
		Dismissed:   false,
		message:     message,
		width:       50, // Default width
		ConfirmKey:  "y",
		CancelKey:   "n",
		borderColor: theme.Current().Palette.Danger, // attention/destructive color
	}
}

// HandleKeyPress processes a key press and updates the state
// Returns true if the overlay should be closed
func (c *ConfirmationOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	s := msg.String()
	switch {
	case s == c.ConfirmKey, c.ConfirmAltKey != "" && s == c.ConfirmAltKey:
		c.Dismissed = true
		c.Confirmed = true
		return true
	case s == c.CancelKey, s == "esc":
		c.Dismissed = true
		return true
	default:
		// Ignore other keys in confirmation state
		return false
	}
}

// Render renders the confirmation overlay
func (c *ConfirmationOverlay) Render(opts ...WhitespaceOption) string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(c.borderColor).
		Padding(1, 2).
		Width(c.width)

	// Add the confirmation instructions. When an alt confirm key is set (e.g. the
	// kill chord for double-tap), surface it alongside the primary confirm key.
	confirmHint := lipgloss.NewStyle().Bold(true).Render(c.ConfirmKey)
	if c.ConfirmAltKey != "" {
		confirmHint += " (or " + lipgloss.NewStyle().Bold(true).Render(c.ConfirmAltKey) + ")"
	}
	content := c.message + "\n\n" +
		"Press " + confirmHint + " to confirm, " +
		lipgloss.NewStyle().Bold(true).Render(c.CancelKey) + " or " +
		lipgloss.NewStyle().Bold(true).Render("esc") + " to cancel"

	// Apply the border style and return
	return style.Render(content)
}

// SetWidth sets the width of the confirmation overlay
func (c *ConfirmationOverlay) SetWidth(width int) {
	c.width = width
}

// SetBorderColor sets the border color of the confirmation overlay
func (c *ConfirmationOverlay) SetBorderColor(color lipgloss.Color) {
	c.borderColor = color
}

// SetConfirmKey sets the key used to confirm the action
func (c *ConfirmationOverlay) SetConfirmKey(key string) {
	c.ConfirmKey = key
}

// SetConfirmAltKey sets an optional second key that also confirms the action.
func (c *ConfirmationOverlay) SetConfirmAltKey(key string) {
	c.ConfirmAltKey = key
}

// SetCancelKey sets the key used to cancel the action
func (c *ConfirmationOverlay) SetCancelKey(key string) {
	c.CancelKey = key
}
