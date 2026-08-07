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
// checks it through needsDisambiguation — the same predicate the override
// validator warns a USER with, rather than a second list that could drift from it.
//
// A default binding on one of those would be dead on every terminal that never
// answers the query, while the cheatsheet went on printing the key — the exact
// failure the override layer refuses to let a user walk into, so shipping it
// ourselves would be worse.
//
// This is a FLOOR, and saying so is the point: needsDisambiguation knows the
// chords Atrium has had reason to name, not every keystroke a legacy terminal
// cannot encode. A green run here means "no binding is on a known-ambiguous
// chord", never "every binding is reachable everywhere". Widening it is a matter
// of teaching that predicate, which is also what makes the user-facing warning
// wider at the same time.
func TestRegistry_NoDefaultBindingNeedsDisambiguation(t *testing.T) {
	for _, e := range Registry {
		for _, k := range e.Binding.Keys() {
			other, ambiguous := needsDisambiguation(k)
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

// TestNeedsDisambiguation_KnowsTheAmbiguousChords is the positive control for the
// audit above: without it, a predicate that always answered false would make that
// test pass over any registry at all.
//
// The negative half carries as much weight as the positive one, because the
// tempting generalisation — "a modifier over a special key cannot be encoded" —
// is false for every entry in it. shift+tab is CSI Z and shift+up is CSI 1;2A, both
// legacy sequences; alt+enter is ESC CR, which is the whole reason it works as the
// portable stand-in. Flagging those would warn users off keys Atrium itself binds.
func TestNeedsDisambiguation_KnowsTheAmbiguousChords(t *testing.T) {
	t.Run("collapses onto an ordinary key", func(t *testing.T) {
		for chord, want := range map[string]string{
			"ctrl+m":      "enter",
			"ctrl+i":      "tab",
			"ctrl+j":      "a newline",
			"ctrl+h":      "backspace",
			"shift+enter": "enter",
			"ctrl+enter":  "enter",
		} {
			other, ambiguous := needsDisambiguation(chord)
			assert.Truef(t, ambiguous, "%q reaches Atrium only with disambiguation", chord)
			assert.Equal(t, want, other)
		}
	})

	t.Run("has a legacy encoding", func(t *testing.T) {
		for _, chord := range []string{
			"alt+enter",  // ESC CR
			"shift+tab",  // CSI Z
			"shift+up",   // CSI 1;2A
			"enter",      // CR
			"ctrl+s",     // DC3
			"ctrl+space", // NUL
		} {
			_, ambiguous := needsDisambiguation(chord)
			assert.Falsef(t, ambiguous, "%q arrives on a legacy terminal too", chord)
		}
	})

	// The floor, stated as a test so it cannot be mistaken for completeness.
	// ctrl+shift+e has no legacy encoding either, so it IS kitty-only — the
	// predicate simply does not know that, and answers false. Anyone widening
	// needsDisambiguation should move a case up from here rather than discover
	// that the negative answer was load-bearing somewhere.
	t.Run("ambiguous but not known to be", func(t *testing.T) {
		_, ambiguous := needsDisambiguation("ctrl+shift+e")
		assert.False(t, ambiguous,
			"a false answer means 'not on the known list', never 'safe everywhere'")
	})
}

// A user override onto a kitty-only chord is warned about, not silently accepted —
// the same treatment ctrl+m gets, and the reason the README can tell a reader which
// keys to avoid. Without this, #396 would have put shift+enter in readers' heads
// while leaving it the one ambiguous chord the validator said nothing about.
func TestValidate_WarnsOnAShiftEnterOverride(t *testing.T) {
	problems := Validate(map[string]Spec{"filter": {Keys: []string{"shift+enter"}}})

	require.Len(t, problems, 1)
	assert.True(t, problems[0].Warning, "the binding is applied; it just will not fire everywhere")
	assert.Contains(t, problems[0].Error(), "shift+enter")
	assert.Contains(t, problems[0].Error(), "does not disambiguate")
}
