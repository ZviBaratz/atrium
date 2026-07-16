package ui

import (
	"math"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/splash"

	"github.com/stretchr/testify/require"
)

// TestSplashRotationPickInBounds guards against a negative-index panic: a clock
// set before the Unix epoch yields a negative UnixNano(), and Go's signed % of
// that would produce a negative slice index. The pick must stay in [0, len) for
// every int64 seed, including negative and boundary values.
func TestSplashRotationPickInBounds(t *testing.T) {
	seeds := []int64{
		0, 1, -1, 42, -42,
		math.MinInt64, math.MaxInt64,
		-1234567890123456789, 1234567890123456789,
	}
	for _, seed := range seeds {
		require.NotPanics(t, func() { splashRotationPick(seed) },
			"splashRotationPick must not panic for seed %d", seed)
	}
}

// resetSplashSelection restores the process-wide splash selection after a test
// pins or re-rolls it, so package siblings see the pristine lazy-random state.
// (Rendering is shielded anyway — TestMain pins the env override, which trumps
// the selection — but the package state should stay canonical regardless.)
func resetSplashSelection(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		splashRandomMode, splashPicked, splashPick = true, false, splashDefaultVariant
	})
}

// TestSetSplashVariantPinsKnownNames locks the name→generator mapping and that
// a pinned name leaves random mode.
func TestSetSplashVariantPinsKnownNames(t *testing.T) {
	resetSplashSelection(t)
	for _, want := range splash.Variants() {
		name := want.String()
		SetSplashVariant(name)
		require.False(t, splashRandomMode, "%q must pin, not stay random", name)
		require.True(t, splashPicked)
		require.Equal(t, want, splashPick, "name %q", name)
	}
}

// TestSetSplashVariantRandomRerolls pins random-mode semantics: anything that
// isn't a known name (config.SplashRandom, junk) enters random mode with an
// immediate rotation draw, and RerollSplashVariant only re-draws in that mode —
// a pinned pattern survives a screensaver activation untouched.
func TestSetSplashVariantRandomRerolls(t *testing.T) {
	resetSplashSelection(t)

	SetSplashVariant("ripple")
	RerollSplashVariant()
	require.Equal(t, splash.Ripple, splashPick, "reroll must not disturb a pinned pattern")

	SetSplashVariant("random")
	require.True(t, splashRandomMode)
	require.True(t, splashPicked, "random mode draws immediately so both panes agree")
	require.Contains(t, splashRotation, splashPick, "the draw must come from the rotation pool")

	// Every re-roll must land somewhere in the pool *and* move: the screensaver
	// re-rolls per activation, so a draw that repeats the current pattern reads
	// as a dead keypress. Looping catches a seed-dependent repeat that a single
	// call would only hit ~1/len of the time.
	for i := 0; i < 40; i++ {
		prev := splashPick
		RerollSplashVariant()
		require.Contains(t, splashRotation, splashPick)
		require.NotEqual(t, prev, splashPick, "a re-roll must change the pattern (iteration %d)", i)
	}
}

// TestSplashRotationRerollUniformOverRemainder pins the exclusion arithmetic
// directly, seed by seed: every consecutive seed maps to some variant other
// than cur, and sweeping a full period hits each of the other variants exactly
// once — i.e. skipping cur's slot doesn't bias the draw toward its neighbour.
func TestSplashRotationRerollUniformOverRemainder(t *testing.T) {
	for _, cur := range splashRotation {
		counts := map[splash.Variant]int{}
		for seed := int64(0); seed < int64(len(splashRotation)-1); seed++ {
			got := splashRotationReroll(seed, cur)
			require.NotEqual(t, cur, got, "cur=%d seed=%d must be excluded", cur, seed)
			counts[got]++
		}
		require.Len(t, counts, len(splashRotation)-1, "cur=%d: every other variant must be reachable", cur)
		for v, n := range counts {
			require.Equal(t, 1, n, "cur=%d variant=%d drawn %d times over one period", cur, v, n)
		}
	}
}

// TestSplashRotationRerollFallsBackOutsidePool: a cur the pool doesn't contain
// has nothing to exclude, so it degrades to a plain draw rather than looping or
// panicking. Negative seeds must stay in bounds here too.
//
// Reaching it takes a cast now: the out-of-pool cur used to occur naturally (the
// legacy baseline was pinnable but never drawn), and since V5 every shipped
// variant is in the pool and the unpicked zero value is rain.
//
// "Stays in the pool" is not the assertion, because it cannot fail — deleting the
// ci < 0 arm keeps every result in the pool, since idx >= -1 always holds and the
// increment lands inside the slice regardless. What breaks without the arm is the
// *distribution*: the draw steps past a slot that isn't there and can never
// return splashRotation[0] again. So the claim is that an out-of-pool cur excludes
// nothing, and it takes a sweep to see it.
func TestSplashRotationRerollFallsBackOutsidePool(t *testing.T) {
	notAVariant := splash.Variant(len(splashRotation)) // one past the pool
	seen := map[splash.Variant]bool{}
	for seed := int64(0); seed < int64(len(splashRotation))*20; seed++ {
		got := splashRotationReroll(seed, notAVariant)
		require.Containsf(t, splashRotation, got, "out-of-pool cur, seed %d", seed)
		seen[got] = true
	}
	require.Lenf(t, seen, len(splashRotation),
		"an out-of-pool cur has nothing to exclude, so every variant must still be "+
			"drawable; only %d of %d were", len(seen), len(splashRotation))

	// Negative and boundary seeds must stay in bounds here too.
	for _, seed := range []int64{-1, -(1 << 62), 1 << 62} {
		require.Containsf(t, splashRotation, splashRotationReroll(seed, notAVariant),
			"out-of-pool cur, seed %d", seed)
	}
}

// TestSplashSelectionConcurrent is TestRenderSplashFieldConcurrent's counterpart
// for the mutable selection: splash.Render is pure and provably safe, but
// SetSplashVariant / RerollSplashVariant write process-wide state that a
// sync.OnceValue used to make safe by construction. Under -race this pins
// splashSelMu actually covering them; without it the package's own
// concurrent-render posture would rest on a comment.
//
// splashActiveVariant short-circuits on the env override TestMain pins, so this
// exercises the writers plus a locked read rather than the lazy-seed path.
func TestSplashSelectionConcurrent(t *testing.T) {
	resetSplashSelection(t)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				switch (g + i) % 3 {
				case 0:
					SetSplashVariant("random")
				case 1:
					SetSplashVariant("tunnel")
				default:
					RerollSplashVariant()
				}
				_ = splashActiveVariant()
			}
		}(g)
	}
	wg.Wait()

	splashSelMu.Lock()
	defer splashSelMu.Unlock()
	require.True(t, splashPicked, "every path above picks")
	require.Contains(t, splashRotation, splashPick,
		"the selection must never be torn into an out-of-pool value")
}

// TestSplashEnvOverrideTrumpsSelection relies on TestMain pinning
// ATRIUM_SPLASH_VARIANT=tunnel: whatever the config pins, the dev override wins
// in splashActiveVariant — which is exactly what keeps every String()-path test
// deterministic even if a future test seeds a config with a splash value.
//
// It is also the only thing that notices if that pin stops being honoured, and
// only because the pin does not name the fallback. parseSplashEnvVariant answers
// an unrecognized value with (splashDefaultVariant, true), so a pin reading
// "rain" would be indistinguishable from a pin the parse never understood. This
// is why TestMain names a variant that is not rain.
func TestSplashEnvOverrideTrumpsSelection(t *testing.T) {
	resetSplashSelection(t)
	SetSplashVariant("ripple")
	require.Equal(t, splash.Tunnel, splashActiveVariant(),
		"the env override (pinned to \"tunnel\" in TestMain) must trump the config selection")
}
