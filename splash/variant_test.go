package splash

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShippedVariantsOps pins the per-variant decisions that render differently
// but break no invariant — the ones nothing else can fail on.
//
// This is the roster table, and it is the shape a new variant inherits. It exists
// because the alternative shape does not work: the tests that *care* about a
// policy field all open with a filter on it, so they check that variants which
// claim a property honour it, and a variant that stops claiming is not caught —
// it is excused. Every such field was reachable by mutation on a fully green
// tree: flipping ripple's stars, or dropping it from the structured set, left
// `go test ./...` entirely passing. Both of those knobs are gone now, but the
// shape that catches them is not, and the two that remain are pinned here.
//
// Literals rather than reads of ops: a table that asks the code what it does
// and then agrees is not a change-detector, it is a mirror. The risk is real
// rather than theoretical — ops.dimToRim once shipped declared-and-never-read
// because a scripted edit silently no-op'd, and a lumRange that quietly reverted
// to 0 would look exactly like a variant that was never tuned.
//
// It replaces two tables, one per surviving field. V5 retired six variants and
// with them five ops fields (a contrast window, a dither, a radial dim, a
// breathing swell), and the two tables' other columns went with those: every
// survivor was structured, so that predicate became a constant and then nothing;
// every survivor spent aux as its hue, so hueIsAux became the universal rule in
// splashColorIdx rather than a per-variant claim.
func TestShippedVariantsOps(t *testing.T) {
	type policy struct {
		stars    bool    // draws the fixed twinkling starfield
		lumRange float64 // share of brightness on colour rather than glyph density
	}
	want := map[Variant]policy{
		// Rain and the tunnel are the moving fields: the eye tracks them, so a fixed
		// star reads as a stuck pixel. Both put all their brightness on the colour —
		// rain because a constant-weight katakana cannot shade by size, the tunnel
		// because its fog is a gradient with no stipple to spend on the trade.
		Rain:   {stars: false, lumRange: 1},
		Tunnel: {stars: false, lumRange: 1},
		// Ripple is the one field that is both in motion and starry, because nothing
		// in it travels except the rings and a still pool reflects a still sky. It is
		// also the only one at neither end of the luminance split: its crests keep the
		// density ramp while the tail rides the colour. The value came from a rendered
		// sweep; see ops.
		Ripple: {stars: true, lumRange: 0.75},
	}
	require.Len(t, want, int(variantCount),
		"every variant needs an explicit policy here — a new one must make these "+
			"choices, not inherit whatever the zero value happens to be")
	for v := Variant(0); v < variantCount; v++ {
		w, ok := want[v]
		require.Truef(t, ok, "variant %d is missing from the table", int(v))
		require.Equalf(t, w.stars, v.ops().stars, "variant %d's shipped ops.stars", int(v))
		require.Equalf(t, w.lumRange, v.ops().lumRange, "variant %d's shipped ops.lumRange", int(v))
	}
}

// TestSplashRotationCoversEveryVariant closes the hole the roster tables leave.
//
// Three comments in this package now assert that the pool is "every shipped
// variant", and until V5 nothing could pin it: the legacy baseline was pinnable
// by name and deliberately never drawn, so the pool was legitimately a subset. It
// isn't any more, and the failure it leaves is silent in the way this package
// keeps getting caught by — a variant added to the enum, the names, the ops table
// and both test maps but forgotten here is pinnable, passes every other guard,
// and simply never comes up at random. Variants() is the one hand-maintained
// list nothing else walked.
func TestSplashRotationCoversEveryVariant(t *testing.T) {
	require.Len(t, Variants(), int(variantCount),
		"the random pool must offer every variant")
	for v := Variant(0); v < variantCount; v++ {
		require.Containsf(t, Variants(), v,
			"variant %d is pinnable but never drawn at random", int(v))
	}
}

// TestSplashVariantNamesCoverAllVariants guards the settings vocabulary: every
// rotation variant must be pinnable by name, so a future variant can't ship
// unreachable from the settings panel. It used to add the legacy baseline by
// hand, which was the one pinnable variant outside the pool; V5 retired it, and
// TestSplashRotationCoversEveryVariant now pins that the pool is the whole enum.
func TestSplashVariantNamesCoverAllVariants(t *testing.T) {
	named := make(map[Variant]bool, len(variantNames))
	for _, v := range variantNames {
		named[v] = true
	}
	for _, v := range Variants() {
		require.Truef(t, named[v], "rotation variant %d has no name", v)
	}
}

// TestVariantStringRoundTrips pins the exported vocabulary the cross-package
// agreement tests lean on (app/splash_vocab_test.go, config/splash_test.go):
// every shipped variant has a name, ParseVariant inverts String for it, an
// unknown name does not parse, and a value outside the set strings as "unknown".
func TestVariantStringRoundTrips(t *testing.T) {
	for _, v := range Variants() {
		name := v.String()
		require.NotEqualf(t, "unknown", name, "variant %d must have a name", int(v))
		got, ok := ParseVariant(name)
		require.Truef(t, ok, "ParseVariant(%q) must succeed", name)
		require.Equalf(t, v, got, "ParseVariant(String(%d)) must round-trip", int(v))
	}
	_, ok := ParseVariant("nope")
	require.False(t, ok, "an unknown name must not parse")
	require.Equal(t, "unknown", variantCount.String(),
		"a value outside the shipped set strings as unknown")
}
