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
	// cancelLabel names what declining does, replacing the generic "cancel" in the
	// key hint. Empty keeps "cancel" — see SetCancelLabel.
	cancelLabel string
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
		width:      52, // Default width (outer cells; the classic 50 of v1 plus the border)
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

// Answers reports whether the overlay itself acts on a key — the y/n/esc set it
// renders in its own hint line, plus any alt-confirm chord.
//
// It exists for the callers that intercept a key ahead of HandleKeyPress (the
// over-capacity dialog's settings deep link, app/app_keys.go). Those run first by
// construction, so without asking here a settings key rebound onto y would take
// the dialog's own answer away from it: the user presses the y the dialog is
// advertising, the interceptor opens the settings panel, and the staged action is
// discarded with nothing said.
func (c *ConfirmationOverlay) Answers(key string) bool {
	if key == "" {
		return false
	}
	switch key {
	case c.ConfirmKey, c.CancelKey, "esc":
		return true
	}
	return c.ConfirmAltKey != "" && key == c.ConfirmAltKey
}

// Render renders the confirmation overlay
func (c *ConfirmationOverlay) Render() string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(c.BorderColor()).
		Padding(1, 2).
		Width(c.width)

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
	dismiss := "cancel"
	if c.cancelLabel != "" {
		dismiss = c.cancelLabel
	}
	content := c.message + "\n\n" +
		hintStyle.Render("Press ") + confirmHint + hintStyle.Render(" to "+action+", ") +
		keyStyle.Render(c.CancelKey) + hintStyle.Render(" or ") +
		keyStyle.Render("esc") + hintStyle.Render(" to "+dismiss)

	// Apply the border style and return
	return style.Render(content)
}

// ConfirmSize keeps the dialog's classic width on normal terminals — 52
// outer cells, the classic 50 plus its border — and shrinks with narrow ones,
// holding a one-column margin outside the border and a readable floor below
// that. It was the one overlay excluded from resize handling before the
// resize walk. An unsized terminal gets the classic width.
var ConfirmSize = SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22}

// SetSize sets the dialog's TOTAL width, border and padding included — outer
// cells, which is what lipgloss v2's Width means (see theme.Panel). The
// height is accepted and ignored so the resize walk can hand every overlay
// the same call: the dialog hugs its message.
func (c *ConfirmationOverlay) SetSize(width, _ int) {
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

// SetConfirmAltKey sets an optional second key that also confirms the action: the
// key that opened the dialog, so pressing it again is the double-tap. Callers go
// through app's armDoubleTap rather than here, which is what applies the config gate
// and refuses a key the dialog already answers.
//
// It is the only key setter. SetConfirmKey and SetCancelKey were deleted with #798
// (#465): they had never had a caller, and `unused` does not flag an exported
// method, so nothing in CI would ever have said so. They also implied a
// configurability this overlay does not have — y / n / esc are the answer keys, and
// this slot is how a dialog gains a second one.
func (c *ConfirmationOverlay) SetConfirmAltKey(key string) {
	c.ConfirmAltKey = key
}

// SetConfirmLabel names what confirming does, as a verb phrase in the caller's own
// words ("pause 3 sessions"), so the hint reads "Press y to pause 3 sessions, n or
// esc to cancel" instead of the generic "to confirm" (#399). Empty (the default)
// keeps "confirm", so adoption is opt-in per dialog.
//
// This is hint text only: the keys stay y / n / esc, deliberately — a verb-labeled
// confirmation is a copy change, not a muscle-memory one. The alternate confirm slot
// is a separate axis, and since #798 it is the opening key of whichever dialog this
// is rather than the kill chord alone (see SetConfirmAltKey).
func (c *ConfirmationOverlay) SetConfirmLabel(label string) {
	c.confirmLabel = label
}

// SetCancelLabel names what DECLINING does, for the dialog whose decline is not a
// plain "nothing happens". Every other confirmation aborts on n/esc and keeps the
// default "cancel"; the repo-trust prompt (#814) proceeds with the create either
// way — declining spawns the session with the repo's config inert — and a hint
// still reading "cancel" there would promise an abort it does not perform.
func (c *ConfirmationOverlay) SetCancelLabel(label string) {
	c.cancelLabel = label
}
