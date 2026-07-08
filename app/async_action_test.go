package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelMsg is a stand-in inner result used to trace how an action's message
// flows back through the async plumbing.
type sentinelMsg struct{ id int }

// beginAsyncAction arms the in-flight state + busy progress row, and its command
// runs the wrapped action off the loop, tagging the result as asyncActionDoneMsg.
func TestBeginAsyncAction_ArmsBusyAndWrapsResult(t *testing.T) {
	h := newCreateFormHome(t)

	cmd := h.beginAsyncAction("pushing…", func() tea.Msg { return sentinelMsg{id: 7} })

	require.True(t, h.actionInFlight, "the action must be marked in flight")
	assert.Equal(t, ui.StateBusy, h.menu.State(), "the menu shows the busy progress row")
	assert.Equal(t, "pushing…", h.menu.BusyText())

	require.NotNil(t, cmd)
	msg := cmd()
	done, ok := msg.(asyncActionDoneMsg)
	require.True(t, ok, "the wrapped command must return asyncActionDoneMsg")
	assert.Equal(t, sentinelMsg{id: 7}, done.result, "the inner result rides inside the wrapper")
}

// The asyncActionDoneMsg handler clears the in-flight state and progress row, then
// re-dispatches the inner message so its own case handles it.
func TestAsyncActionDone_ClearsStateAndForwardsInner(t *testing.T) {
	h := newCreateFormHome(t)
	h.actionInFlight = true
	h.menu.SetBusy("pushing…")

	_, cmd := h.Update(asyncActionDoneMsg{result: sentinelMsg{id: 9}})

	assert.False(t, h.actionInFlight, "completion clears the in-flight flag")
	assert.Equal(t, ui.StateDefault, h.menu.State(), "the busy row gives way to the default bar")
	require.NotNil(t, cmd, "the inner message must be forwarded")
	assert.Equal(t, sentinelMsg{id: 9}, cmd(), "the forwarded command yields the inner message")
}

// A nil inner result forwards harmlessly (the switch falls through).
func TestAsyncActionDone_NilInnerIsNoop(t *testing.T) {
	h := newCreateFormHome(t)
	h.actionInFlight = true

	_, cmd := h.Update(asyncActionDoneMsg{result: nil})

	assert.False(t, h.actionInFlight)
	require.NotNil(t, cmd)
	assert.Nil(t, cmd(), "a nil inner result forwards as a no-op")
}

// A confirm armed with a busy label runs off the UI thread: confirming arms the
// in-flight state and hands back a command that wraps the action's result.
func TestConfirmAsyncAction_ConfirmRunsOffThread(t *testing.T) {
	h := newCreateFormHome(t)
	h.confirmAsyncAction("Push?", "pushing…", func() tea.Msg { return sentinelMsg{id: 3} })
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.handleConfirmState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	assert.Equal(t, stateDefault, h.state, "confirming closes the overlay")
	require.True(t, h.actionInFlight, "an async confirm arms the in-flight state")
	assert.Equal(t, "pushing…", h.menu.BusyText())
	require.NotNil(t, cmd)
	done, ok := cmd().(asyncActionDoneMsg)
	require.True(t, ok, "the async confirm runs through beginAsyncAction")
	assert.Equal(t, sentinelMsg{id: 3}, done.result)
}

// A confirm armed without a busy label keeps the legacy inline path: the action
// runs synchronously on the loop and its result is returned directly (unwrapped),
// with no in-flight state. Kill and other list-mutating confirms rely on this.
func TestConfirmAction_NoLabelRunsInline(t *testing.T) {
	h := newCreateFormHome(t)
	ran := false
	h.confirmAction("Kill?", func() tea.Msg { ran = true; return sentinelMsg{id: 1} })

	_, cmd := h.handleConfirmState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	assert.True(t, ran, "the inline action runs synchronously on confirm")
	assert.False(t, h.actionInFlight, "the inline path never arms the in-flight state")
	require.NotNil(t, cmd)
	assert.Equal(t, sentinelMsg{id: 1}, cmd(), "the inline result is returned directly, unwrapped")
}

// While an action is in flight, a per-session mutating key is refused with a busy
// notice naming the operation; navigation keys still pass through.
func TestInputGate_SwallowsMutatingKeysWhileBusy(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)
	h.actionInFlight = true
	h.menu.SetBusy("pushing…")

	// A navigation key is allowed: it must not raise the busy notice.
	pressKey(h, 'j')
	assert.False(t, h.menu.HasNotice(), "navigation passes through while busy")
	assert.Equal(t, ui.StateBusy, h.menu.State(), "navigation leaves the busy row intact")

	// A mutating key (p → pause) is swallowed with a busy notice.
	pressKey(h, 'p')
	require.True(t, h.menu.HasNotice(), "a mutating key must be refused with a notice")
	assert.Contains(t, h.menu.String(), "busy")
	assert.Contains(t, h.menu.String(), "pushing", "the notice names the in-flight operation")
	assert.Equal(t, stateDefault, h.state, "the swallowed key opened no overlay")
}

// A successful push acknowledges with a notice (push used to return nil silently).
func TestPushedMsg_ShowsNotice(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)

	_, cmd := h.Update(pushedMsg{})
	require.NotNil(t, cmd)
	cmd() // fire the batched notice/refresh commands

	require.True(t, h.menu.HasNotice(), "a successful push must acknowledge itself")
	assert.Contains(t, h.menu.String(), "pushed")
}

// A single pause finished off the UI thread persists, tears down the terminal, and
// opens the rename overlay ("park + jot note"), all on the Update loop.
func TestPauseDone_OpensRenameOverlay(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)

	h.Update(pauseDoneMsg{instance: inst})

	assert.Equal(t, stateRename, h.state, "a completed pause opens the note overlay")
	require.NotNil(t, h.renameOverlay)
	assert.Equal(t, inst, h.renameTarget)
}

// A failed pause surfaces the error rather than opening the note overlay.
func TestPauseDone_ErrorSurfaces(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)

	h.Update(pauseDoneMsg{instance: inst, err: errors.New("boom")})

	assert.NotEqual(t, stateRename, h.state, "a failed pause must not open the note overlay")
	// With the always-on hint bar up, a short error rides the bar's notice row.
	require.True(t, h.menu.HasNotice(), "the pause failure must surface")
	assert.Contains(t, h.menu.String(), "boom")
}
