package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui/overlay"
)

// runes is a small helper to type text into the focused field.
func draftRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDraft_EscapeStashesDirtyForm(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n")) // open, focus on title
	require.NotNil(t, h.textInputOverlay)
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, h.textInputOverlay, "the live overlay is cleared on cancel")
	require.NotNil(t, h.stashedDraft, "a dirty form is stashed")
	assert.Equal(t, "my-draft", h.stashedDraft.GetTitle())
	assert.Equal(t, stateDefault, h.state)
}

func TestDraft_EscapeDiscardsCleanForm(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n")) // open, type nothing
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, h.stashedDraft, "an untouched form leaves no stash")
}

func TestDraft_ReopenRestoresStash(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	h.handleKeyPress(draftRunes("my-draft"))
	h.textInputOverlay.SetPrompt("draft body")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, h.stashedDraft)

	h.handleKeyPress(draftRunes("n")) // reopen
	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "my-draft", h.textInputOverlay.GetTitle(), "the draft is restored")
	assert.Equal(t, "draft body", h.textInputOverlay.GetValue(), "the prompt body is restored too")
	assert.Nil(t, h.stashedDraft, "the stash is consumed into the live overlay")
}

// A restored draft must stay submittable. Escaping a dirty form stashes the very
// overlay whose Esc keypress set Canceled=true; if that flag rides into the stash,
// the restored form's every submit (Enter or Ctrl+S) is misread as a cancel —
// handlePromptState checks IsCanceled before IsSubmitted — so the form closes and
// re-stashes without ever creating the session. Once any draft has been stashed,
// new-session creation is dead. Regression guard for that.
func TestDraft_RestoredDraftIsSubmittable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	addDirectInstance(t, h, "existing", dir) // contextual non-git target → a direct session
	before := h.list.NumInstances()

	h.handleKeyPress(draftRunes("n")) // open, focus title
	typeString(h, "my-draft")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash the dirty form
	require.NotNil(t, h.stashedDraft, "a dirty form is stashed on Escape")

	h.handleKeyPress(draftRunes("n")) // reopen → restore the draft
	require.NotNil(t, h.textInputOverlay)
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle(), "the draft is restored")

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlS}) // submit the restored draft

	assert.Nil(t, h.stashedDraft, "a submitted draft must not be re-stashed as a cancel")
	assert.Equal(t, before+1, h.list.NumInstances(),
		"submitting a restored draft must create the session")
	assert.Nil(t, h.textInputOverlay, "a successful submit closes the form")
	assert.Equal(t, stateDefault, h.state)
}

// The same flow via Enter on the title (the one-handed "n → name → ↵" create
// contract) must also create the restored draft — both submit keys went dead
// behind the stale Canceled flag.
func TestDraft_RestoredDraftSubmitsOnEnter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	addDirectInstance(t, h, "existing", dir)
	before := h.list.NumInstances()

	h.handleKeyPress(draftRunes("n"))
	typeString(h, "my-draft")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash
	h.handleKeyPress(draftRunes("n"))              // restore
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle())

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // submit via Enter on the title

	assert.Nil(t, h.stashedDraft, "Enter on a restored draft must not re-stash it as a cancel")
	assert.Equal(t, before+1, h.list.NumInstances(), "Enter must create the restored draft's session")
	assert.Nil(t, h.textInputOverlay)
}

func TestDraft_EscapeOnNonCreateOverlayDoesNotStash(t *testing.T) {
	h := newCreateFormHome(t)
	ov := overlay.NewSmartDispatchOverlay("Describe the session")
	ov.SetPrompt("some text")
	h.textInputOverlay = ov
	h.state = statePrompt

	h.cancelPromptOverlay()

	assert.Nil(t, h.stashedDraft, "a non-create overlay must never be stashed")
	assert.Nil(t, h.textInputOverlay, "cancel still clears the live overlay")
}

// A Ctrl+C cancel is intercepted by the app before the overlay sees it, so it does
// not disarm a pending double-tap the way Esc (which flows through the overlay) does.
// Stashing must drop that arm; otherwise a single Ctrl+R after reopening would wipe
// the restored draft, defeating the double-tap guard.
func TestDraft_ArmDoesNotSurviveCtrlCCancel(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // arm the clear
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlC}) // cancel (bypasses the overlay's disarm)
	require.NotNil(t, h.stashedDraft, "a dirty form is still stashed on Ctrl+C")

	h.handleKeyPress(draftRunes("n"))                // reopen, restoring the draft
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // a single press must only arm, not wipe

	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "my-draft", h.textInputOverlay.GetTitle(),
		"one Ctrl+R after restore must not clear the draft")
}

func TestDraft_DoubleCtrlRRebuildsFresh(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	h.handleKeyPress(draftRunes("n")) // reopen with the restored draft
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle())

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // arm
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // confirm

	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "", h.textInputOverlay.GetTitle(), "the form is rebuilt fresh")
	assert.Nil(t, h.stashedDraft, "the stash is dropped on clear")
}
