package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/splash"

	"github.com/stretchr/testify/require"
)

// TestParseSplashEnvVariant covers the dev override's parse, which had no test at
// all until it was split out of the sync.OnceValues closure it resolves in — the
// closure memoizes process-wide and TestMain pins the variable, which defeats
// even a subprocess probe.
//
// The fall-through is the interesting half. It answers an unrecognized value with
// ok=true, so ok means "an override was set", not "it was understood": a typo, a
// retired name and a deleted letter all resolve to splashDefaultVariant rather
// than falling back to the rotation, which keeps a mispinned suite deterministic
// instead of flaky. That is also why TestMain pins a variant that is not the
// fallback — a pin naming rain would be indistinguishable from a pin this never
// understood.
func TestParseSplashEnvVariant(t *testing.T) {
	// Unset is the only case that reports "no override".
	v, ok := parseSplashEnvVariant("")
	require.False(t, ok, "an unset override must not claim to be one")
	require.Equal(t, splashDefaultVariant, v)

	for s, want := range map[string]splash.Variant{
		"rain": splash.Rain, "tunnel": splash.Tunnel, "ripple": splash.Ripple,
		// The historical dev letters. They are kept as they are — f/g/h is what the
		// screenshot recipes and the notes use — so a–e are simply gone with the
		// fields they named, and i is next.
		"f": splash.Rain, "g": splash.Tunnel, "h": splash.Ripple,
	} {
		v, ok := parseSplashEnvVariant(s)
		require.Truef(t, ok, "%q must set an override", s)
		require.Equalf(t, want, v, "%q", s)
	}

	// Junk, a retired name and a retired letter are indistinguishable *here*, by
	// design: all of them mean "an override was set" and resolve to the fallback.
	for _, s := range []string{"banana", "nebula", "plasma", "a", "e", "legacy", "Rain", " rain"} {
		v, ok := parseSplashEnvVariant(s)
		require.Truef(t, ok, "%q must report an override was set", s)
		require.Equalf(t, splashDefaultVariant, v, "%q must fall through to the fallback", s)
	}
}

// TestLookupSplashVariantKnowsOnlyWhatItShips is the half of the parse the
// fall-through cannot express, and it is the only place the letters are really
// pinned.
//
// The test above cannot do it. parseSplashEnvVariant answers an unknown string
// with (splashDefaultVariant, true), and the fallback is rain — so "f" and
// "banana" return exactly the same pair, and deleting case "f" leaves that test
// green while breaking the letter every screenshot recipe in the notes uses. The
// vocabulary needs a boundary that distinguishes "known" from "resolved to the
// fallback", which is what lookupSplashVariant is for.
func TestLookupSplashVariantKnowsOnlyWhatItShips(t *testing.T) {
	for s, want := range map[string]splash.Variant{
		"rain": splash.Rain, "tunnel": splash.Tunnel, "ripple": splash.Ripple,
		"f": splash.Rain, "g": splash.Tunnel, "h": splash.Ripple,
	} {
		v, ok := lookupSplashVariant(s)
		require.Truef(t, ok, "%q is a name this build ships", s)
		require.Equalf(t, want, v, "%q", s)
	}
	// Retired names and letters are not known — including the ones that would
	// resolve to the fallback anyway, which is exactly what the layer above hides.
	for _, s := range []string{"banana", "", "nebula", "braille", "contours", "julia",
		"mandala", "plasma", "legacy", "a", "b", "c", "d", "e", "i", "Rain", " rain"} {
		_, ok := lookupSplashVariant(s)
		require.Falsef(t, ok, "%q must not be a known name", s)
	}
}

// TestParseSplashLumRangeRejectsNonFinite guards a real cross-arch hazard rather
// than input hygiene.
//
// strconv.ParseFloat accepts "nan" and "+Inf". A NaN would pass *both* of
// splashShade's endpoint guards — every comparison against NaN is false — reach the
// interior, and end up in an int() conversion of a NaN, which Go leaves
// implementation-defined: amd64 yields minint, arm64 yields 0. That is a silent
// per-architecture difference in rendered output, in a subsystem whose whole
// hashing and golden strategy exists to avoid exactly that.
func TestParseSplashLumRangeRejectsNonFinite(t *testing.T) {
	for _, s := range []string{"nan", "NaN", "+Inf", "-Inf", "inf", "", "banana", "0.5x"} {
		v, ok := parseSplashLumRange(s)
		require.Falsef(t, ok, "%q must not set an override (got %v)", s, v)
	}
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"0", 0}, {"0.5", 0.5}, {"1", 1},
		{"-3", 0}, // clamped, not rejected: a knob is a knob
		{"9", 1},
	} {
		v, ok := parseSplashLumRange(tc.in)
		require.Truef(t, ok, "%q must set an override", tc.in)
		require.Equalf(t, tc.want, v, "%q must resolve to %v", tc.in, tc.want)
	}
}
