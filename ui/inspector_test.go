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

// The inspector tab renders the pane's empty state through the tabbed window —
// the wiring from the tab entry to the pane, not just the pane in isolation.
func TestInspectorTabShowsEmptyState(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(60, 20)
	w.SetActiveTab(InspectorTab)
	require.Contains(t, xansi.Strip(w.String()), inspectorEmptyState,
		"the inspector tab must render the pane's empty state")
}

// Copying on the inspector tab reports nothing to copy. The preview pane is
// seeded with live text so the arm is falsifiable: without its own case the
// switch's default arm would copy that capture instead.
func TestInspectorTabCopyableContentIsEmpty(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.preview.previewState = previewState{text: "live capture"}
	w.SetActiveTab(InspectorTab)

	text, what, ok := w.CopyableContent(nil)
	require.False(t, ok, "the inspector skeleton has nothing to copy")
	require.Empty(t, text)
	require.Empty(t, what)
}
