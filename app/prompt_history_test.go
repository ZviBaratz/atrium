package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// quickSendHome builds a home with one selectable session and an open quick-send
// overlay, ready to submit — the shared setup for the recording tests.
func quickSendHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st
	inst, err := session.NewInstance(session.InstanceOptions{Title: "qs", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	h.state = statePrompt
	h.textInputOverlay = overlay.NewQuickSendOverlay("Send to qs")
	return h, inst
}

// A submitted quick-send prompt is recorded in the reuse history (#388).
func TestQuickSend_RecordsPromptHistory(t *testing.T) {
	h, _ := quickSendHome(t)
	h.textInputOverlay.SetPrompt("ship it")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	got := h.appState.GetPromptHistory()
	require.Len(t, got, 1)
	require.Equal(t, "ship it", got[0].Text, "the submitted quick-send prompt is recorded for reuse")
}

// With recording disabled, a submitted prompt is NOT recorded.
func TestQuickSend_DisableSuppressesRecording(t *testing.T) {
	h, _ := quickSendHome(t)
	off := false
	h.appConfig.RecordPromptHistory = &off
	h.textInputOverlay.SetPrompt("secret")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.Empty(t, h.appState.GetPromptHistory(), "recording disabled must not persist the prompt")
}

// Up-arrow on an EMPTY prompt field opens the history picker; picking a row
// inserts its text into the field and returns to compose (never submits).
func TestPromptHistory_UpOnEmptyOpensPickerAndInserts(t *testing.T) {
	h, inst := quickSendHome(t)
	require.NoError(t, h.appState.AddPromptHistory("remembered"))

	// Empty prompt field + up → history picker.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, stateHistory, h.state, "up on an empty prompt opens the history picker")
	require.NotNil(t, h.promptHistoryOverlay)

	// Enter inserts the highlighted text back into the compose field, no submit.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, statePrompt, h.state, "picking returns to compose")
	require.Nil(t, h.promptHistoryOverlay)
	require.Equal(t, "remembered", h.textInputOverlay.GetValue(), "the picked prompt is inserted, editable")
	require.Empty(t, inst.Prompt(), "picking must NOT submit or queue anything")
}

// Up-arrow with text already in the prompt field edits normally (does not open
// the picker) — the trigger is empty-field-only.
func TestPromptHistory_UpWithTextDoesNotOpen(t *testing.T) {
	h, _ := quickSendHome(t)
	require.NoError(t, h.appState.AddPromptHistory("remembered"))
	h.textInputOverlay.SetPrompt("half typed")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, statePrompt, h.state, "up with text present must not open history")
}
