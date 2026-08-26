package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTabbedWindow_ToggleReverse(t *testing.T) {
	w := NewTabbedWindow(nil, nil, nil)
	require.Equal(t, PreviewTab, w.GetActiveTab(), "starts on Preview")

	w.ToggleReverse()
	require.Equal(t, TerminalTab, w.GetActiveTab(), "reverse from Preview wraps to Terminal")

	w.ToggleReverse()
	require.Equal(t, DiffTab, w.GetActiveTab(), "reverse from Terminal lands on Diff")

	w.ToggleReverse()
	require.Equal(t, PreviewTab, w.GetActiveTab(), "reverse from Diff lands on Preview")
}

func TestTabbedWindow_ToggleAndReverseAreInverse(t *testing.T) {
	w := NewTabbedWindow(nil, nil, nil)
	w.Toggle()        // Preview -> Diff
	w.ToggleReverse() // Diff -> Preview
	require.Equal(t, PreviewTab, w.GetActiveTab(), "Toggle then ToggleReverse returns to start")
}

// The "tabs own the keyboard" predicates are tab-scoped: a preview snapshot
// left scrolled in the background must claim neither the nav keys nor the
// chrome accent while another tab is showing — otherwise the border says
// "captured" one row from a bar saying the list has focus.
func TestTabbedWindow_PaneCaptureIsTabScoped(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), nil, nil)
	w.preview.SetSize(80, 10) // size the pane directly: TabbedWindow.SetSize needs all three panes

	require.False(t, w.ActivePaneInScrollMode(), "live preview claims nothing")
	require.False(t, w.activePaneCaptured())
	require.False(t, w.ActivePaneScrollAtBottom())

	w.SetPreviewScrollContent(nil, "one\ntwo\nthree")
	require.True(t, w.ActivePaneInScrollMode(), "a scrolled preview on the preview tab claims the keys")
	require.True(t, w.activePaneCaptured())
	require.True(t, w.ActivePaneScrollAtBottom(), "scroll-mode entry lands at the bottom")
	require.True(t, w.ActivePaneScrollAtTop(),
		"three lines in a ten-line viewport is a zero-travel snapshot: top and bottom at once")

	w.SetActiveTab(DiffTab)
	require.False(t, w.ActivePaneInScrollMode(), "a background snapshot must not claim the diff tab's keys")
	require.False(t, w.activePaneCaptured(), "nor keep the chrome accent lit")
	require.False(t, w.ActivePaneScrollAtBottom())
	require.False(t, w.ActivePaneScrollAtTop())
}

// The TerminalTab arms of the scroll-position predicates, driven directly the
// way the memo test pokes the pane: the router's held-j guard depends on them,
// and a copy/paste that answered with the preview's position would pass every
// preview-driven test while silently un-holding the terminal snapshot.
func TestTabbedWindow_TerminalScrollPositionArms(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), nil, NewTerminalPane(context.Background()))
	w.terminal.SetSize(40, 5)
	w.SetActiveTab(TerminalTab)

	require.False(t, w.ActivePaneScrollAtBottom(), "a live terminal reports no scroll position")
	require.False(t, w.ActivePaneScrollAtTop())

	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	w.terminal.viewport.SetContent(b.String())
	w.terminal.viewport.GotoBottom()
	w.terminal.isScrolling = true

	require.True(t, w.ActivePaneScrollAtBottom(), "a terminal snapshot enters at its bottom")
	require.False(t, w.ActivePaneScrollAtTop(), "thirty lines in a five-line viewport has travel")

	w.terminal.viewport.GotoTop()
	require.False(t, w.ActivePaneScrollAtBottom())
	require.True(t, w.ActivePaneScrollAtTop())

	w.SetActiveTab(PreviewTab)
	require.False(t, w.ActivePaneScrollAtTop(),
		"a background terminal snapshot must not answer for the preview tab")
}

// The preview's hint overlay is the other key-capturing mode: it lights the
// chrome accent (activePaneCaptured) without entering scroll mode, so the
// focus model's scroll predicate stays false.
func TestTabbedWindow_HintModeLightsCaptureOnly(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), nil, nil)
	w.preview.SetSize(80, 10)

	w.preview.SetHintOverlay(nil, "hints")
	require.True(t, w.activePaneCaptured(), "hint mode captures the keyboard on the preview tab")
	require.False(t, w.ActivePaneInScrollMode(), "hint mode is not scroll mode")

	w.SetActiveTab(DiffTab)
	require.False(t, w.activePaneCaptured(), "a background hint overlay must not light another tab")
}
