package ui

// Fractal/geometric field generators for the splash: an animated Julia set
// ("d") and a log-polar fBm kaleidoscope ("e"). Both plug into the same
// two-pass pipeline as the fBm nebula — they only produce the raw scalar
// field (plus a hue helper) and inherit the envelopes, dithering, starfield,
// and emission unchanged. Like everything in the splash, they are pure over
// (position, phase): animation enters only through trigonometric functions of
// phase, never time or rand.

import "math"

// The Julia variant ("d"): z ← z² + c escape-time iteration where c orbits
// the origin at the classic morphing radius, so the fractal continuously
// reshapes. The exterior is filled with animated equipotential bands (smooth
// iteration count through a cosine), which compress into fine filigree near
// the set boundary; an orbit trap adds glowing filaments near and inside the
// set. Luminance = max(bands, trap glow), so the whole pane stays alive.
const (
	juliaScale   = 0.042  // complex-plane units per aspect-corrected cell
	juliaIter    = 26     // escape iterations (cells are chunky; more buys nothing)
	juliaBailout = 4.0    // |z|² escape threshold
	juliaCR      = 0.7885 // |c|: the canonical morph loop's radius
	juliaCSpeed  = 0.030  // c-angle advance per phase unit — slow, continuous morph
	juliaRotSpd  = 0.006  // whole-plane rotation per phase unit
	juliaTrapR   = 0.25   // orbit-trap circle radius
	juliaTrapK   = 5.5    // trap-glow falloff (higher → thinner filaments)
	juliaBandF   = 0.55   // equipotential band frequency (per smooth-iteration unit)
	juliaBandSpd = 0.20   // band drift per phase unit (bands flow toward the set)
	juliaBandAmp = 0.62   // band peak brightness (trap filaments top out above it)
	juliaHueF    = 0.07   // hue ping-pong frequency along the escape depth
	juliaHueSpd  = 0.010
	// juliaPhase0 offsets the whole animation so frame 0 opens on its
	// "emptiest" state — scanned for minimum lit mass over the first c-orbit.
	// Launch fullness turns out to be dominated by the equipotential bands'
	// drift phase (the c shape barely moves it), so the offset shifts the
	// entire clock rather than the c angle: sparse filigree first, the pane
	// fills as the bands drift in.
	// Scanned minimum: lit fraction ~0.40 here vs ~0.92 at raw phase 0.
	juliaPhase0 = 20.0
)

// splashJuliaAt evaluates the Julia field at one point. The hue helper
// ping-pongs along the smooth escape depth (a triangle wave, so the gradient
// never seam-wraps), making color bands follow the fractal's equipotential
// geometry rather than plain radius.
func splashJuliaAt(_, _ int, dx, dy, phase float64) (val, aux float64) {
	// Launch on the scanned-for "emptiest" point of the animation (see
	// juliaPhase0); everything animates forward from there.
	phase += juliaPhase0
	rot := phase * juliaRotSpd
	cr, sr := math.Cos(rot), math.Sin(rot)
	zx := (dx*cr - dy*sr) * juliaScale
	zy := (dx*sr + dy*cr) * juliaScale
	ca := phase * juliaCSpeed
	cx, cy := juliaCR*math.Cos(ca), juliaCR*math.Sin(ca)

	trap := math.MaxFloat64
	m := zx*zx + zy*zy
	n := 0
	for ; n < juliaIter; n++ {
		zx, zy = zx*zx-zy*zy+cx, 2*zx*zy+cy
		m = zx*zx + zy*zy
		t := math.Abs(math.Sqrt(m) - juliaTrapR)
		if ax := math.Abs(zx); ax < t {
			t = ax
		}
		if ay := math.Abs(zy); ay < t {
			t = ay
		}
		if t < trap {
			trap = t
		}
		if m > juliaBailout {
			break
		}
	}

	// Smooth (fraction-corrected) escape depth: ~n for fast escapes, growing
	// toward juliaIter at the boundary; interior points pin to the top.
	nu := float64(juliaIter)
	if n < juliaIter {
		nu = float64(n) + 1 - math.Log2(math.Max(1e-9, 0.5*math.Log(m)))
	}
	band := juliaBandAmp * (0.5 + 0.5*math.Cos(nu*juliaBandF-phase*juliaBandSpd))
	glow := 1 / (1 + juliaTrapK*juliaTrapK*trap*trap)
	val = clamp01(math.Max(band, glow))
	aux = splashTri(nu*juliaHueF + phase*juliaHueSpd)
	return val, aux
}

// The mandala variant ("e"): the ridged fBm field sampled through a
// log-polar kaleidoscope fold centered on the wordmark. The angle folds into
// a 2N-fold mirror sector (rosette symmetry) and the radius maps
// logarithmically, with phase drifting the radial coordinate — a slow
// infinite zoom. Reuses splashFBMBody, so the filigree has the same organic
// filament quality as the nebula, arranged geometrically.
const (
	mandalaSectors = 6    // rotational symmetry order (2N mirror images)
	mandalaRadF    = 7.0  // radial feature frequency (per log-radius unit)
	mandalaAngF    = 16.0 // angular feature frequency (per folded sector)
	mandalaTwist   = 2.2  // radial shear of the angular coordinate → spiral arms
	mandalaZoomSpd = 0.12 // log-radius drift per phase unit (the infinite zoom)
	mandalaRotSpd  = 0.010
	mandalaHueF    = 0.45 // hue ping-pong frequency along log-radius (concentric bands)
	mandalaHueSpd  = 0.012
)

// splashMandalaAt evaluates the kaleidoscope field at one point. The hue
// helper ping-pongs along log-radius, giving drifting concentric color rings.
func splashMandalaAt(_, _ int, dx, dy, phase float64) (val, aux float64) {
	r := math.Hypot(dx, dy)
	lr := math.Log1p(r)
	th := math.Atan2(dy, dx) + phase*mandalaRotSpd
	// Fold the angle into one mirror sector → 2N-fold rosette symmetry.
	sector := math.Pi / mandalaSectors
	th = math.Mod(th, 2*sector)
	if th < 0 {
		th += 2 * sector
	}
	folded := math.Abs(th-sector) / sector // [0,1], mirror-continuous
	u := lr*mandalaRadF + phase*mandalaZoomSpd
	v := folded*mandalaAngF + lr*mandalaTwist
	val = splashFBMBody(u, v, phase)
	aux = splashTri(lr*mandalaHueF + phase*mandalaHueSpd)
	return val, aux
}

// splashTri is a triangle (ping-pong) wave in [0,1]: like fract, but it
// reverses instead of wrapping, so values mapped through a linear gradient
// never seam.
func splashTri(x float64) float64 {
	f := x - math.Floor(x)
	return 1 - math.Abs(2*f-1)
}

// Bloom (Pass 1.5, fractal variants): classic threshold → blur → add on the
// raw field buffer, so bright edges bleed a soft glow into their
// neighborhood. Running before the envelope keeps the blank-border guarantee
// intact (edgeY is still exactly 0 on the border rows). The blur is
// separable — a 5-tap horizontal and 3-tap vertical pass — wider
// horizontally because a cell is ~half as wide as it is tall.
const (
	bloomThresh = 0.60 // only values above this bleed
	bloomMix    = 0.60 // how much blurred brightness is added back
)

// splashBloom applies the bloom in place on f.vals.
func splashBloom(f splashField, w, h int) {
	bright := make([]float64, w*h)
	for i, v := range f.vals {
		if v > bloomThresh {
			bright[i] = (v - bloomThresh) / (1 - bloomThresh)
		}
	}
	tmp := make([]float64, w*h)
	// Horizontal 5-tap binomial [1 4 6 4 1]/16.
	for row := 0; row < h; row++ {
		base := row * w
		for col := 0; col < w; col++ {
			s := 6 * bright[base+col]
			s += 4 * (bright[base+max(col-1, 0)] + bright[base+min(col+1, w-1)])
			s += bright[base+max(col-2, 0)] + bright[base+min(col+2, w-1)]
			tmp[base+col] = s / 16
		}
	}
	// Vertical 3-tap binomial [1 2 1]/4.
	for row := 0; row < h; row++ {
		up, dn := max(row-1, 0)*w, min(row+1, h-1)*w
		base := row * w
		for col := 0; col < w; col++ {
			blur := (tmp[up+col] + 2*tmp[base+col] + tmp[dn+col]) / 4
			f.vals[base+col] = clamp01(f.vals[base+col] + bloomMix*blur)
		}
	}
}
