package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// runes is a small helper to type text into the focused field.
func draftRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDraft_EscapeStashesDirtyForm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n")) // open, type nothing
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, h.stashedDraft, "an untouched form leaves no stash")
}

func TestDraft_ReopenRestoresStash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

// The whole point of issue #211: a stashed draft must survive a crash/quit, not just
// an in-run reopen. Stashing now mirrors the draft to state.json; a fresh home reading
// that state back (a simulated restart) must rebuild the form from it on the next n/N.
func TestDraft_SurvivesRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	typeString(h, "my-draft")
	h.textInputOverlay.SetPrompt("draft body")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash + persist
	require.NotNil(t, h.stashedDraft)
	require.NotNil(t, config.LoadState().GetDraft(), "the stash is mirrored to disk")

	// Simulate a restart: a fresh home reading state from disk, no in-memory stash.
	h2 := newCreateFormHome(t)
	h2.appState = config.LoadState()
	require.Nil(t, h2.stashedDraft, "a fresh run starts with no in-memory stash")

	h2.handleKeyPress(draftRunes("n")) // reopen → rehydrate from disk, then restore
	require.NotNil(t, h2.textInputOverlay)
	assert.Equal(t, "my-draft", h2.textInputOverlay.GetTitle(), "the title survives the restart")
	assert.Equal(t, "draft body", h2.textInputOverlay.GetValue(), "the prompt body survives too")
	assert.Nil(t, h2.stashedDraft, "the rehydrated stash is consumed into the live overlay")
	assert.Nil(t, config.LoadState().GetDraft(), "consuming the draft clears the disk copy")
}

// A draft recovered from disk after a restart must show the project's auto-routed Claude
// account, exactly like a fresh form — the account is re-derived from the target, never
// persisted, so the rehydrated overlay must re-run the same preselection. Without it the
// picker would default to the first account, misrepresenting which login the session runs.
func TestDraft_RehydratedDraftPreselectsRoutedAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir() // direct (non-git) target → routes by path, hermetic
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},                  // catch-all default (accounts[0])
		{Name: "work", ConfigDir: "/w", PathMatches: []string{dir}}, // path route for this target
	}

	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = accounts
	addDirectInstance(t, h, "existing", dir) // make dir the contextual target

	h.handleKeyPress(draftRunes("n"))
	typeString(h, "my-draft")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash + persist (Path = dir)
	d := config.LoadState().GetDraft()
	require.NotNil(t, d)
	require.Equal(t, dir, d.Path, "the draft persists its project")

	// Simulate a restart: a fresh home reading the persisted draft back.
	h2 := newCreateFormHome(t)
	h2.appConfig.ClaudeAccounts = accounts
	h2.appState = config.LoadState()

	h2.handleKeyPress(draftRunes("n")) // reopen → rehydrate from disk
	require.NotNil(t, h2.textInputOverlay)
	require.Equal(t, "my-draft", h2.textInputOverlay.GetTitle(), "the draft is restored")
	assert.Equal(t, "work", h2.textInputOverlay.SelectedAccountName(),
		"the recovered draft shows the path-routed account, not the first-account default")
}

// Submitting must leave no stale draft on disk, so a created session is never shadowed
// by a lingering crash-recovery copy.
func TestDraft_SubmitClearsPersistedDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	addDirectInstance(t, h, "existing", dir) // contextual non-git target → a direct session

	h.handleKeyPress(draftRunes("n"))
	typeString(h, "my-draft")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash + persist
	require.NotNil(t, config.LoadState().GetDraft())

	h.handleKeyPress(draftRunes("n")) // reopen → restore the draft
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle())
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlS}) // submit

	require.Nil(t, h.textInputOverlay, "a successful submit closes the form")
	assert.Nil(t, config.LoadState().GetDraft(), "submitting leaves no draft on disk")
}

// A confirmed double-tap Ctrl+R wipes the form; the on-disk mirror must go with it,
// so a wiped draft cannot resurface after a restart.
func TestDraft_ClearFormDropsPersistedDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	typeString(h, "my-draft")
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // stash + persist
	h.handleKeyPress(draftRunes("n"))              // reopen with the restored draft
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle())

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // arm
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // confirm → rebuild fresh, drop draft

	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "", h.textInputOverlay.GetTitle(), "the form is rebuilt fresh")
	assert.Nil(t, config.LoadState().GetDraft(), "the wiped draft is gone from disk too")
}

func TestDraft_EscapeOnNonCreateOverlayDoesNotStash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
