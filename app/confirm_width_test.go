package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
)

// The dialog keeps its classic width on normal terminals, shrinks with narrow
// ones, and never collapses below a readable floor.
func TestConfirmWidth(t *testing.T) {
	assert.Equal(t, 50, confirmWidth(0), "unsized (startup/tests) keeps the default")
	assert.Equal(t, 50, confirmWidth(120))
	assert.Equal(t, 50, confirmWidth(54), "54-4 = 50: exactly fits")
	assert.Equal(t, 40, confirmWidth(44))
	assert.Equal(t, 20, confirmWidth(10), "floor at 20 even on absurdly narrow terminals")
}

// A confirmation opened on a narrow terminal must not spill past the screen
// edge — it was the one overlay excluded from resize handling.
func TestConfirmDialogFitsNarrowTerminal(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 44, Height: 20})

	h.confirmAction("Push changes from session 'a-rather-long-session-name'?", nil)

	for i, l := range strings.Split(h.View(), "\n") {
		if w := ansi.PrintableRuneWidth(l); w > 44 {
			t.Fatalf("line %d width %d exceeds the 44-column terminal", i, w)
		}
	}
}

// Resizing while the dialog is open re-fits it, like every other overlay.
func TestConfirmDialogRefitsOnResize(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.confirmAction("Push?", nil)

	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 40, Height: 20})

	for i, l := range strings.Split(h.View(), "\n") {
		if w := ansi.PrintableRuneWidth(l); w > 40 {
			t.Fatalf("line %d width %d exceeds the 40-column terminal after resize", i, w)
		}
	}
}
