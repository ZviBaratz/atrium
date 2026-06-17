package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// newNoteWiringHome builds a fully-wired home (storage, menu, panes) whose
// instances share one repo group (Path "."), so a deep rename onto another
// session's title actually collides.
func newNoteWiringHome(t *testing.T, titles ...string) (*home, []*session.Instance) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := spinner.New()
	l := ui.NewList(&s)
	var insts []*session.Instance
	for _, title := range titles {
		inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: ".", Program: "echo"})
		require.NoError(t, err)
		l.AddInstance(inst)
		insts = append(insts, inst)
	}
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         l,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		storage:      st,
		program:      "echo",
	}
	return h, insts
}

// Submitting the rename overlay persists the note alongside the (unchanged) label.
func TestRenameSubmit_PersistsNote(t *testing.T) {
	h, insts := newNoteWiringHome(t, "alpha")
	inst := insts[0]
	h.renameTarget = inst
	h.renameOverlay = overlay.NewRenameOverlay(inst.DisplayName(), "", true) // focus the note
	h.state = stateRename

	for _, r := range "waiting on CI" {
		press(t, h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	press(t, h, tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, stateDefault, h.state, "submit closes the overlay")
	require.Equal(t, "waiting on CI", inst.Note())
}

// Clearing the note field and submitting removes a previously-set note.
func TestRenameSubmit_EmptyNoteClears(t *testing.T) {
	h, insts := newNoteWiringHome(t, "alpha")
	inst := insts[0]
	inst.SetNote("stale")
	h.renameTarget = inst
	h.renameOverlay = overlay.NewRenameOverlay(inst.DisplayName(), inst.Note(), true)
	h.state = stateRename

	// Backspace over the prefilled "stale", then submit.
	for range "stale" {
		press(t, h, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	press(t, h, tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, stateDefault, h.state)
	require.Empty(t, inst.Note(), "an emptied note field clears the note")
}

// A deep rename that collides with another session's title must not discard the
// user's typed name and note: the dialog reopens pre-filled, and nothing is
// persisted to the instance.
func TestRenameSubmit_DeepCollisionReopensOverlayWithNote(t *testing.T) {
	h, insts := newNoteWiringHome(t, "alpha", "beta")
	alpha := insts[0]
	h.renameTarget = alpha
	h.renameOverlay = overlay.NewRenameOverlay("beta", "park me", false) // collides with beta
	h.state = stateRename

	press(t, h, tea.KeyMsg{Type: tea.KeyCtrlD}) // opt into deep rename
	press(t, h, tea.KeyMsg{Type: tea.KeyEnter}) // submit -> collision

	require.Equal(t, stateRename, h.state, "the dialog reopens instead of closing")
	require.NotNil(t, h.renameOverlay)
	require.Equal(t, "beta", h.renameOverlay.Value(), "the attempted name is preserved")
	require.Equal(t, "park me", h.renameOverlay.NoteValue(), "the note is preserved")
	require.Equal(t, "alpha", alpha.Title, "the collision left the instance untouched")
	require.Empty(t, alpha.Note(), "nothing is persisted on a failed rename")
}
