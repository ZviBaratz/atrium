package app

import (
	"context"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
)

// A paste is text, never a command. The first cut of this fix converted a paste
// into a key message whose Text was the pasted content — and v2's Key.String()
// returns Text verbatim, so the synthesized key was indistinguishable from a
// keypress at every `switch msg.String()` site. Bubble Tea v1 never had this
// problem: its Key.String() wrapped pasted runes in "[...]" precisely so a paste
// could not activate a shortcut. These tests pin the property that bracketing
// bought, without borrowing v1's mechanism: a paste reaches text surfaces and
// nothing else.

// newQuitablePasteHome wires the storage handleQuit persists through, so a paste
// that (wrongly) reaches the quit path returns tea.Quit instead of panicking —
// the failure has to be an assertion, not a crash that takes the package down.
func newQuitablePasteHome(t *testing.T) *home {
	t.Helper()
	s := spinner.New()
	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         ui.NewList(&s),
		menu:         ui.NewMenu(),
		errBox:       ui.NewErrBox(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		appConfig:    config.DefaultConfig(),
		appState:     appState,
		storage:      storage,
		program:      "echo",
	}
}

// TestSingleCharacterPasteIsNeverAKeybinding is the sharpest case: a clipboard
// holding exactly one bound character. "hello" is safe by accident (no binding is
// five characters long); "q" is not, and it quit the app with no confirmation.
func TestSingleCharacterPasteIsNeverAKeybinding(t *testing.T) {
	for _, content := range []string{"q", "n", "U", "?", "/"} {
		t.Run(content, func(t *testing.T) {
			h := newQuitablePasteHome(t)
			h.state = stateDefault

			_, cmd := h.Update(tea.PasteMsg{Content: content})

			require.Nil(t, cmd,
				"pasting %q outside a text field must do nothing, not run its keybinding", content)
			require.Equal(t, stateDefault, h.state,
				"pasting %q must not change state", content)
		})
	}
}

// TestPasteOfAControlWordIsTextNotACommand covers the multi-character half of the
// same defect: handlers dispatch on msg.String(), and "esc"/"enter" are ordinary
// English words a user can legitimately paste.
func TestPasteOfAControlWordIsTextNotACommand(t *testing.T) {
	for _, word := range []string{"esc", "enter", "down", "backspace", "tab"} {
		t.Run(word, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.state = stateFilter
			h.list.SetFilterActive(true)
			h.list.SetFilter("re")

			h.Update(tea.PasteMsg{Content: word})

			require.Equal(t, "re"+word, h.list.FilterQuery(),
				"pasting the word %q must extend the filter, not run the key of that name", word)
			require.Equal(t, stateFilter, h.state,
				"pasting %q must leave the filter open", word)
		})
	}
}

// TestPasteOfEscDoesNotDiscardTheCreateForm is the data-loss case: the create
// form's key switch maps "esc" to Canceled, so a pasted "esc" threw away an
// in-progress draft.
func TestPasteOfEscDoesNotDiscardTheCreateForm(t *testing.T) {
	h := newCreateFormHome(t)
	h.newSessionPath = t.TempDir()
	h.textInputOverlay, _ = h.newSessionFormOverlay()
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = statePrompt
	focusPromptField(t, h)

	h.Update(tea.PasteMsg{Content: "esc"})

	require.Equal(t, statePrompt, h.state, "a pasted \"esc\" must not close the form")
	require.NotNil(t, h.textInputOverlay, "a pasted \"esc\" must not discard the draft")
	require.Contains(t, h.textInputOverlay.GetValue(), "esc",
		"a pasted \"esc\" must land in the prompt as text")
}
