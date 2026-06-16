package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addActive appends a running (pausable) session with the given title to the list.
func addActive(t *testing.T, h *home, title string) *session.Instance {
	t.Helper()
	inst := newBranchInstance(t, title, "zvi/"+title)
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)
	return inst
}

// ctrl+p with several active sessions opens a count confirmation (it must not
// pause anything until the user confirms).
func TestPauseAll_OpensCountConfirmation(t *testing.T) {
	h := newCreateFormHome(t)
	addActive(t, h, "alpha")
	addActive(t, h, "bravo")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlP})

	require.Equal(t, stateConfirm, h.state, "ctrl+p must open the confirmation overlay")
	require.NotNil(t, h.confirmationOverlay)
	assert.Contains(t, h.confirmationOverlay.Render(), "Pause 2 active sessions?")
}

// The count is grammatical for a single session.
func TestPauseAll_SingularMessage(t *testing.T) {
	h := newCreateFormHome(t)
	addActive(t, h, "alpha")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlP})

	require.Equal(t, stateConfirm, h.state)
	assert.Contains(t, h.confirmationOverlay.Render(), "Pause 1 active session?")
}

// ctrl+p with nothing active must not open a confirmation; it explains itself
// with a notice instead.
func TestPauseAll_NoActiveExplains(t *testing.T) {
	h := newCreateFormHome(t)
	addPaused(t, h, "parked")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlP})

	assert.Equal(t, stateDefault, h.state, "no confirmation when there is nothing to pause")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "no active sessions")
}

// The failure summary names every session that didn't pause, rendering each
// error verbatim.
func TestPauseAll_SummaryNamesFailures(t *testing.T) {
	msg := batchPauseDoneMsg{
		paused: 3,
		failures: []pauseFailure{
			{title: "api-fix", err: errors.New("worktree busy")},
			{title: "db", err: errors.New("boom")},
		},
	}

	got := msg.summary()

	assert.Contains(t, got, "Paused 3 of 5 sessions. 2 could not pause:")
	assert.Contains(t, got, "api-fix — worktree busy")
	assert.Contains(t, got, "db — boom")
}

// With no failures the summary is empty (the caller uses a transient notice).
func TestPauseAll_SummaryEmptyOnAllSuccess(t *testing.T) {
	assert.Empty(t, batchPauseDoneMsg{paused: 4}.summary())
}
