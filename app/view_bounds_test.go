package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/ansi"
)

// The composed View() must never exceed the terminal it was given: if it emits
// more rows than the height, the terminal scrolls and bubbletea's line-diffing
// desyncs, leaving stale fragments and a popup that looks mis-placed. Every line
// must also be exactly the terminal width. We assert both invariants across a
// matrix of sizes, for the plain view and each overlay below.
//
// The accounts panel is here because of #478: a pooled account with no route rules
// of its own rendered 96 cells against an inner width of 80, wrapped, and cost the
// box a row its own height budget had not counted. Nothing at this level could see
// it — the panel's own tests measured widths after lipgloss had padded every line of
// the box to the same width. This pair of assertions is the shape that catches it
// end to end, and the panel had never been in it.
func TestViewFitsTerminalBounds(t *testing.T) {
	sizes := [][2]int{{200, 50}, {210, 48}, {120, 30}, {160, 40}, {235, 55}, {80, 24}}

	// Each arms one overlay on an otherwise identical home.
	overlays := map[string]func(t *testing.T, h *home){
		"none": func(*testing.T, *home) {},
		"create form": func(t *testing.T, h *home) {
			h.newSessionPath = t.TempDir()
			h.state = statePrompt
			h.textInputOverlay, _ = h.newSessionFormOverlay()
		},
		// A rotation pool wide enough to reproduce #478's row. The list must be long
		// enough to consume the panel's row budget: that budget is what makes a
		// wrapped row visible here — it sizes the rows to the terminal, so one extra
		// line puts the box over. A three-account fixture wraps just as badly and this
		// test cannot tell, because a 15-line box still fits a 24-row terminal.
		//
		// Every account carries route rules, so there is no catch-all and the
		// "unmatched repos" hint renders — it is the one line rowWindow charges
		// unconditionally, and without it the budget has a spare row that absorbs the
		// first wrap.
		// The palette is the widest generated surface in the app: three columns of
		// registry-derived text, none of it authored to a width. At 80x24 its box is
		// 68 columns against prose written for the ? screen, so the row builder has
		// to be the thing that truncates — this is what proves it is.
		"command palette": func(_ *testing.T, h *home) {
			h.openCommandPalette()
		},
		"accounts": func(_ *testing.T, h *home) {
			for i := 0; i < 30; i++ {
				h.appConfig.ClaudeAccounts = append(h.appConfig.ClaudeAccounts, config.ClaudeAccount{
					Name:          fmt.Sprintf("acct%02d", i),
					ConfigDir:     fmt.Sprintf("~/.claude-work%02d", i),
					Pool:          "quantivly-rotation-pool",
					RemoteMatches: []string{fmt.Sprintf("github.com/org%02d", i)},
				})
			}
			h.state = stateAccounts
			h.accountsOverlay = overlay.NewAccountsOverlay(h.appConfig, h.appState)
		},
	}

	for name, arm := range overlays {
		for _, dim := range sizes {
			w, h := dim[0], dim[1]

			home := newCreateFormHome(t)
			arm(t, home)
			home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})

			lines := strings.Split(home.View(), "\n")

			if len(lines) > h {
				t.Errorf("overlay=%s size=%dx%d: View() emitted %d lines, exceeds height %d",
					name, w, h, len(lines), h)
			}
			for i, l := range lines {
				if pw := ansi.PrintableRuneWidth(l); pw != w {
					t.Errorf("overlay=%s size=%dx%d: line %d width=%d, expected %d",
						name, w, h, i, pw, w)
					break
				}
			}
		}
	}
}
