package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pressing a with nothing selected is a plain no-op: there is no session the
// approval could possibly target, so no notice either.
func TestApprove_NoSelectionSilent(t *testing.T) {
	h := newCreateFormHome(t)

	pressKey(h, 'a')

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.menu.HasNotice(), "no selection means nothing to explain")
}

// Pressing a on a session that isn't blocked on a prompt must explain the
// no-op instead of poking the agent: the gate is what makes a stray keypress
// safe.
func TestApprove_NotWaitingExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "ready", "feat/approve")
	inst.SetStatus(session.Ready)
	h.list.AddInstance(inst)
	require.NotNil(t, h.list.GetSelectedInstance())

	pressKey(h, 'a')

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "waiting")
	assert.Equal(t, session.Ready, inst.GetStatus(), "a guarded press must not touch the status")
}

// A paused session falls under the same not-waiting gate (its status is
// Paused, never NeedsInput).
func TestApprove_PausedExplains(t *testing.T) {
	h := newPausedHome(t)

	pressKey(h, 'a')

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "waiting")
}

// When the tap itself fails (here: the instance was never started), the error
// surfaces and the status stays NeedsInput — the optimistic Running flip must
// only happen after a successful tap.
func TestApprove_NeedsInputButUnstartedSurfacesError(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "stuck", "feat/approve")
	inst.SetStatus(session.NeedsInput)
	h.list.AddInstance(inst)
	require.NotNil(t, h.list.GetSelectedInstance())

	pressKey(h, 'a')

	require.True(t, h.menu.HasNotice(), "a failed approve must not be silent")
	assert.Equal(t, session.NeedsInput, inst.GetStatus(),
		"the optimistic flip must not run when the tap failed")
}

// A Ready session routes to the accept-suggestion branch, but an unstarted
// instance has no pane: the Started() pre-gate must fall through to the
// explanatory notice (now covering both actions) without touching the status.
// The success path — started claude, ghost text visible, Right+Enter sent —
// is untestable from this package (started/tmuxSession are unexported; PR
// #121 hit the same wall) and is pinned in session/instance_test.go instead.
func TestApprove_ReadyUnstartedExplainsAcceptToo(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "ready-suggest", "feat/accept")
	inst.SetStatus(session.Ready)
	h.list.AddInstance(inst)
	require.NotNil(t, h.list.GetSelectedInstance())

	pressKey(h, 'a')

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "nothing to approve or accept")
	assert.Equal(t, session.Ready, inst.GetStatus(), "a guarded press must not touch the status")
}
