package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func postMergeHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st
	inst, err := session.NewInstance(session.InstanceOptions{Title: "done", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	return h, inst
}

// A merged PR is followed by a cleanup offer naming the concrete session (#384).
func TestPostMerge_OffersCleanup(t *testing.T) {
	h, inst := postMergeHome(t)

	model, _ := h.Update(prMergedMsg{number: 42, instance: inst})
	h = model.(*home)

	require.Equal(t, stateConfirm, h.state, "a merge is followed by a cleanup confirmation")
	require.NotNil(t, h.confirmationOverlay)
	msg := ansi.Strip(h.confirmationOverlay.Render())
	require.Contains(t, msg, "PR #42 merged. Clean up session 'done'?",
		"the offer announces the merge and names the session")
}

// The offer carries the kill dialog's consequence-first data warning when the
// session still has at-risk local work.
func TestPostMerge_OfferWarnsOnUncommittedWork(t *testing.T) {
	h, inst := postMergeHome(t)
	inst.SetDiffStats(&git.DiffStats{Dirty: true})

	model, _ := h.Update(prMergedMsg{number: 9, instance: inst})
	h = model.(*home)
	require.Contains(t, ansi.Strip(h.confirmationOverlay.Render()), "uncommitted changes",
		"a dirty session's cleanup offer states what deleting discards")
}

// With no session to clean up, the handler falls back to the plain merged notice
// and does not force a confirmation.
func TestPostMerge_NilInstanceFallsBackToNotice(t *testing.T) {
	h, _ := postMergeHome(t)
	require.False(t, h.offerCleanupAfterMerge(nil, 7), "no session → no offer")

	model, _ := h.Update(prMergedMsg{number: 7, instance: nil})
	h = model.(*home)
	require.NotEqual(t, stateConfirm, h.state, "a merge with no session must not open a confirmation")
	require.True(t, h.menu.HasNotice(), "the merge is still acknowledged via a notice")
}
