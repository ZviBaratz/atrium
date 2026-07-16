package splash

import (
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// TestRenderSplashFieldDeterministic locks the pure-function contract: identical
// inputs must produce byte-identical output (so the field is snapshot-safe and
// the tick can drive it without hidden state).
func TestRenderSplashFieldDeterministic(t *testing.T) {
	pal := splashTestPalette()
	a := renderSplashField(80, 30, 5, pal, centeredFocalRow(30), Rain)
	b := renderSplashField(80, 30, 5, pal, centeredFocalRow(30), Rain)
	require.Equal(t, a, b, "same inputs must render identically")
	require.NotEmpty(t, a)
}

// TestRenderSplashFieldBounds is the view-bounds invariant: exactly h rows, each
// exactly w visible cells, across a spread of sizes (even/odd, wide/tall). This
// is what lets String() drop the field into the pane box without overflow (#251).
func TestRenderSplashFieldBounds(t *testing.T) {
	pal := splashTestPalette()
	sizes := [][2]int{{50, 18}, {66, 20}, {80, 30}, {120, 40}, {51, 19}, {73, 27}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		field := renderSplashField(w, h, 3, pal, centeredFocalRow(h), Rain)
		lines := strings.Split(field, "\n")
		require.Lenf(t, lines, h, "%dx%d: line count", w, h)
		for i, l := range lines {
			require.Equalf(t, w, lipgloss.Width(l), "%dx%d: line %d width", w, h, i)
		}
	}
}

// TestRenderSplashFieldAnimates guards the drift wiring: advancing the frame must
// change the rendered field (otherwise the "slow drift" is dead).
func TestRenderSplashFieldAnimates(t *testing.T) {
	pal := splashTestPalette()
	f0 := renderSplashField(80, 30, 3, pal, centeredFocalRow(30), Rain)
	f1 := renderSplashField(80, 30, 4, pal, centeredFocalRow(30), Rain)
	require.NotEqual(t, f0, f1, "consecutive frames must differ")
}

// TestRenderSplashFieldVignetteCorners locks the edge vignette: the outermost
// rows and columns fade fully to blank at the pane border, so the full-bleed
// field softens into the edges instead of hard-clipping into a lit rectangle.
func TestRenderSplashFieldVignetteCorners(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredFocalRow(h), Rain))
	require.Len(t, lines, h)
	// The border rows fade to zero, so the first and last rows are entirely blank.
	require.Equal(t, strings.Repeat(" ", w), lines[0], "top row must be blank")
	require.Equal(t, strings.Repeat(" ", w), lines[h-1], "bottom row must be blank")
	// And each corner is blank regardless.
	for _, rc := range [][2]int{{0, 0}, {0, w - 1}, {h - 1, 0}, {h - 1, w - 1}} {
		require.Equalf(t, byte(' '), lines[rc[0]][rc[1]], "corner (%d,%d)", rc[0], rc[1])
	}
}

// TestRenderSplashFieldExtremes is the panic-safety net: degenerate sizes must
// return "" or a bounded block, never panic (the caller gates on splashFits, but
// the generator must be robust on its own).
func TestRenderSplashFieldExtremes(t *testing.T) {
	pal := splashTestPalette()
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 2}, {1, 40}, {40, 1}, {50, 1}, {3, 3}, {51, 19}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("renderSplashField(%d,%d) panicked: %v", w, h, r)
				}
			}()
			out := renderSplashField(w, h, 2, pal, centeredFocalRow(h), Rain)
			if out == "" {
				return
			}
			lines := strings.Split(out, "\n")
			require.LessOrEqualf(t, len(lines), h, "%dx%d: too many lines", w, h)
			for _, l := range lines {
				require.LessOrEqualf(t, lipgloss.Width(l), w, "%dx%d: line too wide", w, h)
			}
		}()
	}
}

// TestSplashLUTThemeAnchored locks the gradient to the palette: the ramp starts
// at the warm anchor (A0), ends at the cool anchor (A3), and the rim hue flows
// into the upper stops (so swapping A3 changes the colors). It asserts at the LUT
// level because lipgloss strips truecolor to plain text in a no-TTY test process,
// so the rendered field carries no color to compare.
func TestSplashLUTThemeAnchored(t *testing.T) {
	pal := splashTestPalette()
	lut := lutForAmbient(pal)
	require.Equal(t, lipgloss.Color(pal.A0), lut.colors[0], "core stop is the warm anchor")
	require.Equal(t, lipgloss.Color(pal.A3), lut.colors[len(lut.colors)-1], "rim stop is the cool anchor")

	other := pal
	other.A3 = "#a6e3a1" // a distinctly different rim (catppuccin green)
	otherLUT := lutForAmbient(other)
	require.NotEqual(t, lut.colors, otherLUT.colors,
		"changing the rim hue must change the gradient")
	// The lower stops (warm→blue) are rim-independent; the upper stops must move.
	require.NotEqual(t, lut.colors[len(lut.colors)-4], otherLUT.colors[len(otherLUT.colors)-4],
		"the rim hue must reach the upper stops of the ramp")
}

// TestSplashAffixBracketsMatchRender pins the invariant the whole emitter rests
// on: writing a style's cached prefix, then the cells, then its suffix must
// produce exactly what Style.Render would have produced for those cells.
// Splitting the SGR bracket out of lipgloss is only safe if the bracket really
// is a pure wrapper — for every stop, on every profile, including the colorless
// one where it has to collapse to plain text.
func TestSplashAffixBracketsMatchRender(t *testing.T) {
	profiles := map[string]termenv.Profile{
		"truecolor": termenv.TrueColor,
		"ansi256":   termenv.ANSI256,
		"ansi":      termenv.ANSI,
		"ascii":     termenv.Ascii,
	}
	// Spaces and a wide glyph included: the emitter brackets whatever it is given,
	// and lipgloss has space-sensitive paths for some attributes.
	contents := []string{"x", "▓▓▓", "   ", "⠿⠿", "·-=#"}
	for name, prof := range profiles {
		t.Run(name, func(t *testing.T) {
			withColorProfile(t, prof)
			// Built directly rather than via splashLUTFor: this asserts on the
			// affixes themselves; TestSplashLUTCacheTracksColorProfile covers
			// the cache's keying.
			lut := buildLUTAmbient(splashTestPalette())
			require.Equal(t, len(lut.styles), lut.starIndex(),
				"the star sentinel must sit exactly one past the last gradient stop")
			for _, content := range contents {
				for i, st := range lut.styles {
					require.Equal(t, st.Render(content),
						lut.affix[i].prefix+content+lut.affix[i].suffix,
						"stop %d must bracket %q exactly as Render does", i, content)
				}
				require.Equal(t, lut.star.Render(content),
					lut.starAffix.prefix+content+lut.starAffix.suffix,
					"the star style must bracket %q exactly as Render does", content)
			}
			if prof == termenv.Ascii {
				require.Empty(t, lut.affix[1].prefix, "a colorless profile must degrade to plain")
				require.Empty(t, lut.affix[1].suffix, "a colorless profile must degrade to plain")
			}
		})
	}
}

// TestSplashLUTCacheTracksColorProfile guards the trap that baking the affixes
// introduces. Style.Render re-read the color profile on every call, so a LUT
// cached under one profile still rendered correctly under the next; the affixes
// are frozen at build time, so the cache has to key on the profile or it pins
// whichever one happened to build the entry. That matters most in tests: a test
// binary starts out colorless, so a stale entry would silently turn any later
// truecolor assertion into a test of plain text.
func TestSplashLUTCacheTracksColorProfile(t *testing.T) {
	pal := splashTestPalette()
	pal.A1 = "#123456" // a private cache entry, so other tests are unaffected

	withColorProfile(t, termenv.Ascii)
	require.Empty(t, lutForAmbient(pal).affix[1].prefix, "a colorless profile emits no SGR")

	withColorProfile(t, termenv.TrueColor)
	require.NotEmpty(t, lutForAmbient(pal).affix[1].prefix,
		"a LUT cached under Ascii must not pin the colorless path once the profile is truecolor")
}

// TestRenderSplashFieldColorByProfile pins the emitted bytes end-to-end: a
// truecolor terminal gets SGR-bracketed runs, a colorless one gets the very
// same glyphs with no escapes at all.
func TestRenderSplashFieldColorByProfile(t *testing.T) {
	pal := splashTestPalette()
	render := func() string {
		return renderSplashField(60, 20, 3, pal, centeredFocalRow(20), Tunnel)
	}

	withColorProfile(t, termenv.TrueColor)
	colored := render()
	require.Contains(t, colored, "\x1b[38;2;", "a truecolor profile must emit truecolor SGR")

	withColorProfile(t, termenv.Ascii)
	plain := render()
	require.NotContains(t, plain, "\x1b[", "a colorless profile must emit no escapes")

	require.Equal(t, plain, ansi.Strip(colored),
		"color must be the only difference — the glyphs are identical either way")
}

// TestRenderProfileOverridesAmbient pins the decoupling Options.Profile buys:
// when set, it decides the emitted color depth regardless of the process-global
// lipgloss.ColorProfile(). The ambient profile is forced to the opposite of the
// explicit one in each direction, so a Render that still read the global would
// fail — proving the field is honored, not the terminal.
func TestRenderProfileOverridesAmbient(t *testing.T) {
	pal := splashTestPalette()
	pal.A2 = "#0a0b0c" // a private cache entry, so other tests are unaffected

	truecolor, asciiP := termenv.TrueColor, termenv.Ascii
	render := func(p *termenv.Profile) string {
		return Render(60, 20, 3, Options{Palette: pal, Variant: Tunnel, FocalRow: centeredFocalRow(20), Profile: p})
	}

	withColorProfile(t, termenv.Ascii)
	require.Contains(t, render(&truecolor), "\x1b[38;2;",
		"an explicit truecolor Profile must emit truecolor SGR even when the ambient profile is colorless")

	withColorProfile(t, termenv.TrueColor)
	require.NotContains(t, render(&asciiP), "\x1b[",
		"an explicit Ascii Profile must emit no escapes even when the ambient profile is truecolor")
}

// TestRenderNilProfileDefersToAmbient guards the default: a nil Profile keeps
// the auto-detect behavior package ui relies on, tracking the ambient profile
// in both directions.
func TestRenderNilProfileDefersToAmbient(t *testing.T) {
	pal := splashTestPalette()
	pal.A2 = "#0c0b0a" // a private cache entry, distinct from the override test's
	render := func() string {
		return Render(60, 20, 3, Options{Palette: pal, Variant: Tunnel, FocalRow: centeredFocalRow(20)})
	}

	withColorProfile(t, termenv.TrueColor)
	require.Contains(t, render(), "\x1b[38;2;", "a nil Profile must defer to the ambient truecolor profile")

	withColorProfile(t, termenv.Ascii)
	require.NotContains(t, render(), "\x1b[", "a nil Profile under a colorless ambient must stay colorless")
}

// TestFieldContrastIsStillHermite pins the distinction renderField draws
// between a "full-range" contrast curve and an identity one. The curve every
// field now runs — smoothstep(0, 1, val) — clips nothing, which is the property a
// self-shading field needs. It does not pass values through untouched, and no
// window can: smoothstep is Hermite on the clamped parameter, so a {0,1} window
// still bends every interior value through t*t*(3-2t).
//
// The distinction is the point, and it is why the call is written out rather than
// dropped. "Identity" invites a reader to skip it, and skipping it would be a
// behaviour change — every surviving field carries its own gradient (rain's
// streams, the tunnel's fog, ripple's ring decay) and is tuned against this
// S-curved version of its output, not against its generator's raw values.
//
// It used to be a per-variant window in splashOps, ranging from the nebula's
// narrow 0.36–0.64 to this. V5 retired the fields that wanted a narrow one, all
// three survivors had independently chosen {0,1}, and the field became a constant
// and then a literal at the one call site.
func TestFieldContrastIsStillHermite(t *testing.T) {
	// It clips nothing: the endpoints come back exact.
	require.Zero(t, smoothstep(0, 1, 0), "the widest window must not lift the floor")
	require.Equal(t, 1.0, smoothstep(0, 1, 1), "the widest window must not clip the ceiling")

	// But the interior is bent. 0.5 is the fixed point an S-curve shares with the
	// identity, so it is the one probe that cannot witness anything.
	require.Equal(t, 0.5, smoothstep(0, 1, 0.5), "0.5 is Hermite's fixed point, not evidence")
	for _, x := range []float64{0.1, 0.25, 0.4, 0.6, 0.75, 0.9} {
		require.NotEqualf(t, x, smoothstep(0, 1, x),
			"a {0,1} window is not an identity at x=%v — it is still an S-curve", x)
	}
	// Concretely, and in the direction that matters: the curve pulls the dim half
	// down and the bright half up, which is contrast the caller did not ask for.
	require.Less(t, smoothstep(0, 1, 0.25), 0.25, "the S-curve must darken the dim half")
	require.Greater(t, smoothstep(0, 1, 0.75), 0.75, "the S-curve must brighten the bright half")
}

// TestFieldContrastReachesTheRender is the wiring half of the claim above, and
// both halves are needed. That test proves smoothstep(0, 1, ·) is an S-curve; it
// cannot prove Pass 2 *calls* it. Nothing else could either: the curve has one
// call site and every variant now takes the same one, so there is no second
// behaviour to compare against and no ops field left to pin. A reader who
// believes "full-range" means "identity" — which is exactly what the name invites,
// and why renderField argues against it in a comment — could drop the call
// and every other test in this package would still pass.
//
// That is this package's signature bug: ops.dimToRim shipped declared and never
// read, and the guard that should have caught it was measuring the ops table
// instead of the wiring. So this decodes the emitted SGR back to a luminance stop
// and compares it against the documented pipeline evaluated independently —
// smoothstep, then splashShade's split — in rippleMeasurable's region, where the
// envelope is exactly 1 and the arithmetic is therefore complete.
//
// The second assertion is what makes it able to fail rather than merely pass: it
// counts the cells an identity contrast would land on a *different* stop, so the
// test is only trusted where it can tell the two apart.
func TestFieldContrastReachesTheRender(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	const w, h, frame = 240, 60, 300
	stops, _ := shadeStopGrid(t, w, h, frame, splashTestPalette(), Ripple)
	vals := rippleFieldVals(w, h, frame)
	lumRange := Ripple.ops().lumRange

	// The pipeline renderField documents, from val to emitted luminance stop.
	stopFor := func(intensity float64) int {
		_, lumT := splashShade(intensity, lumRange)
		return clampInt(int(lumT*float64(splashLumStops-1)), 0, splashLumStops-1)
	}

	checked, separable := 0, 0
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			if !rippleMeasurable(col, row, w, h) || stops[row][col] <= 0 {
				continue
			}
			v := vals[row][col]
			checked++
			require.Equalf(t, stopFor(smoothstep(0, 1, v)), stops[row][col],
				"cell (%d,%d) at val %.4f rendered a stop the contrast curve does not "+
					"predict; Pass 2 is not running smoothstep(0, 1, val)", col, row, v)
			if stopFor(clamp01(v)) != stopFor(smoothstep(0, 1, v)) {
				separable++
			}
		}
	}
	require.Greaterf(t, checked, 2000, "only %d measurable lit cells", checked)
	require.Greaterf(t, separable, 500,
		"only %d of %d cells would render a different stop under an identity contrast, "+
			"so this test cannot tell an S-curve from a passthrough", separable, checked)
}

func BenchmarkRenderSplash(b *testing.B) {
	pal := splashTestPalette()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = renderSplashField(80, 30, i, pal, centeredFocalRow(30), Rain)
	}
}

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

// TestSplashTestVariantsCoversEnum guards the map above, which is the entry
// point to this package's two per-variant sweeps: TestSplashVariantsContract
// (determinism, bounds, blank borders, frame-to-frame animation) and
// BenchmarkRenderSplashVariants (the frame budget). Both iterate the map, so a
// variant left out of it is not partially covered — it is invisible to both,
// and nothing fails to say so. Adding one is exactly when a contract breach is
// most likely and least likely to be noticed.
func TestSplashTestVariantsCoversEnum(t *testing.T) {
	seen := make(map[Variant]bool, len(splashTestVariants()))
	for name, v := range splashTestVariants() {
		require.Falsef(t, seen[v], "%q duplicates a variant already in the map", name)
		seen[v] = true
	}
	for v := Variant(0); v < variantCount; v++ {
		require.Truef(t, seen[v],
			"variant %d is missing from splashTestVariants, so it escapes both "+
				"the contract loop and the benchmark", int(v))
	}
}

// TestSplashVariantsContract loops every variant through the core field
// contract: determinism, exact w×h bounds, frame-to-frame animation, and
// fully-blank first/last rows (the edge vignette's by-construction guarantee,
// which no Pass-1 processing may break).
func TestSplashVariantsContract(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	for name, v := range splashTestVariants() {
		a := renderSplashField(w, h, 5, pal, centeredFocalRow(h), v)
		b := renderSplashField(w, h, 5, pal, centeredFocalRow(h), v)
		require.Equalf(t, a, b, "%s: same inputs must render identically", name)
		next := renderSplashField(w, h, 6, pal, centeredFocalRow(h), v)
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

// TestRenderSplashFieldConcurrent guards the two-panes case (preview and
// terminal both render the splash) and any future buffer pooling: concurrent
// renders of the same frame must all be byte-identical.
func TestRenderSplashFieldConcurrent(t *testing.T) {
	pal := splashTestPalette()
	want := renderSplashField(80, 30, 9, pal, centeredFocalRow(30), Rain)
	var wg sync.WaitGroup
	mismatch := make(chan string, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if got := renderSplashField(80, 30, 9, pal, centeredFocalRow(30), Rain); got != want {
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

// TestSplashPointFnRange checks every variant's point evaluator over a spread
// of cells and phases.
//
// Driven off splashFieldAt rather than a list of evaluators, so a variant is
// covered the moment it is dispatched — this used to be two hand-maintained
// lists (one for fBm, one for the fractals) that a new variant had to be
// remembered into, which is why the gap below went unnoticed for so long.
//
// val is [0,1] for every variant: Pass 2's contrast curve, glyph quantization
// and envelope all assume it, and a breach silently clips rather than crashing.
//
// aux is [0,1] for every variant too, and that is newly universal. It is the
// field's hue helper, and splashColorIdx now spends it straight as the gradient
// position, so a field that leaves the range renders a flat end of the gradient.
// The range used to be a contract only where the hue mix *weighted* aux — the
// retired legacy plasma passed an atan2 angle into a sine, which is meaningful
// for any real — and that carve-out went with the variant.
func TestSplashPointFnRange(t *testing.T) {
	for name, v := range splashTestVariants() {
		at := splashFieldAt(v, 96)
		for i := 0; i < 600; i++ {
			col, row := i%60, i/60
			dx := (float64(col) - 30) * 1.37
			dy := (float64(row) - 5) * 2.9
			phase := float64(i%23) * 1.7
			val, aux := at(col, row, dx, dy, phase)
			require.GreaterOrEqualf(t, val, 0.0, "%s val at (%d,%d,p%f)", name, col, row, phase)
			require.LessOrEqualf(t, val, 1.0, "%s val at (%d,%d,p%f)", name, col, row, phase)
			require.GreaterOrEqualf(t, aux, 0.0, "%s aux at (%d,%d,p%f)", name, col, row, phase)
			require.LessOrEqualf(t, aux, 1.0, "%s aux at (%d,%d,p%f)", name, col, row, phase)
		}
	}
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
				focalRow := centeredFocalRow(s.h)
				for i := 0; i < b.N; i++ {
					_ = renderSplashField(s.w, s.h, i, pal, focalRow, v)
				}
			})
		}
	}
}
