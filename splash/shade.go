package splash

// The luminance channel: a second axis the field can shade along, so brightness
// and glyph identity stop being one instruction.
//
// The palette cannot carry brightness. splashAnchors is chosen hue-adjacent so
// HCL blending never backtracks muddy, which lands all four anchors inside
// L* 65-80 — it can say what colour a cell is, never how bright. Brightness is
// therefore carried entirely by the glyph density ramp, which means a dim cell is
// necessarily a *small* one: the faint end of every field degenerates into "."
// and "·", and a scatter of dots is confetti rather than dimming.
//
// This file owns the split (splashShade) and the hue x luminance grid it indexes.
// Rain's own ramp is deliberately NOT folded in here — see buildShadeGrid.

import (
	"math"

	"github.com/charmbracelet/lipgloss"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// splashLumStops is the number of luminance steps each hue is shaded across. It
// matches splashRainStops only by coincidence of taste — the two axes are
// independent, and this one is free to grow (see buildShadeGrid) without touching
// rain.
const splashLumStops = 16

// splashShade splits a cell's brightness between the density channel (how heavy the
// mark is) and the luminance channel (how bright its colour is).
//
// dens*lum == lit for every lumRange: the split MOVES brightness between the two
// channels, it never adds any. That is what makes lumRange a knob for *how* a field
// shades rather than for how much of it there is — without the identity every
// opted-in variant would come out systematically brighter or darker than it was, and
// a screenshot round would tune around that error instead of the design.
//
// Two things it deliberately does not claim. It is an identity about the split, not
// about pixels: each channel spends its share through its own non-linear curve (a
// glyph index, a dot count, an L* ramp), so rendered brightness is preserved only
// approximately — the identity removes the systematic error, not the curvature. And
// the split's approximation only reaches the screen for a caller that spends dens
// on the glyph ramp. An endpoint caller does not and is exact anyway: rain sits at
// lumRange 1, where dens is a constant 1 and all its brightness rides lumT. No
// caller now dims by a private path that bypasses the ramp — the one that did was
// the braille band, which halftoned its own dot count and so dimmed without its
// density rising to pay for it, and it was retired with its variant in V5.
//
// The two endpoints are exact and transcendental-free, and that is load-bearing
// twice over. lumRange 0 is the pure density ramp and 1 is rain's pure luminance,
// so reproducing them by construction rather than by a float landing where it
// should is what made "byte-identical until opted in" a property instead of a
// hope while the roster was being moved onto this channel one variant at a time.
// It also means a variant at an endpoint pays nothing for the channel it does not
// use. Note the roster has since moved past them: rain and the tunnel sit at 1,
// but ripple ships 0.75 and spends both.
//
// The interior is a gamma split: dens = lit^(1-lumRange), lum = lit^lumRange. It
// is written as one Log and one Exp rather than two math.Pow because each Pow does
// its own Frexp/Log/Exp internally — this is half the work, and recovering dens by
// division makes the product identity true to ~1 ulp by construction rather than
// as a claim about two independently-rounded results.
//
// Gamma rather than a linear split (lum = 1-r(1-lit)) because of the shape at the
// faint end, which is the whole point: gamma's density has an infinite slope at 0
// (lit^(1-r)), so it keeps lifting however faint the cell gets, while a linear
// split's is O(lit) and collapses to a dot anyway — just later.
func splashShade(lit, lumRange float64) (dens, lumT float64) {
	switch {
	case lumRange <= 0:
		return lit, 1 // density-only: today's shading, untouched
	case lumRange >= 1:
		return 1, lit // luminance-only: a constant-weight glyph, as rain wants
	case lit <= 0:
		// Load-bearing, not defensive: Log(0) is -Inf, so the division below would
		// be 0/0 = NaN, and int(NaN) is implementation-defined in Go (amd64 gives
		// minint, arm64 gives 0) — it would differ per architecture rather than
		// fail anywhere.
		return 0, 0
	default:
		lumT = math.Exp(lumRange * math.Log(lit))
		return lit / lumT, lumT
	}
}

// shadeAt resolves a cell's hue to the run index the emitter should open, and
// reports whether the cell carries any ink at all.
//
// At lumRange 0 it returns the plain gradient index and never false — the shade
// grid is not consulted, no arithmetic runs, and the emitted bytes are the ones
// every variant produced before this channel existed. That short-circuit is both
// the performance guard and the byte-identity guarantee, which is why it is one
// branch rather than a multiply that happens to come out to the top stop.
//
// The false is the luminance gate, and at lumRange > 0 it is load-bearing in a way
// worth naming: it, not the density ramp's floor, is what keeps a dark cell blank.
// A lifted density reaches glyph 2 or 3 even at lit ≈ 0, so the ramp alone would
// paint the vignette's edges; what blanks them is that stop 0 is near-black ink on
// a dark pane and never worth emitting. Every variant that reaches here ships a
// lumRange above 0, so this gate rather than the ramp is what holds their border.
// Rain is not one of them — it never calls this, drawing from its own ramp — but
// it holds its border the same way, on the same argument, at its own gate in
// renderSplashField.
func shadeAt(hue int, lumT float64, ops splashOps, lut *splashLUT) (int, bool) {
	// <= 0 rather than == 0 to match splashShade's own endpoint predicate: the two
	// decide the same question about the same value and must not be able to
	// disagree at the boundary.
	if ops.lumRange <= 0 {
		return hue, true
	}
	l := clampInt(int(lumT*float64(splashLumStops-1)), 0, splashLumStops-1)
	if l == 0 {
		return -1, false
	}
	return lut.shadeIndex() + hue*splashLumStops + l, true
}

// splashLumHexAt is the luminance curve, and it is the piece rain's ramp and the
// shade grid genuinely share: hold the hue, walk L* from a near-black floor up to
// the colour itself at u == 1, and let chroma fall alongside it under chromaHold.
//
// How fast chroma falls is a look rather than a gamut concession, and the
// distinction is load-bearing: measured, sRGB allows 6.6x to 20x more chroma than
// either law asks for at the dim end, and no stop clips under either. So the curve
// is choosing, not conceding — which is why chromaHold is a parameter at all and why
// its two callers choose differently. See rainChromaHold and shadeChromaHold.
//
// The floor is deliberately not black. A cell that dim renders blank (see the
// l == 0 gate in Pass 2), so the floor is never itself emitted — what it does is
// anchor the low end's shape, and so set how dark the dimmest stop that *does*
// render is. That one has to stay off true black: terminals with a
// minimum-contrast feature rewrite true black to something legible, which would
// speckle a field with the very artifact this curve removes.
func splashLumHexAt(base colorful.Color, u, chromaHold float64) string {
	hue, chroma, lum := base.Hcl()
	return colorful.Hcl(hue, chroma*math.Pow(u, chromaHold), lum*(rainRampFloor+(1-rainRampFloor)*u)).Clamped().Hex()
}

// The chroma laws: the exponent chroma falls by as a column darkens.
//
// Rain's 1.0 is linear, and it is only that because that is what rain shipped
// with. It is drastically more conservative than the gamut requires — measured,
// sRGB allows 6.6x to 20x more chroma than it asks for at the dim end — but rain
// is one hue, so nothing has ever read as grey and there is no reason to move it.
//
// The shade grid cannot afford that. Its whole point is 20 hues, and at the linear
// law the bottom of every column collapses to slate: 8% of the hue's chroma at
// stop 1, 33% at stop 4, so the shaded nebula it was measured on rendered as
// coloured ridges over a grey haze — the exact "downsampled photo" failure the
// luminance channel risked, and still risks. The square root
// holds ~4x the chroma at the faint end (26% at stop 1) and still clips nothing:
// verified across all 20 hues x 16 stops, zero gamut violations.
const (
	rainChromaHold  = 1.0
	shadeChromaHold = 0.5
)

// splashShadeParse resolves a gradient stop to a colour the curve can walk down,
// degrading exactly as buildSplashLUT and rainRampHexAt do rather than emitting
// broken colour for an unusual theme.
func splashShadeParse(c lipgloss.Color) colorful.Color {
	cc, err := colorful.Hex(string(c))
	if err != nil {
		return colorful.Color{R: 0.49, G: 0.81, B: 1}
	}
	return cc
}

// buildShadeGrid builds the hue x luminance grid: every gradient stop, shaded down
// its own luminance axis. Flat rather than [][]splashAffix so the emitter's clamp
// stays one expression and the whole grid is one allocation.
//
// It is deliberately NOT rain's ramp generalized, and the difference is the design.
// Rain's axis has to reach pal.Fg, because a head reads as a head only by the step
// down to the cell behind it. This one has to stop at the hue, because a field
// whose colour is the point cannot have its brightest cells blow out to white. And
// the two cannot be one axis: the climb from an anchor to Fg is 2.1 L* from Cyan
// but 16.4 from Danger, so a shared blow-out would be an 8x stronger gesture at the
// warm end of the hue axis than at the cool end. Two tables, one curve.
//
// Being headless is also what buys the resolution: all 15 steps go to the tail
// rather than 12, so the axis steps ~5.0 L* instead of rain's 6.1.
func buildShadeGrid(colors []lipgloss.Color, affix []splashAffix) []splashAffix {
	grid := make([]splashAffix, len(colors)*splashLumStops)
	top := splashLumStops - 1
	for h, c := range colors {
		base := splashShadeParse(c)
		for l := 0; l < top; l++ {
			hex := splashLumHexAt(base, float64(l)/float64(top), shadeChromaHold)
			grid[h*splashLumStops+l] = splashAffixFor(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)))
		}
		// Pin the top exactly, as buildSplashLUT pins its gradient endpoints: at
		// u == 1 the curve returns the hue, but by way of an HCL round-trip that can
		// nudge the hex a digit. A fully-lit shaded cell has to be the same colour
		// as an unshaded one, not a hair off it.
		grid[h*splashLumStops+top] = affix[h]
	}
	return grid
}
