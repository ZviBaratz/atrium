package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func queueInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	return inst
}

func mustStorage(t *testing.T) *session.Storage {
	t.Helper()
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	return st
}

func TestOpenQueue_EmptyQueueRefused(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateDefault, h.state, "an empty queue is a dead end — don't open")
	require.Nil(t, h.queueOverlay)
	require.True(t, h.menu.HasNotice(), "the refusal is surfaced as a notice")
}

func TestOpenQueue_OpensWithPendingPrompts(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.QueueFollowupPrompt("b")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateQueue, h.state)
	require.NotNil(t, h.queueOverlay)
	require.Same(t, inst, h.queueTarget)
}

func TestOpenQueue_AllowsPausedSession(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.SetStatus(session.Paused)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateQueue, h.state, "queue management needs no live pane")
}

func TestQueueOverlay_EscCloses(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.queueOverlay)
	require.Nil(t, h.queueTarget)
}

func TestQueueCancel_RemovesEntryAndPersists(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.QueueFollowupPrompt("b")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue() // cursor on head "a"

	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, 1, inst.QueueLen(), "the head was cancelled")
	require.Equal(t, "b", inst.Prompt())
	require.Equal(t, stateQueue, h.state, "still open with one entry left")
}

func TestQueueCancel_LastEntryClosesOverlay(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("only")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, 0, inst.QueueLen())
	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.queueOverlay)
	require.True(t, h.menu.HasNotice())
}

func TestQueueCancel_InFlightHeadRefused(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("boot")
	_, ok := inst.ClaimPrompt() // raises the in-flight guard on the head
	require.True(t, ok)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, 1, inst.QueueLen(), "the in-flight head is not cancelled")
	require.Equal(t, stateQueue, h.state, "the overlay stays open")
	require.Contains(t, h.queueOverlay.Render(), "being delivered", "the refusal is explained in-overlay")
}

func TestQueueCancel_StaleIndexRefusedWithShiftMessage(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.QueueFollowupPrompt("b")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue() // snapshot ["a","b"], cursor on head "a"

	// A delivery pops the head after the overlay snapshotted it, so the cursor's
	// index/text no longer matches the live queue — a refusal that is not the
	// in-flight head.
	txt, ok := inst.ClaimPrompt()
	require.True(t, ok)
	inst.ClearPrompt(txt) // "a" delivered and popped; live queue is now ["b"]

	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, 1, inst.QueueLen(), "the stale index cancels nothing")
	require.Equal(t, stateQueue, h.state, "the overlay stays open")
	require.Contains(t, h.queueOverlay.Render(), "the queue just changed",
		"a shifted-queue refusal is named distinctly from an in-flight head")
	require.NotContains(t, h.queueOverlay.Render(), "being delivered")
}

func TestQueueCancel_TargetsOpenedInstanceNotSelection(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	a := queueInstance(t, "a")
	a.QueueFollowupPrompt("xa")
	b := queueInstance(t, "b")
	b.QueueFollowupPrompt("xb")
	h.list.AddInstance(a)
	h.list.AddInstance(b)
	h.list.SelectInstance(a)
	_, _ = h.openQueue() // target = a

	h.list.SelectInstance(b) // selection moves away while the overlay is open
	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, 0, a.QueueLen(), "the opened instance's queue shrank")
	require.Equal(t, 1, b.QueueLen(), "the newly-selected instance is untouched")
}
