package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestQueueOverlay_RendersHeadFirstWithInFlightMark(t *testing.T) {
	o := NewQueueOverlay("auth")
	o.SetQueue([]string{"fix login", "update tests"}, true)
	out := o.Render()
	require.Contains(t, out, `Queue for "auth"`)
	require.Contains(t, out, "fix login")
	require.Contains(t, out, "update tests")
	require.Contains(t, out, "1.")
	require.Contains(t, out, "2.")
	require.Contains(t, out, queueInFlightMark, "an in-flight head is marked")
}

func TestQueueOverlay_EmptyState(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue(nil, false)
	require.Contains(t, o.Render(), "no pending prompts")
	require.Equal(t, "", o.SelectedText())
}

func TestQueueOverlay_CursorMovesAndClamps(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b", "c"}, false)
	require.Equal(t, 0, o.SelectedIndex())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp}) // clamps at 0
	require.Equal(t, 0, o.SelectedIndex())
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j")) // clamps at last
	require.Equal(t, 2, o.SelectedIndex())
	require.Equal(t, "c", o.SelectedText())
	o.HandleKeyPress(runeKey("k"))
	require.Equal(t, 1, o.SelectedIndex())
}

func TestQueueOverlay_SetQueueClampsCursor(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b", "c"}, false)
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j")) // cursor at 2
	o.SetQueue([]string{"a"}, false)
	require.Equal(t, 0, o.SelectedIndex(), "a shorter queue clamps the cursor")
}

func TestQueueOverlay_RemoveArmsOnceWithSelection(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b"}, false)
	o.HandleKeyPress(runeKey("j"))
	shouldClose := o.HandleKeyPress(runeKey("d"))
	require.False(t, shouldClose, "d does not close the overlay")
	require.Equal(t, 1, o.SelectedIndex())
	require.Equal(t, "b", o.SelectedText())
	require.True(t, o.RemoveRequested(), "d arms a remove")
	require.False(t, o.RemoveRequested(), "RemoveRequested is read-once")
}

func TestQueueOverlay_EscCloses(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a"}, false)
	require.True(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}), "esc closes the overlay")
}

func TestQueueOverlay_HeadInFlightReflectsQueue(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a"}, true)
	require.True(t, o.HeadInFlight())
	o.SetQueue([]string{"a"}, false)
	require.False(t, o.HeadInFlight())
}

func TestQueueOverlay_MessageShownAndClearedOnSetQueue(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a"}, true)
	o.SetMessage("can't cancel — prompt is being delivered")
	require.Contains(t, o.Render(), "being delivered")
	o.SetQueue([]string{"a"}, true) // a refresh clears the transient message
	require.False(t, strings.Contains(o.Render(), "being delivered"))
}
