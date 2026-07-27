package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pressing m (merge PR) on a direct (non-git) session has no PR to merge; it must
// explain itself with an error rather than silently doing nothing or opening a
// confirmation it could never satisfy.
func TestMerge_DirectSessionExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   "direct",
		Path:    t.TempDir(),
		Program: "echo",
		Direct:  true,
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)

	pressKey(h, 'm')

	assert.Equal(t, stateDefault, h.state, "no confirmation overlay for a direct session")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "direct")
}

// Pressing m on a session that is still starting must defer with a notice rather
// than reaching for a worktree/PR that does not exist yet.
func TestMerge_LoadingSessionDefers(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "booting", "zvi/feat")
	inst.SetStatus(session.Loading)
	h.list.AddInstance(inst)

	pressKey(h, 'm')

	assert.Equal(t, stateDefault, h.state, "no confirmation overlay while loading")
	require.True(t, h.menu.HasNotice(), "the loading guard must explain itself")
	assert.Contains(t, h.menu.String(), "starting")
}

// Pressing m on a session with no PR (or a blocked one) must explain why with a
// notice rather than opening a confirmation that gh would only reject.
func TestMerge_BlockedPRExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN", Mergeable: "CONFLICTING"})
	h.list.AddInstance(inst)

	pressKey(h, 'm')

	assert.Equal(t, stateDefault, h.state, "a blocked PR must not open the confirmation")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "conflict")
}

// A session whose PR is open and unblocked opens the squash-merge confirmation,
// naming the PR. (The action itself is not run here — it would shell out to gh.)
func TestMerge_MergeablePROpensConfirmation(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 42, State: "OPEN", CI: git.CIPassing, Mergeable: "MERGEABLE"})
	h.list.AddInstance(inst)

	pressKey(h, 'm')

	assert.Equal(t, stateConfirm, h.state, "an unblocked PR must open the confirmation")
	require.NotNil(t, h.pendingConfirmAction, "the confirmed merge action must be staged")
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, rendered, "Merge PR #42 from 'feat' as a squash merge?")
	assert.Contains(t, rendered, "Press y to merge PR #42, n or esc to cancel")
	assert.NotContains(t, rendered, "CI is still running", "passing CI has no caveat")
}

// Merging while CI is still running is allowed (gh decides), but the dialog must say
// so — CI state is not on the screen the user is confirming from. The caveat rides in
// the kill dialog's parenthetical rather than trailing the question as a second
// sentence (#399 item 4).
func TestMerge_PendingCIWarnsInParenthetical(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", CI: git.CIPending, Mergeable: "MERGEABLE"})
	h.list.AddInstance(inst)

	pressKey(h, 'm')

	require.Equal(t, stateConfirm, h.state)
	assert.Contains(t, flattenOverlay(h.confirmationOverlay.Render()),
		"Merge PR #7 from 'feat' as a squash merge? (CI is still running)")
}
