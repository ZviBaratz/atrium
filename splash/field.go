// Package splash renders Atrium's animated empty-state splash field: a
// slow-drifting, theme-coloured pattern that appears to emanate from a focal
// row. It is a self-contained engine — the field math, the gradient LUT, and
// the variant vocabulary — driven by package ui, which owns scene composition
// (the wordmark/message overlay) and variant selection and calls in through
// Render.
package splash

// The splash field generator: deterministic noise primitives and the two-pass
// renderer built on them. Pass 1 evaluates the raw (pre-contrast) scalar field
// into a buffer; Pass 2 applies the contrast curve, the edge vignette, glyph
// and color quantization, the starfield, and emits run-coalesced ANSI.
// Everything is free of time/rand dependence so Render stays pure and
// snapshot-testable — animation enters only through the frame counter.
//
// The gradient LUT lives in lut.go and the variant vocabulary in variant.go;
// this file owns the field math, the per-cell loops, and the Render entrypoint.

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// seedStar keys the fixed starfield (an arbitrary distinct odd constant); see
// starHash in lut.go.
var seedStar = uint32(0x2545F491)

// splashColorIdx maps a cell's hue helper to its gradient stop, and it is the
// package's whole hue contract: aux *is* the gradient position, in [0,1].
//
// There used to be a mix of screen position (radius + a slow angular swirl) with
// the field's own aux, and it was the default arm — which silently swallowed four
// new variants in a row, because a field that made no hue decision still rendered
// something plausible: a stationary rosette painted by pane coordinates. Every
// surviving field's hue is a property of the thing being drawn rather than of the
// cell's address — the tunnel's mipped depth band (splashTunnelAtFor), ripple's
// ring age (splashRippleSum) — so there is one rule and no way to miss it. A
// field wanting position-based hue re-introduces the mix with its own argument
// for it.
//
// Rain is the one variant that never arrives here: it draws from its own
// luminance ramp (see the rain branch in renderSplashField), which is why this
// takes no variant.
//
// A field that feeds aux outside [0,1] gets clamped to a flat end of the
// gradient — loud, and visibly not a design, which is the intent.
func splashColorIdx(aux float64, nColors int) int {
	return clampInt(int(clamp01(aux)*float64(nColors-1)), 0, nColors-1)
}

// splashCellHash is latticeVal for integer cell coordinates. Pane dimensions
// bound col/row, so the narrowing cannot overflow in practice.
func splashCellHash(col, row int, seed uint32) float64 {
	return latticeVal(int32(col), int32(row), seed) //nolint:gosec // G115: cell coords are pane-bounded
}

// splashHash is a deterministic 32-bit lattice hash: seed, y, and x are folded
// in through *nested* avalanche passes (lowbias32-style mixer at each level).
// Nesting matters: folding the coordinates linearly (x*A ^ y*B) collides on
// every point-symmetric pair (x,y)/(-x,-y), which would mirror the noise
// around the field's origin — the wordmark center. Being pure integer math it
// is exact on every architecture, unlike the classic sin-fract hash, whose
// platform-dependent sin precision can make hash-derived features differ
// across builds.
func splashHash(x, y int32, seed uint32) uint32 {
	return lowbias32(splashU32(x) ^ lowbias32(splashU32(y)^lowbias32(seed^0x9E3779B9)))
}

// lowbias32 is Chris Wellons' low-bias 32-bit avalanche mixer: every input bit
// flips ~half the output bits, in two multiplies and three shifts.
func lowbias32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x7FEB352D
	h ^= h >> 15
	h *= 0x846CA68B
	h ^= h >> 16
	return h
}

// splashU32 reinterprets a signed lattice coordinate as its two's-complement
// bit pattern for hashing; the value change on negatives is the point (it
// keeps negative coordinates distinct without branching).
func splashU32(v int32) uint32 { return uint32(v) } //nolint:gosec // G115: intentional bit reinterpretation for hashing

// latticeVal maps the lattice hash to a float in [0,1).
func latticeVal(x, y int32, seed uint32) float64 {
	return float64(splashHash(x, y, seed)) * (1.0 / 4294967296.0)
}

func splashLerp(a, b, t float64) float64 { return a + (b-a)*t }

// splashField is Pass 1's output: the raw scalar field (pre-contrast,
// pre-envelope, in [0,1]) plus a variant-defined hue helper per cell. The helper
// is the gradient position the field wants for that cell — see splashColorIdx.
type splashField struct {
	vals []float64
	aux  []float64
}

// splashEvalField runs Pass 1: the raw field for every cell, in focal-relative
// aspect-corrected coordinates (dx, dy) — the same frame the edge vignette uses
// in Pass 2. Buffers are per-call allocations; at splash
// sizes (19 KB at 80×30) that is cheaper than any pooling would be worth.
func splashEvalField(w, h int, cx, cyFocal, phase float64, at splashPointFn) splashField {
	f := splashField{vals: make([]float64, w*h), aux: make([]float64, w*h)}
	i := 0
	for row := 0; row < h; row++ {
		dy := (float64(row) - cyFocal) * cellAspect
		for col := 0; col < w; col++ {
			dx := float64(col) - cx
			f.vals[i], f.aux[i] = at(col, row, dx, dy, phase)
			i++
		}
	}
	return f
}

// splashPointFn evaluates one cell's raw field value and its hue helper, both
// in [0,1]. It must be pure over its arguments: animation enters only through
// phase, and randomness only through the integer lattice hash.
//
// The continuous (dx, dy) is the focal-relative, aspect-corrected position and
// is what the field math wants. The integer (col, row) is the cell's identity,
// which per-column effects need and cannot recover from dx: dx is col - cx, and
// cx is a half-integer on even-width panes, so a generator would have to guess
// the pane's parity to round back to a column. Most evaluators ignore it.
type splashPointFn func(col, row int, dx, dy, phase float64) (val, aux float64)

// splashFieldAt returns the per-point field evaluator for a variant. Exposed as
// a point function rather than a buffer fill so a field can be sampled directly:
// the braille variant used to re-sample it at sub-cell positions, and now it is
// how the per-variant guards ask what a field actually computes, instead of
// comparing two renders and measuring the ops table by accident.
//
// maxD is the pane's focal-point-to-farthest-corner radius, and only a variant
// whose subject is a single object needs it. The fields are scale-free: rain's
// streams are drawn in absolute cells on purpose, so a bigger pane shows *more*
// streams, which is what more window should buy — and ripple's drops the same.
// The tunnel is one corridor rather than a field of many things, so
// the same rule would instead show more of it — and since perspective bunches all
// of its detail near the vanishing point, a small pane would land entirely inside
// the mipped core and render a vague dark blob with no rings at all. Measured at
// 90×28: the whole pane reaches r≈53 while the wall only resolves past r≈32.
// Scaling it to the pane makes it the same tunnel at every size.
func splashFieldAt(v Variant, maxD float64) splashPointFn {
	switch v {
	case Tunnel:
		return splashTunnelAtFor(maxD)
	case Ripple:
		return splashRippleAt
	default:
		// Rain, and the fallback for a variant that forgot its case here.
		//
		// Which variant this arm serves is load-bearing, and the reason is not the
		// obvious one. The tunnel and ripple each have a guard asserting their field
		// is not the one a forgotten case would hand them, and each probes that by
		// sampling *rain* — which works only because rain is what this arm returns.
		// So the probe and this arm have to name the same variant. Give rain a case
		// of its own and point this somewhere else and both guards keep passing
		// while no longer testing the fallback at all: that is the quiet failure.
		// (Pointing it at the tunnel is the loud one — the guard would compare the
		// tunnel to itself and fail on every run.) Rain needs no guard of its own,
		// because being this arm is what makes it unmisroutable.
		return splashRainAt
	}
}

// Options configures Render.
type Options struct {
	// Palette supplies the four warm→cool gradient anchors plus the star /
	// rain-head highlight.
	Palette Palette
	// Variant selects the field generator.
	Variant Variant
	// FocalRow is the pane row the pattern emanates from — the origin of the
	// focal-relative coordinates every field is evaluated in. A negative value
	// centres it on the pane.
	FocalRow int
	// LumRange, when non-nil, overrides the variant's shipped luminance-range
	// policy (the split between glyph density and colour luminance); nil keeps
	// the variant's default. It is how package ui applies the dev-only
	// ATRIUM_SPLASH_LUMRANGE knob without this package reading the environment.
	LumRange *float64
	// Profile, when non-nil, pins the color depth Render emits at (truecolor,
	// 256, 16, or none), independent of the process-global color profile. nil
	// defers to lipgloss.ColorProfile() — the auto-detected terminal profile —
	// which is what package ui wants when rendering to a real pane. Setting it
	// makes Render pure over its inputs: the same Options yield the same bytes
	// regardless of ambient stdout state, which is what a standalone consumer
	// (and a snapshot test) needs.
	Profile *termenv.Profile
}

// Render builds the colored splash field background: exactly h rows of exactly
// w visible cells, or "" on a degenerate pane. Pure over its inputs
// (deterministic, snapshot-testable). It resolves the focal row and the
// per-variant Pass-2 policy (applying Options.LumRange over the variant default)
// and hands off to renderField.
func Render(w, h, frame int, opts Options) string {
	focalRow := opts.FocalRow
	if focalRow < 0 {
		focalRow = (h - 1) / 2
	}
	ops := opts.Variant.ops()
	if opts.LumRange != nil {
		ops.lumRange = *opts.LumRange
	}
	// Resolve the color depth: an explicit Options.Profile wins, otherwise defer
	// to the auto-detected terminal profile. This is the only place the ambient
	// profile is read, so a caller that pins Profile makes Render pure over its
	// inputs (see splashLUTFor, which bakes the SGR bytes for this profile).
	prof := lipgloss.ColorProfile()
	if opts.Profile != nil {
		prof = *opts.Profile
	}
	return renderField(w, h, frame, opts.Palette, focalRow, opts.Variant, ops, prof)
}

// renderField builds the colored field background: exactly h rows of
// exactly w visible cells. The field fills the whole pane and softens only near
// the four borders (an edge vignette), rather than being a single disc inscribed
// to the shorter axis. The pattern emanates from focalRow — the wordmark's centre
// row, the origin of the focal-relative coordinates every field is evaluated in —
// and a size-relative variant scales itself against the focal-point-to-corner
// radius (see splashFieldAt), so the field stays visually anchored on the wordmark
// while still reaching the edges. Pure over its inputs (deterministic,
// snapshot-testable); returns "" on a degenerate pane. ops is the resolved
// per-variant Pass-2 policy (see Render and Variant.ops), and prof is the
// resolved color profile the emitted SGR is baked for (see Render).
func renderField(w, h, frame int, pal Palette, focalRow int, variant Variant, ops splashOps, prof termenv.Profile) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	cx := float64(w-1) / 2
	cyFocal := float64(focalRow)
	// Distance from the focal point to the farthest corner: the length scale a
	// variant whose subject is one object measures itself against (see
	// splashFieldAt), so it spans the whole pane.
	maxD := math.Hypot(
		math.Max(cx, float64(w-1)-cx),
		math.Max(cyFocal, float64(h-1)-cyFocal)*cellAspect)
	if maxD <= 0 {
		return ""
	}
	// Border-fade reach in cells (min 1 so a tiny pane still fades, never /0).
	marginX := math.Max(1, float64(w)*edgeVignetteFrac)
	marginY := math.Max(1, float64(h)*edgeVignetteFrac)
	phase := float64(frame) * driftPerFrame

	at := splashFieldAt(variant, maxD)
	fld := splashEvalField(w, h, cx, cyFocal, phase, at)

	lut := splashLUTFor(pal, prof)
	nColors := len(lut.styles)
	starIdx := lut.starIndex() // splashRunAffix emits any index >= this as a star
	ramp := []rune(splashRamp)
	maxGlyph := len(ramp) - 1
	starRampR := []rune(starRamp)
	starMax := len(starRampR) - 1

	var sb strings.Builder
	// A seed, not a bound — the run count isn't known until the field is walked.
	// ~4 bytes/cell is where the truecolor output actually lands at lumRange 0
	// (measured 2.1–5.5 across the variants: a cell is one glyph plus its share of
	// the SGR bracket its run pays for). Seeding near the real size is what holds
	// Builder's doubling to a step or two: at 240×60 it is the difference between
	// ~24 allocations a frame and 4. A colorless profile emits ~1 byte/cell and
	// merely over-seeds — one buffer, no copies.
	//
	// A luminance-shaded field lands far higher and the seed has to know it.
	// Brightness stepping per cell is why — a run breaks when *either* hue or
	// luminance changes, and runs already coalesced at only ~1.14 cells, so nearly
	// every cell pays its own SGR bracket. Seeded at 4 the Builder doubled twice
	// more and copied ~150KB a frame for nothing; rain was paying that on the
	// 4-byte seed too, at 6 allocations a frame rather than 4.
	//
	// Measured at 240×60: 7.2 bytes/cell for rain, 9.9–10.8 for a shaded nebula
	// across lumRange 0.35–0.7, and 17.5 for tunnel (17.1 at 90×28). Tunnel is the
	// high-water mark because it is the first field that is *both* shaded and
	// dense: nearly every cell is lit and nearly every one steps a channel, so
	// almost none share a bracket.
	//
	// One constant serves all of them, and it is sized for that worst case rather
	// than the average — which is the trade this seed exists to make. At 11 (rain's
	// figure plus headroom) tunnel took two further doublings: 893KB and 7
	// allocations a frame against 508KB and 5. Sizing for 18 costs sparse rain
	// ~98KB of transient buffer it never fills, at no change to its allocation
	// count, and saves the dense variant ~385KB a frame — ~23MB/s of garbage at
	// 60fps. Neither shows up in the frame time; both sit well inside budget. It is
	// a seed, not a bound: undershooting costs copies, overshooting costs a page
	// that is never touched, and the asymmetry is why it rounds up.
	perCell := 4
	if ops.lumRange > 0 {
		perCell = 18
	}
	sb.Grow(w*h*perCell + h)
	for row := 0; row < h; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		// edgeY is exactly 0 on the first/last rows, and the envelope multiplies
		// the field *after* any Pass-1 processing — that construction (not
		// tuning) is what keeps the border rows blank.
		edgeY := smoothstep(0, 1, clamp01(math.Min(float64(row), float64(h-1-row))/marginY))
		curIdx := -1 // -1 marks a blank (uncolored) run
		for col := 0; col < w; col++ {
			idx, ch := -1, ' '

			if edgeY > 0 {
				cell := row*w + col
				// The contrast curve. It is full-range — it clips nothing, which is
				// what a field carrying its own gradient needs — but it is NOT an
				// identity, and the distinction is worth the call it invites you to
				// skip: smoothstep is Hermite on the *clamped* parameter, so a {0,1}
				// window still bends every interior value through t*t*(3-2t). Each
				// field is tuned against this S-curved version of its output rather
				// than against its generator's raw values. Pinned by
				// TestFieldContrastIsStillHermite.
				intensity := smoothstep(0, 1, fld.vals[cell])
				edgeX := smoothstep(0, 1, clamp01(math.Min(float64(col), float64(w-1-col))/marginX))
				envelope := edgeX * edgeY
				lit := intensity * envelope
				// Split lit between the two channels. At lumRange 0 this is the
				// identity (dens == lit, lumT unused); at 1 the glyph holds full
				// weight and brightness rides the colour entirely, which is rain.
				dens, lumT := splashShade(lit, ops.lumRange)
				if variant == Rain {
					// Rain's own ramp: it blows out past the stream hue to white,
					// which the shade grid deliberately does not (see
					// buildShadeGrid). Its gate is luminance because its glyph has
					// constant weight — there is no light end to fade into.
					//
					// Stop 0 stays blank rather than emitting the ramp's floor: the
					// floor anchors the ramp's shape (see buildRainRamp), but
					// painting it would put a near-black glyph *over* the terminal
					// background, which is a mark where the design wants none. So
					// stop 0 is never emitted and the tail dies into the pane.
					if g := clampInt(int(lumT*float64(len(lut.rain)-1)), 0, len(lut.rain)-1); g > 0 {
						ch = splashRainGlyph(col, row, phase)
						idx = lut.rainIndex() + g
					}
				} else {
					gf := dens * float64(maxGlyph)
					if g := clampInt(int(gf), 0, maxGlyph); g > 0 {
						if si, ok := shadeAt(splashColorIdx(fld.aux[cell], nColors), lumT, ops, lut); ok {
							ch = ramp[g]
							idx = si
						}
					}
				}
				// Starfield on top: a fixed, twinkling point can light even a void
				// the field left dark. Fades with the same border vignette.
				// Variants whose own motion the eye tracks opt out — fixed points
				// over moving ones read as stuck pixels.
				if sh := starHash(col, row); ops.stars && sh > starThreshold {
					tw := 0.7 + 0.3*math.Sin(phase*starTwinkleSpeed+sh*starPhaseScatter)
					if sg := clampInt(int(tw*edgeX*edgeY*float64(starMax)), 0, starMax); sg > 0 {
						ch = starRampR[sg]
						idx = starIdx
					}
				}
			}

			if idx != curIdx {
				splashCloseRun(&sb, curIdx, lut)
				splashOpenRun(&sb, idx, lut)
				curIdx = idx
			}
			sb.WriteRune(ch)
		}
		splashCloseRun(&sb, curIdx, lut)
	}
	return sb.String()
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
