package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Which git/gh actions survive a pause, and which must refuse.
//
// Pause frees the worktree and keeps the branch, so the split is not about the
// session being "inactive" — it is about where the subprocess runs. PushChanges
// and CreatePR run from the worktree path; MergePR and OpenPRURL run gh from the
// repo root, which is always there. So push and create must refuse before the
// confirmation, and merge and open-PR must not.
//
// Both halves matter. Without the refusals the user confirms a dialog and gets a
// raw "cannot change to directory" from git a second later; without the
// permissions, the actions that legitimately work on a parked session would be
// blocked by a plausible-sounding rule. The hint bar cannot stand in for either —
// pausedHintKeys drops all four, and a hidden hint is not a guard: every one of
// them is one enter away in the command palette (#374).
func TestPausedSessionRefusesWorktreeActionsOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action keys.KeyName
		refuse bool
	}{
		{"push runs git from the freed worktree", keys.KeySubmit, true},
		{"create PR runs gh from the freed worktree", keys.KeyCreate, true},
		{"merge runs gh from the repo root", keys.KeyMerge, false},
		{"open PR runs gh from the repo root", keys.KeyOpenPR, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := newGateHome(t, gateScenario{"paused", func(t *testing.T, h *home) *session.Instance {
				inst := newGateInstance(t, h, "parked")
				inst.SetStatus(session.Paused)
				// A pushed, mergeable PR: the only state in which merge and open-PR
				// have anything to do, so "they were not blocked" is a real result
				// rather than their own snapshot guard refusing first.
				inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", Pushed: true})
				return inst
			}})
			require.True(t, inst.Paused())

			_, cmd := h.dispatchAction(tc.action)

			if tc.refuse {
				assert.Equal(t, stateDefault, h.state,
					"a paused session's worktree is gone; this must refuse instead of confirming")
				require.True(t, h.menu.HasNotice(), "a refusal the user cannot see is indistinguishable from a no-op")
				assert.Contains(t, h.menu.String(), "resume",
					"the notice must name the way out, not just the refusal")
			} else {
				assert.False(t, h.menu.HasNotice(),
					"gh runs from the repo root, which a pause does not touch — this must not refuse")
				// Proceeding looks like one of two things: the mutating action arms its
				// confirmation, the read-only one hands back the command that runs gh.
				assert.Truef(t, h.state == stateConfirm || cmd != nil,
					"nothing happened: state=%v cmd=%v", h.state, cmd != nil)
			}
		})
	}
}

// And the palette says so before enter is pressed: push and create dim, merge and
// open-PR stay live. This is the projection of the split above — a gate that
// disagreed with it would make the palette lie in one direction or the other.
func TestPalettePausedGatesMatchTheWorktreeSplit(t *testing.T) {
	h, inst := newGateHome(t, gateScenario{"paused", func(t *testing.T, h *home) *session.Instance {
		inst := newGateInstance(t, h, "parked")
		inst.SetStatus(session.Paused)
		inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", Pushed: true})
		return inst
	}})
	require.True(t, inst.Paused())

	assert.Equal(t, pausedWorktreeReason, h.paletteInertReason(keys.KeySubmit))
	assert.Equal(t, pausedWorktreeReason, h.paletteInertReason(keys.KeyCreate))
	assert.Empty(t, h.paletteInertReason(keys.KeyMerge))
	assert.Empty(t, h.paletteInertReason(keys.KeyOpenPR))
}
