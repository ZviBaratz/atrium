package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestCenterInBox locks the fit-path contract the diff/preview/err/menu panes
// delegate to: for content that already fits, centerInBox is exactly
// lipgloss.Place center/center over a width×height box — the MaxWidth/MaxHeight
// clamp is a no-op — so #249's extraction preserved their placeholder layout.
// TestCenterInBoxClampsOverflow covers the clamp itself.
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

// TestCenterInBoxClampsOverflow is the other half of the contract: content wider
// or taller than the box is truncated to it, so an oversize fallback can't
// inflate the frame and throw centered overlays off-center (#251). lipgloss.Place
// alone centers but does not clip, which is the bug this clamp closes.
func TestCenterInBoxClampsOverflow(t *testing.T) {
	const w, h = 4, 2
	out := centerInBox(w, h, "way too wide\nand too\ntall")

	if gotW := lipgloss.Width(out); gotW > w {
		t.Errorf("clamped width = %d, want <= %d", gotW, w)
	}
	if gotH := lipgloss.Height(out); gotH > h {
		t.Errorf("clamped height = %d, want <= %d", gotH, h)
	}
}
