package splash

// The gradient LUT and the emitter: the theme-anchored colour ramp the field is
// painted with, precomputed once per palette, plus the run-coalescing SGR
// emitter the two-pass renderer brackets its cells with. The field math and the
// per-cell loops live in field.go; the variant vocabulary in variant.go.

import (
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// Palette is the splash field's colour input: four warm→cool gradient anchors
// plus a highlight, each an "#rrggbb" hex string. A0..A3 are the nebula gradient
// (in Atrium's theme: pink, purple, blue, cyan); consecutive anchors are meant
// to be hue-adjacent so HCL blending between them stays smooth. A3 doubles as
// rain's stream hue. Highlight is the star / rain-head white — the brightest
// colour the field can reach (theme.Fg). An anchor that is not parseable hex
// degrades gracefully (see splashGradientColors, splashShadeParse).
type Palette struct {
	A0, A1, A2, A3 string
	Highlight      string
}

const (
	// cellAspect corrects for terminal cells being ~2:1 (tall): vertical
	// distance is weighted up by this factor so the rings and the round
	// vignette render circular rather than oval.
	cellAspect = 2.0

	// driftPerFrame is the outward phase advance per nominal 60fps animation
	// frame (see the app's splash tick) — ~0.9 phase units/second, the same
	// visual speed the field had at its original 5Hz push (0.18/frame), just
	// twelve times smoother along the way.
	driftPerFrame = 0.015

	// edgeVignetteFrac is the fraction of each dimension over which the full-bleed
	// field fades to black at the pane border, so it softens into the edges
	// instead of hard-clipping into a rectangle.
	edgeVignetteFrac = 0.16
	// The starfield: sparse, fixed, twinkling points scattered through the field
	// (including its dark voids) for depth. starThreshold sets rarity (higher →
	// fewer stars), starRamp maps twinkle→glyph, and stars are drawn in a bright
	// near-white so they read as starlight in front of the colored gas.
	starThreshold    = 0.986
	starTwinkleSpeed = 1.7
	starPhaseScatter = 137.0 // desyncs twinkles so stars don't pulse in unison
	starRamp         = " ·+*"

	// splashRamp maps intensity to a glyph, light→heavy. Index 0 (space) is
	// "nothing here"; every glyph is terminal-width 1 (downsample-safe). A longer
	// ramp gives finer density steps, so gradients read smooth instead of banded.
	splashRamp = " .·:;+=*oO0@"

	// splashLUTSize is the number of gradient color stops from core to rim.
	splashLUTSize = 20

	// The rain luminance ramp (see buildRainRamp). splashRainStops is its length
	// — enough steps that a tail of a dozen rows fades smoothly rather than
	// banding. rainRampHeadAt is where along it the stream hue sits: below is the
	// tail's climb out of the dark, above is the head's blow-out to white, so the
	// white is reserved for the few cells at a stream's leading edge.
	// rainRampFloor is the darkest luminance as a fraction of the stream hue's.
	// It anchors the low end rather than being drawn itself (a cell that dim
	// renders blank): low enough that the stops above it read as unlit, high
	// enough that the dimmest one that does render stays clear of the black a
	// minimum-contrast terminal would rewrite.
	splashRainStops = 16
	rainRampHeadAt  = 0.82
	rainRampFloor   = 0.06
)

// splashLUT is a precomputed gradient: parallel color/style stops from the
// warm core hue to the cool rim, plus a bright star color. Built once per
// palette so the per-cell hot loop only does index lookups, never color math.
type splashLUT struct {
	colors []lipgloss.Color
	styles []lipgloss.Style
	star   lipgloss.Style
	// rain is a luminance ramp, dark → the stream hue → the head's white. It
	// exists because the gradient above is not one: splashAnchors is four
	// hue-adjacent stops spanning L* 65–80, with the star only 2 points brighter
	// than Cyan, so it can say what colour a cell is but not how bright. A
	// stream's tail has nowhere to fade to on it, and rendering that fade through
	// the glyph ramp instead makes faint cells *small* rather than dim — a column
	// of "." is a column of dots, not a fading line. See buildRainRamp.
	rain []splashAffix
	// shade is the hue x luminance grid, flat: shade[hue*splashLumStops + lum].
	// It is what lets a variant hold its glyph weight constant and still shade,
	// which the gradient above cannot do and the density ramp can only do by
	// changing the mark's *size*. Deliberately a second table beside rain rather
	// than a generalization of it — the two axes want different tops. See
	// buildShadeGrid.
	shade []splashAffix
	// affix/starAffix are the styles' SGR sequences, split out once per palette
	// so the hot loop can bracket a run with two WriteStrings instead of calling
	// Style.Render — which allocates a fresh string per run. Emission dominates
	// the frame (even the cheapest field measured ~290ns/cell in emission), and
	// runs coalesce at only ~1.1 cells, so that was ~one allocation per cell.
	affix     []splashAffix
	starAffix splashAffix
}

// splashAffix is a style's rendered prefix/suffix around its content.
type splashAffix struct{ prefix, suffix string }

// The emitter's run-index protocol, resolved here rather than by each side
// reaching for a len() of its own:
//
//	 idx < 0             a blank run — raw, uncolored
//	 idx < starIndex     a gradient stop (hue only, full brightness)
//	idx == starIndex     the star / head white
//	 idx < shadeIndex    a rain luminance stop, at idx - rainIndex
//	 idx >= shadeIndex   a shade stop, at (idx - shadeIndex) = hue*splashLumStops + lum
func (l *splashLUT) starIndex() int  { return len(l.affix) }
func (l *splashLUT) rainIndex() int  { return l.starIndex() + 1 }
func (l *splashLUT) shadeIndex() int { return l.rainIndex() + len(l.rain) }

// splashAffixFor extracts a style's SGR bracket by rendering a sentinel and
// splitting on it. Going through Render (rather than formatting the escape by
// hand) is what keeps this correct under lipgloss's color-profile degradation:
// on a no-color profile Render returns the sentinel untouched, so both affixes
// come back empty and the run emits as plain text — exactly what Render would
// have produced.
func splashAffixFor(st lipgloss.Style) splashAffix {
	const sentinel = "\x00"
	prefix, suffix, ok := strings.Cut(st.Render(sentinel), sentinel)
	if !ok {
		return splashAffix{} // style mangled the sentinel; degrade to plain
	}
	return splashAffix{prefix: prefix, suffix: suffix}
}

var (
	splashLUTMu    sync.Mutex
	splashLUTCache = map[string]*splashLUT{}
)

// splashLUTFor returns the memoized gradient for a palette, keyed by every
// anchor it draws from *and* by the active color profile. Bubble Tea renders on
// a single goroutine, but the mutex is cheap insurance since both the preview
// and (future) terminal panes render here.
//
// The profile belongs in the key because the entry now bakes the styles' SGR
// bytes at build time (see splashAffix). Style.Render used to re-read the
// profile on every call, so a palette-only key was enough; a cached entry would
// track a profile change on its own. It no longer would — it would keep
// emitting the profile it was built under. Nothing in the binary changes the
// profile after startup, so this is insurance rather than a live fix, but tests
// do change it, and a cache that silently pins the colorless path is exactly
// the trap that hides a regression in the SGR bytes this LUT exists to emit.
func splashLUTFor(pal Palette) *splashLUT {
	key := strings.Join([]string{
		strconv.Itoa(int(lipgloss.ColorProfile())),
		pal.A0, pal.A1, pal.A2, pal.A3, pal.Highlight,
	}, "|")
	splashLUTMu.Lock()
	defer splashLUTMu.Unlock()
	if lut, ok := splashLUTCache[key]; ok {
		return lut
	}
	lut := buildSplashLUT(pal)
	splashLUTCache[key] = lut
	return lut
}

// splashAnchors is the warm→cool nebula gradient (pink → purple → blue → cyan),
// drawn from the palette's anchors so it tracks the active theme. Consecutive
// anchors are hue-adjacent, so HCL blending between them stays smooth (no muddy
// backtrack).
func splashAnchors(pal Palette) []lipgloss.Color {
	return []lipgloss.Color{
		lipgloss.Color(pal.A0), lipgloss.Color(pal.A1),
		lipgloss.Color(pal.A2), lipgloss.Color(pal.A3),
	}
}

// splashGradientColors blends the anchors across splashLUTSize stops in HCL for
// smooth, non-muddy hue steps, and pins the exact endpoints (HCL round-tripping
// can nudge the hex a hair). If any anchor is not parseable hex (an unusual
// theme), the ramp degrades to flat purple rather than emitting broken colors.
//
// Split out from buildSplashLUT so the luminance grid can shade the same stops the
// gradient renders (see buildShadeGrid) without rebuilding a second, subtly
// different gradient beside it.
func splashGradientColors(pal Palette) []lipgloss.Color {
	anchorCols := splashAnchors(pal)
	colors := make([]lipgloss.Color, splashLUTSize)
	anchors := make([]colorful.Color, len(anchorCols))
	ok := true
	for i, c := range anchorCols {
		cc, err := colorful.Hex(string(c))
		if err != nil {
			ok = false
			break
		}
		anchors[i] = cc
	}
	segs := len(anchorCols) - 1
	for i := range colors {
		c := lipgloss.Color(pal.A1)
		if ok {
			// Map stop i to a position along the multi-segment anchor path.
			t := float64(i) / float64(splashLUTSize-1) * float64(segs)
			seg := clampInt(int(t), 0, segs-1)
			c = lipgloss.Color(anchors[seg].BlendHcl(anchors[seg+1], t-float64(seg)).Clamped().Hex())
		}
		colors[i] = c
	}
	colors[0] = anchorCols[0]
	colors[splashLUTSize-1] = anchorCols[segs]
	return colors
}

// buildSplashLUT builds every table the emitter indexes: the hue gradient, the
// star white, rain's luminance ramp, and the hue x luminance shade grid.
func buildSplashLUT(pal Palette) *splashLUT {
	lut := &splashLUT{
		colors: splashGradientColors(pal),
		styles: make([]lipgloss.Style, splashLUTSize),
		star:   lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Highlight)),
		affix:  make([]splashAffix, splashLUTSize),
	}
	// Split every style's SGR bracket once, so the hot loop can bracket a run with
	// two WriteStrings instead of calling Style.Render.
	for i, c := range lut.colors {
		lut.styles[i] = lipgloss.NewStyle().Foreground(c)
		lut.affix[i] = splashAffixFor(lut.styles[i])
	}
	lut.starAffix = splashAffixFor(lut.star)
	lut.rain = buildRainRamp(pal)
	lut.shade = buildShadeGrid(lut.colors, lut.affix)
	return lut
}

// buildRainRamp builds the luminance ramp rain fades along.
//
// The gradient LUT cannot do this job. Its anchors are chosen hue-adjacent so
// blending never backtracks muddy, which lands all four inside L* 65–80 — a
// bright band with no floor and no highlight. Fading a stream's tail along it
// changes only hue, and pushing the fade through the glyph density ramp instead
// substitutes size for brightness: the faint end becomes "·" and "." and the
// stream reads as scattered dots rather than a dimming line.
//
// So: take the theme's stream hue, walk it down to near-black for the tail, and
// up to the foreground white for the head. Hue is held constant on the way down
// (HCL, chroma falling with luminance) so a dim tail cell is the same colour as
// a bright one, only darker — which is exactly what the eye reads as a fade.
//
// The floor is deliberately not black — though it is never itself emitted. A
// cell that dim renders blank (see the g == 0 gate in renderField), which
// is how a tail dies into the pane rather than painting near-black over it. What
// the floor does is anchor the low end's *shape*, and so set how dark the dimmest
// stop that does render is. That one has to stay off true black: terminals with a
// minimum-contrast feature would rewrite it to something legible and scatter
// bright specks through the tail — the very artifact this ramp removes.
func buildRainRamp(pal Palette) []splashAffix {
	stops := make([]splashAffix, splashRainStops)
	for i := range stops {
		stops[i] = splashAffixFor(lipgloss.NewStyle().Foreground(lipgloss.Color(rainRampHexAt(pal, i))))
	}
	return stops
}

// rainRampHexAt is stop i's color. Split out from buildRainRamp so the ramp's
// shape can be asserted as color rather than by parsing SGR back out of an
// affix — the property that matters is that it climbs in *luminance*, and a
// ramp that only changed hue would look identical to every other check.
//
// The tail is the shared luminance curve (splashLumHexAt), which the shade grid
// walks too. The head is rain's alone: it blows out past the stream hue to the
// foreground white, and that is exactly what a hue x luminance grid must not do —
// see buildShadeGrid.
func rainRampHexAt(pal Palette, i int) string {
	base := splashShadeParse(lipgloss.Color(pal.A3))
	head, err := colorful.Hex(pal.Highlight)
	if err != nil {
		head = base
	}
	t := float64(i) / float64(splashRainStops-1)
	if t < rainRampHeadAt {
		// Tail: near-black → the stream hue, on rain's own axis.
		return splashLumHexAt(base, t/rainRampHeadAt, rainChromaHold)
	}
	// Head: the stream hue → white.
	u := (t - rainRampHeadAt) / (1 - rainRampHeadAt)
	return base.BlendHcl(head, u).Clamped().Hex()
}

// splashRunAffix resolves a run's SGR bracket from its style index, per the
// protocol documented above.
func splashRunAffix(styleIdx int, lut *splashLUT) splashAffix {
	switch {
	case styleIdx < 0: // blank run — raw spaces, no color
		return splashAffix{}
	case styleIdx < lut.starIndex(): // gradient run
		return lut.affix[styleIdx]
	case styleIdx == lut.starIndex(): // star / head run — bright near-white
		return lut.starAffix
	case styleIdx < lut.shadeIndex(): // rain luminance run
		return lut.rain[clampInt(styleIdx-lut.rainIndex(), 0, len(lut.rain)-1)]
	default: // hue x luminance shade run
		return lut.shade[clampInt(styleIdx-lut.shadeIndex(), 0, len(lut.shade)-1)]
	}
}

// splashOpenRun / splashCloseRun bracket a run of same-color cells with one SGR
// pair, while the cells themselves are written straight into sb. The cells used
// to accumulate in a second strings.Builder that was Render'd and Reset per run,
// but Reset drops the buffer — so every run re-allocated, and runs coalesce at
// only ~1.1 cells because the hue gradient steps almost every cell. Writing
// through to sb keeps the emitted bytes identical while making the per-frame
// allocation count a function of sb's growth rather than of the cell count.
func splashOpenRun(sb *strings.Builder, styleIdx int, lut *splashLUT) {
	sb.WriteString(splashRunAffix(styleIdx, lut).prefix)
}

func splashCloseRun(sb *strings.Builder, styleIdx int, lut *splashLUT) {
	sb.WriteString(splashRunAffix(styleIdx, lut).suffix)
}

// starHash is a deterministic per-cell pseudo-random value in [0,1), so the
// starfield is fixed in place (and snapshot-stable) while the field drifts
// behind it. Built on the integer lattice hash: exact on every architecture,
// unlike the sin-fract hash it replaced.
func starHash(col, row int) float64 {
	return splashCellHash(col, row, seedStar)
}
