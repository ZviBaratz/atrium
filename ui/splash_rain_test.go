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
