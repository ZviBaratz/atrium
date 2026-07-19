package app

// The auto-accept safety banner (#378, item 5): a persistent, attention-colored
// top row shown while app-wide auto-accept is armed. Its layout row is reserved in
// lockstep by updateHandleWindowSizeEvent / paneContentHeight (height) and offset by
// handleMouse (the divider Y-bound), so the frame stays exactly as tall as the
// terminal.

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// autoYesArmed reports whether app-wide auto-accept is on. When armed, every
// session's daemon taps through prompts unattended — an intentional but
// consequential mode the user should never lose track of, hence the banner.
// m.autoYes is the single global flag (per-session AutoYes is only ever derived
// from it), so there is no per-session arming path to account for here.
func (m *home) autoYesArmed() bool { return m.autoYes }

// topBannerHeight is the number of rows the safety banner claims at the top of the
// frame: 1 while armed, else 0. The two layout-budget sites subtract it alongside
// the hint-bar/error rows, and the divider Y-bound is offset by it, so the panes
// stay exactly as tall as the frame minus its reserved rows.
func (m *home) topBannerHeight() int {
	if m.autoYesArmed() {
		return 1
	}
	return 0
}

// autoYesBanner renders the full-width auto-accept safety bar: an attention-colored
// (amber — never the error red, which the palette reserves for real failures) row
// naming the armed state and its consequence. It is exactly one line and exactly
// width printable cells (truncated, then padded), so it can't break the frame's
// height/width invariants on a narrow terminal. The leading marker is the
// glyph-set-aware Warn glyph (⚠ / !), degrading with the fidelity rung rather than
// tofuing.
func (m *home) autoYesBanner(width int) string {
	if width <= 0 {
		return ""
	}
	t := theme.Current()
	label := "AUTO-ACCEPT ARMED — every prompt is accepted unattended"
	if t.Glyphs.Warn != "" {
		label = t.Glyphs.Warn + " " + label
	}
	label = " " + label // a hair of left inset so the text isn't flush to the edge
	label = runewidth.Truncate(label, width, "")
	if pad := width - runewidth.StringWidth(label); pad > 0 {
		label += strings.Repeat(" ", pad)
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Palette.Bg).
		Background(t.Palette.Attention).
		Render(label)
}
