package overlay

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// flattenConfirm reduces a rendered confirmation dialog to one space-normalized
// line: ANSI stripped, the box border dropped, and every whitespace run collapsed.
// The 50-column box wraps both the message and the key hint, so an assertion can
// only name either in full through this.
func flattenConfirm(rendered string) string {
	var b strings.Builder
	for _, r := range xansi.Strip(rendered) {
		if r >= 0x2500 && r <= 0x257F { // box-drawing: the border
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// The key hint names what confirming does, not the word "confirm", once a call site
// sets a verb label (#399). Keys are untouched, so the label is hint text only.
func TestConfirmationOverlay_ConfirmLabel(t *testing.T) {
	t.Run("default says confirm", func(t *testing.T) {
		c := NewConfirmationOverlay("Pause 3 active sessions?")

		hint := flattenConfirm(c.Render())

		assert.Contains(t, hint, "Press y to confirm, n or esc to cancel")
	})

	t.Run("a label replaces the word confirm", func(t *testing.T) {
		c := NewConfirmationOverlay("Pause 3 active sessions?")
		c.SetConfirmLabel("pause 3 sessions")

		hint := flattenConfirm(c.Render())

		assert.Contains(t, hint, "Press y to pause 3 sessions, n or esc to cancel")
		assert.NotContains(t, hint, "to confirm", "the label replaces the generic word")
	})

	t.Run("an empty label keeps the default", func(t *testing.T) {
		c := NewConfirmationOverlay("Kill session 'x'?")
		c.SetConfirmLabel("")

		hint := flattenConfirm(c.Render())

		assert.Contains(t, hint, "Press y to confirm, n or esc to cancel")
	})

	t.Run("a label composes with the alt confirm key", func(t *testing.T) {
		c := NewConfirmationOverlay("Kill 2 marked sessions?")
		c.SetConfirmAltKey("ctrl+x")
		c.SetConfirmLabel("kill 2 sessions")

		hint := flattenConfirm(c.Render())

		assert.Contains(t, hint, "Press y (or ctrl+x) to kill 2 sessions, n or esc to cancel")
	})
}

func TestConfirmationOverlay_AltConfirmKey(t *testing.T) {
	ctrlX := keyMsg("ctrl+x")

	t.Run("alt key confirms when set", func(t *testing.T) {
		c := NewConfirmationOverlay("Kill session?")
		c.SetConfirmAltKey("ctrl+x")

		shouldClose := c.HandleKeyPress(ctrlX)

		assert.True(t, shouldClose)
		assert.True(t, c.Confirmed)
	})

	t.Run("same key is ignored when alt key unset", func(t *testing.T) {
		c := NewConfirmationOverlay("Kill session?") // ConfirmAltKey defaults to ""

		shouldClose := c.HandleKeyPress(ctrlX)

		assert.False(t, shouldClose, "an empty ConfirmAltKey must not match a real key")
		assert.False(t, c.Confirmed)
	})

	t.Run("primary confirm key still works alongside an alt key", func(t *testing.T) {
		c := NewConfirmationOverlay("Kill session?")
		c.SetConfirmAltKey("ctrl+x")

		shouldClose := c.HandleKeyPress(textMsg("y"))

		assert.True(t, shouldClose)
		assert.True(t, c.Confirmed)
	})
}
