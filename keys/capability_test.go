package keys

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerminalDisambiguates_DefaultsToOffAndRestores pins the two properties the
// latch's callers rely on and neither of which the type gives for free.
//
// The default is the load-bearing one. A terminal without the protocol never
// answers the query, so nothing ever calls the setter for it — the value it is
// born with is the value it dies with, for the majority of terminals. Defaulting
// to true would make every one of them advertise a key it cannot send, which is
// the whole defect (#396) with the sign flipped.
//
// The restore half is what keeps a test that flips it from teaching the rest of
// the suite that its terminal disambiguates.
func TestTerminalDisambiguates_DefaultsToOffAndRestores(t *testing.T) {
	require.False(t, TerminalDisambiguates(),
		"the zero value must mean 'no answer yet', because for most terminals there never is one")

	restore := SetTerminalDisambiguates(true)
	assert.True(t, TerminalDisambiguates(), "the setter must be visible to the getter")

	restore()
	assert.False(t, TerminalDisambiguates(), "restore must put back what was there")

	// Restoring a value that was already set is the case a naive restore (one that
	// stores false rather than the previous value) gets wrong, and the suite would
	// otherwise never exercise it.
	outer := SetTerminalDisambiguates(true)
	inner := SetTerminalDisambiguates(false)
	inner()
	assert.True(t, TerminalDisambiguates(), "restore returns the PREVIOUS value, not false")
	outer()
	assert.False(t, TerminalDisambiguates())
}

// TestRegistry_NoDefaultBindingNeedsDisambiguation is #396's audit acceptance
// criterion — "no action is bound release-only or kitty-only" — in the only form
// that can be checked mechanically.
//
// Release-only is unreachable by construction: Atrium never sets
// tea.View.KeyboardEnhancements, so ReportEventTypes is never requested and a
// KeyReleaseMsg cannot arrive (pinned in app by
// TestView_RequestsNoKeyboardEnhancements). What remains is kitty-only, and this
// package already knows exactly which keys those are — the four ctrl chords whose
// control code is also an ordinary key's, which only a disambiguating terminal can
// separate. Reusing wireAmbiguousCtrlChord rather than restating the list is what
// keeps this from becoming a second source of truth that drifts from the one the
// override validator warns against.
//
// A default binding on one of them would be dead on every terminal that never
// answers the query, while the cheatsheet went on printing the key — which is the
// exact failure the override layer refuses to let a USER walk into, so shipping it
// ourselves would be worse.
func TestRegistry_NoDefaultBindingNeedsDisambiguation(t *testing.T) {
	for _, e := range Registry {
		for _, k := range e.Binding.Keys() {
			other, ambiguous := wireAmbiguousCtrlChord(k)
			// KeyName is an int with no String method, so the entry is named by the
			// description help already renders for it — the phrasing a user would
			// recognise from the cheatsheet row that would go dead.
			assert.Falsef(t, ambiguous,
				"%q (%s) is bound to %q, which a terminal without key disambiguation sends as %s — "+
					"the action would be dead there while help still named the key",
				e.Binding.Help().Desc, e.Action, k, other)
		}
	}
}

// TestWireAmbiguousCtrlChord_DetectsTheChords is the positive control for the
// audit above: without it, a wireAmbiguousCtrlChord that always answered false
// would make that test pass over any registry at all.
func TestWireAmbiguousCtrlChord_DetectsTheChords(t *testing.T) {
	for chord, want := range map[string]string{
		"ctrl+m": "enter",
		"ctrl+i": "tab",
		"ctrl+j": "a newline",
		"ctrl+h": "backspace",
	} {
		other, ambiguous := wireAmbiguousCtrlChord(chord)
		assert.Truef(t, ambiguous, "%q is wire-ambiguous", chord)
		assert.Equal(t, want, other)
	}
}
