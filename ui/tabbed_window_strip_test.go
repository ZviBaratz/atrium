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
// SetSize(20, 12) the three tabs get 6/6/8 cells (4/4/6 inner), so "Preview"
// and "Terminal" overflow and the guard is load-bearing on both.
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
	require.Contains(t, lines[1], "Pre…", "an overflowing label is truncated, not wrapped")
	// The strip's closing border sits on row 2; a wrapped label would push its
	// remainder there instead.
	require.NotRegexp(t, "[A-Za-z]", lines[2], "row 2 is the strip's bottom border, not wrapped label text")
}
