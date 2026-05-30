package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctrl+q is a symmetric attach/detach toggle: in a session it detaches (handled by
// the tmux layer), and on the list it attaches the selected session — i.e. it routes
// to the same action as enter. With no instances, that action's guard makes it a
// safe no-op rather than panicking, exactly like enter.
func TestCtrlQ_OnListWithNoInstances_IsNoOp(t *testing.T) {
	h := newTestHomeWithInstances(t) // zero instances
	h.state = stateDefault

	model, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlQ})

	require.NotNil(t, model)
	assert.Nil(t, cmd, "ctrl+q with no instances should be a no-op, like enter")
	assert.Equal(t, stateDefault, h.state, "ctrl+q must not change state on an empty list")
}

// Scope guarantee: ctrl+q's attach behavior is main-screen-only. In the new-session
// naming overlay it must NOT submit the name (that is enter's job, matched by key
// type), so the in-progress instance stays unfinalized and the overlay stays open.
func TestCtrlQ_InStateNew_DoesNotSubmitName(t *testing.T) {
	h := newCreateFormHome(t)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   "feature",
		Path:    t.TempDir(),
		Program: "echo",
	})
	require.NoError(t, err)

	h.state = stateNew
	h.newInstance = inst
	h.keySent = true // skip the menu-highlight pre-pass, process the key directly

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlQ})

	assert.Equal(t, stateNew, h.state, "ctrl+q must not leave the naming overlay")
	require.NotNil(t, h.newInstance, "ctrl+q must not finalize the new instance")
	assert.Equal(t, "feature", h.newInstance.Title, "the title must be untouched")
}
