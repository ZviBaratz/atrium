package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// Submitting the quick-send compose box must not call SendPrompt inline: its verify polling
// would block the UI thread, and its soft "pane not ready yet" outcomes would surface to the
// user as errors. Instead it queues the message on the selected instance so the metadata
// tick delivers it through the same verified, idempotent, retrying path as the new-session
// prompt (and persists it so it survives a restart before delivery).
func TestQuickSendQueuesPromptForVerifiedDelivery(t *testing.T) {
	h := newCreateFormHome(t)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st // the submit handler persists the queued prompt

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "qs", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	h.state = statePrompt
	h.textInputOverlay = overlay.NewQuickSendOverlay("Send to qs")
	h.textInputOverlay.SetPrompt("ship it")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, "ship it", inst.Prompt(), "the message must be queued for delivery, not sent inline")
	require.True(t, inst.PromptQueuedAt().IsZero(),
		"a quick-send is a follow-up with a zero clock: strict idle-only, never force-injected mid-turn")
	require.True(t, h.menu.HasNotice(), "a queued quick-send must flash an acknowledgment, not close silently")
	require.Nil(t, h.textInputOverlay, "a submitted quick-send closes the overlay")
	require.Equal(t, stateDefault, h.state, "submit drops straight back to the list")
}

// TestQuickSendAppendsWhenPromptQueued is the repro for loss scenario (C)/(B): a quick-send
// landing while a prompt is already queued must APPEND to the FIFO, not overwrite the slot,
// so the earlier message is preserved and both deliver in order.
func TestQuickSendAppendsWhenPromptQueued(t *testing.T) {
	h := newCreateFormHome(t)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "qs", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	inst.QueuePrompt("first") // a prompt is already waiting (e.g. the still-undelivered boot prompt)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	h.state = statePrompt
	h.textInputOverlay = overlay.NewQuickSendOverlay("Send to qs")
	h.textInputOverlay.SetPrompt("second")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, 2, inst.QueueLen(), "the quick-send must append, not overwrite the queued prompt")
	require.Equal(t, "first", inst.Prompt(), "the earlier prompt stays at the head and delivers first")
}
