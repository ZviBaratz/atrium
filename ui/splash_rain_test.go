package ui

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRainHeadAdvancesLessThanARowPerFrame pins the premise the rest of the
// rain design rests on. At ~13.5 rows/second against a 60fps tick a head sits
// in the same row for about four frames, so integer row stepping cannot carry
// the animation — brightness has to be a continuous function of the distance to
// the head instead. Every other rain test is only meaningful while this holds:
// if a head ever advanced a full row per frame, row stepping would animate the
// field on its own and the sub-row gradient would go untested.
func TestRainHeadAdvancesLessThanARowPerFrame(t *testing.T) {
	rowsPerFrame := rainFall * rainSpdMax * driftPerFrame / cellAspect
	require.Less(t, rowsPerFrame, 1.0,
		"the fastest head must advance under a row per frame (got %.3f), else the "+
			"sub-row brightness gradient this design depends on is untested", rowsPerFrame)
	require.Greater(t, rowsPerFrame, 0.0, "rain must actually fall")
}

// TestRainAnimatesEveryFrame guards the trap that makes rain different from the
// dense fields.
//
// TestSplashVariantsContract checks one frame pair per variant, which the
// nebula passes trivially: thousands of lit cells, so something always crosses
// a quantization boundary. Rain lights far fewer, and its heads only cross a row
// every ~4 frames — so had brightness been quantized to integer rows, most
// consecutive pairs would be *identical* and that contract check would pass or
// fail on a coin flip depending on which frames it happened to sample.
//
// A run of frames is what actually distinguishes "animates" from "got lucky".
func TestRainAnimatesEveryFrame(t *testing.T) {
	pal := splashTestPalette()
	prev := ""
	for f := 0; f < 30; f++ {
		got := renderSplashField(80, 30, f, pal, centeredClearing(30, 20, 4), splashVariantRain)
		if f > 0 {
			require.NotEqualf(t, prev, got,
				"frames %d and %d render identically: rain must move every frame, "+
					"not once per row crossing", f-1, f)
		}
		prev = got
	}
}

// TestRainTailFadesFromTheHead pins the gradient itself, which is what carries
// the fade: the pipeline has no brightness channel of its own (the color LUT is
// a near-equal-luminance hue ramp), so a trail that failed to fall off in value
// would render as a flat bar of glyphs and no test above would notice.
func TestRainTailFadesFromTheHead(t *testing.T) {
	// Walk one column upward from its brightest cell and require the field to
	// decay. Sample in aspect units, the space splashRainAt works in.
	const col = 17
	var headDy, headVal float64
	for i := 0; i < 4000; i++ {
		dy := float64(i) * 0.05
		if v, _ := splashRainAt(col, 0, 0, dy, 0); v > headVal {
			headVal, headDy = v, dy
		}
	}
	require.Greater(t, headVal, 0.9, "a column should contain a saturated head somewhere")

	// Immediately behind the head (above it) the value must be below the peak,
	// and further back it must be lower still.
	near, _ := splashRainAt(col, 0, 0, headDy-rainHeadR*1.5, 0)
	far, _ := splashRainAt(col, 0, 0, headDy-rainHeadR*1.5-rainTailMin*0.5, 0)
	require.Less(t, near, headVal, "the cell behind the head must be dimmer than the head")
	require.Less(t, far, near, "the tail must keep fading with distance behind the head")
	require.Greater(t, near, 0.0, "the tail must be lit at all, not merely absent")
}

// TestRainHeadIsContinuousInPhase is the mechanism behind
// TestRainAnimatesEveryFrame, asserted directly rather than through the
// renderer: a phase nudge far smaller than a row must still move the field.
// Quantizing brightness to the head's integer row would flatten this to zero.
func TestRainHeadIsContinuousInPhase(t *testing.T) {
	const col = 23
	// A cell somewhere in a stream, and a phase step of one frame.
	dy := 6.0
	moved := 0
	for f := 0; f < 40; f++ {
		a, _ := splashRainAt(col, 0, 0, dy, float64(f)*driftPerFrame)
		b, _ := splashRainAt(col, 0, 0, dy, float64(f+1)*driftPerFrame)
		if math.Abs(a-b) > 1e-9 {
			moved++
		}
	}
	require.Greater(t, moved, 30,
		"a one-frame phase step must change the field at nearly every frame (moved %d/40)", moved)
}

// TestRainTailSurvivesPass2 is the regression for the bug that made the first
// rain prototype render as confetti.
//
// The variant inherited the fBm contrast window, which exists to push a noise
// field's mid-tones apart and assumes the field has no gradient worth keeping.
// Rain's tail *is* a gradient. Measured down one column, smoothstep(0.36, 0.64)
// erased the faint 44% of every tail outright, flattened the brightest 22% to a
// solid bar, and left the fade squeezed into the third in between — so streams
// rendered as short blobs with holes, with no trails to see and therefore no
// parallax either.
//
// Assert the shape rather than the constants: most of a column's cells light,
// and the glyph indices actually *descend* behind a head instead of clipping to
// the ramp's ceiling.
func TestRainTailSurvivesPass2(t *testing.T) {
	ops := splashVariantRain.ops()
	ramp := []rune(splashRamp)
	maxGlyph := len(ramp) - 1

	glyphAt := func(dy float64) int {
		val, _ := splashRainAt(17, 0, 0, dy, 0)
		return clampInt(int(smoothstep(ops.contrastLo, ops.contrastHi, val)*float64(maxGlyph)), 0, maxGlyph)
	}

	lit, saturated := 0, 0
	for i := 0; i < 60; i++ {
		switch g := glyphAt(float64(i) * 0.6); {
		case g >= maxGlyph:
			saturated++
			lit++
		case g > 0:
			lit++
		}
	}
	require.Greaterf(t, lit, 40, "most of a rain column should be lit; only %d/60 were", lit)
	require.Lessf(t, saturated, 6,
		"a tail must be a gradient, not a bar clipped to the ramp's ceiling (%d/60 saturated)", saturated)
}

// TestRainHueNamesItsLayer pins the second half of that bug. Rain's hue must
// come from the stream, not from where the glyph sits: the shared mix spends
// half its weight on distance-from-the-focus, so all three layers at one screen
// position landed on the same colour and the parallax was invisible. The layers'
// aux bands must stay disjoint, or a near stream's tail drifts into a far
// stream's hue and the separation is lost again.
func TestRainHueNamesItsLayer(t *testing.T) {
	type band struct{ lo, hi float64 }
	bands := make([]band, len(rainLayers))
	for i, L := range rainLayers {
		bands[i] = band{
			lo: clamp01(L.hue - rainHueSpread),
			hi: clamp01(L.hue + rainHueSpread),
		}
	}
	for i := 0; i < len(bands); i++ {
		for j := i + 1; j < len(bands); j++ {
			require.Truef(t, bands[i].hi < bands[j].lo || bands[j].hi < bands[i].lo,
				"layers %d and %d share hue (%v vs %v): a glyph's colour must name its depth",
				i, j, bands[i], bands[j])
		}
	}
	// And the mix must actually be aux-only — a position term would reintroduce
	// the bug regardless of how well-separated the bands are.
	const nColors = 20
	for _, dRaw := range []float64{0, 20, 60} {
		got := splashColorIdx(splashVariantRain, rainLayers[0].hue, 3, 5, dRaw, 1.7, 80, nColors)
		want := splashColorIdx(splashVariantRain, rainLayers[0].hue, -9, 2, 0, 0.0, 80, nColors)
		require.Equalf(t, want, got,
			"rain's hue must not depend on screen position or phase (dRaw=%v changed it)", dRaw)
	}
}

// TestRainOpsSuppressWhatFightsIt records the rest of the Pass-2 policy as
// intent rather than as three loose constants: dither smooths a wash but eats a
// one-cell-wide stream, the starfield's fixed points read as stuck pixels
// against moving ones, and the head needs a value the near-equal-luminance
// gradient cannot supply.
func TestRainOpsSuppressWhatFightsIt(t *testing.T) {
	ops := splashVariantRain.ops()
	require.False(t, ops.dither, "dither eats thin streams rather than smoothing them")
	require.False(t, ops.stars, "fixed stars over moving rain read as stuck pixels")
	require.Positive(t, ops.headLo, "rain needs its own highlight; the gradient has no bright value")
	require.Greaterf(t, ops.headLo, rainTailAmp,
		"only the head lobe may promote to white; a tail at %.2f must not reach headLo", rainTailAmp)

	// The near layer must be able to reach it, or nothing is ever white.
	require.GreaterOrEqual(t, rainLayers[0].bright, ops.headLo,
		"the near layer's peak must clear headLo, or no stream ever gets a white head")
}
