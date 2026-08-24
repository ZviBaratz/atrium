package theme

import (
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTokenTableCoversEveryPaletteField is the bidirectional guard that makes
// paletteTokens the palette's vocabulary rather than a second, hand-maintained copy of
// it: every field on Palette is named exactly once, and every entry addresses a
// distinct field.
//
// Reflection in both directions, the shape settings_schema.go's row guard uses. The
// direction that matters is the one a person forgets: adding a nineteenth token to the
// struct and no key to the table would leave it unsettable by any theme file and
// unchecked by the oracle, with nothing failing.
//
// It compares by ADDRESS rather than by name, which is the half a name-based check
// cannot do: two entries could both point at &p.Fg under different names and a
// name-keyed test would call that full coverage.
func TestTokenTableCoversEveryPaletteField(t *testing.T) {
	var p Palette
	base := reflect.ValueOf(&p).Elem()
	typ := base.Type()

	offsets := map[uintptr]string{}
	for i := range typ.NumField() {
		offsets[typ.Field(i).Offset] = typ.Field(i).Name
	}
	require.Len(t, offsets, typ.NumField(), "two Palette fields share an offset")

	seen := map[string]string{} // field name -> token name
	origin := reflect.ValueOf(&p).Pointer()
	for _, tok := range paletteTokens {
		off := reflect.ValueOf(tok.at(&p)).Pointer() - origin
		field, ok := offsets[off]
		require.Truef(t, ok, "token %q does not address a Palette field", tok.name)
		prev, dup := seen[field]
		assert.Falsef(t, dup, "Palette.%s is addressed by both %q and %q", field, prev, tok.name)
		seen[field] = tok.name
	}

	for _, field := range offsets {
		assert.Containsf(t, seen, field,
			"Palette.%s has no on-disk token name, so no theme file can set it and no floor can cover it", field)
	}
}

// TestTokenNamesAreStableAndOnDisk pins the shape of the names themselves. They are a
// user-typed vocabulary — a key in a theme file — so they are lowercase snake_case and,
// like a keybinding action name, may be added to but not renamed.
func TestTokenNamesAreStableAndOnDisk(t *testing.T) {
	names := TokenNames()
	require.Len(t, names, reflect.TypeOf(Palette{}).NumField())
	for _, n := range names {
		assert.Regexpf(t, `^[a-z][a-z_]*$`, n, "%q is not a lowercase snake_case on-disk name", n)
	}
	seen := map[string]bool{}
	for _, n := range names {
		assert.Falsef(t, seen[n], "duplicate token name %q", n)
		seen[n] = true
	}
}

// TestSetToken writes through the table and refuses what is not a token. The refusal
// direction is the load-bearing one: it is what lets ui/theme/themefile name a
// misspelt key back to its author instead of dropping it.
func TestSetToken(t *testing.T) {
	p := Get(DefaultThemeName).Palette
	require.True(t, SetToken(&p, "attention", lipgloss.Color("#ffb454")))
	assert.Equal(t, "#ffb454", Hex(p.Attention))
	assert.Equal(t, Hex(Get(DefaultThemeName).Palette.Fg), Hex(p.Fg), "only the named token moves")

	before := p
	assert.False(t, SetToken(&p, "forground", lipgloss.Color("#ffffff")), "a misspelt key is not a token")
	assert.Equal(t, before, p, "a refused key changes nothing")
	assert.False(t, SetToken(nil, "fg", lipgloss.Color("#ffffff")), "a nil palette is not writable")
}

// TestParseHexRoundTripsHex pins the two halves of the colour seam against each other.
// A user theme file is text on the way in and, for the tmux bar, text on the way out
// again, so a palette that survived the trip only approximately would tint the band
// off by a bit nobody would ever trace back to here.
func TestParseHexRoundTrips(t *testing.T) {
	for _, hex := range []string{"#000000", "#ffffff", "#1a1b26", "#7aa2f7"} {
		assert.Equal(t, hex, Hex(ParseHex(hex)))
	}
	assert.Equal(t, "#aabbcc", Hex(ParseHex("#AABBCC")), "case folds on the way out")
}
