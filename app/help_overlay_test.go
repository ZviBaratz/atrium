package app

import (
	"github.com/ZviBaratz/atrium/internal/testutil"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

	lines := strings.Split(home.View().Content, "\n")
	if len(lines) > h {
		t.Fatalf("View() emitted %d lines, exceeds terminal height %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := xansi.StringWidth(l); lw != w {
			t.Fatalf("line %d width=%d, want exactly %d", i, lw, w)
		}
	}

	plain := xansi.Strip(home.View().Content)
	if !strings.Contains(plain, "Atrium — Keys") {
		t.Fatal("help title not visible; the dialog top is cut off")
	}
	if !strings.Contains(plain, "scroll") {
		t.Fatal("scroll footer not visible on an overflowing help dialog")
	}

	// Scrolling must keep the help open and reveal later content.
	home.handleKeyPress(keyMsg("down"))
	if home.state != stateHelp {
		t.Fatal("down closed the help overlay; want it to scroll")
	}
	home.handleKeyPress(textMsg("x"))
	if home.state != stateDefault {
		t.Fatal("a non-scroll key did not close the help overlay")
	}
}

// TestHelpOverlayFullBleedsAtTheFloor pins #695's fix at the width it was
// reported: the real general cheatsheet at the 80-column floor takes the full
// terminal width. Its old cap kept a one-column margin, which rendered as a
// doubled border beside the frame's own border; SnapFullBleed is the rule
// that forbids that zone. Inverting or off-by-one-ing the rule leaves the box
// at 77-79 columns, and this equality is what dies.
func TestHelpOverlayFullBleedsAtTheFloor(t *testing.T) {
	home := newCreateFormHome(t)
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	home.showHelpScreen(helpTypeGeneral{}, nil)

	widest := 0
	for _, l := range strings.Split(xansi.Strip(home.textOverlay.Render()), "\n") {
		if lw := xansi.StringWidth(l); lw > widest {
			widest = lw
		}
	}
	if widest != 80 {
		t.Fatalf("cheatsheet box at the 80-column floor is %d wide; want the full 80 (#695)", widest)
	}
}

// While the help modal is up, the wheel scrolls it (wherever it hovers), a
// click inside the box is inert, and a click outside dismisses — the mouse
// mirror of the scroll-keys-scroll / any-other-key-closes semantics.
func TestHelpOverlayMouse(t *testing.T) {
	const w, h = 160, 15

	mouse := func(btn tea.MouseButton, x, y int) tea.MouseMsg {
		if btn == tea.MouseWheelUp || btn == tea.MouseWheelDown ||
			btn == tea.MouseWheelLeft || btn == tea.MouseWheelRight {
			return testutil.MouseWheel(x, y, btn)
		}
		return testutil.MouseClick(x, y, btn)
	}

	home := newCreateFormHome(t)
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})
	home.showHelpScreen(helpTypeGeneral{}, nil)

	// At 160×15 the overflowing dialog spans the full height and hugs its
	// natural width, centered well inside the terminal, so column 0 is
	// outside the box. Narrower stagings stopped working with #695: at the
	// 80-column floor the box now takes the full width (SnapFullBleed), and
	// there is no outside column to click.
	before := xansi.Strip(home.View().Content)
	home.Update(mouse(tea.MouseWheelDown, w/2, h/2))
	if home.state != stateHelp {
		t.Fatal("wheel closed the help overlay; want it to scroll")
	}
	if after := xansi.Strip(home.View().Content); after == before {
		t.Fatal("wheel down did not scroll the help overlay")
	}

	home.Update(mouse(tea.MouseLeft, w/2, h/2))
	if home.state != stateHelp {
		t.Fatal("a click inside the box closed the help overlay; want it inert")
	}

	home.Update(mouse(tea.MouseLeft, 0, h/2))
	if home.state != stateDefault {
		t.Fatal("a click outside the box did not close the help overlay")
	}
}
