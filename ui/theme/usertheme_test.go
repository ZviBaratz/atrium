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

	// Get consulting the registry first is only half of it. Names() and SelectableNames()
	// take the UNION, so an entry that survived registration would list the name twice
	// and make the picker take two presses to move past it — a shadow that Get's ordering
	// cannot see. SetUserThemes drops it instead, which is what makes "cannot shadow" a
	// property of this package rather than of the loader's check in another one.
	assert.Equal(t, 1, countName(Names(), DefaultThemeName),
		"a colliding entry must be dropped, not merely out-ranked: the union lists it twice")
	assert.Equal(t, 1, countName(SelectableNames(), DefaultThemeName),
		"the settings picker offers SelectableNames verbatim")
}

// countName is how many times a name appears in a list — the question a Contains cannot
// answer, and the one a duplicate registration turns on.
func countName(names []string, want string) int {
	n := 0
	for _, got := range names {
		if got == want {
			n++
		}
	}
	return n
}

// TestSetUserThemesCopiesTheMap: a caller that keeps its own map and mutates it later
// must not reach the registered set. The loader builds its map fresh each time, so this
// is defence for the next caller rather than for that one.
func TestSetUserThemesCopiesTheMap(t *testing.T) {
	const name = "fixture-copied"
	owned := userThemeFixture(name, nil)
	mine := map[string]*Theme{name: owned}
	defer SetUserThemes(mine)()

	delete(mine, name)
	assert.Contains(t, Names(), name, "deleting from the caller's map must not un-register")

	// The copy is SHALLOW, and pinning that here is the point: the *Theme values are
	// shared, so a caller that keeps a theme it handed over and mutates it DOES reach the
	// one Get returns. Not a defect to fix by deep-copying — every caller builds its
	// themes and lets go of them, and Get already hands out the registry's own pointers —
	// but a doc comment claiming the caller "cannot reach the registered one" would be
	// false, and this is what stops it being written again.
	owned.Palette.Bg = lipgloss.Color("#ff0000")
	assert.Equal(t, "#ff0000", Hex(Get(name).Palette.Bg),
		"the map is copied; the themes inside it are not")
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

// TestUserThemeCannotShadowByAnotherSpelling is the half of "cannot shadow" that the
// collision drop got wrong at first: it indexed the built-in registry with the caller's
// RAW key while Get normalises (ToLower+TrimSpace), so the drop was a spelling test
// rather than a name test.
//
// Probed against the real package before it was fixed: SetUserThemes({"Tokyo-Night": th})
// registered, Names and SelectableNames offered "Tokyo-Night", the picker let you select
// and persist it — and Get lowercased it straight back onto the built-in, leaving a row
// that is reachable, saved, and does nothing. Nothing in production reached it, because
// themefile's nameRE demands lowercase — which is to say the property was being held up
// by the loader-side policy in another package that dropping the entry here was written
// to replace.
func TestUserThemeCannotShadowByAnotherSpelling(t *testing.T) {
	for _, spelling := range []string{"Tokyo-Night", "TOKYO-NIGHT", "  tokyo-night  "} {
		t.Run(spelling, func(t *testing.T) {
			imposter := userThemeFixture(spelling, func(p *Palette) { p.Bg = lipgloss.Color("#ff0000") })
			defer SetUserThemes(map[string]*Theme{spelling: imposter})()

			assert.NotContains(t, SelectableNames(), spelling,
				"the picker must not offer a second spelling of a built-in")
			assert.Equal(t, "#1a1b26", Hex(Get(spelling).Palette.Bg),
				"and it must not be reachable through Get either")
			assert.Equal(t, 1, countName(Names(), DefaultThemeName))
		})
	}
}

// TestUserThemeCannotClaimAuto. `auto` is not in the built-in registry at all — compose()
// special-cases it, because Get must return a concrete palette and `auto` has none — so
// the collision drop needs its own arm for it and does not get one for free.
//
// SelectableNames puts `auto` at the front unconditionally, so an entry that survived
// registration would make the picker list it twice: the row would take two presses to
// leave, which is the exact defect TestUserThemeCannotShadowABuiltin exists to prevent
// one map over.
func TestUserThemeCannotClaimAuto(t *testing.T) {
	defer SetUserThemes(map[string]*Theme{AutoThemeName: userThemeFixture(AutoThemeName, nil)})()

	assert.Equal(t, 1, countName(SelectableNames(), AutoThemeName),
		"the picker lists `auto` once; a user entry under that name doubles it")
	assert.NotContains(t, Names(), AutoThemeName,
		"`auto` is composed, not registered; it must not appear in the registered set")
}
