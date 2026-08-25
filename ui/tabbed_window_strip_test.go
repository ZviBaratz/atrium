package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// A label wider than its tab's inner cells is truncated with an ellipsis rather
// than wrapped: lipgloss wraps overflow onto a second strip row, and the
// MaxHeight clamp in compose would then eat the window's bottom border. At
// SetSize(20, 12) every tab is narrower than its label, so the guard is
// load-bearing on all of them.
func TestTabStripTruncatesOverlongLabels(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(20, 12)

	frame := xansi.Strip(w.String())
	lines := strings.Split(frame, "\n")
	require.Len(t, lines, 12, "the pane must hold its height budget")
	for i, line := range lines {
		require.Equalf(t, 20, lipgloss.Width(line), "line %d must fill the pane width exactly", i)
	}
	require.Contains(t, lines[1], "…", "an overflowing label is truncated, not wrapped")
	// The strip's closing border sits on row 2; a wrapped label would push its
	// remainder there instead.
	require.NotRegexp(t, "[A-Za-z]", lines[2], "row 2 is the strip's bottom border, not wrapped label text")
}

// The narrowest strip a preset can produce: the monitor preset pins the list at
// config.MaxListRatio (0.60), leaving 32 columns for this pane at the 80-column
// floor. Every label overflows its 6 inner cells there; the strip must hold its
// three rows and exact width regardless.
func TestTabStripHoldsTheMonitorPresetFloor(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(32, 20)

	frame := xansi.Strip(w.String())
	lines := strings.Split(frame, "\n")
	require.Len(t, lines, 20, "the pane must hold its height budget at the floor")
	for i, line := range lines {
		require.Equalf(t, 32, lipgloss.Width(line), "line %d must fill the pane width exactly", i)
	}
	require.NotRegexp(t, "[A-Za-z]", lines[2], "row 2 is the strip's bottom border, not wrapped label text")
}

// The remainder of the strip's width division lands on the last tab, keeping
// the strip's right edge flush with the window frame at widths the tab count
// does not divide. The bounds sweep cannot see this: JoinVertical pads a short
// strip row with spaces to the frame width, so the width invariant holds even
// with the remainder dropped — the edge itself is what has to be asserted.
// (Both golden sizes currently divide evenly by the tab count, so no frame
// golden covers the remainder path either.)
func TestTabStripRemainderKeepsTheRightEdgeFlush(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(21, 12) // strip width 21 = 4×5 + 1: one remainder cell

	lines := strings.Split(xansi.Strip(w.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "the frame must at least hold the strip")
	for i, line := range lines[:3] {
		require.Falsef(t, strings.HasSuffix(line, " "),
			"strip row %d ends in padding, not the frame edge — the width remainder was dropped", i)
	}
}

// At the 80-column default split this pane gets 56 columns — 14 per tab, 12
// inner — and every label must appear in full. This is the assertion behind
// any claim that the current tab set fits the default floor: a label longer
// than 12 cells fails here and forces a naming decision.
func TestTabStripFitsEveryLabelAtTheDefaultFloor(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(56, 20)

	strip := xansi.Strip(w.String())
	for _, tab := range w.tabs {
		require.Contains(t, strip, tab.Name, "label %q must render untruncated at the default 80-column split", tab.Name)
	}
	require.Len(t, w.tabs, 4, "four tabs share the strip; a new one re-opens the width math above")
}
