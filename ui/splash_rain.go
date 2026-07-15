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

	// rainTailAmp caps the tail's brightness so it stays below the head's, which
	// is what reserves the luminance ramp's white top end for the head alone.
	rainTailAmp = 0.82
)

// rainLayers are the parallax depths, near to far.
//
// Depth is luminance first. Each layer's bright caps how far up the ramp its
// streams can climb, and the ramp runs dark → the stream hue → white: the near
// layer reaches the white head, the mid layer tops out around the stream hue,
// and the far layer never leaves the dim end. That is atmospheric perspective,
// and it is the cue the earlier hue-per-layer attempt was standing in for —
// badly, because hue says *which* layer without saying which is nearer.
//
// speed is the second cue, and an independent one: motion parallax is monocular
// and needs no vanishing point, so nearer simply means faster. period spaces the
// far layers' streams more tightly, the way distance packs anything together.
var rainLayers = [3]struct {
	speed, bright, period float64
}{
	{speed: 1.00, bright: 1.00, period: 58.0}, // near: reaches white
	{speed: 0.62, bright: 0.72, period: 42.0}, // mid:  the stream hue
	{speed: 0.40, bright: 0.45, period: 30.0}, // far:  dim only
}

// Lattice seeds for the per-stream draws (distinct from every field seed).
const (
	seedRainOff   uint32 = 0x51A7C39B
	seedRainSpd   uint32 = 0x7B3D2E11
	seedRainTail  uint32 = 0x2C9E4F07
	seedRainLive  uint32 = 0x6D1B8A53
	seedRainGlyph uint32 = 0x3F5B7C21
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
		lit *= L.bright
		if lit > best {
			best = lit
			bestAux = lit // unused by the luminance path; kept in [0,1] for the contract
		}
	}
	// Layers combine by max, not by sum: a far stream crossing behind a near
	// one must not brighten its head — and taking the max is also what makes the
	// near layer *occlude* the far one rather than blend with it.
	return clamp01(best), bestAux
}

// splashRainGlyphs is the vocabulary a stream's cells are drawn from.
//
// Deliberately all ASCII, for two reasons. It is byte-indexable, so the modulo
// below picks a character rather than slicing a multi-byte rune in half — a
// trap the moment this set grows a box-drawing or katakana glyph. And every
// character renders on any font: half-width katakana would be the authentic
// Matrix look and is correctly terminal-width-1, but its coverage is far
// patchier than the box-drawing and braille this codebase already leans on, and
// a pane of tofu is worse than the wrong alphabet.
//
// The glyphs are chosen for even visual weight. Brightness is the luminance
// ramp's job now, so a light "." mixed in among them would read as a hole in
// the stream rather than as a dimmer cell.
const splashRainGlyphs = "0123456789ABCDEFHKLMNPRSTVXYZ<>[]{}=+*#%&$@?!/\\|"

// splashRainMutSpeed is how fast a cell re-draws its glyph, in mutations per
// phase unit. Slow on purpose: mutating every frame boils, and the eye reads
// churn as noise rather than as falling.
const splashRainMutSpeed = 1.6

// splashRainGlyph picks a cell's character. It is keyed on the cell rather than
// on the stream, so a glyph belongs to a position the rain falls *through* —
// which is what makes a stream read as passing over the screen rather than as a
// rigid object sliding down it.
func splashRainGlyph(col, row int, phase float64) rune {
	epoch := int(phase * splashRainMutSpeed)
	h := splashHash(int32(col), int32(row*977+epoch), seedRainGlyph) //nolint:gosec // G115: cell coords are pane-bounded
	return rune(splashRainGlyphs[h%uint32(len(splashRainGlyphs))])
}
