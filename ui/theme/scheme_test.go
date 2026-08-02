package theme

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func boolp(b bool) *bool { return &b }

// ResolveScheme is the detection ladder as a pure function, so every rung and
// every failure is testable with no terminal involved. The rungs, highest first:
// an OSC 11 answer, then COLORFGBG, then unknown.
//
// The no-answer cases lead deliberately. Latching is the safety property the rest
// of the ladder rests on — "nothing replied" must resolve to SchemeUnknown so the
// caller can leave the scheme alone, never to a default that would flip a correctly
// detected terminal back on the first silent query.
//
// COLORFGBG sits BELOW OSC 11 and can never correct it: the variable is
// stale-prone — it survives into child processes after the terminal's theme
// changes — so it is a hint used only in the absence of an answer, never a
// correction to one.
func TestResolveScheme(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bgIsDark  *bool
		colorfgbg string
		want      Scheme
	}{
		{"nothing at all", nil, "", SchemeUnknown},
		{"COLORFGBG default background is no answer", nil, "15;default", SchemeUnknown},
		{"COLORFGBG malformed is no answer", nil, "nonsense", SchemeUnknown},
		{"COLORFGBG out of range is no answer", nil, "0;99", SchemeUnknown},
		{"osc11 says dark", boolp(true), "", SchemeDark},
		{"osc11 says light", boolp(false), "", SchemeLight},
		{"osc11 outranks a disagreeing COLORFGBG", boolp(true), "0;15", SchemeDark},
		{"osc11 outranks an agreeing COLORFGBG", boolp(false), "0;15", SchemeLight},
		{"COLORFGBG light background", nil, "0;15", SchemeLight},
		{"COLORFGBG dark background", nil, "15;0", SchemeDark},
		{"COLORFGBG three fields uses the last", nil, "0;7;15", SchemeLight},
		{"COLORFGBG index 7 is light", nil, "0;7", SchemeLight},
		{"COLORFGBG index 8 is dark", nil, "15;8", SchemeDark},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ResolveScheme(tc.bgIsDark, tc.colorfgbg))
		})
	}
}

// AC#5, as a statement about the code path: `auto` with NO detection resolves to
// exactly the default theme. This is the palette-level half; the frame-level half
// is that app/testdata/colours.txt stays byte-identical, asserted in app.
func TestAutoWithoutDetectionIsTheDefaultTheme(t *testing.T) {
	defer SetScheme(SchemeUnknown)()
	defer Set(AutoThemeName)()

	require.Equal(t, Get(DefaultThemeName).Palette, Current().Palette,
		"auto with no detection must be byte-for-byte the shipped default")
}

// SchemeUnknown is the zero value on purpose, so a process that never runs
// detection at all renders the shipped dark default. Asserted against the constant
// rather than against curScheme's initializer, because the initializer is exactly
// what a future edit could change without noticing.
func TestSchemeUnknownIsTheZeroValue(t *testing.T) {
	var zero Scheme
	require.Equal(t, SchemeUnknown, zero,
		"absence of detection must be the zero value, or a never-detected process picks a palette")
}

// A detected light terminal under `auto` selects the default family's light twin.
func TestAutoWithLightSchemeSelectsTheTwin(t *testing.T) {
	defer SetScheme(SchemeLight)()
	defer Set(AutoThemeName)()

	require.Equal(t, Get(lightTwin[DefaultThemeName]).Palette, Current().Palette)
	require.True(t, IsLight(Current().Palette))
}

// A detected DARK terminal under `auto` is the default, not the twin. Without this
// the light branch could be unconditional and TestAutoWithoutDetectionIsTheDefaultTheme
// would still pass, since it only ever exercises SchemeUnknown.
func TestAutoWithDarkSchemeSelectsTheDefault(t *testing.T) {
	defer SetScheme(SchemeDark)()
	defer Set(AutoThemeName)()

	require.Equal(t, Get(DefaultThemeName).Palette, Current().Palette)
	require.False(t, IsLight(Current().Palette))
}

// AC#4, structurally rather than by convention: only `auto` reads the scheme
// axis, so an explicitly named theme cannot be switched by detection. Asserted
// for EVERY registered name, not a sample — a theme added later is covered.
func TestNamedThemesNeverFollowTheScheme(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			restoreName := Set(name)
			defer restoreName()

			restoreDark := SetScheme(SchemeDark)
			dark := Current().Palette
			restoreDark()

			restoreLight := SetScheme(SchemeLight)
			light := Current().Palette
			restoreLight()

			require.Equal(t, dark, light,
				"an explicitly named theme must render identically under either scheme")
		})
	}
}

// SetScheme must restore, like Set and SetGlyphSet — and it must restore the
// scheme WITHOUT clobbering the palette name, since the two axes are independent.
func TestSetSchemeRestoresWithoutTouchingTheName(t *testing.T) {
	defer Set("catppuccin-mocha")()

	restore := SetScheme(SchemeLight)
	require.Equal(t, SchemeLight, CurrentScheme())
	require.Equal(t, "catppuccin-mocha", Current().Name)
	restore()
	require.Equal(t, SchemeUnknown, CurrentScheme())
	require.Equal(t, "catppuccin-mocha", Current().Name)
}

// The mirror of TestSetSchemeRestoresWithoutTouchingTheName: Set and SetGlyphSet
// restore their own two axes, and a scheme recorded by detection must survive
// them. Three restores each undoing three axes is how one starts clobbering a
// sibling, so the independence is asserted from both sides.
func TestSetAndSetGlyphSetLeaveTheSchemeAlone(t *testing.T) {
	defer SetScheme(SchemeLight)()

	restoreName := Set("catppuccin-mocha")
	require.Equal(t, SchemeLight, CurrentScheme(), "Set must not touch the scheme axis")
	restoreName()
	require.Equal(t, SchemeLight, CurrentScheme(), "Set's restore must not touch the scheme axis")

	restoreGlyphs := SetGlyphSet(GlyphSetASCII)
	require.Equal(t, SchemeLight, CurrentScheme(), "SetGlyphSet must not touch the scheme axis")
	restoreGlyphs()
	require.Equal(t, SchemeLight, CurrentScheme(), "SetGlyphSet's restore must not touch the scheme axis")
}

// CurrentScheme sits beside Current(), which promises any goroutine, so it makes
// the same promise. Nothing reads it off the loop today — barStyleColours reaches
// Current() and Mono() and never this — but an exported getter whose safety rests
// on an invariant of its CALLERS is one feature away from being false, which is
// the transition `mono` already went through. Fails under -race the moment
// curScheme goes back to a plain int.
func TestCurrentScheme_IsSafeToReadOffTheLoop(t *testing.T) {
	// Seeded before the readers start, so "never SchemeUnknown" is a true statement
	// about every value a reader can legally observe rather than a race with the
	// writer's first store.
	t.Cleanup(SetScheme(SchemeDark))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the loop: the only writer
		defer wg.Done()
		for range 50 {
			SetScheme(SchemeDark)
			SetScheme(SchemeLight)
		}
	}()
	for range 4 { // readers on their own goroutines
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				require.NotEqual(t, SchemeUnknown, CurrentScheme(),
					"a reader must never observe a scheme the writer never set")
			}
		}()
	}
	wg.Wait()
}

// SelectableNames is what the settings picker offers: `auto` plus every registered
// theme. It lives here rather than in the overlay so theme vocabulary has one
// home — and it is guarded in both directions so the list cannot drift from the
// registry.
func TestSelectableNames(t *testing.T) {
	got := SelectableNames()
	require.Equal(t, AutoThemeName, got[0], "auto leads: it is the recommended value")
	require.Len(t, got, len(Names())+1)

	for _, n := range Names() {
		require.Contains(t, got, n)
	}
	for _, n := range got {
		if n == AutoThemeName {
			continue
		}
		require.NotNil(t, Get(n))
		require.Equalf(t, n, Get(n).Name,
			"%q is offered by the picker but Get falls back for it — a dead option", n)
	}
}

// AutoThemeName must NOT be a registry entry. Get has to return a concrete
// eighteen-token palette and `auto` has none, so an entry would hold a fiction the
// canonical-hex and contrast oracles would then dutifully validate.
func TestAutoIsNotARegistryEntry(t *testing.T) {
	require.NotContains(t, Names(), AutoThemeName)
	require.Equal(t, Get(DefaultThemeName), Get(AutoThemeName),
		"Get must fall back for auto like any other unknown name")
}
