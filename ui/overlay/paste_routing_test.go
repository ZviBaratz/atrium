package overlay

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// Each overlay that accepts typing needs its own paste entry point, because the
// alternative — feeding a paste to HandleKeyPress — reads the clipboard as a
// command: these switches are keyed on msg.String(), and v2's String() returns the
// pasted text verbatim. "esc" and "enter" are ordinary words, so the risk is a
// user pasting English, not a contrived input.

// TestRenameOverlayPasteOfAControlWordIsText covers both fields: the name is what
// a user pastes into, and the note is reachable with one tab.
func TestRenameOverlayPasteOfAControlWordIsText(t *testing.T) {
	for _, word := range []string{"esc", "enter", "ctrl+c", "tab"} {
		t.Run(word, func(t *testing.T) {
			r := NewRenameOverlay("", "", false)

			r.HandlePaste(tea.PasteMsg{Content: word})

			require.Equal(t, word, r.Value(), "a pasted %q must be typed into the name", word)
			require.False(t, r.IsSubmitted(), "a paste must never submit the rename")
			require.False(t, r.IsCanceled(), "a paste must never cancel the rename")
		})
	}
}

// TestRenameOverlayPasteGoesToTheFocusedField pins that paste follows focus, the
// way the key path's default branch does.
func TestRenameOverlayPasteGoesToTheFocusedField(t *testing.T) {
	r := NewRenameOverlay("", "", true) // opens focused on the note

	r.HandlePaste(tea.PasteMsg{Content: "a note"})

	require.Equal(t, "a note", r.NoteValue())
	require.Empty(t, r.Value(), "the name must not receive a paste aimed at the note")
}

// TestCommandPalettePasteNarrowsRatherThanRuns is the palette's version of the
// same hazard: "enter" would have run the highlighted action.
func TestCommandPalettePasteNarrowsRatherThanRuns(t *testing.T) {
	p := NewCommandPaletteOverlay([]PaletteAction{{Label: "enter the void"}, {Label: "quit"}})

	p.HandlePaste("enter")

	require.Equal(t, "enter", p.filter, "a pasted \"enter\" must narrow the palette")
	_, _, chose := p.Chosen()
	require.False(t, chose, "a paste must never choose an action")
}

// TestCreateFormPasteOfAControlWordDoesNotCancel is the data-loss case at the
// overlay level: HandleKeyPress maps "esc" to Canceled, which discards the draft.
func TestCreateFormPasteOfAControlWordDoesNotCancel(t *testing.T) {
	o := NewTextInputOverlay("title", "")
	o.focusStop(stopTextarea)

	o.HandlePaste(tea.PasteMsg{Content: "esc"})

	require.False(t, o.Canceled, "a pasted \"esc\" must not cancel the form")
	require.False(t, o.Submitted, "a pasted \"esc\" must not submit the form")
	require.Contains(t, o.GetValue(), "esc", "the pasted text belongs in the prompt")
}

// TestCreateFormPasteIsInertOnASelectionField pins the stop where a paste means
// nothing: the Create button takes no text, and must not be nudged by one.
func TestCreateFormPasteIsInertOnASelectionField(t *testing.T) {
	o := NewTextInputOverlay("title", "")
	o.focusStop(stopEnter)

	o.HandlePaste(tea.PasteMsg{Content: "enter"})

	require.False(t, o.Submitted, "a pasted \"enter\" on the button must not submit")
	require.Empty(t, o.GetValue(), "a paste on the button must not leak into the prompt")
}
