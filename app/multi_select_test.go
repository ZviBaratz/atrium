package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pressKey drives a single rune key through the home model.
func pressRune(h *home, r rune) {
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// v enters multi-select mode: it flips the discrete state and points the hint bar
// at the gesture cues.
func TestMultiSelect_VEntersMode(t *testing.T) {
	h := newCreateFormHome(t)
	addActive(t, h, "alpha")

	pressRune(h, 'v')

	require.Equal(t, stateVisual, h.state, "v must enter multi-select mode")
	assert.Equal(t, ui.StateVisual, h.menu.State(), "the hint bar must teach the gestures")
}

// v on an empty list is a no-op with an explanation — the mode never opens with
// nothing to act on.
func TestMultiSelect_VEmptyListExplains(t *testing.T) {
	h := newCreateFormHome(t)

	pressRune(h, 'v')

	assert.Equal(t, stateDefault, h.state, "no mode without sessions")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "no sessions")
}

// v toggles the mode: pressing it again exits and clears the marks (like esc).
func TestMultiSelect_VTogglesModeOff(t *testing.T) {
	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")

	pressRune(h, 'v')
	h.list.ToggleMark(a)
	require.Equal(t, stateVisual, h.state)

	pressRune(h, 'v')

	assert.Equal(t, stateDefault, h.state, "v again exits multi-select mode")
	assert.Equal(t, 0, h.list.MarkedCount(), "exiting clears the marks")
}

// space marks the highlighted session; pressing it again unmarks.
func TestMultiSelect_SpaceMarksAndUnmarks(t *testing.T) {
	h := newCreateFormHome(t)
	addActive(t, h, "alpha")

	pressRune(h, 'v')
	require.Equal(t, 0, h.list.MarkedCount())

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	require.Equal(t, 1, h.list.MarkedCount(), "space marks the selected session")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	require.Equal(t, 0, h.list.MarkedCount(), "space again unmarks it")
}

// esc clears the marks and returns to the default state.
func TestMultiSelect_EscClearsAndExits(t *testing.T) {
	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")

	pressRune(h, 'v')
	h.list.ToggleMark(a)
	require.Equal(t, 1, h.list.MarkedCount())

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state, "esc exits multi-select mode")
	assert.Equal(t, 0, h.list.MarkedCount(), "esc clears the marks")
	assert.Equal(t, ui.StateDefault, h.menu.State(), "the hint bar reverts to default")
}

// p confirms over only the *pausable* subset of the marked set (active sessions),
// ignoring marked paused/loading ones.
func TestMultiSelect_PauseConfirmsPausableSubset(t *testing.T) {
	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")
	b := addActive(t, h, "bravo")
	c := addPaused(t, h, "charlie")

	pressRune(h, 'v')
	h.list.ToggleMark(a)
	h.list.ToggleMark(b)
	h.list.ToggleMark(c) // paused — not pausable

	pressRune(h, 'p')

	require.Equal(t, stateConfirm, h.state, "p opens a confirmation")
	require.NotNil(t, h.confirmationOverlay)
	assert.Contains(t, h.confirmationOverlay.Render(), "Pause 2 marked sessions?")
}

// r confirms over only the *paused* subset of the marked set.
func TestMultiSelect_ResumeConfirmsPausedSubset(t *testing.T) {
	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")
	c := addPaused(t, h, "charlie")

	pressRune(h, 'v')
	h.list.ToggleMark(a) // active — not resumable
	h.list.ToggleMark(c)

	pressRune(h, 'r')

	require.Equal(t, stateConfirm, h.state)
	assert.Contains(t, h.confirmationOverlay.Render(), "Resume 1 marked session?")
}

// x confirms over the killable subset and wears the danger border (kill is the
// one destructive batch action).
func TestMultiSelect_KillConfirmsWithDangerBorder(t *testing.T) {
	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")
	c := addPaused(t, h, "charlie")

	pressRune(h, 'v')
	h.list.ToggleMark(a)
	h.list.ToggleMark(c)

	pressRune(h, 'x')

	require.Equal(t, stateConfirm, h.state)
	assert.Contains(t, h.confirmationOverlay.Render(), "Kill 2 marked sessions?")
	assert.Equal(t, theme.Current().Palette.Danger, h.confirmationOverlay.BorderColor(),
		"a batch kill must wear the danger border")
}

// Pressing an action key with no eligible marked session explains itself and
// keeps the user in the mode (rather than opening an empty confirmation).
func TestMultiSelect_PauseNoEligibleStaysInMode(t *testing.T) {
	h := newCreateFormHome(t)
	c := addPaused(t, h, "charlie")

	pressRune(h, 'v')
	h.list.ToggleMark(c) // only a paused session is marked

	pressRune(h, 'p')

	assert.Equal(t, stateVisual, h.state, "no eligible target keeps the mode open")
	require.True(t, h.menu.HasNotice(), "the guard must explain itself")
	assert.Contains(t, h.menu.String(), "no marked sessions to pause")
}

// The batch-kill failure summary names every session that survived and why.
func TestBatchKill_SummaryNamesFailures(t *testing.T) {
	msg := batchKillDoneMsg{
		killed: 3,
		failures: []killFailure{
			{title: "api-fix", err: errors.New("branch checked out in the main repo")},
			{title: "db", err: errors.New("boom")},
		},
	}

	got := msg.summary()

	assert.Contains(t, got, "Killed 3 of 5 sessions. 2 could not be killed:")
	assert.Contains(t, got, "api-fix — branch checked out in the main repo")
	assert.Contains(t, got, "db — boom")
}

// With no failures the summary is empty (the caller uses a transient notice).
func TestBatchKill_SummaryEmptyOnAllSuccess(t *testing.T) {
	msg := batchKillDoneMsg{killed: 4}
	assert.Empty(t, msg.summary())
}
