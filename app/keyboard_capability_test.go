package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestView_RequestsNoKeyboardEnhancements is #396's "no action is bound
// release-only or kitty-only" acceptance criterion, in the form that makes the
// first half of it unreachable rather than merely unused.
//
// Key releases can only arrive if this field asks for them. Leaving it zero means
// a KeyReleaseMsg is never delivered, so no binding CAN depend on one — which is a
// stronger guarantee than auditing the handlers, and it does not decay as handlers
// are added. (The other half, kitty-only chords, is audited in keys by
// TestRegistry_NoDefaultBindingNeedsDisambiguation.)
//
// It is a whole-struct equality rather than a check on ReportEventTypes alone
// because the other three fields change how ordinary keys are delivered — as
// escape codes, with alternate codes, with associated text — and this app
// dispatches on msg.String() end to end. None of them has been measured against
// that vocabulary, and the point of the guard is that none of them needs to be:
// the zero value keeps the unmeasured path out of the app entirely.
func TestView_RequestsNoKeyboardEnhancements(t *testing.T) {
	h := newCreateFormHome(t)
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	require.Equal(t, tea.KeyboardEnhancements{}, h.View().KeyboardEnhancements,
		"Atrium requests no keyboard enhancements: key disambiguation is already on "+
			"unconditionally, and every field here only adds a flag that breaks something")
}

// TestKeyboardEnhancementsMsgSetsTheLatch drives the terminal's reply through
// Update and asserts it reaches the latch the footers read.
//
// The Flags: 0 case is the one worth having. SupportsKeyDisambiguation is
// Flags > 0, so a terminal that answers the query while reporting nothing enabled
// must leave the latch alone — dropping that guard would make the mere ARRIVAL of
// a reply count as support, which is a different (and wrong) predicate that the
// happy path cannot distinguish.
func TestKeyboardEnhancementsMsgSetsTheLatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags int
		want  bool
	}{
		{"disambiguation enabled", 1, true},
		{"disambiguation plus event types", 3, true},
		{"answered with nothing enabled", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(keys.SetTerminalDisambiguates(false))
			h := newCreateFormHome(t)

			_, cmd := h.Update(tea.KeyboardEnhancementsMsg{Flags: tc.flags})

			assert.Equal(t, tc.want, keys.TerminalDisambiguates())
			assert.Nil(t, cmd, "learning a capability commands nothing; the next frame re-renders anyway")
		})
	}
}

// TestShiftEnterReachesTheComposerThroughUpdate is the end-to-end half the overlay
// tests cannot give: they call HandleKeyPress directly, so they would stay green if
// something upstream — handleKeyPress's prelude, the statePrompt router, a mode
// handler — swallowed the key before the overlay ever saw it.
//
// That is not a hypothetical route. handlePromptState already intercepts "ctrl+c"
// and an "up" on an empty prompt before delegating, and handleKeyPress has a
// ctrl+l and screensaver prelude ahead of both.
func TestShiftEnterReachesTheComposerThroughUpdate(t *testing.T) {
	h := newSmokeHome(t, statePrompt, func(h *home, _ *session.Instance) {
		h.textInputOverlay = overlay.NewQuickSendOverlay("Send to s")
	})

	h.handleKeyPress(testutil.Runes("line one"))
	h.handleKeyPress(testutil.Key("shift+enter"))
	h.handleKeyPress(testutil.Runes("line two"))

	assert.Equal(t, "line one\nline two", h.textInputOverlay.GetValue())
	assert.Equal(t, statePrompt, h.state, "Shift+Enter must not close the composer")
}

// TestShiftEnterInTheDiffCommentComposerInsertsANewline covers the one composer with
// its own key router in front of it (handleDiffCommentComposer), which forwards
// everything but ctrl+c — so a shift+enter that reached the submit leg would queue a
// half-written comment and drop the user back on the line cursor.
func TestShiftEnterInTheDiffCommentComposerInsertsANewline(t *testing.T) {
	h := newDiffCommentHome(t)
	h.textInputOverlay = overlay.NewQuickSendOverlay("Comment on foo.go:1")
	h.composingDiffComment = true
	h.state = statePrompt

	h.handleKeyPress(testutil.Runes("first"))
	h.handleKeyPress(testutil.Key("shift+enter"))
	h.handleKeyPress(testutil.Runes("second"))

	require.NotNil(t, h.textInputOverlay, "the composer must still be open")
	assert.Equal(t, "first\nsecond", h.textInputOverlay.GetValue())
	assert.Equal(t, statePrompt, h.state)
	assert.Empty(t, h.list.GetSelectedInstance().Prompt(), "nothing may be queued by a newline")
}

// TestKeyboardEnhancementsSilenceLeavesTheLatchOff is the negative control the two
// above cannot supply between them: a terminal without the protocol never answers,
// so the latch's default is the value it holds for the whole session. Nothing here
// sends the message, and that absence is the test.
func TestKeyboardEnhancementsSilenceLeavesTheLatchOff(t *testing.T) {
	t.Cleanup(keys.SetTerminalDisambiguates(false))
	h := newCreateFormHome(t)

	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.View()

	assert.False(t, keys.TerminalDisambiguates(),
		"a terminal that never replies must leave Atrium in the pre-protocol behaviour")
}
