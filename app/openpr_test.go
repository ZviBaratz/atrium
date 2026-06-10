package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pressing w (open PR) on a direct (non-git) session has no PR to open; it must
// explain itself with a notice rather than silently doing nothing.
func TestOpenPR_DirectSessionExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   "direct",
		Path:    t.TempDir(),
		Program: "echo",
		Direct:  true,
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)

	pressKey(h, 'w')

	assert.Equal(t, stateDefault, h.state, "no overlay for a direct session")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "direct")
}

// Pressing w on a session that is still starting must defer with a notice rather
// than reaching for a worktree/PR that does not exist yet.
func TestOpenPR_LoadingSessionDefers(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "booting", "zvi/feat")
	inst.SetStatus(session.Loading)
	h.list.AddInstance(inst)

	pressKey(h, 'w')

	assert.Equal(t, stateDefault, h.state, "no overlay while loading")
	require.True(t, h.menu.HasNotice(), "the loading guard must explain itself")
	assert.Contains(t, h.menu.String(), "starting")
}

// Pressing w on a session with no PR must explain why with a notice.
func TestOpenPR_NoPRExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	// GetPRStatus() returns nil (never set); the zero PRStatus has HasPR=false.
	h.list.AddInstance(inst)

	pressKey(h, 'w')

	assert.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "PR")
}

// A blocked PR (draft / CI pending / conflicts) is still openable — viewing is
// permissive where merging is strict. The guard must NOT fire a notice. (The open
// action itself is not run here — it would shell out to gh.)
func TestOpenPR_BlockedPRIsStillOpenable(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", Mergeable: "CONFLICTING"})
	h.list.AddInstance(inst)

	pressKey(h, 'w')

	assert.Equal(t, stateDefault, h.state, "opening a PR never opens an overlay")
	assert.False(t, h.menu.HasNotice(), "a present PR must not trip a guard notice")
}

// A valid open PR triggers the open action with no guard notice. The returned
// command is discarded by pressKey, so no real gh runs.
func TestOpenPR_OpenPRTriggersAction(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 42, State: "OPEN", CI: git.CIPassing, Mergeable: "MERGEABLE"})
	h.list.AddInstance(inst)

	pressKey(h, 'w')

	assert.Equal(t, stateDefault, h.state, "opening a PR never opens an overlay")
	assert.False(t, h.menu.HasNotice(), "the happy path fires no guard notice")
}
