package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestCenterInBox locks the contract the diff/preview/err/menu panes delegate to:
// centerInBox is exactly lipgloss.Place center/center over a width×height box, so
// the extraction preserves their existing placeholder layout.
func TestCenterInBox(t *testing.T) {
	const w, h = 12, 5
	out := centerInBox(w, h, "hi")

	if want := lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, "hi"); out != want {
		t.Fatalf("centerInBox is not lipgloss.Place center/center")
	}
	if gotW := lipgloss.Width(out); gotW != w {
		t.Errorf("box width = %d, want %d", gotW, w)
	}
	if gotH := lipgloss.Height(out); gotH != h {
		t.Errorf("box height = %d, want %d", gotH, h)
	}
}
