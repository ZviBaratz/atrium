package app

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addStubInstances fills the home's list with n never-started instances so the
// session-cap guard sees an existing population.
func addStubInstances(t *testing.T, h *home, n int) {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   fmt.Sprintf("s%d", i),
			Path:    dir,
			Program: "echo",
		})
		require.NoError(t, err)
		h.list.AddInstance(inst)
	}
}

// With no max_sessions configured the default cap is host-derived and *soft*: it
// never blocks the form from opening (the confirmation happens at submit), so the
// create form still opens even well past the derived cap. (test homes leave hostCap
// at 0, so the soft cap here is effectively unlimited — the point is only that a
// soft cap does not block at form-open.)
func TestOpenCreateForm_SoftCapNeverBlocksFormOpen(t *testing.T) {
	h := newCreateFormHome(t)
	addStubInstances(t, h, 12)

	h.handleKeyPress(textMsg("n"))

	assert.Equal(t, statePrompt, h.state, "a soft cap must not block the form from opening")
	require.NotNil(t, h.textInputOverlay)
	assert.True(t, h.textInputOverlay.IsCreateForm())
}

// A configured cap that has not been reached must not block creation: with
// max_sessions = 2 and one existing session, the form still opens. Together
// with the at-cap test below this pins the guard's >= from both sides.
func TestOpenCreateForm_AllowedBelowConfiguredCap(t *testing.T) {
	h := newCreateFormHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addStubInstances(t, h, 1)

	h.handleKeyPress(textMsg("n"))

	assert.Equal(t, statePrompt, h.state, "below-cap creation must not be blocked")
	require.NotNil(t, h.textInputOverlay)
	assert.True(t, h.textInputOverlay.IsCreateForm())
}

// An explicit max_sessions in config.json is still enforced as an opt-in cap.
func TestOpenCreateForm_BlockedAtConfiguredCap(t *testing.T) {
	h := newCreateFormHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addStubInstances(t, h, 2)

	h.handleKeyPress(textMsg("n"))

	assert.Equal(t, stateDefault, h.state, "configured cap must block creation")
	assert.Nil(t, h.textInputOverlay)
}
