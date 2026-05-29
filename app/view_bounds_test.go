package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/ansi"
)

// The composed View() must never exceed the terminal it was given: if it emits
// more rows than the height, the terminal scrolls and bubbletea's line-diffing
// desyncs, leaving stale fragments and a popup that looks mis-placed. Every line
// must also be exactly the terminal width. We assert both invariants across a
// matrix of sizes, for the plain view and the (tallest) create-form overlay.
func TestViewFitsTerminalBounds(t *testing.T) {
	sizes := [][2]int{{200, 50}, {210, 48}, {120, 30}, {160, 40}, {235, 55}, {80, 24}}

	for _, withOverlay := range []bool{false, true} {
		for _, dim := range sizes {
			w, h := dim[0], dim[1]

			home := newCreateFormHome(t)
			if withOverlay {
				home.newSessionPath = t.TempDir()
				home.state = statePrompt
				home.textInputOverlay = home.newSessionFormOverlay()
			}
			home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})

			lines := strings.Split(home.View(), "\n")

			if len(lines) > h {
				t.Errorf("overlay=%v size=%dx%d: View() emitted %d lines, exceeds height %d",
					withOverlay, w, h, len(lines), h)
			}
			for i, l := range lines {
				if pw := ansi.PrintableRuneWidth(l); pw != w {
					t.Errorf("overlay=%v size=%dx%d: line %d width=%d, expected %d",
						withOverlay, w, h, i, pw, w)
					break
				}
			}
		}
	}
}
