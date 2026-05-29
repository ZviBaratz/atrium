package ui

import (
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
