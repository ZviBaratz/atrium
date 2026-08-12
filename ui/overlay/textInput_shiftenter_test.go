package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shift+Enter is the newline key the composer footers advertise, and until #396 it
// was the one that did not work: Bubble Tea v2 asks every terminal to disambiguate
// modified keys, so on a terminal that obliges the keystroke arrives as
// "shift+enter" — matching no case, falling through to the textarea, and inserting
// its empty Text. That was not even a clean no-op; bubbles' insert path still ran to
// SetCursorColumn, which zeroes the sticky column CursorUp/CursorDown navigate by.
//
// These tests drive the string a disambiguating terminal actually sends. On any
// other terminal Shift+Enter is byte-identical to Enter and none of this is
// reachable, which is exactly why nothing here is gated on the capability latch —
// the handler is unconditional and only the FOOTER is honest about where it works
// (see textInput_hints_test.go).

// promptFocusedForm returns a create form with the cursor on the prompt textarea,
// which is the only create-form stop where a newline means anything.
func promptFocusedForm(t *testing.T) *TextInputOverlay {
	t.Helper()
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	tab(o) // title → prompt
	require.True(t, o.isTextarea())
	return o
}

// TestShiftEnterInsertsNewlineInEveryComposer covers all three live roles at once,
// because they share one HandleKeyPress: a fix that reached only the create form
// would still leave the quick-send footer — the one that names ⇧↵ most prominently
// — lying.
func TestShiftEnterInsertsNewlineInEveryComposer(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T) *TextInputOverlay
	}{
		{"create form prompt", promptFocusedForm},
		{"quick send", func(*testing.T) *TextInputOverlay { return NewQuickSendOverlay("Send to foo") }},
		{"smart dispatch", func(*testing.T) *TextInputOverlay { return NewSmartDispatchOverlay("What next?") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.make(t)
			o.HandleKeyPress(textMsg("line one"))

			shouldClose, _ := o.HandleKeyPress(keyMsg("shift+enter"))

			assert.False(t, shouldClose, "Shift+Enter must not close the overlay")
			assert.False(t, o.IsSubmitted(), "Shift+Enter must not submit")
			assert.True(t, o.isTextarea(), "Shift+Enter must leave the cursor in the textarea")

			o.HandleKeyPress(textMsg("line two"))
			assert.Equal(t, "line one\nline two", o.GetValue(),
				"Shift+Enter must insert a newline, not vanish")
		})
	}
}

// TestShiftEnterNeverSubmitsOrNavigates is the negative control the test above
// cannot supply, and it is what makes the dispatch SHAPE falsifiable.
//
// The tempting implementation is to add "shift+enter" to the existing
// `case "enter", "alt+enter"`. That case submits from the Create button and from a
// filled title before it reaches the textarea, so the tempting version turns a
// reach for a newline into a created session — and every assertion in the test
// above still passes, because none of them is on a non-textarea stop. These are.
func TestShiftEnterNeverSubmitsOrNavigates(t *testing.T) {
	t.Run("filled title", func(t *testing.T) {
		o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
		o.FocusTitle()
		o.HandleKeyPress(textMsg("my-task"))

		shouldClose, _ := o.HandleKeyPress(keyMsg("shift+enter"))

		assert.False(t, shouldClose)
		assert.False(t, o.IsSubmitted(), "Shift+Enter on a filled title must not create the session")
		assert.True(t, o.isTitle(), "and must not advance off the title either")
		assert.Equal(t, "my-task", o.GetTitle(), "the title text is untouched")
	})

	t.Run("create button", func(t *testing.T) {
		o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
		o.FocusTitle()
		o.HandleKeyPress(textMsg("my-task"))
		o.focusStop(stopEnter)
		require.True(t, o.isEnterButton())

		shouldClose, _ := o.HandleKeyPress(keyMsg("shift+enter"))

		assert.False(t, shouldClose)
		assert.False(t, o.IsSubmitted(), "Shift+Enter on the Create button must not submit")
	})

	t.Run("branch filter", func(t *testing.T) {
		o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
		o.focusStop(stopBranch)
		require.True(t, o.isBranchPicker())

		shouldClose, filterChanged := o.HandleKeyPress(keyMsg("shift+enter"))

		assert.False(t, shouldClose)
		assert.False(t, filterChanged,
			"Shift+Enter must not report a filter edit, which would schedule a branch search")
	})
}

// TestAltEnterOnTheCreateButtonStillSubmits is CHARACTERIZATION, not contract.
//
// Alt+Enter shares the "enter" case label, so on the Create button it submits —
// an inheritance, not a decision: Alt+Enter predates key disambiguation as the
// Shift+Enter stand-in and no footer has ever named it on the button. It is pinned
// here so that a later consistency pass which wants to align the two newline keys
// has to argue with a named test rather than discover the change in review.
func TestAltEnterOnTheCreateButtonStillSubmits(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	o.HandleKeyPress(textMsg("my-task"))
	o.focusStop(stopEnter)
	require.True(t, o.isEnterButton())

	shouldClose, _ := o.HandleKeyPress(keyMsg("alt+enter"))

	assert.True(t, shouldClose)
	assert.True(t, o.IsSubmitted())
}
