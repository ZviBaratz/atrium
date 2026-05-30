package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestConfirmationOverlay_AltConfirmKey(t *testing.T) {
	ctrlX := tea.KeyMsg{Type: tea.KeyCtrlX}

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

		shouldClose := c.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

		assert.True(t, shouldClose)
		assert.True(t, c.Confirmed)
	})
}
