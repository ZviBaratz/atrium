package app

import (
	"testing"

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
