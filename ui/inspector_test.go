package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The skeleton's empty state is a designed placeholder, not a bare blank: the
// copy is present, dim, centered, and the pane fills its box exactly so the
// tabbed window's height budget holds.
func TestInspectorPaneEmptyState(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	p := NewInspectorPane()
	p.SetSize(54, 20)

	frame := p.String()
	require.Contains(t, xansi.Strip(frame), inspectorEmptyState, "the empty state carries its copy")
	require.NotEqual(t, frame, xansi.Strip(frame), "the copy is styled, not default text")

	lines := strings.Split(frame, "\n")
	require.Len(t, lines, 20, "the pane fills its height exactly")
	for i, line := range lines {
		require.Equalf(t, 54, lipgloss.Width(line), "line %d must fill the pane width", i)
	}
}

// Before the first SetSize the pane renders nothing, matching
// TabbedWindow.String's own zero-size guard.
func TestInspectorPaneZeroSizeRendersNothing(t *testing.T) {
	require.Empty(t, NewInspectorPane().String())
}
