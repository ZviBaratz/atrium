package ui

import (
	"math"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// TestSplashHashGolden pins exact hash outputs. The hash is pure integer math,
// so these goldens hold on every architecture — this is the one place where
// exact-value snapshots are safe (float-based field output can differ across
// arches via FMA contraction, so frame tests stay property-based).
func TestSplashHashGolden(t *testing.T) {
	cases := []struct {
		x, y int32
		seed uint32
		want uint32
	}{
		{0, 0, 0x0, 0x944FB554},
		{1, 0, 0x0, 0xB2FCF063},
		{0, 1, 0x0, 0xC67C684D},
		{-1, -1, 0x0, 0xCF737785},
		{13, -7, 0x9E3779B9, 0x85F59F37},
		{-200, 143, 0x85EBCA6B, 0x5CD9FA5C},
		{math.MaxInt32, math.MinInt32, 0xC2B2AE35, 0x688BDB26},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, splashHash(c.x, c.y, c.seed),
			"splashHash(%d, %d, 0x%X)", c.x, c.y, c.seed)
	}
}

// TestSplashHashDecorrelates guards the hash's fitness for lattice noise:
// neighboring lattice points and different seeds must produce unrelated
// values (a weak hash here shows up as visible grid artifacts in the field).
func TestSplashHashDecorrelates(t *testing.T) {
	seen := map[uint32]bool{}
	for x := int32(-64); x <= 64; x++ {
		for y := int32(-64); y <= 64; y++ {
			h := splashHash(x, y, 0x9E3779B9)
			require.Falsef(t, seen[h], "collision in a 129x129 neighborhood at (%d,%d)", x, y)
			seen[h] = true
		}
	}
	require.NotEqual(t, splashHash(3, 4, 1), splashHash(3, 4, 2), "seed must change the value")
}

// TestSplashValNoiseRange checks output stays in [0,1) over a spread of
// coordinates, including negatives and non-integer positions.
func TestSplashValNoiseRange(t *testing.T) {
	for i := 0; i < 500; i++ {
		x := (float64(i) - 250) * 0.377
		y := (float64(i%37) - 18) * 1.713
		n := splashValNoise(x, y, 0x27D4EB2F)
		require.GreaterOrEqualf(t, n, 0.0, "noise(%f,%f) below 0", x, y)
		require.Lessf(t, n, 1.0, "noise(%f,%f) at or above 1", x, y)
	}
}

// TestSplashValNoiseContinuity checks the smoothstep interpolation: a small
// step in the domain must produce a small step in the value (no jumps at
// lattice boundaries), which is what keeps the rendered gas free of seams.
func TestSplashValNoiseContinuity(t *testing.T) {
	const eps = 1e-4
	for i := 0; i < 400; i++ {
		// March across several lattice cells, deliberately crossing integers.
		x := -3.0 + float64(i)*0.017
		y := 2.5 - float64(i)*0.011
		a := splashValNoise(x, y, 0x165667B1)
		b := splashValNoise(x+eps, y+eps, 0x165667B1)
		require.InDeltaf(t, a, b, 0.01, "discontinuity near (%f,%f)", x, y)
	}
}

// TestSplashValNoiseAnchorsLattice pins the interpolation contract: at exact
// lattice points the noise equals the lattice value itself.
func TestSplashValNoiseAnchorsLattice(t *testing.T) {
	for _, p := range [][2]int32{{0, 0}, {3, -2}, {-7, 5}} {
		require.InDelta(t, latticeVal(p[0], p[1], 42),
			splashValNoise(float64(p[0]), float64(p[1]), 42), 1e-12)
	}
}

// splashTestVariants enumerates every variant for contract loops, with names
// for failure messages. Hand-maintained — TestSplashTestVariantsCoversEnum is
// what keeps it honest.
func splashTestVariants() map[string]splashVariant {
	return map[string]splashVariant{
		"legacy":  splashVariantLegacy,
		"fbm":     splashVariantFBM,
		"braille": splashVariantBraille,
		"flow":    splashVariantFlow,
		"julia":   splashVariantJulia,
		"mandala": splashVariantMandala,
		"rain":    splashVariantRain,
	}
}

// TestSplashTestVariantsCoversEnum guards the map above, which is the entry
// point to this package's two per-variant sweeps: TestSplashVariantsContract
// (determinism, bounds, blank borders, frame-to-frame animation) and
// BenchmarkRenderSplashVariants (the frame budget). Both iterate the map, so a
// variant left out of it is not partially covered — it is invisible to both,
// and nothing fails to say so. Adding one is exactly when a contract breach is
// most likely and least likely to be noticed.
func TestSplashTestVariantsCoversEnum(t *testing.T) {
	seen := make(map[splashVariant]bool, len(splashTestVariants()))
	for name, v := range splashTestVariants() {
		require.Falsef(t, seen[v], "%q duplicates a variant already in the map", name)
		seen[v] = true
	}
	for v := splashVariant(0); v < splashVariantCount; v++ {
		require.Truef(t, seen[v],
			"variant %d is missing from splashTestVariants, so it escapes both "+
				"the contract loop and the benchmark", int(v))
	}
}

// TestSplashVariantsContract loops every variant through the core field
// contract: determinism, exact w×h bounds, frame-to-frame animation, and
// fully-blank first/last rows (the edge vignette's by-construction guarantee,
// which dithering and any Pass-1 processing must never break).
func TestSplashVariantsContract(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	for name, v := range splashTestVariants() {
		a := renderSplashField(w, h, 5, pal, centeredClearing(h, 20, 4), v)
		b := renderSplashField(w, h, 5, pal, centeredClearing(h, 20, 4), v)
		require.Equalf(t, a, b, "%s: same inputs must render identically", name)
		next := renderSplashField(w, h, 6, pal, centeredClearing(h, 20, 4), v)
		require.NotEqualf(t, a, next, "%s: consecutive frames must differ", name)

		lines := stripLines(a)
		require.Lenf(t, lines, h, "%s: line count", name)
		for i, l := range lines {
			require.Equalf(t, w, len([]rune(l)), "%s: line %d width", name, i)
		}
		require.Equalf(t, strings.Repeat(" ", w), lines[0], "%s: top row must be blank", name)
		require.Equalf(t, strings.Repeat(" ", w), lines[h-1], "%s: bottom row must be blank", name)
	}
}

// TestSplashDitherBlankFloor locks the invariant that makes dithering safe
// under the vignette: with ditherAmp < 1 glyph step, a fully-dark cell
// (lit = 0) must quantize to glyph 0 for every cell position, so dither can
// never light a blank border row.
func TestSplashDitherBlankFloor(t *testing.T) {
	require.Less(t, ditherAmp, 1.0, "ditherAmp must stay below one glyph step")
	maxGlyph := len([]rune(splashRamp)) - 1
	for row := 0; row < 60; row++ {
		for col := 0; col < 200; col++ {
			gf := 0*float64(maxGlyph) + (splashDither(col, row)-0.5)*ditherAmp
			require.Zerof(t, clampInt(int(gf), 0, maxGlyph),
				"dark cell at (%d,%d) must stay blank", col, row)
		}
	}
}

// TestSplashDitherDistribution sanity-checks the dither noise: values in
// [0,1), roughly centered (so dithering is unbiased — it breaks banding
// without brightening or darkening the field on average).
func TestSplashDitherDistribution(t *testing.T) {
	sum, n := 0.0, 0
	for row := 0; row < 40; row++ {
		for col := 0; col < 120; col++ {
			v := splashDither(col, row)
			require.GreaterOrEqual(t, v, 0.0)
			require.Less(t, v, 1.0)
			sum += v
			n++
		}
	}
	require.InDelta(t, 0.5, sum/float64(n), 0.02, "dither must be unbiased")
}

// TestRenderSplashFieldConcurrent guards the two-panes case (preview and
// terminal both render the splash) and any future buffer pooling: concurrent
// renders of the same frame must all be byte-identical.
func TestRenderSplashFieldConcurrent(t *testing.T) {
	pal := splashTestPalette()
	want := renderSplashField(80, 30, 9, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	var wg sync.WaitGroup
	mismatch := make(chan string, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if got := renderSplashField(80, 30, 9, pal, centeredClearing(30, 20, 4), splashDefaultVariant); got != want {
					select {
					case mismatch <- "concurrent render diverged":
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(mismatch)
	require.Empty(t, <-mismatch)
}

// TestBrailleMaskLayout pins the dot-bit table to the Unicode braille layout:
// dots 1,2,3,7 run down the left column, dots 4,5,6,8 down the right — the
// bit order is NOT linear in the grid, so this is worth locking.
func TestBrailleMaskLayout(t *testing.T) {
	require.Equal(t, uint8(0x01), brailleBit[0][0], "dot 1: top-left")
	require.Equal(t, uint8(0x08), brailleBit[0][1], "dot 4: top-right")
	require.Equal(t, uint8(0x02), brailleBit[1][0], "dot 2")
	require.Equal(t, uint8(0x10), brailleBit[1][1], "dot 5")
	require.Equal(t, uint8(0x04), brailleBit[2][0], "dot 3")
	require.Equal(t, uint8(0x20), brailleBit[2][1], "dot 6")
	require.Equal(t, uint8(0x40), brailleBit[3][0], "dot 7: bottom-left")
	require.Equal(t, uint8(0x80), brailleBit[3][1], "dot 8: bottom-right")
	var all uint8
	for _, row := range brailleBit {
		all |= row[0] | row[1]
	}
	require.Equal(t, uint8(0xFF), all, "the 8 bits must cover the full mask")
}

// TestBrailleGlyphsWidthOne asserts every braille pattern renders at terminal
// width 1 (the column-alignment invariant every splash glyph must satisfy).
func TestBrailleGlyphsWidthOne(t *testing.T) {
	for m := 1; m <= 0xFF; m++ {
		g := string(rune(0x2800) | rune(m))
		require.Equalf(t, 1, ansi.StringWidth(g), "braille mask %#x must be width 1", m)
	}
}

// TestBrailleHalftoneInvariants locks the halftone's two structural
// relations: a fully dark sub-cell can never fire a dot (strict comparison
// against non-negative dither), and the scale exceeds the band top so a cell
// leaving the band doesn't out-weigh the ramp glyph it hands over to.
func TestBrailleHalftoneInvariants(t *testing.T) {
	for i := 0; i < 2000; i++ {
		require.False(t, 0.0 > splashDither(i%97, i/97)*brailleHalftoneScale,
			"a dark sub-cell must never fire a dot")
	}
	require.Greater(t, float64(brailleHalftoneScale), float64(brailleBandHi),
		"band-top cells must not light all 8 dots")
}

// TestSplashBrailleVariantOutput drives the braille variant end to end: the
// faint band must actually produce braille runes, and bare U+2800 (which some
// fonts draw as eight hollow circles) must never be emitted.
func TestSplashBrailleVariantOutput(t *testing.T) {
	pal := splashTestPalette()
	sawBraille := false
	for frame := 0; frame < 8; frame++ {
		out := ansi.Strip(renderSplashField(80, 30, frame*7, pal, centeredClearing(30, 20, 4), splashVariantBraille))
		for _, r := range out {
			require.NotEqual(t, rune(0x2800), r, "bare U+2800 must never be emitted")
			if r > 0x2800 && r <= 0x28FF {
				sawBraille = true
			}
		}
	}
	require.True(t, sawBraille, "the faint band must produce braille dots")
}

// TestSplashPointFnRange checks every variant's point evaluator over a spread
// of cells and phases.
//
// Driven off splashFieldAt rather than a list of evaluators, so a variant is
// covered the moment it is dispatched — this used to be two hand-maintained
// lists (one for fBm, one for the fractals) that a new variant had to be
// remembered into, which is why the gap below went unnoticed for so long.
//
// val is [0,1] for every variant: Pass 2's contrast window, glyph quantization
// and envelope all assume it, and a breach silently clips rather than crashing.
//
// aux is deliberately *not* universal. It is a variant-defined hue helper, and
// splashColorIdx consumes it two different ways: the legacy plasma passes an
// atan2 angle straight into sin(aux + …), which is meaningful for any real,
// while every other variant contributes aux as a *weighted term* in the hue mix
// and so must keep it in [0,1] or the hue wraps to the far end of the gradient.
// Assert the range only where the mix actually weights it.
func TestSplashPointFnRange(t *testing.T) {
	for name, v := range splashTestVariants() {
		at := splashFieldAt(v)
		// The legacy plasma's aux is an angle by construction, consumed inside a
		// sine rather than weighted — see splashColorIdx and splashField's doc.
		auxIsWeight := v != splashVariantLegacy
		for i := 0; i < 600; i++ {
			col, row := i%60, i/60
			dx := (float64(col) - 30) * 1.37
			dy := (float64(row) - 5) * 2.9
			phase := float64(i%23) * 1.7
			val, aux := at(col, row, dx, dy, phase)
			require.GreaterOrEqualf(t, val, 0.0, "%s val at (%d,%d,p%f)", name, col, row, phase)
			require.LessOrEqualf(t, val, 1.0, "%s val at (%d,%d,p%f)", name, col, row, phase)
			if auxIsWeight {
				require.GreaterOrEqualf(t, aux, 0.0, "%s aux at (%d,%d,p%f)", name, col, row, phase)
				require.LessOrEqualf(t, aux, 1.0, "%s aux at (%d,%d,p%f)", name, col, row, phase)
			}
		}
	}
}

// TestSplashBloom checks the bloom's structural properties: it only ever
// brightens (additive), it spreads a bright spike into its neighborhood, and
// it leaves an all-dark buffer untouched (nothing above the threshold).
func TestSplashBloom(t *testing.T) {
	const w, h = 11, 9
	dark := splashField{vals: make([]float64, w*h)}
	splashBloom(dark, w, h)
	for i, v := range dark.vals {
		require.Zerof(t, v, "dark cell %d must stay dark", i)
	}

	spike := splashField{vals: make([]float64, w*h)}
	center := (h/2)*w + w/2
	spike.vals[center] = 1.0
	before := append([]float64(nil), spike.vals...)
	splashBloom(spike, w, h)
	for i, v := range spike.vals {
		require.GreaterOrEqualf(t, v, before[i], "bloom must never darken (cell %d)", i)
		require.LessOrEqual(t, v, 1.0)
	}
	require.Greater(t, spike.vals[center-1], 0.0, "bloom must spread to the left neighbor")
	require.Greater(t, spike.vals[center+w], 0.0, "bloom must spread to the row below")
	require.Zero(t, spike.vals[0], "bloom must not reach the far corner")
}

// flowTestField builds a 5×5 buffer from a linear function of (col, row) and
// returns the glyph choice at the center cell.
func flowTestField(t *testing.T, f func(col, row int) float64) (rune, bool) {
	t.Helper()
	const w, h = 5, 5
	vals := make([]float64, w*h)
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			vals[r*w+c] = f(c, r)
		}
	}
	return splashFlowGlyph(vals, w, h, 2, 2)
}

// TestSplashFlowGlyphBins pins the angle→glyph mapping on synthetic linear
// fields, including the two traps: the y-down→y-up flip (getting it wrong
// swaps ╱ and ╲) and the aspect-corrected diagonals (a screen diagonal is a
// 2-rows-per-2-cols line, not 1:1).
func TestSplashFlowGlyphBins(t *testing.T) {
	// f increases rightward → vertical iso-lines.
	g, ok := flowTestField(t, func(c, _ int) float64 { return 0.1 * float64(c) })
	require.True(t, ok)
	require.Equal(t, '│', g)

	// f increases downward → horizontal iso-lines.
	g, ok = flowTestField(t, func(_, r int) float64 { return 0.1 * float64(r) })
	require.True(t, ok)
	require.Equal(t, '─', g)

	// f = 0.1c − 0.2r: iso-lines run 2 cols right per row down — a rendered
	// ╲ diagonal (one row is two visual units tall).
	g, ok = flowTestField(t, func(c, r int) float64 { return 0.1*float64(c) - 0.2*float64(r) })
	require.True(t, ok)
	require.Equal(t, '╲', g)

	// Mirrored: f = 0.1c + 0.2r → ╱.
	g, ok = flowTestField(t, func(c, r int) float64 { return 0.1*float64(c) + 0.2*float64(r) })
	require.True(t, ok)
	require.Equal(t, '╱', g)

	// A flat field is direction-free: no glyph.
	_, ok = flowTestField(t, func(_, _ int) float64 { return 0.5 })
	require.False(t, ok, "flat field must not stroke a contour")
}

// TestSplashFlowVariantOutput drives the flow variant end to end: the
// contour band must actually stroke line glyphs.
func TestSplashFlowVariantOutput(t *testing.T) {
	pal := splashTestPalette()
	saw := false
	for frame := 0; frame < 8 && !saw; frame++ {
		out := ansi.Strip(renderSplashField(80, 30, frame*7, pal, centeredClearing(30, 20, 4), splashVariantFlow))
		saw = strings.ContainsAny(out, "─╱│╲")
	}
	require.True(t, saw, "the contour band must produce flow glyphs")
}

// TestSplashRotationPickInBounds guards against a negative-index panic: a clock
// set before the Unix epoch yields a negative UnixNano(), and Go's signed % of
// that would produce a negative slice index. The pick must stay in [0, len) for
// every int64 seed, including negative and boundary values.
func TestSplashRotationPickInBounds(t *testing.T) {
	seeds := []int64{
		0, 1, -1, 42, -42,
		math.MinInt64, math.MaxInt64,
		-1234567890123456789, 1234567890123456789,
	}
	for _, seed := range seeds {
		require.NotPanics(t, func() { splashRotationPick(seed) },
			"splashRotationPick must not panic for seed %d", seed)
	}
}

// resetSplashSelection restores the process-wide splash selection after a test
// pins or re-rolls it, so package siblings see the pristine lazy-random state.
// (Rendering is shielded anyway — TestMain pins the env override, which trumps
// the selection — but the package state should stay canonical regardless.)
func resetSplashSelection(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		splashRandomMode, splashPicked, splashPick = true, false, splashDefaultVariant
	})
}

// TestSplashVariantNamesCoverAllVariants guards the settings vocabulary: every
// rotation variant (plus the legacy baseline) must be pinnable by name, so a
// future variant can't ship unreachable from the settings panel.
func TestSplashVariantNamesCoverAllVariants(t *testing.T) {
	named := make(map[splashVariant]bool, len(splashVariantNames))
	for _, v := range splashVariantNames {
		named[v] = true
	}
	for _, v := range splashRotation {
		require.True(t, named[v], "rotation variant %d has no config name", v)
	}
	require.True(t, named[splashVariantLegacy], "the legacy baseline must stay pinnable")
}

// TestSetSplashVariantPinsKnownNames locks the name→generator mapping and that
// a pinned name leaves random mode.
func TestSetSplashVariantPinsKnownNames(t *testing.T) {
	resetSplashSelection(t)
	for name, want := range splashVariantNames {
		SetSplashVariant(name)
		require.False(t, splashRandomMode, "%q must pin, not stay random", name)
		require.True(t, splashPicked)
		require.Equal(t, want, splashPick, "name %q", name)
	}
}

// TestSetSplashVariantRandomRerolls pins random-mode semantics: anything that
// isn't a known name (config.SplashRandom, junk) enters random mode with an
// immediate rotation draw, and RerollSplashVariant only re-draws in that mode —
// a pinned pattern survives a screensaver activation untouched.
func TestSetSplashVariantRandomRerolls(t *testing.T) {
	resetSplashSelection(t)

	SetSplashVariant("julia")
	RerollSplashVariant()
	require.Equal(t, splashVariantJulia, splashPick, "reroll must not disturb a pinned pattern")

	SetSplashVariant("random")
	require.True(t, splashRandomMode)
	require.True(t, splashPicked, "random mode draws immediately so both panes agree")
	require.Contains(t, splashRotation, splashPick, "the draw must come from the rotation pool")

	// Every re-roll must land somewhere in the pool *and* move: the screensaver
	// re-rolls per activation, so a draw that repeats the current pattern reads
	// as a dead keypress. Looping catches a seed-dependent repeat that a single
	// call would only hit ~1/len of the time.
	for i := 0; i < 40; i++ {
		prev := splashPick
		RerollSplashVariant()
		require.Contains(t, splashRotation, splashPick)
		require.NotEqual(t, prev, splashPick, "a re-roll must change the pattern (iteration %d)", i)
	}
}

// TestSplashRotationRerollUniformOverRemainder pins the exclusion arithmetic
// directly, seed by seed: every consecutive seed maps to some variant other
// than cur, and sweeping a full period hits each of the other variants exactly
// once — i.e. skipping cur's slot doesn't bias the draw toward its neighbour.
func TestSplashRotationRerollUniformOverRemainder(t *testing.T) {
	for _, cur := range splashRotation {
		counts := map[splashVariant]int{}
		for seed := int64(0); seed < int64(len(splashRotation)-1); seed++ {
			got := splashRotationReroll(seed, cur)
			require.NotEqual(t, cur, got, "cur=%d seed=%d must be excluded", cur, seed)
			counts[got]++
		}
		require.Len(t, counts, len(splashRotation)-1, "cur=%d: every other variant must be reachable", cur)
		for v, n := range counts {
			require.Equal(t, 1, n, "cur=%d variant=%d drawn %d times over one period", cur, v, n)
		}
	}
}

// TestSplashRotationRerollFallsBackOutsidePool: a cur the pool doesn't contain
// (the unpicked zero value, or the legacy baseline — pinnable but never drawn)
// has nothing to exclude, so it degrades to a plain draw rather than looping or
// panicking. Negative seeds must stay in bounds here too.
func TestSplashRotationRerollFallsBackOutsidePool(t *testing.T) {
	for _, seed := range []int64{-1, 0, 1, -(1 << 62), 1 << 62} {
		require.Contains(t, splashRotation, splashRotationReroll(seed, splashVariantLegacy),
			"legacy cur, seed %d", seed)
	}
}

// TestSplashSelectionConcurrent is TestRenderSplashFieldConcurrent's counterpart
// for the mutable selection: renderSplashField is pure and provably safe, but
// SetSplashVariant / RerollSplashVariant write process-wide state that a
// sync.OnceValue used to make safe by construction. Under -race this pins
// splashSelMu actually covering them; without it the package's own
// concurrent-render posture would rest on a comment.
//
// splashActiveVariant short-circuits on the env override TestMain pins, so this
// exercises the writers plus a locked read rather than the lazy-seed path.
func TestSplashSelectionConcurrent(t *testing.T) {
	resetSplashSelection(t)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				switch (g + i) % 3 {
				case 0:
					SetSplashVariant("random")
				case 1:
					SetSplashVariant("julia")
				default:
					RerollSplashVariant()
				}
				_ = splashActiveVariant()
			}
		}(g)
	}
	wg.Wait()

	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	require.True(t, splashPicked, "every path above picks")
	require.Contains(t, append(slices.Clone(splashRotation), splashVariantJulia), splashPick,
		"the selection must never be torn into an out-of-pool value")
}

// TestSplashEnvOverrideTrumpsSelection relies on TestMain pinning
// ATRIUM_SPLASH_VARIANT=a: whatever the config pins, the dev override wins in
// splashActiveVariant — which is exactly what keeps every String()-path test
// deterministic even if a future test seeds a config with a splash value.
func TestSplashEnvOverrideTrumpsSelection(t *testing.T) {
	resetSplashSelection(t)
	SetSplashVariant("mandala")
	require.Equal(t, splashVariantFBM, splashActiveVariant(),
		"the env override (pinned to \"a\" in TestMain) must trump the config selection")
}

func BenchmarkSplashValNoise(b *testing.B) {
	var sink float64
	for i := 0; i < b.N; i++ {
		sink += splashValNoise(float64(i)*0.13, float64(i%97)*0.29, 0x9E3779B9)
	}
	_ = sink
}

// BenchmarkRenderSplashVariants tracks the ≤3ms/frame budget per variant
// (checked manually; never a timed assertion — a timed assertion would flake on
// shared CI).
//
// Truecolor is forced because the profile decides what this measures. A
// benchmark binary's stdout is not a TTY, so termenv resolves to Ascii, every
// SGR affix is the empty string, and the emitter is timed with nothing to emit.
// That skews less than it used to — the affix cache made emission cheap either
// way — but it is exactly why the colorless default is the wrong budget check:
// the Render-per-run cost the cache replaced measured 3.7ms/frame at 240×60
// under Ascii and 6.3ms in a real terminal. Time the frame the user actually
// renders, or a regression of that shape reads ~40% cheaper than it is.
//
// Two sizes, because they answer different questions. 80×30 is the reference
// preview pane. 240×60 is the *screensaver*, which renders full-window: it is
// ~6× the cells but ~8× the cost, and it is measured against a 16.7ms/60fps
// frame — so a variant can sit comfortably inside the 80×30 budget and still be
// a slideshow full-screen. Benchmark both before shipping one.
func BenchmarkRenderSplashVariants(b *testing.B) {
	forceBenchTrueColor(b)
	pal := splashTestPalette()
	sizes := []struct {
		name string
		w, h int
	}{
		{"80x30", 80, 30},
		{"240x60", 240, 60}, // the full-window screensaver
	}
	for _, s := range sizes {
		for name, v := range splashTestVariants() {
			b.Run(s.name+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				clearing := centeredClearing(s.h, 20, 4)
				for i := 0; i < b.N; i++ {
					_ = renderSplashField(s.w, s.h, i, pal, clearing, v)
				}
			})
		}
	}
}

// forceBenchTrueColor pins the color profile for a benchmark so the SGR path
// runs, and asserts it took — a silent degrade would turn every number the
// benchmark reports into a measurement of the wrong code.
func forceBenchTrueColor(b *testing.B) {
	b.Helper()
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	b.Cleanup(func() { lipgloss.SetColorProfile(prof) })

	out := renderSplashField(80, 30, 1, splashTestPalette(),
		centeredClearing(30, 20, 4), splashVariantLegacy)
	if !strings.Contains(out, "\x1b[38;2;") {
		b.Fatal("no truecolor SGR emitted: benchmark would measure the colorless path")
	}
}
