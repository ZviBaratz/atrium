package ui

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The skeleton's empty state is a designed placeholder, not a bare blank: the
// copy is present and styled, and the frame carries the stored size — which is
// what pins String feeding SetSize's dimensions into centerInBox, unswapped.
// (Exact box geometry is TestCenterInBox's job, and the window's height budget
// is protected by compose's Place either way, so neither needs re-proving
// line by line here.)
func TestInspectorPaneEmptyState(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	p := NewInspectorPane()
	p.SetSize(54, 20)

	frame := p.String()
	require.Contains(t, xansi.Strip(frame), inspectorEmptyState, "the empty state carries its copy")
	require.NotEqual(t, frame, xansi.Strip(frame), "the copy is styled, not default text")
	require.Equal(t, 54, lipgloss.Width(frame), "the frame must carry the stored width")
	require.Equal(t, 20, lipgloss.Height(frame), "the frame must carry the stored height")
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

// Copying on the inspector tab reports nothing to copy. The inspector rides
// CopyableContent's fail-closed default arm; the preview pane is seeded with
// live text so that arm is falsifiable — a default that copied the preview
// capture (the switch's original shape) would return the seeded text here.
func TestInspectorTabCopyableContentIsEmpty(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.preview.previewState = previewState{text: "live capture"}
	w.SetActiveTab(InspectorTab)

	text, what, ok := w.CopyableContent(nil)
	require.False(t, ok, "the inspector skeleton has nothing to copy")
	require.Empty(t, text)
	require.Empty(t, what)
}
