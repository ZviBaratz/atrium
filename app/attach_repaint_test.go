package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firstIsClearScreen reports that cmd is a batch whose FIRST command yields the
// clearScreenMsg tea.ClearScreen produces — the hard full repaint every
// handleAttachFinished path that returns to the list must wire (see
// repaintAfterAttach). It runs ONLY batch element 0, never the side-effectful
// instanceChanged/sweep extras. clearScreenMsg is an unexported empty struct, so
// it is compared by value against tea.ClearScreen().
func firstIsClearScreen(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	require.NotNil(t, cmd)
	batch, ok := cmd().(tea.BatchMsg) // Batch(>=2 non-nil cmds) always yields BatchMsg
	if !ok {
		return false
	}
	require.NotEmpty(t, batch)
	return batch[0]() == tea.ClearScreen()
}

// repaintAfterAttach must front-load a hard ClearScreen, then a re-layout
// WindowSize, then any extra cmds — the contract firstIsClearScreen and the
// return-path tests below rely on.
func TestRepaintAfterAttach_ClearsScreenFirstThenResizes(t *testing.T) {
	h := &home{}

	batch, ok := h.repaintAfterAttach()().(tea.BatchMsg)
	require.True(t, ok, "repaintAfterAttach must yield a batch")
	require.Len(t, batch, 2)
	assert.Equal(t, tea.ClearScreen(), batch[0](), "the hard repaint must come first")
	assert.Equal(t, tea.WindowSize()(), batch[1](), "the re-layout must come second")

	// Extra cmds are appended after the repaint pair, so ClearScreen stays first
	// (and thus safely inspectable without running the side-effectful extras).
	marker := func() tea.Msg { return "marker" }
	batch, ok = h.repaintAfterAttach(marker)().(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 3)
	assert.Equal(t, tea.ClearScreen(), batch[0]())
	assert.Equal(t, tea.Msg("marker"), batch[2](), "extras are appended after the repaint pair")
}

// The reported bug: detaching from an attach (the ctrl+q path) returns to the
// session list, and Bubble Tea's implicit soft repaint can leave it blank/stale.
// The handler must force a hard ClearScreen so the reclaimed list actually paints.
func TestAttachFinished_NormalDetach_ForcesRepaint(t *testing.T) {
	h, inst := newUnreadHome(t)

	// inst has no tmux session, so AttachExitError/AttachKillRequested are zero and
	// cycleTarget is nil — the exact normal detach path from the bug.
	_, cmd := h.Update(attachFinishedMsg{killTarget: inst})

	require.Equal(t, stateDefault, h.state)
	assert.True(t, firstIsClearScreen(t, cmd),
		"a normal detach must force a hard ClearScreen so the reclaimed list repaints")
}

// A failed attach also returns to the list (it never handed off, or Run errored),
// so it must force a repaint too — not leave the list blank behind a toast/modal.
func TestAttachFinished_Error_ForcesRepaint(t *testing.T) {
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox() // handleError dereferences errBox

	_, cmd := h.Update(attachFinishedMsg{err: errors.New("boom"), killTarget: inst})

	assert.True(t, firstIsClearScreen(t, cmd),
		"a failed attach returns to the list and must force a repaint")
}
