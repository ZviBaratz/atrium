package splash

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// flatten collapses a decoded grid for set assertions.
func flatten(g [][]int) []int {
	var out []int
	for _, r := range g {
		out = append(out, r...)
	}
	return out
}

// shadeHexAt is hue h's colour at luminance stop l, re-derived from the gradient
// rather than read back out of the grid.
//
// Deliberately a parallel derivation rather than a call into buildShadeGrid: the
// property worth asserting is that a column climbs in *luminance*, and re-deriving
// it from the curve keeps that an independent check instead of a restatement of the
// code under test. Asserting it on colours is also honest where parsing L* back out
// of an SGR affix is not.
//
// It returns the curve's own value at the top stop, where buildShadeGrid *pins* the
// gradient affix instead. The two agree only to within an HCL round-trip, which is
// exactly the discrepancy the pin exists to remove — so
// TestShadeAffixBracketsMatchRender asserts the top stop against lut.styles and the
// rest against this.
func shadeHexAt(colors []lipgloss.Color, h, l int) string {
	return splashLumHexAt(splashShadeParse(colors[h]), float64(l)/float64(splashLumStops-1), shadeChromaHold)
}

// shadeLum is a shade stop's L*, for the luminance assertions below. It takes the
// gradient rather than the palette so a caller walking all 20x16 stops blends it
// once instead of per stop.
func shadeLum(t *testing.T, colors []lipgloss.Color, hue, stop int) float64 {
	t.Helper()
	c, err := colorful.Hex(shadeHexAt(colors, hue, stop))
	require.NoError(t, err)
	l, _, _ := c.Lab()
	return l * 100
}

// TestShadeGridTopStopIsTheGradientColour is the pin, and it is what lets a shaded
// variant keep the gradient the theme actually chose.
//
// The top of every column is assigned from lut.affix rather than recomputed, for
// the same reason buildSplashLUT pins its gradient endpoints: an HCL round-trip
// can nudge a hex by a digit. Without the pin a fully-lit cell would render a hair
// off the colour it has at lumRange 0 — a difference no test would name and every
// A/B screenshot would carry silently.
func TestShadeGridTopStopIsTheGradientColour(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	lut := buildLUTAmbient(splashTestPalette())
	require.Len(t, lut.shade, splashLUTSize*splashLumStops)

	for h := 0; h < splashLUTSize; h++ {
		require.Equalf(t, lut.affix[h], lut.shade[h*splashLumStops+splashLumStops-1],
			"hue %d's brightest shade stop must be exactly its gradient colour", h)
	}
}

// TestShadeColumnClimbsInLuminance is the one that catches the failure mode the
// whole change exists to avoid: a ramp that only changes *hue*.
//
// The gradient LUT is already 20 stops of colour, and it spans barely 14 L*. If
// the shade axis merely walked colour around again it would pass a shape test, a
// range test and a render test, and still leave brightness welded to glyph size.
// The axis has to get darker, in L*, measured.
func TestShadeColumnClimbsInLuminance(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	colors := splashGradientColors(splashTestPalette())

	for h := 0; h < splashLUTSize; h++ {
		prev := -1.0
		for l := 0; l < splashLumStops; l++ {
			got := shadeLum(t, colors, h, l)
			require.Greaterf(t, got, prev,
				"hue %d: stop %d (L* %.1f) must be brighter than stop %d (L* %.1f)",
				h, l, got, l-1, prev)
			prev = got
		}
		require.Lessf(t, shadeLum(t, colors, h, 0), 12.0,
			"hue %d's darkest stop must read as unlit", h)
		require.Positivef(t, shadeLum(t, colors, h, 0),
			"hue %d's floor must not be pure black: a minimum-contrast terminal "+
				"rewrites true black to something legible and speckles the field", h)
	}
}

// TestShadeAxisIsHeadlessAndRainKeepsItsHead pins the one structural decision the
// design turns on.
//
// The brief asked for rain's ramp to fold in as the one-hue case of this grid. It
// cannot: rain's head has to reach pal.Fg, and the climb from an anchor to Fg is
// 2.1 L* from Cyan but 16.4 from Danger — so a shared blow-out is an 8x stronger
// gesture at the warm end of the hue axis than at the cool end. The two ramps want
// different tops (rain's is white, a colour-carrying field's is its own hue), which
// is why they are two tables sharing one curve rather than one table.
//
// If someone later "unifies" them, this is what should fail.
func TestShadeAxisIsHeadlessAndRainKeepsItsHead(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()
	colors := splashGradientColors(pal)

	fgLum := func() float64 {
		c, err := colorful.Hex(pal.Highlight)
		require.NoError(t, err)
		l, _, _ := c.Lab()
		return l * 100
	}()

	// Rain's ramp reaches the foreground white.
	require.InDeltaf(t, fgLum, rainRampLum(t, pal, splashRainStops-1), 0.5,
		"rain's head must still blow out to pal.Fg")

	// The shade axis does not: every column tops out at its own hue.
	for h := 0; h < splashLUTSize; h++ {
		top := shadeLum(t, colors, h, splashLumStops-1)
		require.Lessf(t, top, fgLum+0.5,
			"hue %d's shade axis must top out at its own hue, not blow out to white "+
				"(L* %.1f vs Fg %.1f)", h, top, fgLum)
	}
	// And the warm end proves it is not vacuous: Danger is 16.4 L* below Fg, so a
	// column that blew out to white would be caught here by a wide margin.
	require.Lessf(t, shadeLum(t, colors, 0, splashLumStops-1), fgLum-10.0,
		"the warm end of the hue axis must stay far below the head white")
}

// TestSharedLumCurveMatchesRainsTail proves the "one curve" half of "two tables,
// one curve": rain's tail and every shade column are the same function, evaluated
// on different axes. If the extraction ever drifts, rain's shape moves with it and
// this is the cheapest place to notice.
func TestSharedLumCurveMatchesRainsTail(t *testing.T) {
	pal := splashTestPalette()
	base, err := colorful.Hex(pal.A3)
	require.NoError(t, err)

	for i := 0; i < splashRainStops; i++ {
		tt := float64(i) / float64(splashRainStops-1)
		if tt >= rainRampHeadAt {
			continue // the head blend is rain's alone
		}
		require.Equalf(t, rainRampHexAt(pal, i), splashLumHexAt(base, tt/rainRampHeadAt, rainChromaHold),
			"rain's tail stop %d must be the shared luminance curve, not a second copy of it", i)
	}
}

// shadeDecoder inverts the shade grid so a rendered cell can be read back as the
// (hue, luminance) it actually carries.
//
// Note the ambiguity it reports rather than hides. The luminance curve desaturates
// toward the floor (chroma falls with L*), so the dim rows of neighbouring hue
// columns converge: at stop 1 the 20 columns collapse to 16 distinct colours, and
// at stop 0 to three. Luminance stays recoverable — the columns converge *within* a
// row, never across rows — but hue does not, so a decoder that returned a single
// hue would be quietly making one up. Callers get the candidate set and assert
// membership.
type shadeDecoder struct {
	lum map[string]int   // prefix → luminance stop (unambiguous)
	hue map[string][]int // prefix → the hue columns that could have emitted it
}

func newShadeDecoder(t *testing.T, lut *splashLUT) *shadeDecoder {
	t.Helper()
	d := &shadeDecoder{lum: map[string]int{}, hue: map[string][]int{}}
	for h := 0; h < splashLUTSize; h++ {
		for l := 0; l < splashLumStops; l++ {
			p := lut.shade[h*splashLumStops+l].prefix
			if prev, ok := d.lum[p]; ok {
				require.Equalf(t, prev, l,
					"prefix %q decodes to luminance %d and %d: the shade grid's rows "+
						"overlap, so a rendered cell's brightness is unrecoverable", p, prev, l)
			}
			d.lum[p] = l
			d.hue[p] = append(d.hue[p], h)
		}
	}
	return d
}

// shadeStopGrid renders a variant and decodes each cell back to the luminance stop
// it carries (-1 for blank), by tracking the run the emitter opened.
//
// It reads the rendered bytes rather than recomputing the field, and that is the
// whole point: rain shipped a bug where ops.dimToRim was declared and never read,
// and every brightness test in splash_rain_test.go was structurally blind to it
// because they all asserted Pass-1 math and never Pass 2's envelope. Mirrors
// rainStopGrid, which exists for the same reason.
func shadeStopGrid(t *testing.T, w, h, frame int, pal Palette, v Variant) ([][]int, [][]string) {
	t.Helper()
	d := newShadeDecoder(t, buildLUTAmbient(pal))
	out := renderSplashField(w, h, frame, pal,
		centeredFocalRow(h), v)

	stops := make([][]int, 0, h)
	prefixes := make([][]string, 0, h)
	for _, line := range strings.Split(out, "\n") {
		row := make([]int, 0, w)
		prow := make([]string, 0, w)
		cur, curP := -1, ""
		for len(line) > 0 {
			if seq := sgrPrefix(line); seq != "" {
				stop, ok := d.lum[seq]
				if !ok {
					stop = -1 // a reset, or a run outside the shade grid
				}
				cur, curP = stop, seq
				line = line[len(seq):]
				continue
			}
			r, size := utf8.DecodeRuneInString(line)
			if r == ' ' {
				row, prow = append(row, -1), append(prow, "")
			} else {
				row, prow = append(row, cur), append(prow, curP)
			}
			line = line[size:]
		}
		require.Lenf(t, row, w, "row must be exactly %d cells", w)
		stops, prefixes = append(stops, row), append(prefixes, prow)
	}
	return stops, prefixes
}

// TestShadedFieldVariesLuminanceAndHoldsHue is the assertion the entire change is
// for, made where it renders rather than where it is computed.
//
// Two halves, and the second is the one that matters. That luminance varies is
// merely evidence the new path runs at all. That *hue is unchanged by lumRange* is
// the claim: it is what makes density and luminance two channels rather than one
// dressed up as two. If shading a cell also moved its colour, this would be a
// fancier way to do what the density ramp already does.
func TestShadedFieldVariesLuminanceAndHoldsHue(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()
	const w, h = 120, 40

	// The control is synthesized rather than shipped. Every variant used to be at
	// lumRange 0 and the nebula was the natural one to read it off; V5 retired the
	// organic fields and no survivor is at 0, so the knob drives it. The tunnel
	// because it must be a field that reaches shadeAt at all — rain draws from its
	// own ramp and never arrives — and because it is the densest, so the spread
	// below is measured over most of the pane.
	withLumRange(t, 0)
	flat, flatPre := shadeStopGrid(t, w, h, 7, pal, Tunnel)
	// At lumRange 0 every cell is blank or at its hue's *full* colour.
	//
	// Not "no cell decodes into the grid" — it cannot be, and asserting it would be
	// vacuous. The grid's top stop is pinned to the gradient affix, so a lumRange 0
	// cell decodes as stop 15 and is byte-indistinguishable from a shaded one at
	// full luminance. That is the pin working, not a leak. What this rules out is
	// any *dimmed* stop: the luminance axis must be entirely unused here, so that
	// the spread found below is the new path running rather than noise.
	for _, s := range flatten(flat) {
		require.Containsf(t, []int{-1, splashLumStops - 1}, s,
			"at lumRange 0 every cell must be blank or at its hue's full colour; got stop %d", s)
	}

	withLumRange(t, 0.5)
	lit, litPre := shadeStopGrid(t, w, h, 7, pal, Tunnel)

	seen := map[int]bool{}
	for _, row := range lit {
		for _, s := range row {
			if s >= 0 {
				seen[s] = true
			}
		}
	}
	require.Greaterf(t, len(seen), 4,
		"a shaded field must span the luminance axis; it only reached %d stops", len(seen))
	require.NotContains(t, seen, 0,
		"luminance stop 0 is near-black ink on a dark pane: it must render blank, not painted")

	// Hue: every cell lit under both must carry the same hue column.
	d := newShadeDecoder(t, buildLUTAmbient(pal))
	lutFlat := buildLUTAmbient(pal)
	hueOf := map[string]int{}
	for i, a := range lutFlat.affix {
		hueOf[a.prefix] = i
	}
	checked, ambiguous := 0, 0
	for r := range lit {
		for c := range lit[r] {
			if lit[r][c] < 0 || flat[r][c] == -1 && flatPre[r][c] == "" {
				continue
			}
			want, ok := hueOf[flatPre[r][c]]
			if !ok {
				continue // a star or blank at lumRange 0
			}
			cands := d.hue[litPre[r][c]]
			if len(cands) == 0 {
				continue
			}
			if len(cands) > 1 {
				ambiguous++
			}
			require.Containsf(t, cands, want,
				"cell (%d,%d) is hue %d at lumRange 0 but could not have been at 0.5 "+
					"(candidates %v): shading a cell must not move its colour", r, c, want, cands)
			checked++
		}
	}
	require.Greaterf(t, checked, 500, "only %d cells were comparable; the test proves too little", checked)
	t.Logf("hue held across %d cells (%d decoded ambiguously by the dim-end convergence)", checked, ambiguous)
}

// TestShadedFaintCellsAreDimNotTiny is the defect itself, asserted on rendered
// glyphs.
//
// "Dim" and "tiny" are one instruction today, so a fading field degenerates into a
// scatter of "." and "·" — confetti, not dimming. The promise is not that a shaded
// field never renders a dot; it is that the faint end reaches for a heavier mark
// than it would have, and pays for it in luminance.
func TestShadedFaintCellsAreDimNotTiny(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()
	const w, h = 120, 40

	faintFrac := func() float64 {
		out := ansi.Strip(renderSplashField(w, h, 7, pal,
			centeredFocalRow(h), Ripple))
		lit, faint := 0, 0
		for _, ch := range out {
			switch ch {
			case ' ', '\n':
			case '.', '·':
				faint++
				lit++
			default:
				lit++
			}
		}
		require.Positive(t, lit, "the field must light something")
		return float64(faint) / float64(lit)
	}

	// The lumRange-0 control is synthesized: no variant ships 0 since V5 retired
	// the organic fields, and this needs a field with a broad faint *band* to
	// measure — ripple's packet halo is most of its area, and it is the reason
	// ripple stops at 0.75 rather than going to 1. The tunnel has no such band at
	// all: its wall is multiplied by fog, so a cell is lit or crushed to blank with
	// almost nothing in between, and it renders 0.0% faint dots at either setting.
	withLumRange(t, 0)
	flat := faintFrac()
	withLumRange(t, 0.5)
	shaded := faintFrac()

	require.Lessf(t, shaded, flat,
		"the ramp's two lightest dots are %.1f%% of lit cells at lumRange 0 and %.1f%% at 0.5; "+
			"the split must trade mark *size* for brightness, not keep making faint cells small",
		flat*100, shaded*100)
	t.Logf("faint-dot share: %.1f%% at lumRange 0 → %.1f%% at 0.5", flat*100, shaded*100)
}

// TestSplashShadeEndpointsAreExact is the test the whole change rests on.
//
// splashShade's two endpoints are not approximations that happen to land in the
// right place — they are the shading every shipped variant already uses, and they
// have to come back bit-exact or the byte-identity promise is a coincidence
// waiting to break. lumRange 0 is today's density-only shading (the glyph carries
// everything, the colour is the gradient's); lumRange 1 is rain's (a
// constant-weight glyph, all brightness in the colour).
//
// Asserted with Equal, not InDelta, deliberately: "within an epsilon" is exactly
// what would let a stray multiply through.
func TestSplashShadeEndpointsAreExact(t *testing.T) {
	for _, lit := range []float64{0, 0.001, 0.0083, 0.1, 0.5, 0.7734, 1} {
		dens, lumT := splashShade(lit, 0)
		require.Equalf(t, lit, dens, "lumRange 0: density must carry lit untouched (lit=%v)", lit)
		require.Equalf(t, 1.0, lumT, "lumRange 0: luminance must sit at the top, unused (lit=%v)", lit)

		dens, lumT = splashShade(lit, 1)
		require.Equalf(t, 1.0, dens, "lumRange 1: the glyph must hold full weight (lit=%v)", lit)
		require.Equalf(t, lit, lumT, "lumRange 1: luminance must carry lit untouched (lit=%v)", lit)
	}
}

// TestSplashShadeMovesBrightnessRatherThanAddingIt pins the invariant that makes
// the split a *split*: whatever rides the colour is taken off the glyph, so the
// product is the brightness the field asked for. A formulation that failed this
// would silently brighten or darken every opted-in variant relative to lumRange 0,
// and the screenshot round would be tuning around the error instead of the design.
func TestSplashShadeMovesBrightnessRatherThanAddingIt(t *testing.T) {
	for _, lumRange := range []float64{0.1, 0.35, 0.5, 0.7, 0.9} {
		for _, lit := range []float64{1e-6, 0.001, 0.05, 0.2, 0.5, 0.9, 1} {
			dens, lumT := splashShade(lit, lumRange)
			require.InEpsilonf(t, lit, dens*lumT, 1e-12,
				"lumRange %v, lit %v: dens*lum must reconstruct lit (got %v*%v = %v)",
				lumRange, lit, dens, lumT, dens*lumT)
		}
	}
}

// TestSplashShadeLiftsTheFaintEnd is the defect, asserted directly.
//
// Brightness and glyph identity are the same channel today: a faint cell is
// necessarily a *small* mark, so the faint end of every field degenerates to "."
// and "·" — confetti rather than dimming. The split exists so a faint cell can be
// dim without being tiny, which means its density must come out strictly higher
// than lumRange 0 would give it, with the difference paid for in luminance.
func TestSplashShadeLiftsTheFaintEnd(t *testing.T) {
	const lit = 0.1 // a faint cell: glyph index int(0.1*11) = 1, i.e. "."
	dens, lumT := splashShade(lit, 0.5)

	require.Greaterf(t, dens, lit,
		"the density channel must be lifted above raw lit (%v), else a faint cell is still a small mark", lit)
	require.Lessf(t, lumT, 1.0, "the lift must be paid for in luminance, not conjured")

	// And show it in the units that actually render: the glyph the ramp picks.
	maxGlyph := len([]rune(splashRamp)) - 1
	flat := clampInt(int(lit*float64(maxGlyph)), 0, maxGlyph)
	split := clampInt(int(dens*float64(maxGlyph)), 0, maxGlyph)
	require.Greaterf(t, split, flat,
		"lit %v renders as %q at lumRange 0 and %q at 0.5; the split must reach for a heavier mark",
		lit, []rune(splashRamp)[flat], []rune(splashRamp)[split])
}

// TestSplashShadeIsFiniteAndBounded guards the cross-arch property the golden
// strategy rests on. Both channels are multiplied by a stop count and truncated to
// an int, and a float→int conversion of a NaN or an infinity is
// implementation-defined in Go (amd64 → minint, arm64 → 0) — it would silently
// differ per architecture rather than fail anywhere.
//
// The lit <= 0 case is the live one: Log(0) is -Inf, so lit/Exp(lumRange·-Inf) is
// 0/0 = NaN without the guard. (Under a two-Pow formulation that guard would be
// dead code, which is exactly why it is worth pinning here rather than trusting
// the comment.)
func TestSplashShadeIsFiniteAndBounded(t *testing.T) {
	for _, lumRange := range []float64{0, 0.25, 0.5, 0.75, 1} {
		for _, lit := range []float64{0, 1e-300, 1e-12, 0.5, 1} {
			dens, lumT := splashShade(lit, lumRange)
			require.Falsef(t, math.IsNaN(dens) || math.IsInf(dens, 0),
				"density is not finite at lit=%v lumRange=%v (got %v)", lit, lumRange, dens)
			require.Falsef(t, math.IsNaN(lumT) || math.IsInf(lumT, 0),
				"luminance is not finite at lit=%v lumRange=%v (got %v)", lit, lumRange, lumT)
			require.GreaterOrEqualf(t, dens, 0.0, "density below 0 at lit=%v lumRange=%v", lit, lumRange)
			require.LessOrEqualf(t, dens, 1.0, "density above 1 at lit=%v lumRange=%v", lit, lumRange)
			require.GreaterOrEqualf(t, lumT, 0.0, "luminance below 0 at lit=%v lumRange=%v", lit, lumRange)
			require.LessOrEqualf(t, lumT, 1.0, "luminance above 1 at lit=%v lumRange=%v", lit, lumRange)
		}
	}
}

// TestSplashShadeIsMonotone pins that both channels still read as brightness: a
// brighter cell may not come back with a lighter mark or a darker colour. A split
// that inverted anywhere would render the field's own gradient backwards.
func TestSplashShadeIsMonotone(t *testing.T) {
	for _, lumRange := range []float64{0, 0.35, 0.5, 0.9, 1} {
		prevD, prevL := -1.0, -1.0
		for i := 0; i <= 200; i++ {
			lit := float64(i) / 200
			dens, lumT := splashShade(lit, lumRange)
			require.GreaterOrEqualf(t, dens, prevD,
				"density fell as lit rose to %v at lumRange %v", lit, lumRange)
			require.GreaterOrEqualf(t, lumT, prevL,
				"luminance fell as lit rose to %v at lumRange %v", lit, lumRange)
			prevD, prevL = dens, lumT
		}
	}
}

// TestShadeAffixBracketsMatchRender extends TestSplashAffixBracketsMatchRender's
// invariant to the shade grid: the emitter writes a cached prefix, the cells, then
// a cached suffix, and that is only safe while the bracket really is a pure
// wrapper around whatever Render would have produced — on every profile, including
// the colorless one where it must collapse to plain text.
//
// It lives here rather than in that loop because the grid has no parallel
// []lipgloss.Style to walk, and because its top stop is *pinned* to the gradient
// affix rather than recomputed — so the two halves have to be asserted against
// different sources, which is exactly the discrepancy the pin exists to create.
func TestShadeAffixBracketsMatchRender(t *testing.T) {
	for name, prof := range map[string]termenv.Profile{
		"truecolor": termenv.TrueColor,
		"ansi256":   termenv.ANSI256,
		"ascii":     termenv.Ascii,
	} {
		t.Run(name, func(t *testing.T) {
			withColorProfile(t, prof)
			pal := splashTestPalette()
			lut := buildLUTAmbient(pal)
			colors := splashGradientColors(pal)
			require.Equal(t, lut.rainIndex()+len(lut.rain), lut.shadeIndex(),
				"the shade grid must start exactly one past rain's last stop")

			for _, content := range []string{"x", "▓▓▓", "   ", "·-=#"} {
				for h := 0; h < splashLUTSize; h++ {
					for l := 0; l < splashLumStops; l++ {
						a := lut.shade[h*splashLumStops+l]
						want := lipgloss.NewStyle().Foreground(lipgloss.Color(shadeHexAt(colors, h, l)))
						if l == splashLumStops-1 {
							// The pin: the brightest stop is the gradient's own colour,
							// not the curve's round-tripped approximation of it.
							want = lut.styles[h]
						}
						require.Equalf(t, want.Render(content), a.prefix+content+a.suffix,
							"shade[%d][%d] must bracket %q exactly as Render does", h, l, content)
					}
				}
			}
			if prof == termenv.Ascii {
				require.Empty(t, lut.shade[1].prefix, "a colorless profile must degrade to plain")
			}
		})
	}
}

// BenchmarkRenderSplashShaded is the cost of the luminance channel, measured
// rather than reasoned about.
//
// It exists because the estimate that motivated this design ("one extra multiply
// per cell") was wrong by an order of magnitude: the split is a Log and an Exp,
// which are tens of nanoseconds each and do not pipeline. The lumRange 0 row is
// the control: the cost of the field with the channel switched off, via the
// short-circuit in shadeAt.
//
// That row used to be what every shipped variant ran, which made it the row that
// must not move. It is not any more — V5 retired the fields that wanted their
// stipple, and all three survivors are at 0.75 or above — so it is now the
// baseline the other row is read against, and the path a future variant would
// take, rather than a promise about the roster.
//
// Truecolor is forced for the reason forceBenchTrueColor documents: a bench
// binary's stdout is not a TTY, so the emitter would otherwise be timed with
// nothing to emit.
func BenchmarkRenderSplashShaded(b *testing.B) {
	forceBenchTrueColor(b)
	pal := splashTestPalette()
	for _, s := range []struct {
		name string
		w, h int
	}{{"80x30", 80, 30}, {"240x60", 240, 60}} {
		for _, r := range []float64{0, 0.5} {
			b.Run(fmt.Sprintf("%s/lum%.1f", s.name, r), func(b *testing.B) {
				withLumRange(b, r)
				focalRow := centeredFocalRow(s.h)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = renderSplashField(s.w, s.h, i, pal, focalRow, Tunnel)
				}
			})
		}
	}
}

// TestShadedVariantsKeepTheContract runs the render contract at lumRange 0.5 for
// every variant, because opting one in must not be able to break it.
//
// This is the guard that matters whenever the knob moves. The blank border
// changes *owner* at lumRange > 0: it used to be carried by the dither staying
// under one glyph step (lit=0 rounds to glyph 0), but a lifted density reaches
// glyph 2 or 3 at lit≈0, so that argument stopped holding — and the dither is
// gone with the fields that wanted it, so nothing is left of it. What keeps the
// border is splashShade's lit <= 0 guard and shadeAt's luminance gate. The
// border rows survive by construction either way — edgeY is exactly 0 there and
// gates the whole cell body — but the columns do not, and the field measurably
// grows toward them (the leftmost lit column moves from 8 to 2 at 240 wide).
//
// Contract mirrored from TestSplashVariantsContract, which only ever sees lumRange 0.
func TestShadedVariantsKeepTheContract(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	withLumRange(t, 0.5)
	pal := splashTestPalette()
	const w, h = 80, 30

	for name, v := range splashTestVariants() {
		a := renderSplashField(w, h, 5, pal, centeredFocalRow(h), v)
		require.Equalf(t, a, renderSplashField(w, h, 5, pal, centeredFocalRow(h), v),
			"%s: same inputs must render identically when shaded", name)
		require.NotEqualf(t, a, renderSplashField(w, h, 6, pal, centeredFocalRow(h), v),
			"%s: consecutive frames must still differ when shaded", name)

		lines := stripLines(a)
		require.Lenf(t, lines, h, "%s: line count", name)
		for i, l := range lines {
			require.Equalf(t, w, len([]rune(l)), "%s: line %d width", name, i)
		}
		require.Equalf(t, strings.Repeat(" ", w), lines[0], "%s: top row must stay blank when shaded", name)
		require.Equalf(t, strings.Repeat(" ", w), lines[h-1], "%s: bottom row must stay blank when shaded", name)
	}
}
