package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	// confirmLabel names what confirming does ("pause 3 sessions"), replacing the
	// generic "confirm" in the key hint. Empty keeps "confirm" — see SetConfirmLabel.
	confirmLabel string
	// destructive selects which palette role paints the border. It stores the
	// *role*, not a resolved colour: a colour captured here would be the palette in
	// force when the overlay was built, and a theme change while a confirmation is
	// open would leave this one dialog on the old palette after everything else had
	// restyled. Same reasoning ApplyBarStyle gives for reading theme.Current() at
	// call time — a colour fixed too early is the bug, and a colour passed in as an
	// argument is a wire no test guards.
	destructive bool
}

// NewConfirmationOverlay creates a new confirmation dialog overlay with the given
// message. The border is the accent color; destructive confirmations (kill) opt
// into the danger color via SetDestructive, so red keeps meaning "destructive"
// instead of "any confirmation".
func NewConfirmationOverlay(message string) *ConfirmationOverlay {
	return &ConfirmationOverlay{
		Dismissed:  false,
		message:    message,
		width:      50, // Default width
		ConfirmKey: "y",
		CancelKey:  "n",
	}
}

// HandleKeyPress processes a key press and updates the state
// Returns true if the overlay should be closed
func (c *ConfirmationOverlay) HandleKeyPress(msg tea.KeyPressMsg) bool {
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
func (c *ConfirmationOverlay) Render() string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(c.BorderColor()).
		Padding(1, 2).
		// +2 for the left/right border: Lip Gloss v2 counts the border inside
		// Width, where v1 added it outside. Same silent semantic change as in
		// theme.Panel — see the note there.
		Width(c.width + 2)

	// Add the confirmation instructions. When an alt confirm key is set (e.g. the
	// kill chord for double-tap), surface it alongside the primary confirm key.
	hintStyle := theme.Current().OverlayHintStyle()
	keyStyle := hintStyle.Bold(true)
	confirmHint := keyStyle.Render(c.ConfirmKey)
	if c.ConfirmAltKey != "" {
		confirmHint += hintStyle.Render(" (or ") + keyStyle.Render(c.ConfirmAltKey) + hintStyle.Render(")")
	}
	// A call site that set a verb label has the hint name the action instead of the
	// generic "confirm" (#399); the keys it names are the same either way.
	action := "confirm"
	if c.confirmLabel != "" {
		action = c.confirmLabel
	}
	content := c.message + "\n\n" +
		hintStyle.Render("Press ") + confirmHint + hintStyle.Render(" to "+action+", ") +
		keyStyle.Render(c.CancelKey) + hintStyle.Render(" or ") +
		keyStyle.Render("esc") + hintStyle.Render(" to cancel")

	// Apply the border style and return
	return style.Render(content)
}

// SetWidth sets the width of the confirmation overlay
func (c *ConfirmationOverlay) SetWidth(width int) {
	c.width = width
}

// SetDestructive paints the border with the danger role instead of accent, for a
// confirmation that destroys work (kill).
func (c *ConfirmationOverlay) SetDestructive() {
	c.destructive = true
}

// BorderColor resolves the border color against the live palette: danger for a
// destructive confirmation, accent otherwise. Resolved on every call rather than
// stored, so an open dialog follows a theme change like the rest of the frame.
func (c *ConfirmationOverlay) BorderColor() theme.Color {
	if c.destructive {
		return theme.Current().Palette.Danger
	}
	return theme.Current().Palette.Accent
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

// SetConfirmLabel names what confirming does, as a verb phrase in the caller's own
// words ("pause 3 sessions"), so the hint reads "Press y to pause 3 sessions, n or
// esc to cancel" instead of the generic "to confirm" (#399). Empty (the default)
// keeps "confirm", so adoption is opt-in per dialog.
//
// This is hint text only: the keys stay y / n / esc, deliberately — a verb-labeled
// confirmation is a copy change, not a muscle-memory one, and the single alternate
// confirm slot is already spoken for by the kill chord (see SetConfirmAltKey, #448).
func (c *ConfirmationOverlay) SetConfirmLabel(label string) {
	c.confirmLabel = label
}
