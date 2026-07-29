package app

import (
	"github.com/ZviBaratz/atrium/keys"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// Bubble Tea v1 delivered a bracketed paste as an ordinary key message carrying
// the pasted text, so it reached whatever had focus through the normal dispatch.
// v2 gives paste its own message type, which took it off that path entirely —
// pasting into the new-session form silently did nothing, and nothing failed to
// compile because nothing had ever named v1's Paste flag. These pin the routing
// that puts it back.
//
// They assert through home.Update rather than any conversion helper because the
// defect was that no case in Update matched a paste at all — and because routing,
// not formatting, is where the second defect class lives too: see
// paste_safety_test.go for the commands a paste must never be mistaken for.

// focusPromptField tabs to the multi-line prompt, which is where the reported
// paste failed. PromptFocusedAndEmpty is true only on the textarea stop, so it
// doubles as the "are we there yet" probe while the field is still empty.
func focusPromptField(t *testing.T, h *home) {
	t.Helper()
	for i := 0; i < 12; i++ {
		if h.textInputOverlay.PromptFocusedAndEmpty() {
			return
		}
		h.textInputOverlay.HandleKeyPress(keyMsg("tab"))
	}
	t.Fatal("could not focus the prompt field")
}

// TestPasteReachesTheCreateFormPrompt is the reported regression: text pasted
// into the new-session dialog must land in the prompt field.
func TestPasteReachesTheCreateFormPrompt(t *testing.T) {
	h := newCreateFormHome(t)
	h.newSessionPath = t.TempDir()
	h.textInputOverlay, _ = h.newSessionFormOverlay()
	// Size first, then set the state: this home starts on a fresh config.State, so
	// the resize opens the first-run welcome modal, and a state assigned before it
	// is overwritten — the modal would then swallow the paste and the test would
	// "fail" for a reason that has nothing to do with paste.
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = statePrompt
	focusPromptField(t, h)

	const pasted = "review the auth refactor and flag anything risky"
	h.Update(tea.PasteMsg{Content: pasted})

	require.Contains(t, h.textInputOverlay.GetValue(), pasted,
		"a paste must reach the focused prompt field")
}

// TestPasteAppendsToTheSessionFilter covers the other kind of text surface: a
// buffer Atrium owns itself, rather than a bubbles model. Both are fed by the
// same dispatch, which is why routing paste back through it fixes them together.
func TestPasteAppendsToTheSessionFilter(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateFilter
	h.list.SetFilterActive(true)
	h.list.SetFilter("re")

	h.Update(tea.PasteMsg{Content: "factor"})

	require.Equal(t, "refactor", h.list.FilterQuery(),
		"a paste must extend the filter query the way typing does")
}

// TestPasteIsInertWhereNoTextCanLand pins the other half of the routing: paste is
// enumerated per state, so a state with no text surface must ignore it outright
// rather than fall back to interpreting the characters as keys.
//
// The bound single characters are the ones that matter. A word like "hello" is
// safe by accident — no binding is five characters long — so a test that only
// pasted prose would pass against a dispatch that happily quit on "q".
func TestPasteIsInertWhereNoTextCanLand(t *testing.T) {
	for name, content := range map[string]string{
		"a bound character":   "q",
		"another bound one":   "n",
		"a bound punctuation": "?",
		"ordinary prose":      "hello",
	} {
		t.Run(name, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.state = stateDefault

			_, cmd := h.Update(tea.PasteMsg{Content: content})

			require.Nil(t, cmd, "a paste in the list must produce no command")
			require.Equal(t, stateDefault, h.state, "a paste in the list must not change state")
			// Every one of these is a live binding: the assertion above is only
			// meaningful because dispatch would otherwise have had something to find.
			if content != "hello" {
				_, bound := keys.GlobalKeyStringsMap[content]
				require.True(t, bound, "%q must be a real binding for this test to bite", content)
			}
		})
	}
}

// TestPasteOfMultiLineTextKeepsEveryLine guards the case most easily mangled in
// transit: the overlay hands the real tea.PasteMsg to the bubbles textarea, whose
// native paste case inserts it through insertRunesFromUserInput, so newlines must
// survive rather than being swallowed or collapsing the field.
func TestPasteOfMultiLineTextKeepsEveryLine(t *testing.T) {
	h := newCreateFormHome(t)
	h.newSessionPath = t.TempDir()
	h.textInputOverlay, _ = h.newSessionFormOverlay()
	// Size first, then set the state: this home starts on a fresh config.State, so
	// the resize opens the first-run welcome modal, and a state assigned before it
	// is overwritten — the modal would then swallow the paste and the test would
	// "fail" for a reason that has nothing to do with paste.
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = statePrompt
	focusPromptField(t, h)

	h.Update(tea.PasteMsg{Content: "first line\nsecond line"})

	got := h.textInputOverlay.GetValue()
	require.Contains(t, got, "first line")
	require.Contains(t, got, "second line")
	require.Equal(t, 2, len(strings.Split(strings.TrimRight(got, "\n"), "\n")),
		"a two-line paste must stay two lines: %q", got)
}
