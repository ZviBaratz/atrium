package ui

// The splash field generator: deterministic noise primitives and the two-pass
// renderer built on them. Pass 1 evaluates the raw (pre-contrast,
// pre-envelope) scalar field into a buffer; Pass 2 applies contrast, the
// envelopes (edge vignette, radial dim, breathing), glyph and color
// quantization, the starfield, and emits run-coalesced ANSI. Everything is
// free of time/rand dependence so renderSplashField stays pure and
// snapshot-testable — animation enters only through the frame counter.
//
// The scene composition (wordmark/message overlay, clearing, gradient LUT)
// lives in splash.go; this file owns the field math and the per-cell loops.

import (
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// splashVariant selects the field generator + glyph technique. Users pin one
// (or keep the random per-launch rotation) via the "splash" config setting;
// the dev-only ATRIUM_SPLASH_VARIANT env override still trumps it for
// screenshot A/B runs and test pinning.
type splashVariant int

const (
	// splashVariantLegacy is the PR #314 sum-of-sines plasma — the comparison
	// baseline while the noise-based variants are tuned.
	splashVariantLegacy splashVariant = iota
	// splashVariantFBM is the domain-warped fBm nebula ("a"): organic,
	// non-repeating filaments instead of periodic rings, with dithering.
	splashVariantFBM
	// splashVariantBraille ("b") is the fBm nebula with its faint zones
	// refined to 2×4 sub-cell braille dots — much finer gradation in the
	// thin gas, while bright cores keep the solid ramp.
	splashVariantBraille
	// splashVariantFlow ("c") is the fBm nebula with a mid-intensity contour
	// band stroked in gradient-oriented line glyphs (─ ╱ │ ╲) — filament
	// edges read as drawn streamlines.
	splashVariantFlow
	// splashVariantJulia ("d") is an animated Julia set with orbit-trap
	// luminance and bloom — the fractal morphs continuously as its c
	// parameter orbits.
	splashVariantJulia
	// splashVariantMandala ("e") is a log-polar fBm kaleidoscope centered on
	// the wordmark — a 2N-fold rosette of the ridged noise field with a slow
	// infinite zoom, rotation, and bloom.
	splashVariantMandala
)

// isFractal groups the escape/trap-based variants, which share bloom, a wide
// contrast window, and the structure-locked hue mix.
func (v splashVariant) isFractal() bool {
	return v == splashVariantJulia || v == splashVariantMandala
}

// splashDefaultVariant is the fallback for an unrecognized override value
// (an unset override rotates instead — see splashActiveVariant) and the
// variant the contract tests pin.
const splashDefaultVariant = splashVariantFBM

// splashRotation is the pool random mode draws from: every finished variant
// except the superseded legacy baseline (still pinnable as "plasma", or via
// the env override).
var splashRotation = []splashVariant{
	splashVariantFBM, splashVariantBraille, splashVariantFlow,
	splashVariantJulia, splashVariantMandala,
}

// splashVariantNames maps the user-facing pattern names (config.SplashVariants,
// cycled in the settings panel) onto the variant enum. ui deliberately takes
// the name as a plain string (SetSplashVariant) so it needs no config import.
var splashVariantNames = map[string]splashVariant{
	"nebula":   splashVariantFBM,
	"braille":  splashVariantBraille,
	"contours": splashVariantFlow,
	"julia":    splashVariantJulia,
	"mandala":  splashVariantMandala,
	"plasma":   splashVariantLegacy,
}

// splashEnvVariant resolves the dev-only ATRIUM_SPLASH_VARIANT override once
// per process. It trumps the config setting so screenshot A/B runs and the
// test suites (which pin "a" in TestMain against rotation nondeterminism)
// stay deterministic whatever the config under test says. The second value
// is false when the variable is unset. Accepts both the user-facing names
// and the historical dev letters.
var splashEnvVariant = sync.OnceValues(func() (splashVariant, bool) {
	s := os.Getenv("ATRIUM_SPLASH_VARIANT")
	if s == "" {
		return splashDefaultVariant, false
	}
	if v, ok := splashVariantNames[s]; ok {
		return v, true
	}
	switch s {
	case "legacy":
		return splashVariantLegacy, true
	case "a":
		return splashVariantFBM, true
	case "b":
		return splashVariantBraille, true
	case "c":
		return splashVariantFlow, true
	case "d":
		return splashVariantJulia, true
	case "e":
		return splashVariantMandala, true
	}
	return splashDefaultVariant, true
})

// The process-wide variant selection: lazily a per-launch rotation draw, or
// whatever SetSplashVariant last pinned or re-rolled. splashRandomMode records
// whether the config asked for random, which is what lets the screensaver
// re-roll per showing while a pinned choice stays put.
//
// splashSelMu guards them. Bubble Tea drives every caller from its single
// update/view goroutine, so the lock is uncontended insurance rather than a
// live need — but it is the same insurance splashLUTFor buys one file over,
// and TerminalPane.String() already takes its own mutex before reaching
// splashScene, i.e. that pane is treated as multi-goroutine-reachable. These
// vars replaced a sync.OnceValue that was safe by construction; a leaf mutex
// keeps the invariant enforced instead of merely documented, at one
// uncontended lock per frame.
var (
	splashSelMu      sync.Mutex
	splashRandomMode = true
	splashPicked     bool
	splashPick       splashVariant
)

// splashActiveVariant resolves the variant splashScene renders: the dev env
// override if set, else the current selection (seeded lazily from the launch
// time, so an unconfigured launch gets a fresh look each time). Resolving to
// one process-wide value is what keeps the preview and terminal panes in
// agreement; only splashScene consults it — renderSplashField takes the
// variant as a parameter and stays pure over its inputs.
func splashActiveVariant() splashVariant {
	if v, ok := splashEnvVariant(); ok {
		return v
	}
	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	if !splashPicked {
		splashPick, splashPicked = splashRotationPick(time.Now().UnixNano()), true
	}
	return splashPick
}

// SetSplashVariant applies the config's splash mode (config.GetSplash): a
// known pattern name pins that generator; anything else (config.SplashRandom,
// or an unknown value) re-rolls a fresh random pick. Called at startup and on
// a live settings change — with the settings panel open over the idle empty
// state, cycling the enum previews each pattern in place.
func SetSplashVariant(name string) {
	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	if v, ok := splashVariantNames[name]; ok {
		splashRandomMode, splashPick, splashPicked = false, v, true
		return
	}
	splashRandomMode = true
	splashPick, splashPicked = splashRotationPick(time.Now().UnixNano()), true
}

// RerollSplashVariant draws a fresh random pick when in random mode (a pinned
// config keeps its pattern). The screensaver calls it on activation so each
// showing within a launch differs from the last: the draw deliberately excludes
// the current pattern, since a re-roll that lands back on it reads as a dead
// keypress rather than a fresh look.
func RerollSplashVariant() {
	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	if splashRandomMode {
		splashPick, splashPicked = splashRotationReroll(time.Now().UnixNano(), splashPick), true
	}
}

// splashRotationPick maps a launch-time nanosecond seed to a rotation variant.
func splashRotationPick(nano int64) splashVariant {
	return splashRotation[splashRotationIdx(nano, len(splashRotation))]
}

// splashRotationReroll maps a seed to a rotation variant other than cur, so a
// re-roll always visibly changes the pattern. It draws over the pool minus cur
// (len-1 slots) and steps past cur's slot, which keeps the result uniform over
// the remaining variants rather than biased toward cur's neighbour. A cur
// outside the pool (the unpicked zero value, or the legacy baseline — pinnable
// but never drawn) has nothing to exclude, so it falls back to a plain draw.
func splashRotationReroll(nano int64, cur splashVariant) splashVariant {
	ci := slices.Index(splashRotation, cur)
	if ci < 0 || len(splashRotation) < 2 {
		return splashRotationPick(nano)
	}
	idx := splashRotationIdx(nano, len(splashRotation)-1)
	if idx >= ci {
		idx++
	}
	return splashRotation[idx]
}

// splashRotationIdx folds a nanosecond seed into [0, n). Go's % preserves the
// dividend's sign, so a clock set before the Unix epoch (a negative UnixNano)
// would otherwise yield a negative index and panic.
func splashRotationIdx(nano int64, n int) int {
	idx := nano % int64(n)
	if idx < 0 {
		idx += int64(n)
	}
	return int(idx)
}

// The domain-warped fBm field ("a" and its derivatives). Frequencies are per
// aspect-corrected cell; drifts are noise-units per phase-unit (phase
// advances driftPerFrame per frame, ~0.9/s at the 60fps splash tick).
const (
	fieldFreq  = 0.10 // base octave frequency → ~10-cell features
	fbmOctaves = 3
	fbmGain    = 0.55 // amplitude falloff per octave
	// fbmRidged0 folds octave 0 into a ridge (n → 1−|2n−1|): bright filament
	// crests instead of soft blobs — the legacy field's charm, kept.
	fbmRidged0 = true

	warpFreq = 0.035 // spatial frequency of the warp vector field
	warpAmp  = 9.0   // warp displacement reach in cells

	// The IQ-style "roil": a small sinusoidal perturbation of the warp vector
	// whose phase varies with |q|, so animation churns the gas in place
	// instead of sliding the whole texture (the wallpaper failure mode).
	roilAmp = 0.05
	roilT1  = 0.27
	roilT2  = 0.23
	roilQ1  = 4.1
	roilQ2  = 4.3

	// A weak warped ring term keeps the field reading as emanating from the
	// wordmark (0 = pure free-form nebula).
	ringWeight = 0.25
	ringFreq   = 0.35

	// fBm concentrates values near 0.5, so its contrast window is narrower
	// than the legacy sum-of-sines one.
	fbmContrastLo = 0.36
	fbmContrastHi = 0.64

	// Hue mix: radius + angular swirl (as legacy) + the warp-vector magnitude
	// — IQ's nebula-coloring trick, a free Pass-1 byproduct that gives the
	// gas layered color structure. Weights are clamped after summing.
	fbmHueRadial    = 0.50
	fbmHueSwirl     = 0.35
	fbmHueWarp      = 0.30
	fbmHueWarpScale = 2.0 // normalizes |q| (max ~0.71) toward [0,1]

	// Fractal variants: a wide contrast window (the trap glow is already
	// contrasty) and a hue mix dominated by the structure helper (escape
	// depth / fold depth), so color bands follow the fractal's geometry.
	fractalContrastLo = 0.12
	fractalContrastHi = 0.88
	fractalHueRadial  = 0.25
	fractalHueSwirl   = 0.15
	fractalHueAux     = 0.60

	// ditherAmp is the dither amplitude in glyph-index steps. MUST stay
	// < 1.0: that is what guarantees a fully-dark cell (lit=0) still rounds
	// to glyph 0, which keeps the vignette's border rows blank.
	ditherAmp = 0.9
)

// seedDither keys the per-cell dither noise (distinct from every field seed).
const seedDither uint32 = 0x94D049BB

// The braille faint band (variant "b"): cells whose lit intensity falls below
// brailleBandHi are re-sampled at their 8 sub-cell dot centers instead of
// taking a ramp glyph.
const (
	brailleBandHi = 0.34
	// brailleHalftoneScale maps a sub-cell's lit value to its dot-firing
	// odds: a dot fires when subLit > dither·scale, so the expected dot
	// count rises linearly with intensity (a halftone) instead of snapping
	// all 8 dots on at once — all-or-nothing thresholding made the faint
	// band read *denser* than the midtones (walls of ⣿). Chosen above
	// brailleBandHi so a cell at the band top lights ~4–5 of its 8 dots,
	// matching the visual weight of the ramp glyph it hands over to. A
	// fully dark sub-cell (subLit = 0) can never fire: the comparison is
	// strict and dither is non-negative.
	brailleHalftoneScale = 0.6
)

// The flow contour band (variant "c"): cells in this lit range whose local
// gradient is strong enough swap their ramp glyph for a line glyph oriented
// along the iso-contour. Direction is only well-conditioned where |∇f| is
// large — everywhere-application would render angular noise, so flat cells
// and the band's outside keep the density ramp (the published prior art's
// edges-only rule).
const (
	flowBandLo  = 0.45
	flowBandHi  = 0.70
	flowGradMin = 0.02
)

// splashFlowGlyph picks the contour-tangent line glyph for a cell from
// central differences of the raw field buffer (one-sided at borders). All
// vector math happens in aspect-corrected space, which is proportional to
// rendered pixel space, so angles are true visual angles — but note the
// diagonals: a cell is cellAspect× taller than wide, so ╱ renders at
// atan(2) ≈ 63.4°, not 45°, and the bin edges sit at the midpoints between
// glyph angles (0°, 63.4°, 90°, 116.6°). Rows grow downward while glyph
// angles are y-up; the sign flip below does that conversion (getting it
// wrong silently swaps ╱ and ╲).
func splashFlowGlyph(vals []float64, w, h, row, col int) (rune, bool) {
	gx := (vals[row*w+min(col+1, w-1)] - vals[row*w+max(col-1, 0)]) / 2
	gy := (vals[min(row+1, h-1)*w+col] - vals[max(row-1, 0)*w+col]) / 2
	// ∂f per aspect-space unit: a row step is cellAspect units.
	gyv := gy / cellAspect
	// Contour tangent = perpendicular to the gradient; then flip to y-up.
	tu, tv := -gyv, gx
	if math.Hypot(tu, tv) < flowGradMin {
		return 0, false
	}
	ang := math.Atan2(-tv, tu) * (180 / math.Pi)
	if ang < 0 {
		ang += 180
	}
	switch {
	case ang < 31.7 || ang >= 148.3:
		return '─', true
	case ang < 76.7:
		return '╱', true
	case ang < 103.3:
		return '│', true
	default:
		return '╲', true
	}
}

// brailleBit maps sub-cell (sy, sx) to its dot bit in U+2800..U+28FF: dots
// 1,2,3,7 run down the left column, dots 4,5,6,8 down the right.
var brailleBit = [4][2]uint8{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// splashBrailleMask re-samples the fBm field at the 8 dot centers of one cell
// and halftones each into a braille dot. dx/dy are the cell's focal-relative
// aspect-corrected center; a dot column step is 0.5 cell and a dot row step
// is 0.5 aspect units (cellAspect/4). The warp vector is evaluated once for
// the cell and shared by all 8 dots (it is constant at cell scale — see
// splashFBMWarpAt); the cell's envelope (vignette × radial × breathe) scales
// every dot, and the per-dot dither runs at sub-cell resolution so dot
// patterns stay granular. A zero mask means "render a space" — bare U+2800
// is never emitted (some fonts draw it as eight hollow circles).
func splashBrailleMask(col, row int, dx, dy, phase, lo, hi, envelope float64) uint8 {
	qx, qy, _ := splashFBMWarpAt(dx, dy, phase)
	wx, wy := warpAmp*qx, warpAmp*qy
	var mask uint8
	for sy := 0; sy < 4; sy++ {
		suby := dy + (float64(sy)-1.5)*0.5
		for sx := 0; sx < 2; sx++ {
			subx := dx + (float64(sx)-0.5)*0.5
			raw := splashFBMBody(subx+wx, suby+wy, phase)
			subLit := smoothstep(lo, hi, raw) * envelope
			if subLit > splashDither(2*col+sx, 4*row+sy)*brailleHalftoneScale {
				mask |= brailleBit[sy][sx]
			}
		}
	}
	return mask
}

// Lattice seeds (arbitrary distinct odd constants) and drift vectors: each
// octave and each warp component drifts in its own direction/speed, which —
// together with the per-octave domain rotation below — is what animates the
// field without 3D noise.
var (
	seedOct   = [fbmOctaves]uint32{0x9E3779B9, 0x85EBCA6B, 0xC2B2AE35}
	octDrift  = [fbmOctaves][2]float64{{0.050, 0.030}, {-0.040, 0.065}, {0.075, -0.050}}
	fbmLacun  = [fbmOctaves - 1]float64{2.01, 2.02} // detuned off 2.0 (IQ: avoids octave self-alignment)
	warpDrift = [2]float64{0.022, -0.018}
	seedWarpX = uint32(0x27D4EB2F)
	seedWarpY = uint32(0x165667B1)
	seedStar  = uint32(0x2545F491)
)

// splashFBMAt evaluates the domain-warped fBm field at one point: a warp
// vector q from two decorrelated noise fields (advected and roiled by phase)
// displaces the sample point, then 3 fBm octaves — ridged crest first,
// per-octave rotated by the exact Pythagorean matrix (0.8, 0.6; −0.6, 0.8) —
// are blended with a weak ring anchored on the wordmark. Returns the raw
// value in [0,1] and the normalized warp magnitude (the hue helper).
func splashFBMAt(dx, dy, phase float64) (val, qLen float64) {
	qx, qy, qq := splashFBMWarpAt(dx, dy, phase)
	return splashFBMBody(dx+warpAmp*qx, dy+warpAmp*qy, phase), clamp01(qq * fbmHueWarpScale)
}

// splashFBMWarpAt evaluates the animated warp vector: two decorrelated noise
// fields (offsets advected by phase) plus the roil perturbation. Split from
// the body so sub-cell refinement can reuse one warp per cell — the warp
// varies over ~1/warpFreq ≈ 28 cells, so it is constant within a cell for
// all visual purposes, and it is the more expensive half of the field.
func splashFBMWarpAt(dx, dy, phase float64) (qx, qy, qq float64) {
	wxn, wyn := dx*warpFreq, dy*warpFreq
	qx = splashValNoise(wxn+warpDrift[0]*phase, wyn, seedWarpX) - 0.5
	qy = splashValNoise(wxn, wyn+warpDrift[1]*phase, seedWarpY) - 0.5
	qq = math.Hypot(qx, qy)
	qx += roilAmp * math.Sin(roilT1*phase+qq*roilQ1)
	qy += roilAmp * math.Sin(roilT2*phase+qq*roilQ2)
	return qx, qy, qq
}

// splashFBMBody is the fBm-plus-ring stack, evaluated at an already-warped
// point.
func splashFBMBody(x, y, phase float64) float64 {
	sum, norm, amp := 0.0, 0.0, 1.0
	fx, fy := x*fieldFreq, y*fieldFreq
	for o := 0; o < fbmOctaves; o++ {
		n := splashValNoise(fx+octDrift[o][0]*phase, fy+octDrift[o][1]*phase, seedOct[o])
		if o == 0 && fbmRidged0 {
			n = 1 - math.Abs(2*n-1)
		}
		sum += amp * n
		norm += amp
		amp *= fbmGain
		if o < fbmOctaves-1 {
			lac := fbmLacun[o]
			fx, fy = lac*(0.8*fx+0.6*fy), lac*(-0.6*fx+0.8*fy)
		}
	}
	n := sum / norm
	ring := 0.5 + 0.5*math.Sin(math.Hypot(x, y)*ringFreq-phase)
	return clamp01((1-ringWeight)*n + ringWeight*ring)
}

// splashColorIdx maps a cell to its gradient stop. The hue swirls across the
// field (radius + a slow angular sweep) so the gradient reads as a drifting
// multi-hued nebula. Legacy keeps its original formula, where aux is the
// warped angle its swirl has always used; noise variants use the unwarped
// angle and add the warp magnitude (their aux) for layered gas-cloud hues.
func splashColorIdx(variant splashVariant, aux, dx, dy, dRaw, phase, maxD float64, nColors int) int {
	var colorT float64
	switch {
	case variant == splashVariantLegacy:
		swirl := 0.5 + 0.5*math.Sin(aux+dRaw*colorSwirlF-phase*colorSwirlSpeed)
		colorT = clamp01(colorRadialMix*(dRaw/maxD) + (1-colorRadialMix)*swirl)
	case variant.isFractal():
		theta := math.Atan2(dy, dx)
		swirl := 0.5 + 0.5*math.Sin(theta+dRaw*colorSwirlF-phase*colorSwirlSpeed)
		colorT = clamp01(fractalHueRadial*(dRaw/maxD) + fractalHueSwirl*swirl + fractalHueAux*aux)
	default:
		theta := math.Atan2(dy, dx)
		swirl := 0.5 + 0.5*math.Sin(theta+dRaw*colorSwirlF-phase*colorSwirlSpeed)
		colorT = clamp01(fbmHueRadial*(dRaw/maxD) + fbmHueSwirl*swirl + fbmHueWarp*aux)
	}
	return clampInt(int(colorT*float64(nColors-1)), 0, nColors-1)
}

// splashCellHash is latticeVal for integer cell coordinates. Pane dimensions
// bound col/row (braille sub-cells at most quadruple them), so the narrowing
// cannot overflow in practice.
func splashCellHash(col, row int, seed uint32) float64 {
	return latticeVal(int32(col), int32(row), seed) //nolint:gosec // G115: cell coords are pane-bounded
}

// splashDither is the per-cell quantization dither in [0,1): plain hash
// (white) noise off the integer lattice hash. White noise was chosen over
// interleaved gradient noise deliberately — IGN is linear along a row (slope
// ≈3.556 mod 1), so in sparse zones it lights cells in a mechanical
// period-2 lattice, while white-noise clumping reads as organic gas grain at
// character-cell scale. Deliberately frame-free: a time term would make
// faint zones boil at the push rate.
func splashDither(col, row int) float64 {
	return splashCellHash(col, row, seedDither)
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

// splashValNoise is bilinear value noise with a smoothstep fade: continuous,
// deterministic, in [0,1). Frequency is applied by the caller (pass x*freq).
// Different seeds give statistically independent fields, which is how the
// warp vector and each fBm octave decorrelate.
func splashValNoise(x, y float64, seed uint32) float64 {
	xi, yi := math.Floor(x), math.Floor(y)
	xf, yf := x-xi, y-yi
	u := xf * xf * (3 - 2*xf)
	v := yf * yf * (3 - 2*yf)
	ix, iy := int32(xi), int32(yi)
	return splashLerp(
		splashLerp(latticeVal(ix, iy, seed), latticeVal(ix+1, iy, seed), u),
		splashLerp(latticeVal(ix, iy+1, seed), latticeVal(ix+1, iy+1, seed), u),
		v)
}

func splashLerp(a, b, t float64) float64 { return a + (b-a)*t }

// splashField is Pass 1's output: the raw scalar field (pre-contrast,
// pre-envelope, in [0,1]) plus a variant-defined hue helper per cell. For the
// legacy field the helper is the warped-coordinate angle its color swirl has
// always used; noise variants will carry their warp-vector magnitude instead
// (the "free" nebula-coloring byproduct).
type splashField struct {
	vals []float64
	aux  []float64
}

// splashEvalField runs Pass 1: the raw field for every cell, in focal-relative
// aspect-corrected coordinates (dx, dy) — the same frame the envelopes and
// color gradient use in Pass 2. Buffers are per-call allocations; at splash
// sizes (19 KB at 80×30) that is cheaper than any pooling would be worth.
func splashEvalField(w, h int, cx, cyFocal, phase float64, at func(dx, dy, phase float64) (float64, float64)) splashField {
	f := splashField{vals: make([]float64, w*h), aux: make([]float64, w*h)}
	i := 0
	for row := 0; row < h; row++ {
		dy := (float64(row) - cyFocal) * cellAspect
		for col := 0; col < w; col++ {
			dx := float64(col) - cx
			f.vals[i], f.aux[i] = at(dx, dy, phase)
			i++
		}
	}
	return f
}

// splashFieldAt returns the per-point field evaluator for a variant. Exposed
// as a point function (not just a buffer fill) so sub-cell techniques can
// re-sample the same field at finer positions.
func splashFieldAt(v splashVariant) func(dx, dy, phase float64) (val, aux float64) {
	switch v {
	case splashVariantLegacy:
		return splashLegacyAt
	case splashVariantJulia:
		return splashJuliaAt
	case splashVariantMandala:
		return splashMandalaAt
	default:
		return splashFBMAt
	}
}

// splashLegacyAt is the PR #314 sum-of-sines plasma, evaluated at one point:
// two domain-warped ring octaves + rotationally-symmetric petals + an
// isotropic fine texture (three plane waves 120° apart, so no diagonal
// grain). Returns the raw value and the warped angle (its swirl input).
func splashLegacyAt(dx, dy, phase float64) (val, theta float64) {
	wx := dx + rippleWarp*math.Sin(dy*rippleWarpF-phase*0.4)
	wy := dy + rippleWarp*math.Sin(dx*rippleWarpF-phase*0.4)
	d := math.Hypot(wx, wy)
	theta = math.Atan2(wy, wx)
	tex := math.Sin(dx*isoFreq-phase*isoSpeed) +
		math.Sin((dx*iso1Cos+dy*iso1Sin)*isoFreq-phase*isoSpeed) +
		math.Sin((dx*iso2Cos+dy*iso2Sin)*isoFreq-phase*isoSpeed)
	v := math.Sin(d*rippleFreq1-phase) +
		0.55*math.Sin(d*rippleFreq2-phase*0.7) +
		0.40*math.Sin(d*rippleFreq3-phase*0.5)*math.Cos(theta*petalCount) +
		isoWeight*tex
	return clamp01((v/rippleAmp + 1) * 0.5), theta
}

// renderSplashField builds the colored plasma background: exactly h rows of
// exactly w visible cells, with the clearing ellipses blanked out for the
// composited text. The field fills the whole pane and softens only near the
// four borders (an edge vignette), rather than being a single disc inscribed
// to the shorter axis. The pattern emanates from the wordmark's center
// (clearing.wordCenterRow) and the color gradient / gentle radial dim are
// normalized to the farthest corner, so the field stays visually anchored on
// the wordmark while still reaching the edges. Pure over its inputs
// (deterministic, snapshot-testable); returns "" on a degenerate pane.
func renderSplashField(w, h, frame int, pal theme.Palette, clearing splashClearing, variant splashVariant) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	cx := float64(w-1) / 2
	cyFocal := float64(clearing.wordCenterRow)
	// Distance from the focal point to the farthest corner: the denominator for
	// the color gradient and the core→rim dim, so both span the whole pane.
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

	at := splashFieldAt(variant)
	fld := splashEvalField(w, h, cx, cyFocal, phase, at)
	if variant.isFractal() {
		splashBloom(fld, w, h)
	}

	lut := splashLUTFor(pal)
	nColors := len(lut.styles)
	starIdx := lut.starIndex() // splashRunAffix emits any index >= this as a star
	ramp := []rune(splashRamp)
	maxGlyph := len(ramp) - 1
	starRampR := []rune(starRamp)
	starMax := len(starRampR) - 1
	// Slow global brightness swell (breathing), computed once per frame.
	breathe := 1 - breatheDepth*(0.5-0.5*math.Sin(phase*breatheSpeed))
	// Per-variant Pass-2 behavior: the legacy field keeps its wide contrast
	// window and no dither (it stays a faithful baseline); noise variants get
	// the narrower fBm window, fractals a wide one (trap glow is already
	// contrasty); both get dithering.
	contrastLo, contrastHi := splashContrastLo, splashContrastHi
	dither := false
	switch {
	case variant == splashVariantLegacy:
	case variant.isFractal():
		contrastLo, contrastHi = fractalContrastLo, fractalContrastHi
		dither = true
	default:
		contrastLo, contrastHi = fbmContrastLo, fbmContrastHi
		dither = true
	}

	var sb strings.Builder
	// A seed, not a bound — the run count isn't known until the field is walked.
	// ~4 bytes/cell is where the truecolor output actually lands (measured
	// 2.1–5.5 across the variants: a cell is one glyph plus its share of the SGR
	// bracket its run pays for). Seeding near the real size is what holds
	// Builder's doubling to a step or two: at 240×60 it is the difference
	// between ~24 allocations a frame and 4. A colorless profile emits ~1
	// byte/cell and merely over-seeds — one buffer, no copies.
	sb.Grow(w*h*4 + h)
	for row := 0; row < h; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		dy := (float64(row) - cyFocal) * cellAspect
		// edgeY is exactly 0 on the first/last rows, and the envelope multiplies
		// the field *after* any Pass-1 processing — that construction (not
		// tuning) is what keeps the border rows blank.
		edgeY := smoothstep(0, 1, clamp01(math.Min(float64(row), float64(h-1-row))/marginY))
		curIdx := -1 // -1 marks a blank (uncolored) run
		for col := 0; col < w; col++ {
			dx := float64(col) - cx
			idx, ch := -1, ' '

			if edgeY > 0 && !clearing.blanks(dx, row) {
				cell := row*w + col
				dRaw := math.Hypot(dx, dy)
				// Contrast: push mid-tones apart so bright ridges read as
				// filaments against darker voids.
				intensity := smoothstep(contrastLo, contrastHi, fld.vals[cell])
				edgeX := smoothstep(0, 1, clamp01(math.Min(float64(col), float64(w-1-col))/marginX))
				radial := 1 - radialDim*clamp01(dRaw/maxD)
				envelope := edgeX * edgeY * radial * breathe
				lit := intensity * envelope
				if variant == splashVariantBraille && lit < brailleBandHi {
					// Faint gas: refine to sub-cell braille dots instead of a
					// (coarse) ramp glyph; bright cores keep the solid ramp.
					if mask := splashBrailleMask(col, row, dx, dy, phase, contrastLo, contrastHi, envelope); mask != 0 {
						ch = rune(0x2800) | rune(mask)
						idx = splashColorIdx(variant, fld.aux[cell], dx, dy, dRaw, phase, maxD, nColors)
					}
				} else {
					gf := lit * float64(maxGlyph)
					if dither {
						// Sub-step dither in glyph-index space: kills banding on
						// smooth gradients. Amplitude < 1 step, so lit=0 stays
						// glyph 0 (the blank-border invariant).
						gf += (splashDither(col, row) - 0.5) * ditherAmp
					}
					if g := clampInt(int(gf), 0, maxGlyph); g > 0 {
						ch = ramp[g]
						if variant == splashVariantFlow && lit >= flowBandLo && lit <= flowBandHi {
							// Stroke the contour band along the field's
							// iso-lines; flat cells keep the ramp glyph.
							if fg, ok := splashFlowGlyph(fld.vals, w, h, row, col); ok {
								ch = fg
							}
						}
						idx = splashColorIdx(variant, fld.aux[cell], dx, dy, dRaw, phase, maxD, nColors)
					}
				}
				// Starfield on top: a fixed, twinkling point can light even a void
				// the plasma left dark. Fades with the same border vignette.
				if sh := starHash(col, row); sh > starThreshold {
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
