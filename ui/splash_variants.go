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
