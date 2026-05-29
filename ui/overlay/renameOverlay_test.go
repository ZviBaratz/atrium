package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestRenameOverlay_PrefillAndSubmit(t *testing.T) {
	o := NewRenameOverlay("current")
	assert.Equal(t, "current", o.Value(), "overlay should be pre-filled with the current label")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-x")})
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, shouldClose)
	assert.True(t, o.IsSubmitted())
	assert.False(t, o.IsCanceled())
	assert.Equal(t, "current-x", o.Value())
}

func TestRenameOverlay_Cancel(t *testing.T) {
	o := NewRenameOverlay("current")
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.True(t, shouldClose)
	assert.True(t, o.IsCanceled())
	assert.False(t, o.IsSubmitted())
}

func TestRenameOverlay_TrimsValue(t *testing.T) {
	o := NewRenameOverlay("")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("  spaced  ")})
	assert.Equal(t, "spaced", o.Value())
}

func TestRenameOverlay_EnforcesCharLimit(t *testing.T) {
	o := NewRenameOverlay("")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", 40))})
	assert.LessOrEqual(t, len(o.Value()), 32, "the title input is capped at 32 characters")
}

// The dialog defaults to the safe, non-destructive label-only rename (the historical R
// behavior); a deep rename (branch + worktree + tmux) is a deliberate opt-in one Tab away.
func TestRenameOverlay_DefaultsToLabelOnly(t *testing.T) {
	o := NewRenameOverlay("formalize-packaing")
	assert.False(t, o.IsDeep())
}

// Tab toggles between label-only and deep without submitting or canceling the dialog.
func TestRenameOverlay_TabTogglesMode(t *testing.T) {
	o := NewRenameOverlay("x")

	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.False(t, shouldClose, "Tab must not close the overlay")
	assert.True(t, o.IsDeep(), "first Tab switches to deep")
	assert.False(t, o.IsSubmitted())
	assert.False(t, o.IsCanceled())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.False(t, o.IsDeep(), "second Tab switches back to label only")
}

// The default (label only) is listed first so the selected option leads, rather than sitting
// on the second line below the unselected deep option.
func TestRenameOverlay_RendersDefaultModeFirst(t *testing.T) {
	out := NewRenameOverlay("x").Render()
	labelIdx := strings.Index(out, "label only")
	deepIdx := strings.Index(out, "deep")
	assert.GreaterOrEqual(t, labelIdx, 0, "label-only option should render")
	assert.GreaterOrEqual(t, deepIdx, 0, "deep option should render")
	assert.Less(t, labelIdx, deepIdx, "the default (label only) must be listed before deep")
}

// Toggling mode does not leak into the entered value, and Enter still submits.
func TestRenameOverlay_TabDoesNotAffectValue(t *testing.T) {
	o := NewRenameOverlay("alpha")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	shouldClose := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, shouldClose)
	assert.True(t, o.IsSubmitted())
	assert.Equal(t, "alpha", o.Value())
}
