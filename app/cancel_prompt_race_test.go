package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// TestCancelPromptOverlayCmdTouchesNoModelState is a race guard: it means nothing
// unless the binary is built with -race (`just test-race`), where it fails on the
// defect rather than on an assertion.
//
// Bubble Tea dispatches every tea.Cmd on its own goroutine, so a Cmd that writes
// model state races the update loop's reads of that state. cancelPromptOverlay
// used to return a tea.Sequence whose second element set the menu's state from
// inside the Cmd (#527, absorbed by #794); the fix moved that write inline, onto
// the update loop where the function's other mutations already live, leaving the
// Cmd as a bare tea.RequestWindowSize that touches no model state at all.
//
// The test drives the two halves against each other with nothing between them:
// the whole Cmd tree on one goroutine, the menu read on this one. runCmdTree is
// what makes it bite — it unwraps tea.Sequence structurally, so a regression that
// buries the write in a sequence element still executes it here instead of
// returning the unexecuted sequenceMsg and passing vacuously.
//
// It reads Menu.State (the accessor app_msgs.go uses on the update loop) rather
// than rendering View(). Both read the same field — viewContent reaches it through
// menu.String() — but the frame build allocates hard enough that the detector
// stops seeing the write entirely: measured against the reverted fix, an
// otherwise identical View()-driven loop reported the race in 0 of 12 runs, where
// this one reports it in 12 of 12. Read the cheap accessor, not the pretty one.
func TestCancelPromptOverlayCmdTouchesNoModelState(t *testing.T) {
	h := newCreateFormHome(t)
	// The layout the cancel asks to be recomputed; without a size there is no
	// geometry for the resize request to land in.
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	// A clean smart-dispatch overlay, not a create form: cancelling a dirty create
	// form stashes and persists a draft, an on-loop path this guard has no business
	// dragging in.
	h.textInputOverlay = overlay.NewSmartDispatchOverlay("Describe the session")
	h.state = statePrompt
	h.menu.SetState(ui.StateEmpty) // so the reset below is an observable change

	cmd := h.cancelPromptOverlay()
	require.NotNil(t, cmd, "cancel must still return the resize request the layout depends on")

	done := make(chan struct{})
	go func() {
		defer close(done)
		runCmdTree(cmd)
	}()
	// Reads on both sides of wherever the Cmd's write would land: the detector needs
	// an unsynchronized access before or after it, not one simultaneous with it.
	for range 64 {
		_ = h.menu.State()
	}
	<-done

	// Without this the guard would be satisfied by deleting the menu reset outright:
	// no write, no race, green. The reset is the behaviour; being on this goroutine
	// is the fix.
	require.Equal(t, ui.StateDefault, h.menu.State(), "cancel returns the hint bar to its default entries")
	require.Equal(t, stateDefault, h.state, "cancel returns the app to plain navigation")
}
