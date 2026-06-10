package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

// On a terminal shorter than the cheatsheet, the help overlay must window its
// content into the frame instead of overflowing it: the composed View() stays
// exactly terminal-sized, the title row is visible (it used to be cut off the
// top), and the scroll footer replaces the tail of the content.
func TestHelpOverlayFitsShortTerminal(t *testing.T) {
	const w, h = 80, 15

	home := newCreateFormHome(t)
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})
	// Fill the preview pane with the empty-state fallback, like the real app
	// after its first tick: its banner+message block regressed once into
	// widening the frame past the terminal, which shoved the centered overlay
	// off-screen to the right.
	if err := home.tabbedWindow.UpdatePreview(nil); err != nil {
		t.Fatal(err)
	}
	home.showHelpScreen(helpTypeGeneral{}, nil)

	lines := strings.Split(home.View(), "\n")
	if len(lines) > h {
		t.Fatalf("View() emitted %d lines, exceeds terminal height %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := xansi.StringWidth(l); lw != w {
			t.Fatalf("line %d width=%d, want exactly %d", i, lw, w)
		}
	}

	plain := xansi.Strip(home.View())
	if !strings.Contains(plain, "Atrium — Keys") {
		t.Fatal("help title not visible; the dialog top is cut off")
	}
	if !strings.Contains(plain, "scroll") {
		t.Fatal("scroll footer not visible on an overflowing help dialog")
	}

	// Scrolling must keep the help open and reveal later content.
	home.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	if home.state != stateHelp {
		t.Fatal("down closed the help overlay; want it to scroll")
	}
	home.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if home.state != stateDefault {
		t.Fatal("a non-scroll key did not close the help overlay")
	}
}

// While the help modal is up, the wheel scrolls it (wherever it hovers), a
// click inside the box is inert, and a click outside dismisses — the mouse
// mirror of the scroll-keys-scroll / any-other-key-closes semantics.
func TestHelpOverlayMouse(t *testing.T) {
	const w, h = 80, 15

	mouse := func(btn tea.MouseButton, x, y int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: btn}
	}

	home := newCreateFormHome(t)
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})
	home.showHelpScreen(helpTypeGeneral{}, nil)

	// At 80×15 the overflowing dialog spans the full height and is centered
	// horizontally (78 cols wide → x ∈ [1, 78]), so column 0 is outside.
	before := xansi.Strip(home.View())
	home.Update(mouse(tea.MouseButtonWheelDown, w/2, h/2))
	if home.state != stateHelp {
		t.Fatal("wheel closed the help overlay; want it to scroll")
	}
	if after := xansi.Strip(home.View()); after == before {
		t.Fatal("wheel down did not scroll the help overlay")
	}

	home.Update(mouse(tea.MouseButtonLeft, w/2, h/2))
	if home.state != stateHelp {
		t.Fatal("a click inside the box closed the help overlay; want it inert")
	}

	home.Update(mouse(tea.MouseButtonLeft, 0, h/2))
	if home.state != stateDefault {
		t.Fatal("a click outside the box did not close the help overlay")
	}
}
