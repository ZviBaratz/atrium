package ui

// The empty-state splash: a slow-drifting field that appears to emanate from
// the ATRIUM wordmark and fades out at the pane's edges. The field is sampled
// per character cell from one of several generators (see splashVariant),
// modulated by a radial envelope, colored by a theme-anchored gradient, and
// composited *behind* the existing wordmark+message block (which is left
// untouched, so its styling survives). Only the idle "no agents" screen uses
// it; every other empty state keeps the plain FallbackBanner.
//
// This file owns the scene composition, the gradient LUT, and the emitter; the
// field math and the per-cell loops live in splash_field.go.

import (
	"strconv"
	"strings"
	"sync"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	colorful "github.com/lucasb-eyer/go-colorful"
)

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

	// The field is a small sum of sines evaluated per cell: two domain-warped
	// concentric ring octaves + rotationally-symmetric petals + an isotropic
	// fine texture. Every term is direction-free (radial, or a set of plane waves
	// whose directions cancel), so the plasma reads rich but never skewed.
	plasmaFreq1 = 0.55  // primary ring spacing
	plasmaFreq2 = 0.31  // second ring octave (drifts at a different rate)
	plasmaFreq3 = 0.14  // slow ring that pulses the angular petals
	petalCount  = 6.0   // even → rotationally symmetric petals, no directional lean
	plasmaWarp  = 3.6   // domain-warp amplitude in cells: wavy, organic filaments
	plasmaWarpF = 0.055 // domain-warp spatial frequency
	// isoFreq/isoSpeed drive three plane waves 120° apart (iso*Cos/Sin below);
	// their directions sum to zero, so the fine texture shimmers isotropically
	// with no diagonal grain — the fix for the field looking skewed.
	isoFreq   = 0.13
	isoSpeed  = 0.8
	isoWeight = 0.20
	iso1Cos   = -0.5
	iso1Sin   = 0.8660254037844386
	iso2Cos   = -0.5
	iso2Sin   = -0.8660254037844386
	// plasmaAmp is the sum of the term weights below; normalizes v into [0,1].
	plasmaAmp = 1.0 + 0.55 + 0.40 + 3*isoWeight

	// edgeVignetteFrac is the fraction of each dimension over which the full-bleed
	// field fades to black at the pane border, so it softens into the edges
	// instead of hard-clipping into a rectangle.
	edgeVignetteFrac = 0.16
	// radialDim is how much the field dims from the wordmark (core) out to the
	// farthest corner — enough to read as a glow emanating from ATRIUM while the
	// field still reaches the edges.
	radialDim = 0.42

	// Contrast curve applied to intensity: values below Lo fade toward blank,
	// above Hi saturate — so bright ridges read as filaments against darker
	// voids instead of a uniform mid-tone wash, and the top of the ramp is used.
	splashContrastLo = 0.20
	splashContrastHi = 0.86
	// Color mixing: how much the gradient follows radius vs. a slow angular
	// swirl. A lower radius weight makes the hue wander so the field reads as a
	// multi-hued nebula rather than one flat band.
	colorRadialMix  = 0.50
	colorSwirlF     = 0.045
	colorSwirlSpeed = 0.30

	// The starfield: sparse, fixed, twinkling points scattered through the plasma
	// (including its dark voids) for depth. starThreshold sets rarity (higher →
	// fewer stars), starRamp maps twinkle→glyph, and stars are drawn in a bright
	// near-white so they read as starlight in front of the colored gas.
	starThreshold    = 0.986
	starTwinkleSpeed = 1.7
	starPhaseScatter = 137.0 // desyncs twinkles so stars don't pulse in unison
	starRamp         = " ·+*"

	// Breathing: a slow global brightness swell so the whole nebula feels alive
	// (inhaling) rather than only drifting outward.
	breatheDepth = 0.16
	breatheSpeed = 0.33

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

	// minSplashW/minSplashH gate the effect: below this the pane is too small
	// for the field to read, so String() falls back to the plain wordmark. The
	// width floor sits just above the 48-col wordmark; the height floor leaves
	// room for a few ring rows above and below the ~10-row text block.
	minSplashW = 50
	minSplashH = 18
)

// splashFits reports whether a pane is large enough to render the splash field
// legibly. Callers fall back to the plain centered wordmark when it is not.
func splashFits(w, h int) bool { return w >= minSplashW && h >= minSplashH }

// SplashFits is splashFits for callers outside ui — the screensaver's entry
// and stay-alive gate in app.
func SplashFits(w, h int) bool { return splashFits(w, h) }

// SplashScreensaver renders the full-window splash easter egg: the same
// animated scene as the idle empty state, wordmark centered, but without the
// guidance message line. Callers gate on SplashFits and own the frame ticks.
func SplashScreensaver(width, height, frame int) string {
	return splashScene(width, height, frame, "")
}

// splashScene composites the idle empty screen: the animated nebula field with
// the wordmark centered on top and the message tucked just below it. The wordmark
// and message are overlaid separately at their own widths (not one padded block)
// so the field's rings hug the narrow wordmark rather than being pushed out by
// the wider message; each gets its own tight clearing so no glyphs bleed through
// the text. The outer clamp honors the pane box (#251). Shared by the preview and
// terminal panes so their idle empty states match. Callers gate on splashFits.
//
// The message line is optional: an empty message renders the wordmark alone over
// an uninterrupted field, which is the screensaver (see SplashScreensaver). Both
// panes always pass one.
func splashScene(width, height, frame int, message string) string {
	// Resolved once: the variant picks both the field generator and whether its
	// field wants a clearing around the text at all (see splashVariant.textPad).
	variant := splashActiveVariant()
	pad, clears := variant.textPad()

	word := trimBlankLines(FallbackBanner())
	wordW, wordH := lipgloss.Width(word), lipgloss.Height(word)

	const gap = 2 // blank rows between the wordmark and the message
	cy := (height - 1) / 2
	wordX := (width - wordW) / 2
	wordY := max(0, cy-wordH/2) // wordmark centered on the pane

	// wordCenterRow is set unconditionally: it doubles as the field's focal row
	// (renderSplashField), so it is not the clearing's to skip. Only the
	// half-extents are — zero disables an ellipse (see splashClearing.blanks).
	clearing := splashClearing{wordCenterRow: wordY + wordH/2}
	if clears {
		clearing.wordHalfW = wordW/2 + pad.wordX
		clearing.wordHalfH = wordH/2 + pad.wordY
	}

	// Sized only when there is a message: the zero-value msg half-extents left
	// behind otherwise disable that ellipse, so the field flows unbroken below
	// the wordmark instead of being blanked for text nobody passed.
	var msg string
	var msgX, msgY int
	if message != "" {
		msg = theme.Current().FgStyle().Render(message)
		msgW, msgH := lipgloss.Width(msg), lipgloss.Height(msg)
		msgX = (width - msgW) / 2
		msgY = wordY + wordH + gap
		if clears {
			clearing.msgHalfW = msgW/2 + pad.msgX
			clearing.msgHalfH = msgH/2 + pad.msgY
			clearing.msgCenterRow = msgY + msgH/2
		}
	}

	field := renderSplashField(width, height, frame, theme.Current().Palette, clearing, variant)
	scene := overlayAt(field, word, wordX, wordY)
	if message != "" {
		scene = overlayAt(scene, msg, msgX, msgY)
	}
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(scene)
}

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
	// the frame (the field-free legacy variant still costs ~290ns/cell), and
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
func splashLUTFor(pal theme.Palette) *splashLUT {
	key := strings.Join([]string{
		strconv.Itoa(int(lipgloss.ColorProfile())),
		string(pal.Danger), string(pal.Purple), string(pal.Accent),
		string(pal.Cyan), string(pal.Fg),
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
// drawn from theme tokens so it tracks the active theme. Consecutive anchors are
// hue-adjacent, so HCL blending between them stays smooth (no muddy backtrack).
func splashAnchors(pal theme.Palette) []lipgloss.Color {
	return []lipgloss.Color{pal.Danger, pal.Purple, pal.Accent, pal.Cyan}
}

// splashGradientColors blends the anchors across splashLUTSize stops in HCL for
// smooth, non-muddy hue steps, and pins the exact endpoints (HCL round-tripping
// can nudge the hex a hair). If any anchor is not parseable hex (an unusual
// theme), the ramp degrades to flat purple rather than emitting broken colors.
//
// Split out from buildSplashLUT so the luminance grid can shade the same stops the
// gradient renders (see buildShadeGrid) without rebuilding a second, subtly
// different gradient beside it.
func splashGradientColors(pal theme.Palette) []lipgloss.Color {
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
		c := pal.Purple
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
func buildSplashLUT(pal theme.Palette) *splashLUT {
	lut := &splashLUT{
		colors: splashGradientColors(pal),
		styles: make([]lipgloss.Style, splashLUTSize),
		star:   lipgloss.NewStyle().Foreground(pal.Fg),
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
// cell that dim renders blank (see the g == 0 gate in renderSplashField), which
// is how a tail dies into the pane rather than painting near-black over it. What
// the floor does is anchor the low end's *shape*, and so set how dark the dimmest
// stop that does render is. That one has to stay off true black: terminals with a
// minimum-contrast feature would rewrite it to something legible and scatter
// bright specks through the tail — the very artifact this ramp removes.
func buildRainRamp(pal theme.Palette) []splashAffix {
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
func rainRampHexAt(pal theme.Palette, i int) string {
	base := splashShadeParse(pal.Cyan)
	head, err := colorful.Hex(string(pal.Fg))
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

// splashClearing marks the cells to leave blank for the composited text: a tight
// ellipse hugging the wordmark, plus a shorter, wider one around the message,
// each centered on its own row. Keeping them separate (rather than one clearing
// sized to the whole text block) is what lets the rings hug the narrow wordmark
// instead of being pushed out by the wider message. Half-extents are in cells; a
// zero half-extent disables that ellipse. Both are centered on the field's
// horizontal axis, since the text is centered horizontally.
type splashClearing struct {
	wordHalfW, wordHalfH, wordCenterRow int
	msgHalfW, msgHalfH, msgCenterRow    int
}

// blanks reports whether the cell at horizontal offset dx (from the field axis)
// and absolute row lies inside either clearing ellipse. Uses raw cell distance
// (not the aspect-corrected dy) so each ellipse hugs its text rectangle —
// deliberately distinct from the round vignette metric.
func (c splashClearing) blanks(dx float64, row int) bool {
	inEllipse := func(halfW, halfH, centerRow int) bool {
		if halfW <= 0 || halfH <= 0 {
			return false
		}
		dy := float64(row - centerRow)
		return (dx*dx)/float64(halfW*halfW)+(dy*dy)/float64(halfH*halfH) < 1
	}
	return inEllipse(c.wordHalfW, c.wordHalfH, c.wordCenterRow) ||
		inEllipse(c.msgHalfW, c.msgHalfH, c.msgCenterRow)
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
// starfield is fixed in place (and snapshot-stable) while the plasma drifts
// behind it. Built on the integer lattice hash: exact on every architecture,
// unlike the sin-fract hash it replaced.
func starHash(col, row int) float64 {
	return splashCellHash(col, row, seedStar)
}

// overlayAt composites fg over bg (the splash field) at cell (placeX, placeY),
// splicing width-correctly around bg's ANSI escapes. Adapted from
// overlay.PlaceOverlay (ui/overlay/overlay.go) but deliberately WITHOUT its
// background fade — the gradient must show through, not be dimmed — and without
// the whitespace-option plumbing (plain spaces fill any gap). The field carves a
// blank clearing under fg, so nothing colored bleeds through the text.
func overlayAt(bg, fg string, placeX, placeY int) string {
	fgLines, fgWidth := splashLines(fg)
	bgLines, bgWidth := splashLines(bg)
	fgHeight, bgHeight := len(fgLines), len(bgLines)

	if fgWidth >= bgWidth && fgHeight >= bgHeight {
		return fg
	}

	placeX = clampInt(placeX, 0, max(0, bgWidth-fgWidth))
	placeY = clampInt(placeY, 0, max(0, bgHeight-fgHeight))

	var b strings.Builder
	for i, bgLine := range bgLines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i < placeY || i >= placeY+fgHeight {
			b.WriteString(bgLine)
			continue
		}

		pos := 0
		if placeX > 0 {
			left := xansi.Truncate(bgLine, placeX, "")
			pos = xansi.StringWidth(left)
			b.WriteString(left)
			if pos < placeX {
				b.WriteString(strings.Repeat(" ", placeX-pos))
				pos = placeX
			}
		}

		fgLine := fgLines[i-placeY]
		b.WriteString(fgLine)
		pos += xansi.StringWidth(fgLine)

		right := xansi.TruncateLeft(bgLine, pos, "")
		bgLineWidth := xansi.StringWidth(bgLine)
		rightWidth := xansi.StringWidth(right)
		if rightWidth <= bgLineWidth-pos {
			b.WriteString(strings.Repeat(" ", bgLineWidth-rightWidth-pos))
		}
		b.WriteString(right)
	}
	return b.String()
}

// splashLines splits s into lines and returns the widest visible (ANSI-aware)
// line width. Mirrors overlay.getLines.
func splashLines(s string) (lines []string, widest int) {
	lines = strings.Split(s, "\n")
	for _, l := range lines {
		if wdt := xansi.StringWidth(l); wdt > widest {
			widest = wdt
		}
	}
	return lines, widest
}

// trimBlankLines drops leading/trailing all-whitespace lines (the wordmark art
// is padded with blank rows) so the composited block is exactly its glyph rows —
// letting the clearing hug the wordmark tightly.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(xansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(xansi.Strip(lines[end-1])) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// smoothstep is the classic Hermite ease between edges a and b, clamped to [0,1].
func smoothstep(a, b, x float64) float64 {
	if a == b {
		if x < a {
			return 0
		}
		return 1
	}
	t := clamp01((x - a) / (b - a))
	return t * t * (3 - 2*t)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
