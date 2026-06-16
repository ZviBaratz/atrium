package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addPaused appends a paused session with the given title to the list.
func addPaused(t *testing.T, h *home, title string) *session.Instance {
	t.Helper()
	inst := newBranchInstance(t, title, "zvi/"+title)
	inst.SetStatus(session.Paused)
	h.list.AddInstance(inst)
	return inst
}

// ctrl+r with several paused sessions opens a count confirmation (it must not
// resume anything until the user confirms).
func TestResumeAll_OpensCountConfirmation(t *testing.T) {
	h := newCreateFormHome(t)
	addPaused(t, h, "alpha")
	addPaused(t, h, "bravo")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR})

	require.Equal(t, stateConfirm, h.state, "ctrl+r must open the confirmation overlay")
	require.NotNil(t, h.confirmationOverlay)
	assert.Contains(t, h.confirmationOverlay.Render(), "Resume 2 paused sessions?")
}

// The count is grammatical for a single session.
func TestResumeAll_SingularMessage(t *testing.T) {
	h := newCreateFormHome(t)
	addPaused(t, h, "alpha")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR})

	require.Equal(t, stateConfirm, h.state)
	assert.Contains(t, h.confirmationOverlay.Render(), "Resume 1 paused session?")
}

// ctrl+r with nothing paused must not open a confirmation; it explains itself
// with a notice instead.
func TestResumeAll_NoPausedExplains(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "running", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR})

	assert.Equal(t, stateDefault, h.state, "no confirmation when there is nothing to resume")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "no paused sessions")
}

// The failure summary names every session that didn't resume, rendering a
// branch-busy error as a short reason and any other error verbatim.
func TestResumeAll_SummaryNamesFailures(t *testing.T) {
	msg := batchResumeDoneMsg{
		resumed: 3,
		failures: []resumeFailure{
			{title: "api-fix", err: &git.BranchCheckedOutError{Branch: "zvi/api-fix", Path: "/repo"}},
			{title: "db", err: errors.New("boom")},
		},
	}

	got := msg.summary()

	assert.Contains(t, got, "Resumed 3 of 5 sessions. 2 could not resume:")
	assert.Contains(t, got, "api-fix — branch checked out elsewhere")
	assert.Contains(t, got, "db — boom")
}

// With no failures the summary is empty (the caller uses a transient notice).
func TestResumeAll_SummaryEmptyOnAllSuccess(t *testing.T) {
	assert.Empty(t, batchResumeDoneMsg{resumed: 4}.summary())
}
