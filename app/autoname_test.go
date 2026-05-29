package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// newAutoNameHome builds a home wired enough to drive the auto-name and rename
// flows: a list with the given instances, a menu, an error box, a tabbed window
// (instanceChanged touches it), and real storage (the rename submit persists).
func newAutoNameHome(t *testing.T, titles ...string) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s, false)
	for _, title := range titles {
		inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: t.TempDir(), Program: "echo"})
		require.NoError(t, err)
		l.AddInstance(inst)
	}
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         l,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		storage:      st,
		program:      "echo",
	}
}

// On success, the generated name is offered through the rename overlay (pre-filled,
// not applied silently) and the in-flight guard is cleared.
func TestAutoNameDone_OpensPrefilledRenameOverlay(t *testing.T) {
	h := newAutoNameHome(t, "a")
	inst := h.list.GetInstances()[0]
	h.generatingName = true

	h.Update(autoNameDoneMsg{instance: inst, name: "Retry backoff"})

	require.Equal(t, stateRename, h.state)
	require.False(t, h.generatingName)
	require.Same(t, inst, h.renameTarget)
	require.NotNil(t, h.renameOverlay)
	require.Equal(t, "Retry backoff", h.renameOverlay.Value())
	require.Equal(t, "a", inst.DisplayName(), "name is not applied until the user confirms")
}

// On failure, the error surfaces and nothing about the session changes — no overlay,
// no rename, no lingering "generating" state.
func TestAutoNameDone_ErrorLeavesNameUntouched(t *testing.T) {
	h := newAutoNameHome(t, "a")
	inst := h.list.GetInstances()[0]
	h.generatingName = true

	_, cmd := h.Update(autoNameDoneMsg{instance: inst, err: errors.New("claude command not found")})

	require.False(t, h.generatingName)
	require.Nil(t, h.renameOverlay)
	require.Equal(t, stateDefault, h.state)
	require.NotNil(t, cmd, "an error should be surfaced via a command")
	require.Equal(t, "a", inst.DisplayName())
}

// Regression: the rename must land on the instance the overlay was opened for, even
// if the list selection moves while it is open (as it can during async auto-naming).
// Before the fix the submit handler re-fetched GetSelectedInstance() and renamed the
// wrong session.
func TestRename_AppliesToTargetNotMovedSelection(t *testing.T) {
	h := newAutoNameHome(t, "a", "b")
	instA := h.list.GetInstances()[0]
	instB := h.list.GetInstances()[1]

	// Overlay opened for A (as KeyRename / a successful autoNameDoneMsg would).
	h.list.SelectInstance(instA)
	h.renameTarget = instA
	h.renameOverlay = overlay.NewRenameOverlay("renamed-A")
	h.state = stateRename
	h.menu.SetState(ui.StatePrompt)

	// Selection hijacked onto B while the overlay is open.
	h.list.SelectInstance(instB)
	require.Same(t, instB, h.list.GetSelectedInstance(), "precondition: selection moved off A")

	// Submit the overlay.
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, "renamed-A", instA.DisplayName(), "name must land on the overlay's target")
	require.Equal(t, "b", instB.DisplayName(), "the now-selected session must be untouched")
	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.renameTarget, "target is cleared after submit")
}

// Pressing A while a generation is already in flight is a no-op (no second request).
func TestKeyAutoName_NoOpWhileGenerating(t *testing.T) {
	h := newAutoNameHome(t, "a")
	h.generatingName = true
	h.keySent = true // bypass the two-phase menu-highlight pass

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})

	require.True(t, h.generatingName)
	require.Nil(t, cmd)
	require.Equal(t, stateDefault, h.state)
}

// Pressing A on a selectable session starts a background generation and shows the hint.
func TestKeyAutoName_StartsGeneration(t *testing.T) {
	h := newAutoNameHome(t, "a")
	h.keySent = true // bypass the two-phase menu-highlight pass

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})

	require.True(t, h.generatingName)
	require.NotNil(t, cmd, "generation runs as a background command")
}
