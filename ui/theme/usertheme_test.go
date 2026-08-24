package theme

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userThemeFixture builds a registerable user palette from a built-in, so these tests
// exercise the same shape ui/theme/themefile produces without importing it (that would
// be a cycle — the loader depends on this package, never the reverse).
func userThemeFixture(name string, tweak func(*Palette)) *Theme {
	th := *Get(DefaultThemeName)
	th.Name = name
	if tweak != nil {
		tweak(&th.Palette)
	}
	return &th
}

// TestSetUserThemesRegistersAndRestores covers the whole registration contract in the
// order a launch exercises it: an unknown name falls back before the swap, resolves
// after it, and falls back again once the restore runs. The restore half is why these
// tests can share a package with every sweep that iterates Names().
func TestSetUserThemesRegistersAndRestores(t *testing.T) {
	const name = "fixture-midnight"
	require.Equal(t, Get(DefaultThemeName), Get(name), "precondition: not registered yet")

	restore := SetUserThemes(map[string]*Theme{name: userThemeFixture(name, nil)})
	got := Get(name)
	assert.Equal(t, name, got.Name, "a registered user theme resolves to itself, not the fallback")
	assert.Contains(t, Names(), name)
	assert.Contains(t, SelectableNames(), name, "a loaded palette the picker cannot offer is a file that did nothing")

	restore()
	assert.NotContains(t, Names(), name)
	assert.Equal(t, Get(DefaultThemeName), Get(name), "the restore un-registers it")
}

// TestUserThemeCannotShadowABuiltin is the invariant the loader's "that name is taken"
// refusal rests on. If a user map could shadow tokyo-night, the refusal would be a
// policy one line of code could contradict; because Get consults the built-ins first,
// it is structural.
func TestUserThemeCannotShadowABuiltin(t *testing.T) {
	imposter := userThemeFixture(DefaultThemeName, func(p *Palette) { p.Bg = lipgloss.Color("#ff0000") })
	defer SetUserThemes(map[string]*Theme{DefaultThemeName: imposter})()

	assert.Equal(t, "#1a1b26", Hex(Get(DefaultThemeName).Palette.Bg),
		"a user entry under a built-in name must not be reachable through Get")
}

// TestSetUserThemesCopiesTheMap: a caller that keeps its own map and mutates it later
// must not reach the registered set. The loader builds its map fresh each time, so this
// is defence for the next caller rather than for that one.
func TestSetUserThemesCopiesTheMap(t *testing.T) {
	const name = "fixture-copied"
	mine := map[string]*Theme{name: userThemeFixture(name, nil)}
	defer SetUserThemes(mine)()

	delete(mine, name)
	assert.Contains(t, Names(), name, "deleting from the caller's map must not un-register")
}

// TestUserPaletteDrivesIsLightAndTheBrandTable proves a user palette is a first-class
// one rather than a colour table bolted on: IsLight reads its Bg like any other, so the
// agent identity glyphs pick the light-background brand accents for a light user theme
// without anyone teaching them about user themes.
//
// The values are asserted through AgentGlyph, which is what renders, rather than
// through the two brand maps — the distinction TestAgentBrandColoursStayLegible makes
// for the same reason.
func TestUserPaletteDrivesIsLightAndTheBrandTable(t *testing.T) {
	const dark, light = "fixture-dark", "fixture-light"
	darkTheme := userThemeFixture(dark, nil)
	lightTheme := userThemeFixture(light, func(p *Palette) {
		*p = Get("tokyo-night-day").Palette
	})
	defer SetUserThemes(map[string]*Theme{dark: darkTheme, light: lightTheme})()

	require.False(t, IsLight(Get(dark).Palette))
	require.True(t, IsLight(Get(light).Palette))

	keys := Get(dark).AgentKeys()
	require.NotEmpty(t, keys)
	differ := false
	for _, key := range keys {
		_, dc := Get(dark).AgentGlyph(key)
		_, lc := Get(light).AgentGlyph(key)
		if Hex(dc) != Hex(lc) {
			differ = true
		}
	}
	assert.True(t, differ,
		"a light user palette must reach the light brand-accent table; no agent glyph changed colour")
}
