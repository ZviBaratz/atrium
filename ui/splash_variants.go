package ui

// Splash variant selection: which generator is currently active, and the
// dev-only ATRIUM_SPLASH_* env overrides that can force it. The vocabulary
// itself — the variant enum, its names, and each variant's Pass-2 policy — lives
// in the splash package; this file resolves config and env into a splash.Variant
// and hands it to splash.Render (see splashScene).
//
// Note the two directions of "name". The pinnable vocabulary is shared with
// config (splash.Variants / config.SplashVariants); ATRIUM_SPLASH_VARIANT
// additionally accepts historical dev letters and trumps config, which is what
// keeps screenshot A/B runs and the test suites deterministic.

import (
	"math"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/splash"
)

// splashDefaultVariant is the fallback for an unrecognized override value; an
// unset override rotates instead (see splashActiveVariant).
//
// splash.Rain, so that there is exactly one fallback rather than three: this,
// and the field-generator and Pass-2-policy default arms in package splash, all
// resolve to it, and a variant that is mis-wired in any of them renders rain
// rather than something that merely looks plausible. Deliberately NOT what the
// test suites pin — see parseSplashEnvVariant, whose fall-through is invisible to
// a pin that names the fallback.
const splashDefaultVariant = splash.Rain

// splashRotation is the pool random mode draws from: every shipped variant. It
// is splash.Variants() captured once, so the rotation helpers below index a
// stable slice.
var splashRotation = splash.Variants()

// splashEnvVariant resolves the dev-only ATRIUM_SPLASH_VARIANT override once
// per process. It trumps the config setting so screenshot A/B runs and the test
// suites (which pin a variant in TestMain against rotation nondeterminism) stay
// deterministic whatever the config under test says. The second value is false
// when the variable is unset.
var splashEnvVariant = sync.OnceValues(func() (splash.Variant, bool) {
	return parseSplashEnvVariant(os.Getenv("ATRIUM_SPLASH_VARIANT"))
})

// parseSplashEnvVariant reads the override: the variant to render, and whether
// the variable was set at all.
//
// Split from the env read for one reason — it is the only way any of this is
// testable. The resolution lives in a sync.OnceValues closure and TestMain pins
// the variable, which defeats even a subprocess probe, so the letters had no test
// at all and the fall-through here had no way to be stated. Its sibling knob
// (parseSplashLumRange) already had this shape.
//
// The two knobs answer the same question differently, and the asymmetry is
// deliberate. Junk gives parseSplashLumRange ok=false (no override), but gives
// this ok=true: the bool means "an override was set", not "it was understood", so
// a typo, a retirement and a deletion all resolve to splashDefaultVariant rather
// than falling back to the rotation. That keeps a mispinned suite deterministic —
// wrong, but not flaky, which is the failure worth having.
//
// It is also why the understanding is a separate function rather than a fifth
// case here. Collapsing them makes the letter that names the fallback untestable:
// "f" resolves to rain and so does every unrecognized string, so deleting case
// "f" cannot be observed at this boundary and the whole letter vocabulary sits on
// a guard that passes either way. And it is why TestMain pins a variant that is
// not the fallback, for the same reason one level up.
func parseSplashEnvVariant(s string) (splash.Variant, bool) {
	if s == "" {
		return splashDefaultVariant, false
	}
	if v, ok := lookupSplashVariant(s); ok {
		return v, true
	}
	return splashDefaultVariant, true
}

// lookupSplashVariant answers only "does this build know that name", which is the
// question the fall-through above destroys. It accepts the user-facing names (via
// splash.ParseVariant) and the historical dev letters, kept as they were: f/g/h
// are what the screenshot recipes, the notes and the muscle memory all use, and
// re-lettering to a/b/c would buy tidiness and break every recipe. a–e and
// "legacy" named the organic fields retired in V5; next free letter is i.
func lookupSplashVariant(s string) (splash.Variant, bool) {
	if v, ok := splash.ParseVariant(s); ok {
		return v, true
	}
	switch s {
	case "f":
		return splash.Rain, true
	case "g":
		return splash.Tunnel, true
	case "h":
		return splash.Ripple, true
	}
	return splashDefaultVariant, false
}

// splashLumRange is the dev-only ATRIUM_SPLASH_LUMRANGE override: it replaces
// every variant's shipped lumRange, so a screenshot round can sweep the knob
// without a rebuild per value. Unset (the shipped path) leaves each variant's own.
//
// It applies to every variant, rain included, and on rain a low value is a
// deliberate absurdity rather than a bug: rain's glyphs have constant weight, so
// taking brightness off the colour leaves it nowhere to go and the pane fills with
// white katakana. That is the whole thesis of this file, rendered. Pin the variant
// you mean to look at (ATRIUM_SPLASH_VARIANT) when sweeping.
//
// A plain var behind splashSelMu rather than a sync.OnceValue, for two reasons.
// The benchmarks have to drive the shaded path, and a OnceValue cannot be driven
// from one — it would resolve on whatever the first test touched it with and pin
// that for the binary. And splashSelMu already guards exactly this shape of state
// (see splashPick below), at one uncontended lock per *frame* — the override is
// read once per render, not once per cell.
var (
	splashLumRangeSet bool
	splashLumRangeVal float64
)

func init() {
	splashLumRangeVal, splashLumRangeSet = parseSplashLumRange(os.Getenv("ATRIUM_SPLASH_LUMRANGE"))
}

func splashLumRangeOverride() (float64, bool) {
	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	return splashLumRangeVal, splashLumRangeSet
}

// parseSplashLumRange reads the override, rejecting anything that would not be a
// share of brightness.
//
// The NaN and infinity rejections are not hygiene. strconv.ParseFloat accepts
// "nan" and "+Inf" happily, and a NaN would pass *both* of splashShade's endpoint
// guards (every comparison against NaN is false), reach the interior, and land in
// an int() conversion of a NaN — which is implementation-defined in Go: amd64
// gives minint, arm64 gives 0. That is a silent per-architecture difference in
// rendered output, which is the one property this whole field is careful about.
func parseSplashLumRange(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return math.Min(math.Max(v, 0), 1), true
}

// The process-wide variant selection: lazily a per-launch rotation draw, or
// whatever SetSplashVariant last pinned or re-rolled. splashRandomMode records
// whether the config asked for random, which is what lets the screensaver
// re-roll per showing while a pinned choice stays put.
//
// splashSelMu guards them. Bubble Tea drives every caller from its single
// update/view goroutine, so the lock is uncontended insurance rather than a
// live need — but it is the same insurance splash's LUT cache buys one package
// over, and TerminalPane.String() already takes its own mutex before reaching
// splashScene, i.e. that pane is treated as multi-goroutine-reachable. These
// vars replaced a sync.OnceValue that was safe by construction; a leaf mutex
// keeps the invariant enforced instead of merely documented, at one
// uncontended lock per frame.
var (
	splashSelMu      sync.Mutex
	splashRandomMode = true
	splashPicked     bool
	splashPick       splash.Variant
)

// splashActiveVariant resolves the variant splashScene renders: the dev env
// override if set, else the current selection (seeded lazily from the launch
// time, so an unconfigured launch gets a fresh look each time). Resolving to
// one process-wide value is what keeps the preview and terminal panes in
// agreement; only splashScene consults it — splash.Render takes the variant as a
// parameter and stays pure over its inputs.
func splashActiveVariant() splash.Variant {
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
	if v, ok := splash.ParseVariant(name); ok {
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
func splashRotationPick(nano int64) splash.Variant {
	return splashRotation[splashRotationIdx(nano, len(splashRotation))]
}

// splashRotationReroll maps a seed to a rotation variant other than cur, so a
// re-roll always visibly changes the pattern. It draws over the pool minus cur
// (len-1 slots) and steps past cur's slot, which keeps the result uniform over
// the remaining variants rather than biased toward cur's neighbour.
//
// A cur outside the pool has nothing to exclude, so it falls back to a plain
// draw over the whole pool. Every shipped variant is in the pool now — the
// retired legacy baseline used to be the one that wasn't — so that arm is
// defensive, and it is worth being exact about what it defends: not an index
// panic. slices.Index returns -1, every idx is >= -1, so the step-past-cur
// increment fires unconditionally and still lands inside the slice. What is lost
// without it is slot 0 — the draw silently becomes uniform over the pool *minus
// its first variant*, which is a bias no caller asked for and nothing would
// notice. (The len < 2 arm is the one that stops a panic: splashRotationIdx
// would take nano % 0.)
func splashRotationReroll(nano int64, cur splash.Variant) splash.Variant {
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
