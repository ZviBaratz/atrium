package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/ZviBaratz/atrium/ui/overlay"
)

// The dialog keeps its classic width on normal terminals, shrinks with narrow
// ones, and never collapses below a readable floor. The table is the old
// confirmWidth helper's, translated to outer cells (each value is the old
// border-exclusive one plus 2) — the box these numbers describe is unchanged.
func TestConfirmWidth(t *testing.T) {
	fitW := func(termW int) int {
		w, _ := overlay.ConfirmSize.Fit(termW, 0)
		return w
	}
	assert.Equal(t, 52, fitW(0), "unsized (startup/tests) keeps the default")
	assert.Equal(t, 52, fitW(120))
	assert.Equal(t, 52, fitW(54), "54-2 = 52: exactly fits")
	assert.Equal(t, 42, fitW(44))
	assert.Equal(t, 22, fitW(10), "floor even on absurdly narrow terminals")
}

// A confirmation opened on a narrow terminal must not spill past the screen
// edge — it was the one overlay excluded from resize handling.
func TestConfirmDialogFitsNarrowTerminal(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 44, Height: 20})

	h.confirmAction("Push changes from session 'a-rather-long-session-name'?", instantAction, nil)

	for i, l := range strings.Split(h.View().Content, "\n") {
		if w := ansi.PrintableRuneWidth(l); w > 44 {
			t.Fatalf("line %d width %d exceeds the 44-column terminal", i, w)
		}
	}
}

// Resizing while the dialog is open re-fits it, like every other overlay.
func TestConfirmDialogRefitsOnResize(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.confirmAction("Push?", instantAction, nil)

	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 40, Height: 20})

	for i, l := range strings.Split(h.View().Content, "\n") {
		if w := ansi.PrintableRuneWidth(l); w > 40 {
			t.Fatalf("line %d width %d exceeds the 40-column terminal after resize", i, w)
		}
	}
}
