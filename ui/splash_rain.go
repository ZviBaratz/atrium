package ui

// Matrix-style digital rain: per-column streams of glyphs falling with bright
// heads and fading tails, layered at three depths for parallax.
//
// PROTOTYPE. This exists to answer one question before the variant is worth
// finishing: the wordmark's clearing ellipse hard-blanks the field where the
// text goes (splashClearing.blanks). The nebula's organic texture hides that
// completely, but rain is *structured* — a stream that vanishes for eight rows
// and resumes below might read as correct occlusion (the wordmark is in front
// of the rain) or as a rendering bug. No amount of field math settles that; it
// needs eyes on a real terminal.

import "math"

const (
	// rainFall is the fall speed in aspect units per phase unit. phase advances
	// driftPerFrame (0.015) per frame at ~60fps, so 30 aspect units/phase is
	// 30 * 0.9 / cellAspect ≈ 13.5 rows/second — cmatrix's own pace. Per frame a
	// head moves 30*0.015/2 = 0.225 rows, i.e. it sits in the same row for ~4
	// frames.
	//
	// That is exactly why brightness below is a function of the *continuous*
	// distance to the head and never of a rounded row count. Quantized rain
	// would be a 4fps stutter inside a 60fps tick, and — because the contract
	// requires consecutive frames to differ, while rain lights far fewer cells
	// than the dense fields do — it would also make that test a coin flip
	// rather than a guarantee. The sub-row gradient fixes both at once.
	rainFall = 30.0

	// Per-column speed spread. Columns must not fall in lockstep or the rain
	// reads as one sliding texture instead of many streams.
	rainSpdMin = 0.55
	rainSpdMax = 1.45

	// Tail length range in aspect units, hashed per stream. Must stay under
	// half the layer period, or a stream's tail reaches the head behind it and
	// the column reads as a solid line with no gaps. The gaps are load-bearing:
	// uninterrupted rain reads as static noise; rain with rhythm reads as
	// falling.
	rainTailMin = 8.0
	rainTailMax = 26.0

	// rainHeadR is the head lobe's radius in aspect units. Rows are cellAspect
	// (2.0) apart, so a radius above 2 guarantees at least one saturated head
	// cell plus a soft leading edge that slides between rows — that slide is
	// the sub-cell interpolation made visible.
	rainHeadR = 3.2

	// rainDensity is the fraction of (column, layer, stream) slots that carry a
	// stream at all; the rest are gaps.
	rainDensity = 0.85

	// rainTailAmp caps the tail's brightness so it stays below the head's.
	rainTailAmp = 0.82

	// rainHeadLo is the raw field value at or above which a cell is drawn in the
	// bright near-white head colour rather than the gradient. It sits above
	// rainTailAmp by construction, so only the head lobe can reach it — the tail
	// never promotes itself.
	rainHeadLo = 0.9
)

// rainLayers are the parallax depths, near to far.
//
// Depth is carried by four cues at once, because on this palette no single one
// is enough. speed and bright are the classic pair — motion parallax is
// monocular and needs no vanishing point, so nearer simply means faster and
// stronger. period spaces the far layers' streams more tightly, the way distance
// packs anything together. hue is the one that does the heavy lifting here: the
// gradient LUT is near-equal-luminance by construction, so it cannot shade for
// depth, but it *can* separate the layers outright — near streams sit at the
// cyan end, far ones at the warm end, and a glyph's colour says which layer it
// belongs to no matter where on screen it lands.
//
// Only the near layer's peak clears rainHeadLo, so only it gets white heads.
// That is deliberate and is the fifth cue: the brightest thing on screen is
// always the nearest.
var rainLayers = [3]struct {
	speed, bright, period, hue float64
}{
	{speed: 1.00, bright: 1.00, period: 58.0, hue: 1.00}, // near: cyan, white-headed
	{speed: 0.62, bright: 0.72, period: 42.0, hue: 0.55}, // mid:  blue/violet
	{speed: 0.40, bright: 0.45, period: 30.0, hue: 0.14}, // far:  warm, dim
}

// rainHueSpread is how much of the LUT a single stream wanders across as it
// fades from head to tail, around its layer's anchor. Small on purpose: it keeps
// a stream from reading as a flat bar of one colour, without letting a near
// stream's tail drift into the far layer's hue and undo the separation.
const rainHueSpread = 0.10

// Lattice seeds for the per-stream draws (distinct from every field seed).
const (
	seedRainOff  uint32 = 0x51A7C39B
	seedRainSpd  uint32 = 0x7B3D2E11
	seedRainTail uint32 = 0x2C9E4F07
	seedRainLive uint32 = 0x6D1B8A53
)

// splashRainAt evaluates the rain field at one cell.
//
// The formulation is a *stream train*: rather than tracking one head per column
// and wrapping it at the pane height, each column carries an infinite train of
// heads spaced `period` apart, drifting downward with phase. A cell asks which
// head is nearest and how far behind it sits. Two things fall out of that.
// First, no pane height is needed — the evaluator never learns h, so a taller
// pane simply shows more of the same rain instead of the same rain stretched.
// Second, a stream's identity is the head index k, which is fixed for that
// stream's whole life, so its speed and tail length can be hashed from it and
// never flicker as it falls.
func splashRainAt(col, _ int, _, dy, phase float64) (val, aux float64) {
	best, bestAux := 0.0, 0.0
	for li := range rainLayers {
		L := rainLayers[li]

		// Per-column draws, constant for the column's whole life.
		sp := rainSpdMin + (rainSpdMax-rainSpdMin)*splashCellHash(col, li, seedRainSpd)
		// A full-period offset, not a jitter: a small scatter would leave frame 0
		// showing a rank of heads marching in lockstep, and columns only desync
		// slowly afterward via their speed spread.
		off := splashCellHash(col, li, seedRainOff) * L.period

		// Which head of this column's train is nearest, and how far behind it.
		g := (dy - phase*rainFall*sp - off) / L.period
		kf := math.Round(g)
		// Round, not Floor: it makes d signed, so the head's lobe straddles the
		// two rows it lies between instead of snapping to the one below it.
		d := (kf - g) * L.period // >0 ⇒ this cell trails the head (its tail)
		// g is finite by construction (every term is, and period is a nonzero
		// constant), so this conversion is defined — unlike a float→int of an
		// Inf, which is implementation-defined and would differ across arches.
		// It grows with phase at ~0.5 units/frame, so int32's range is some
		// centuries of continuous animation away.
		k := int(kf)

		// Per-stream draws, keyed on the stream's identity so they hold for its
		// whole life rather than changing under it as it falls.
		if splashCellHash(col^k, li, seedRainLive) > rainDensity {
			continue // a gap in this column's train
		}
		tail := rainTailMin + (rainTailMax-rainTailMin)*splashCellHash(col+k, li, seedRainTail)

		// Head lobe, then tail. Both are continuous in d — that is the whole
		// trick (see rainFall).
		lit := clamp01((rainHeadR - math.Abs(d)) / rainHeadR)
		if d > 0 {
			if t := rainTailAmp * clamp01(1-d/tail); t > lit {
				lit = t
			}
		}
		along := lit // position along the stream, before the layer dims it
		lit *= L.bright
		if lit > best {
			best = lit
			// Hue names the layer, so the eye can tell the depths apart wherever
			// they cross; the stream's own fade only nudges it around that anchor
			// (see rainHueSpread).
			bestAux = clamp01(L.hue + (along-0.5)*2*rainHueSpread)
		}
	}
	// Layers combine by max, not by sum: a far stream crossing behind a near
	// one must not brighten its head — and taking the max is also what makes the
	// near layer *occlude* the far one rather than blend with it.
	return clamp01(best), bestAux
}
