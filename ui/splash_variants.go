package ui

// The splash variant vocabulary: which field generators exist, what users call
// them, and which one is currently selected. Kept apart from splash_field.go so
// that file is only the noise core and the renderer — this is the part that
// changes whenever a variant is added, retired, or renamed.
//
// Note the two directions of "name" here. splashVariantNames is the pinnable
// vocabulary shared with config (see SplashVariantNames); ATRIUM_SPLASH_VARIANT
// additionally accepts historical dev letters and trumps config, which is what
// keeps screenshot A/B runs and the test suites deterministic.

import (
	"os"
	"slices"
	"sort"
	"sync"
	"time"
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
	// splashVariantRain ("f") is Matrix-style digital rain: per-column streams
	// with bright heads and fading tails, layered at three depths. The only
	// variant with persistent directional motion — and the only one that shades
	// by luminance rather than by glyph density.
	splashVariantRain

	// splashVariantCount is the enum's cardinality, not a variant — it must
	// stay last. It exists so the tests can prove they cover every variant:
	// the contract loop and the benchmark both walk a hand-maintained map, and
	// a variant missing from it escapes both without failing anything. Safe to
	// key off the iota here because nothing persists these ordinals — config
	// stores the pattern's name, not its number.
	splashVariantCount
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
	splashVariantJulia, splashVariantMandala, splashVariantRain,
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
	"rain":     splashVariantRain,
}

// structured reports whether a variant's field carries directional geometry
// that a hole punched in it is visible *against*. It is the difference between
// a field that hides the text clearing and one that exposes it: the nebula and
// its relatives drift and fade, so a soft void around the wordmark reads as gas
// thinning out, while rain's streams are long, straight and vertical — a band
// with no streams in it reads as a band, and the eye asks what put it there.
func (v splashVariant) structured() bool {
	return v == splashVariantRain
}

// splashOps is a variant's Pass-2 policy: how its raw field is turned into
// glyphs and colour.
type splashOps struct {
	// contrastLo/contrastHi are the smoothstep window applied to the raw field.
	// It exists to push a noise field's mid-tones apart — but it is destructive
	// where the field already carries its own gradient, since everything below
	// Lo is erased and everything above Hi flattens.
	contrastLo, contrastHi float64
	// dither adds sub-glyph-step noise to break banding on smooth gradients.
	dither bool
	// stars draws the fixed twinkling starfield over the field.
	stars bool
	// dimToRim is how much the field dims from the focal point out to the
	// farthest corner. It reads as a glow emanating from the wordmark on a field
	// that has no depth of its own — and fights one that does, by dimming
	// whatever is furthest from the centre regardless of how near it is meant to
	// look.
	dimToRim float64
	// breathes applies the slow global brightness swell. It makes a static field
	// feel alive; on a field already in motion it is a flicker over everything at
	// once, and it costs the brightest cells the top of their range.
	breathes bool
	// lumRamp shades the field along the LUT's luminance ramp instead of the
	// glyph density ramp: the glyph keeps a constant weight and brightness rides
	// the colour. Only a field whose own gradient is the point wants this — see
	// buildRainRamp for why the hue gradient cannot carry one.
	lumRamp bool
}

// ops returns a variant's Pass-2 policy.
//
// The contrast window is the interesting one. A noise field concentrates its
// values near the middle, so the narrow fBm window is what turns a flat wash
// into filaments — but it assumes the field has no gradient of its own worth
// keeping. Rain's does: its tail *is* a ramp from the head down to nothing, and
// running the fBm window over it erased the faint 44% of every tail outright,
// flattened the brightest 22%, and crushed the fade into the third that was
// left. With dither scattering the survivors, streams rendered as loose
// confetti — no visible trails, and so no parallax to see either, since there
// were no streams to be nearer or further away. An identity window keeps the
// tail the generator drew.
func (v splashVariant) ops() splashOps {
	switch {
	case v == splashVariantLegacy:
		// The superseded baseline, kept faithful: its wide window and no dither.
		return splashOps{
			contrastLo: splashContrastLo, contrastHi: splashContrastHi,
			stars: true, dimToRim: radialDim, breathes: true,
		}
	case v == splashVariantRain:
		return splashOps{
			// Identity: pass the tail through untouched (see above).
			contrastLo: 0, contrastHi: 1,
			// Dither is for banding on a smooth wash. A stream is a thin line of
			// cells, so per-cell noise does not smooth it — it eats it.
			dither: false,
			// Stars are fixed points; rain is moving ones. Together the fixed ones
			// read as stuck pixels, and rain has its own highlight anyway.
			stars: false,
			// Brightness rides the luminance ramp; the glyph stays a constant
			// mark. This is the whole difference between a stream and a column of
			// dots — see buildRainRamp.
			lumRamp: true,
			// Both envelope terms are off, and for the same reason: they cost the
			// head the top of the ramp, which is the only white on screen and the
			// thing the eye tracks. dimToRim would also actively undo the depth —
			// it dims by distance from the centre, so a near stream at the rim
			// would render dimmer than a far one at the middle.
			dimToRim: 0,
			breathes: false,
		}
	case v.isFractal():
		// The trap glow is already contrasty; a wide window keeps its range.
		return splashOps{
			contrastLo: fractalContrastLo, contrastHi: fractalContrastHi,
			dither: true, stars: true, dimToRim: radialDim, breathes: true,
		}
	default:
		return splashOps{
			contrastLo: fbmContrastLo, contrastHi: fbmContrastHi,
			dither: true, stars: true, dimToRim: radialDim, breathes: true,
		}
	}
}

// splashTextPad is the margin a clearing leaves around the text it protects,
// in cells, added to the text's half-extents.
type splashTextPad struct{ wordX, wordY, msgX, msgY int }

// textPad returns a variant's clearing margin, and whether it wants a clearing
// at all.
//
// It is worth being precise about what the clearing is for, because the name
// suggests a job it does not have. It does *not* keep the field from bleeding
// through the text: overlayAt is an opaque compositor — it writes each overlaid
// line's cells wholesale, spaces included — and the banner is solid to begin
// with (it fills with ░ and contains no spaces at all). The text covers its own
// footprint no matter what the field does underneath. The clearing's only job is
// aesthetic: it opens a margin of quiet *around* the text.
//
// That margin is the whole charm on an organic field, which fades into it, so
// the gas appears to part around the wordmark. On a structured field it is the
// opposite: rain's streams are long, straight and vertical, and a margin is a
// band of missing streams with nothing drawn in it to account for them. Measured
// against the inherited padding that came to three such bands — one below the
// wordmark, two around a one-row message whose ellipse spanned three rows.
//
// So structured variants take no clearing rather than a smaller one. Tightening
// the padding could only ever fix two of those three bands: wordCenterRow rounds
// to wordY + wordH/2, half a row below the even-height banner's true center, so
// the art spans dy ∈ [-3, +2] and no integer half-extent covers exactly that —
// 4 takes a row past the bottom, 3 uncovers the top. Dropping the clearing skips
// the geometry entirely, and loses nothing: the text was never relying on it.
func (v splashVariant) textPad() (splashTextPad, bool) {
	if v.structured() {
		return splashTextPad{}, false
	}
	return splashTextPad{wordX: 2, wordY: 1, msgX: 2, msgY: 2}, true
}

// SplashVariantNames lists every pinnable pattern name, sorted.
//
// It exists to be checked against config.SplashVariants(). The two lists are
// hand-maintained in packages that cannot import each other — config knows
// nothing of ui, and ui takes the name as a plain string precisely so it needs
// no config import — and for a long time nothing tied them. The failure is
// silent in the worst direction: a name in config but not here is offered by
// the settings panel, falls through SetSplashVariant's lookup, and quietly
// means "random", so the pattern the user pinned simply never appears. app
// imports both and asserts they agree.
func SplashVariantNames() []string {
	names := make([]string, 0, len(splashVariantNames))
	for name := range splashVariantNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	case "f", "rain":
		return splashVariantRain, true
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
