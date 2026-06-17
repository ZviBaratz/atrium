package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenameOverlay_PrefillAndSubmit(t *testing.T) {
	o := NewRenameOverlay("current", "", false)
	assert.Equal(t, "current", o.Value(), "overlay should be pre-filled with the current label")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-x")})
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, shouldClose)
	assert.True(t, o.IsSubmitted())
	assert.False(t, o.IsCanceled())
	assert.Equal(t, "current-x", o.Value())
}

func TestRenameOverlay_Cancel(t *testing.T) {
	o := NewRenameOverlay("current", "", false)
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.True(t, shouldClose)
	assert.True(t, o.IsCanceled())
	assert.False(t, o.IsSubmitted())
}

func TestRenameOverlay_TrimsValue(t *testing.T) {
	o := NewRenameOverlay("", "", false)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("  spaced  ")})
	assert.Equal(t, "spaced", o.Value())
}

func TestRenameOverlay_EnforcesCharLimit(t *testing.T) {
	o := NewRenameOverlay("", "", false)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", 40))})
	assert.LessOrEqual(t, len(o.Value()), 32, "the title input is capped at 32 characters")
}

// The dialog defaults to the safe, non-destructive label-only rename (the historical R
// behavior); a deep rename (branch + worktree + tmux) is a deliberate opt-in one Tab away.
func TestRenameOverlay_DefaultsToLabelOnly(t *testing.T) {
	o := NewRenameOverlay("formalize-packaing", "", false)
	assert.False(t, o.IsDeep())
}

// ctrl+d toggles between label-only and deep without submitting or canceling the dialog.
func TestRenameOverlay_CtrlDTogglesDeepMode(t *testing.T) {
	o := NewRenameOverlay("x", "", false)

	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})
	assert.False(t, shouldClose, "ctrl+d must not close the overlay")
	assert.True(t, o.IsDeep(), "first ctrl+d switches to deep")
	assert.False(t, o.IsSubmitted())
	assert.False(t, o.IsCanceled())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})
	assert.False(t, o.IsDeep(), "second ctrl+d switches back to label only")
}

// The default (label only) is listed first so the selected option leads, rather than sitting
// on the second line below the unselected deep option.
func TestRenameOverlay_RendersDefaultModeFirst(t *testing.T) {
	out := NewRenameOverlay("x", "", false).Render()
	labelIdx := strings.Index(out, "label only")
	deepIdx := strings.Index(out, "deep")
	assert.GreaterOrEqual(t, labelIdx, 0, "label-only option should render")
	assert.GreaterOrEqual(t, deepIdx, 0, "deep option should render")
	assert.Less(t, labelIdx, deepIdx, "the default (label only) must be listed before deep")
}

// Toggling mode via ctrl+d does not leak into the entered value, and Enter still submits.
func TestRenameOverlay_CtrlDDoesNotAffectValue(t *testing.T) {
	o := NewRenameOverlay("alpha", "", false)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, shouldClose)
	assert.True(t, o.IsSubmitted())
	assert.Equal(t, "alpha", o.Value())
}

func TestRenameOverlay_NoteFieldEditsAndReturns(t *testing.T) {
	o := NewRenameOverlay("auth-refactor", "", true) // focus the note
	for _, r := range "waiting on CI" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.True(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}), "enter closes")
	require.True(t, o.IsSubmitted())
	require.Equal(t, "auth-refactor", o.Value(), "name field untouched")
	require.Equal(t, "waiting on CI", o.NoteValue())
}

func TestRenameOverlay_TabCyclesNameAndNote(t *testing.T) {
	o := NewRenameOverlay("name", "note", false) // focus the name
	for _, r := range "X" {                      // edits the name field
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // move to note
	for _, r := range "Y" {                        // edits the note field
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.Equal(t, "nameX", o.Value())
	require.Equal(t, "noteY", o.NoteValue())
}

func TestRenameOverlay_NoteCharLimit(t *testing.T) {
	o := NewRenameOverlay("n", "", true)
	for i := 0; i < 200; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	}
	require.LessOrEqual(t, len(o.NoteValue()), 80, "note capped at 80 chars")
}
