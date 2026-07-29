package testutil

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// TestKeySpecsRoundTripThroughString is the guard that makes the spec vocabulary
// mean something. Atrium dispatches keys by string —
// keys.GlobalKeyStringsMap[msg.String()] — so a helper that builds a message whose
// String() is not the spec asked for builds a keystroke no production code path
// can match, and every test using it would pass or fail for the wrong reason.
//
// Asserting the round trip rather than the field values is deliberate: the field
// encoding is exactly what Bubble Tea v2 changes, and this assertion survives that
// change unaltered.
func TestKeySpecsRoundTripThroughString(t *testing.T) {
	for spec := range specialKeys {
		require.Equal(t, spec, Key(spec).String(),
			"Key(%q) must stringify back to %q", spec, spec)
	}
}

// TestKeyBuildsPrintableCharactersAsText covers the other arm: a single printable
// character is text, not a named key.
func TestKeyBuildsPrintableCharactersAsText(t *testing.T) {
	for _, s := range []string{"j", "?", "/", "1", "→"} {
		msg := Key(s)
		require.Equal(t, s, msg.Text, "Key(%q) must be text", s)
		require.Equal(t, s, msg.String())
	}
}

// TestKeyAppliesTheAltModifier pins the one modifier v1 carries as a bool rather
// than as its own key type — the field v2 folds into a Mod bitmask.
func TestKeyAppliesTheAltModifier(t *testing.T) {
	msg := Key("alt+enter")
	require.True(t, msg.Mod.Contains(tea.ModAlt))
	require.Equal(t, tea.KeyEnter, msg.Code)
	require.Equal(t, "alt+enter", msg.String())
}

// TestKeyPanicsOnAnUnknownSpec pins the deliberate loudness: a typo'd keystroke
// must not degrade into a zero-valued message that quietly matches nothing.
func TestKeyPanicsOnAnUnknownSpec(t *testing.T) {
	require.PanicsWithValue(t,
		`testutil.Key: unknown key spec "nope" — add it to specialKeys, or use Runes for literal text`,
		func() { Key("ctrl+shift+nope") })
}

// TestRunesTakesTextLiterally is the escape hatch's whole point: text that spells a
// key name must stay text.
func TestRunesTakesTextLiterally(t *testing.T) {
	msg := Runes("enter")
	require.Equal(t, "enter", msg.Text)
	require.NotEqual(t, Key("enter"), msg, "Runes must not build the return key")
}

// TestSpaceAcceptsBothSpellings pins the alias that exists for #393: Bubble Tea v1
// calls the space bar " " and v2 calls it "space". A call site may use either, so
// the rename lands in one place at the cut instead of in every test that marks a
// session.
func TestSpaceAcceptsBothSpellings(t *testing.T) {
	require.Equal(t, Key(" "), Key("space"))
	require.Equal(t, tea.KeySpace, Key("space").Code,
		`Key("space") must build the space bar, not the letters`)
}
