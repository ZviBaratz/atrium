package splash

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
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
// TestSplashVariantsContract checks one frame pair per variant, which a dense
// field like the tunnel passes trivially: thousands of lit cells, so something
// always crosses a quantization boundary. Rain lights far fewer, and its heads only cross a row
// every ~4 frames — so had brightness been quantized to integer rows, most
// consecutive pairs would be *identical* and that contract check would pass or
// fail on a coin flip depending on which frames it happened to sample.
//
// A run of frames is what actually distinguishes "animates" from "got lucky".
func TestRainAnimatesEveryFrame(t *testing.T) {
	pal := splashTestPalette()
	prev := ""
	for f := 0; f < 30; f++ {
		got := renderSplashField(80, 30, f, pal, centeredFocalRow(30), Rain)
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
	// and further back it must be lower still. Step back in units of the
	// shortest tail any layer can draw, so the samples land inside a tail
	// whichever layer owns this column.
	shortestTail := rainLayers[len(rainLayers)-1].period * rainTailFracMin
	near, _ := splashRainAt(col, 0, 0, headDy-rainHeadR*1.5, 0)
	far, _ := splashRainAt(col, 0, 0, headDy-rainHeadR*1.5-shortestTail*0.5, 0)
	require.Less(t, near, headVal, "the cell behind the head must be dimmer than the head")
	require.Less(t, far, near, "the tail must keep fading with distance behind the head")
	require.Greater(t, near, 0.0, "the tail must be lit at all, not merely absent")
}

// TestRainIsContinuousInPhase is the mechanism behind TestRainAnimatesEveryFrame,
// asserted directly rather than through the renderer: a phase nudge far smaller
// than a row must still move the field.
//
// Measured over a population rather than one cell, because two parts of the
// design are deliberately flat and would defeat a single-cell probe: a cell on
// the head's plateau holds full brightness for the ~5 frames it takes to cross
// (see rainHeadFlat), and most cells are in gaps at all (see rainDensity).
//
// The threshold discriminates. Measured on this field, a one-frame step moves
// ~46% of (cell, frame) pairs; quantizing the distance-to-head to whole rows —
// the naive design this replaced — drops it to ~13%, since a cell can then only
// change when a head crosses a row boundary.
func TestRainIsContinuousInPhase(t *testing.T) {
	moved, total := 0, 0
	for col := 0; col < 40; col++ {
		for i := 0; i < 12; i++ {
			dy := float64(i) * cellAspect // on the row grid, as the renderer samples
			for f := 0; f < 20; f++ {
				a, _ := splashRainAt(col, 0, 0, dy, float64(f)*driftPerFrame)
				b, _ := splashRainAt(col, 0, 0, dy, float64(f+1)*driftPerFrame)
				if math.Abs(a-b) > 1e-9 {
					moved++
				}
				total++
			}
		}
	}
	frac := float64(moved) / float64(total)
	require.Greaterf(t, frac, 0.30,
		"only %.0f%% of (cell, frame) pairs moved on a one-frame phase step; rain's "+
			"brightness must be continuous in the distance to the head, not quantized "+
			"to rows (quantized measures ~13%%)", frac*100)
}

// TestRainRendersAsLinesNotDots is the regression for the failure that took two
// rounds to name.
//
// The field was never the problem — the streams were there, 3-to-12 rows long,
// the whole time. What was wrong is what they were *made of*. Rain first shaded
// through the glyph density ramp, which substitutes size for brightness: the
// faint end of every tail became "." and "·", and a column of dots is not a
// dimming line, it is confetti. The fix is a luminance ramp, so a dim cell is
// the same mark as a bright one, darker.
//
// Assert it where it shows: a rendered column's lit cells must nearly all carry
// real ink, not the ramp's two lightest marks.
func TestRainRendersAsLinesNotDots(t *testing.T) {
	const w, h = 80, 40
	out := ansi.Strip(renderSplashField(w, h, 40, splashTestPalette(),
		h/2, Rain))
	rows := strings.Split(out, "\n")

	lit, faint := 0, 0
	for _, r := range rows {
		for _, ch := range r {
			switch ch {
			case ' ':
			case '.', '·':
				faint++
				lit++
			default:
				lit++
			}
		}
	}
	require.Positive(t, lit, "rain must light something")
	require.Lessf(t, float64(faint)/float64(lit), 0.05,
		"%d of %d lit cells are the ramp's lightest dots; rain must shade by "+
			"luminance, not by glyph size", faint, lit)
}

// TestRainStreamsAreLong pins the other half of "reads as rain": a stream has to
// be a *run*. Isolated lit cells are noise however they are coloured, so this
// fails both a broken field and a shading scheme that punches holes in a tail.
func TestRainStreamsAreLong(t *testing.T) {
	const w, h = 80, 40
	out := ansi.Strip(renderSplashField(w, h, 40, splashTestPalette(),
		h/2, Rain))
	rows := strings.Split(out, "\n")

	longest, runs, cells := 0, 0, 0
	for col := 0; col < w; col++ {
		run := 0
		for _, r := range rows {
			rs := []rune(r)
			if col < len(rs) && rs[col] != ' ' {
				run++
				cells++
				continue
			}
			if run > 0 {
				runs++
				if run > longest {
					longest = run
				}
			}
			run = 0
		}
		if run > 0 {
			runs++
		}
	}
	require.Positive(t, runs, "rain must render some streams")
	require.Greaterf(t, longest, 8, "the longest stream is only %d rows", longest)
	require.Greaterf(t, float64(cells)/float64(runs), 3.0,
		"streams average %.1f cells; rain must fall in runs, not specks",
		float64(cells)/float64(runs))
}

// TestRainRampIsALuminanceRamp pins the ramp's shape, which is the thing the hue
// gradient could not provide: it must actually get darker toward the tail and
// brighter toward the head. A ramp that merely changed hue would pass every
// other test here and still render as the flat confetti this replaced.
func TestRainRampIsALuminanceRamp(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()
	lut := buildLUTAmbient(pal)
	require.Len(t, lut.rain, splashRainStops)

	lum := func(hex string) float64 {
		c, err := colorful.Hex(hex)
		require.NoError(t, err)
		l, _, _ := c.Lab()
		return l
	}
	// Recover each stop's colour from its SGR prefix by rebuilding the ramp's
	// intent rather than parsing escapes: assert on the colours directly.
	prev := -1.0
	for i := 0; i < splashRainStops; i++ {
		got := lum(rainRampHexAt(pal, i))
		require.Greaterf(t, got, prev, "stop %d must be brighter than stop %d", i, i-1)
		prev = got
	}
	require.Lessf(t, lum(rainRampHexAt(pal, 0)), 0.12,
		"the tail's darkest stop must read as unlit")
	require.Greaterf(t, lum(rainRampHexAt(pal, splashRainStops-1)), 0.75,
		"the head's brightest stop must read as white-hot")
	// And the low end must stay off true black. The floor itself never renders
	// (a cell that dim goes blank), but it anchors the shape of the stops that
	// do — and a minimum-contrast terminal rewrites those to something legible,
	// speckling the tail with the very artifact this ramp removes.
	require.Positive(t, lum(rainRampHexAt(pal, 0)), "the floor must not be pure black")
	require.Positive(t, lum(rainRampHexAt(pal, 1)),
		"the dimmest stop that actually renders must not be pure black")
}

// TestRainTailsLeaveGaps pins the constraint that a comment stated and the code
// then broke.
//
// A stream's tail must be shorter than half its layer's period, or it reaches
// the head behind it and the column renders as one unbroken line. The tail range
// was originally absolute (8–26) while the periods are per-layer (58/42/30), so
// the mid and far layers ran solid — and a field with a glyph in every cell is a
// texture, not rain. The gaps carry the rhythm.
func TestRainTailsLeaveGaps(t *testing.T) {
	require.Less(t, rainTailFracMax, 0.5,
		"a tail longer than half its period leaves the column with no gap in it")
	require.Less(t, rainTailFracMin, rainTailFracMax, "the tail range must be a range")
	require.Positive(t, rainTailFracMin, "streams need a tail")

	// And prove it end to end: no column may be solid over a tall pane.
	const w, h = 80, 60
	out := ansi.Strip(renderSplashField(w, h, 40, splashTestPalette(),
		h/2, Rain))
	rows := strings.Split(out, "\n")
	for col := 0; col < w; col++ {
		lit := 0
		for _, r := range rows {
			rs := []rune(r)
			if col < len(rs) && rs[col] != ' ' {
				lit++
			}
		}
		require.Lessf(t, lit, len(rows)-2,
			"column %d is lit in %d of %d rows — a solid column is a texture, not a stream",
			col, lit, len(rows))
	}
}

// TestRainHeadAlwaysLandsOnACell pins the head's plateau against the row grid.
//
// Rows sample the field every cellAspect units, so a head that is a pure peak is
// only caught when a row happens to land on it: at the original 3.2-unit radius
// the bright part spanned 43% of a row, so over half of all heads rendered with
// no white cell and the stream had nothing for the eye to track. The plateau has
// to be wider than one row for a head to be guaranteed.
func TestRainHeadAlwaysLandsOnACell(t *testing.T) {
	require.Greaterf(t, rainHeadFlat*2, cellAspect,
		"the head plateau spans %.2f units against a %.2f-unit row pitch; a head "+
			"that narrow blinks as it falls between rows", rainHeadFlat*2, cellAspect)
	require.Greater(t, rainHeadR, rainHeadFlat, "the lobe must fall off outside its plateau")

	// Sweep a head across a row boundary: some cell must reach the ramp's top
	// stop at every sub-row offset it can occupy.
	stops := splashRainStops
	for i := 0; i < 24; i++ {
		offset := float64(i) / 24 * cellAspect // where the head sits between rows
		best := 0
		for row := -4; row <= 4; row++ {
			dy := float64(row)*cellAspect + offset
			// A synthetic head at dy=0: sample the lobe directly.
			d0 := math.Abs(dy)
			lit := 0.0
			switch {
			case d0 <= rainHeadFlat:
				lit = 1
			case d0 < rainHeadR:
				lit = (rainHeadR - d0) / (rainHeadR - rainHeadFlat)
			}
			if g := clampInt(int(lit*float64(stops-1)), 0, stops-1); g > best {
				best = g
			}
		}
		require.Equalf(t, stops-1, best,
			"a head offset %.2f between rows peaks at ramp stop %d, not the top: "+
				"it renders without white", offset, best)
	}
}

// rainRampLum is a ramp stop's L*, for the luminance assertions below.
func rainRampLum(t *testing.T, pal Palette, stop int) float64 {
	t.Helper()
	c, err := colorful.Hex(rainRampHexAt(pal, stop))
	require.NoError(t, err)
	l, _, _ := c.Lab()
	return l * 100
}

// rainStopFor is where a brightness lands on the ramp, as Pass 2 quantizes it.
func rainStopFor(lit float64) int {
	return clampInt(int(lit*float64(splashRainStops-1)), 0, splashRainStops-1)
}

// TestRainHeadOutshinesItsTail is the one that took three screenshots to find.
//
// A head reads as a head because of the step down to the cell behind it, and
// nothing else — not its glyph, which is the same weight as every other, and not
// its lobe's width, which only decides whether it lands on a row at all. The
// step has to be big.
//
// It was not. rainTailAmp was 0.82, which put the tail's brightest cell on ramp
// stop 12 (L* 78.0) against a head at 81.9 — under four points apart, so the
// head was the same brightness as its own tail and vanished into it. The instinct
// is to brighten the head, and it is unavailable: pal.Fg is L* 81.9, only 2.2
// above pal.Cyan, so the ramp's top four stops are one colour and there is
// nothing brighter in the palette to reach for. The tail has to come down
// instead.
func TestRainHeadOutshinesItsTail(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()

	for li, L := range rainLayers {
		head := rainRampLum(t, pal, rainStopFor(1.0*L.bright))
		tail := rainRampLum(t, pal, rainStopFor(rainTailAmp*L.bright))
		require.Greaterf(t, head-tail, 15.0,
			"layer %d's head is only %.1f L* above its own tail-top (head %.1f, tail %.1f); "+
				"a head that close to its tail does not read as a head",
			li, head-tail, head, tail)
	}
}

// TestRainLayersSeparateInBrightness pins depth to the cue that actually carries
// it. An earlier attempt gave each layer its own hue, which says *which* layer a
// glyph belongs to but never which is nearer — and read, correctly, as "more
// colourful, not depth". Brightness is what the eye reads as distance, so the
// layers' heads must land distinctly apart on the ramp.
func TestRainLayersSeparateInBrightness(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()

	prev := 999.0
	for li, L := range rainLayers {
		head := rainRampLum(t, pal, rainStopFor(1.0*L.bright))
		require.Lessf(t, head, prev-10.0,
			"layer %d's head (L* %.1f) must sit clearly below the layer in front of it "+
				"(L* %.1f); layers that close read as one plane", li, head, prev)
		prev = head
	}
}

// sgrPrefix returns the SGR sequence at the head of s, or "" if s starts with
// text. Each affix the emitter opens a run with is exactly one such sequence.
func sgrPrefix(s string) string {
	if !strings.HasPrefix(s, "\x1b[") {
		return ""
	}
	if i := strings.IndexByte(s, 'm'); i >= 0 {
		return s[:i+1]
	}
	return ""
}

// rainStopGrid renders rain and decodes each cell back to the luminance stop it
// actually carries (-1 for a blank cell), by tracking the run the emitter
// opened. It reads the rendered bytes rather than recomputing the field, which
// is the point: the brightness assertions elsewhere in this file work off the
// layer table and so never exercise Pass 2's envelope at all.
func rainStopGrid(t *testing.T, w, h, frame int, pal Palette) [][]int {
	t.Helper()
	lut := buildLUTAmbient(pal)
	stopOf := make(map[string]int, len(lut.rain))
	for i, a := range lut.rain {
		stopOf[a.prefix] = i
	}
	out := renderSplashField(w, h, frame, pal,
		centeredFocalRow(h), Rain)

	grid := make([][]int, 0, h)
	for _, line := range strings.Split(out, "\n") {
		row := make([]int, 0, w)
		cur := -1
		for len(line) > 0 {
			if seq := sgrPrefix(line); seq != "" {
				stop, ok := stopOf[seq]
				if !ok {
					stop = -1 // a reset, or a run this variant should not emit
				}
				cur = stop
				line = line[len(seq):]
				continue
			}
			r, size := utf8.DecodeRuneInString(line)
			if r == ' ' {
				row = append(row, -1)
			} else {
				row = append(row, cur)
			}
			line = line[size:]
		}
		require.Lenf(t, row, w, "row must be exactly %d cells", w)
		grid = append(grid, row)
	}
	return grid
}

// TestRainKeepsItsHeadsAwayFromTheFocalPoint is the regression for a defect that
// every other test in this file was structurally unable to see.
//
// Pass 2 used to carry a radial dim — brightness falling with distance from the
// wordmark — which *inverts* rain's depth, since a near stream at the rim then
// renders dimmer than a far stream at the middle, and costs every head out there
// the top of the ramp, the only white on screen and the thing the eye tracks.
// Rain declined it via splashOps.dimToRim, and the field was declared and then
// not consumed: Pass 2 read the package constant instead, so rain got the 42% dim
// it had opted out of and a full-brightness head at the rim landed on stop 8 of
// 15.
//
// V5 deleted the dim outright — no surviving variant wanted one — so this no
// longer guards an opt-out. What it still guards is the envelope: nothing between
// the layer table and the emitted cell may darken a head by where it happens to
// be. That was always the claim; the ops field was only how rain asked for it.
//
// The brightness tests above could not catch it. They compute stops straight
// from the layer table (rainStopFor(L.bright)), which is Pass-1 math — the
// envelope never enters. This one reads the rendered cells back instead.
func TestRainKeepsItsHeadsAwayFromTheFocalPoint(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	pal := splashTestPalette()
	const w, h = 120, 40
	const top = splashRainStops - 1

	// The vignette legitimately fades the border, so sample only where it has
	// reached full strength — what is under test is everything else.
	marginX := math.Max(1, float64(w)*edgeVignetteFrac)
	marginY := math.Max(1, float64(h)*edgeVignetteFrac)
	cx, cyFocal := float64(w-1)/2, float64((h-1)/2)
	maxD := math.Hypot(
		math.Max(cx, float64(w-1)-cx),
		math.Max(cyFocal, float64(h-1)-cyFocal)*cellAspect)

	best, sampled := -1, 0
	for frame := 0; frame < 12 && best < top; frame++ {
		grid := rainStopGrid(t, w, h, frame, pal)
		for row := range grid {
			if math.Min(float64(row), float64(h-1-row)) < marginY {
				continue // inside the vertical vignette
			}
			dy := (float64(row) - cyFocal) * cellAspect
			for col, stop := range grid[row] {
				if math.Min(float64(col), float64(w-1-col)) < marginX {
					continue // inside the horizontal vignette
				}
				// Far from the wordmark: where the radial dim bites hardest.
				if math.Hypot(float64(col)-cx, dy)/maxD < 0.5 {
					continue
				}
				sampled++
				if stop > best {
					best = stop
				}
			}
		}
	}

	require.Positive(t, sampled, "the far region must contain sampleable cells")
	require.Equalf(t, top, best,
		"the brightest cell more than half-way to the rim reached ramp stop %d, not "+
			"the top (%d): something between rain's layer table and the emitted cell "+
			"is dimming heads by their distance from the wordmark. The dim that did "+
			"this was 42%%, which lands a rim head on stop %d",
		best, top, rainStopFor(1-0.42*0.5))
}

// TestRainGlyphsAreWidthOne is what actually settles the glyph set, rather than
// a reading of the East-Asian-Width tables. Every emitted glyph must occupy
// exactly one terminal cell: the contract requires each row to be exactly w
// runes wide, and a width-2 glyph would shift every cell after it and break the
// column alignment rain is made of.
//
// It also guards the trap the set is a []rune to avoid — indexing a multi-byte
// set by byte yields half-runes, which are not width-1 either.
func TestRainGlyphsAreWidthOne(t *testing.T) {
	require.NotEmpty(t, splashRainGlyphs)
	for _, r := range splashRainGlyphs {
		require.Equalf(t, 1, ansi.StringWidth(string(r)),
			"glyph %q (U+%04X) is not terminal-width-1", r, r)
	}
}

// TestRainGlyphsRenderIntact guards the []rune choice end to end: every glyph
// the renderer emits must be one the set actually contains. A byte-indexed set
// would emit half-runes here, which is the failure this is watching for — it is
// silent in the field math and only shows up as mojibake on screen.
func TestRainGlyphsRenderIntact(t *testing.T) {
	want := make(map[rune]bool, len(splashRainGlyphs))
	for _, r := range splashRainGlyphs {
		want[r] = true
	}
	const w, h = 80, 40
	out := ansi.Strip(renderSplashField(w, h, 40, splashTestPalette(),
		h/2, Rain))
	seen := 0
	for _, r := range out {
		if r == ' ' || r == '\n' {
			continue
		}
		require.Truef(t, want[r], "rendered glyph %q (U+%04X) is not in the set", r, r)
		seen++
	}
	require.Positive(t, seen, "rain must render glyphs")
}
