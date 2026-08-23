package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intp(n int) *int { return &n }

// TestCadenceAccessorsClamp walks every exposed cadence knob (#799) through the same five
// questions: what an absent field resolves to, that an in-range value survives untouched,
// and what happens below the floor, above the ceiling, and at each bound exactly.
//
// The boundary rows are the ones worth having. A clamp written with the wrong comparison
// still passes a below/above pair — it fails only at the bound itself, which is also the
// value a user is most likely to type after reading the range off the settings row.
func TestCadenceAccessorsClamp(t *testing.T) {
	for _, tc := range []struct {
		name string
		get  func(c *Config) int
		set  func(c *Config, n *int)
		def  int
		lo   int
		hi   int
	}{
		{
			name: "notify_throttle_seconds",
			get:  (*Config).GetNotifyThrottleSeconds,
			set:  func(c *Config, n *int) { c.NotifyThrottleSeconds = n },
			def:  defaultNotifyThrottleSeconds, lo: 0, hi: maxNotifyThrottleSeconds,
		},
		{
			name: "context_danger_percent",
			get:  (*Config).GetContextDangerPercent,
			set:  func(c *Config, n *int) { c.ContextDangerPercent = n },
			def:  defaultContextDangerPercent, lo: 1, hi: 100,
		},
		{
			name: "pending_watchdog_minutes",
			get:  (*Config).GetPendingWatchdogMinutes,
			set:  func(c *Config, n *int) { c.PendingWatchdogMinutes = n },
			def:  defaultPendingWatchdogMinutes, lo: 1, hi: maxPendingWatchdogMinutes,
		},
		{
			name: "diff_refresh_seconds",
			get:  (*Config).GetDiffRefreshSeconds,
			set:  func(c *Config, n *int) { c.DiffRefreshSeconds = n },
			def:  defaultDiffRefreshSeconds, lo: 1, hi: maxDiffRefreshSeconds,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Greater(t, tc.hi, tc.lo, "the fixture's bounds must be a real range")
			require.GreaterOrEqual(t, tc.def, tc.lo, "the default must sit inside the range")
			require.LessOrEqual(t, tc.def, tc.hi)

			assert.Equal(t, tc.def, tc.get(&Config{}), "an absent field resolves to the default")
			assert.Equal(t, tc.def, tc.get(nil), "a nil Config resolves to the default too")

			// In range, untouched. Picked off the default rather than a literal so this
			// row cannot accidentally coincide with a bound.
			mid := tc.def
			c := &Config{}
			tc.set(c, intp(mid))
			assert.Equal(t, mid, tc.get(c), "an in-range value is stored verbatim")

			tc.set(c, intp(tc.lo))
			assert.Equal(t, tc.lo, tc.get(c), "the floor itself is admitted, not clamped past")
			tc.set(c, intp(tc.hi))
			assert.Equal(t, tc.hi, tc.get(c), "the ceiling itself is admitted")

			tc.set(c, intp(tc.lo-1))
			assert.Equal(t, tc.lo, tc.get(c), "below the floor clamps up")
			tc.set(c, intp(tc.hi+1))
			assert.Equal(t, tc.hi, tc.get(c), "above the ceiling clamps down")

			// A hand-edited config is the only way any of these arrive, so the
			// pathological cases have to resolve rather than propagate.
			tc.set(c, intp(-999_999))
			assert.Equal(t, tc.lo, tc.get(c), "a large negative still lands on the floor")
		})
	}
}

// TestContextWarnNeverExceedsDanger pins the one cross-field rule: the two context bands
// are a single ladder, and an inverted pair would paint Attention above the occupancy that
// should already read Danger — the chip's top rung silently lost.
//
// The collapse is one-directional on purpose: danger wins and warn narrows to meet it,
// never the reverse. Both directions are asserted so a "fix" that swapped them fails.
func TestContextWarnNeverExceedsDanger(t *testing.T) {
	t.Run("an inverted pair collapses onto danger", func(t *testing.T) {
		c := &Config{ContextWarnPercent: intp(95), ContextDangerPercent: intp(60)}
		assert.Equal(t, 60, c.GetContextDangerPercent(), "danger is unaffected by the inversion")
		assert.Equal(t, 60, c.GetContextWarnPercent(), "warn collapses down onto danger")
	})

	t.Run("a danger below the built-in warn pulls the unset warn down", func(t *testing.T) {
		// Nothing configured for warn, so it would otherwise resolve to 75 — above the
		// configured danger. The default is not exempt from the ladder.
		c := &Config{ContextDangerPercent: intp(40)}
		assert.Equal(t, 40, c.GetContextWarnPercent(),
			"the built-in warn default is held under a lower configured danger")
	})

	t.Run("an ordered pair is left alone", func(t *testing.T) {
		c := &Config{ContextWarnPercent: intp(50), ContextDangerPercent: intp(80)}
		assert.Equal(t, 50, c.GetContextWarnPercent())
		assert.Equal(t, 80, c.GetContextDangerPercent())
	})

	t.Run("an out-of-range warn is clamped, not read as unset", func(t *testing.T) {
		// 0 is the zero value, and the renderer treats a zero band as "nobody
		// configured this". The accessor must not: a stored 0 came from a
		// hand-edited config that asked for something, and the answer is the floor.
		c := &Config{ContextWarnPercent: intp(0), ContextDangerPercent: intp(80)}
		assert.Equal(t, 1, c.GetContextWarnPercent())
	})
}

// TestCadenceDefaultsMatchTheirExportedReaders pins the settings panel's contract: each row
// advertises its default through the exported reader, and the accessor resolves an unset
// field through the unexported constant. A row whose advertised default disagreed with the
// value in force would mark an untouched config as "changed from default".
func TestCadenceDefaultsMatchTheirExportedReaders(t *testing.T) {
	empty := &Config{}
	assert.Equal(t, empty.GetNotifyThrottleSeconds(), DefaultNotifyThrottleSeconds())
	assert.Equal(t, empty.GetContextWarnPercent(), DefaultContextWarnPercent())
	assert.Equal(t, empty.GetContextDangerPercent(), DefaultContextDangerPercent())
	assert.Equal(t, empty.GetPendingWatchdogMinutes(), DefaultPendingWatchdogMinutes())
	assert.Equal(t, empty.GetDiffRefreshSeconds(), DefaultDiffRefreshSeconds())

	// The built-in pair must itself satisfy the ladder, or the very first render would
	// need the collapse to rescue it.
	assert.Less(t, DefaultContextWarnPercent(), DefaultContextDangerPercent(),
		"the built-in warn band must sit below the built-in danger band")
}

// TestCadenceDefaultsReproduceThePreConfigConstants is the compatibility pin. Each number
// is what the corresponding constant held before #799 made it configurable, so a user who
// upgrades and configures nothing sees exactly the Atrium they had.
//
// The literals are spelled out rather than derived from the constants they guard. Every
// other assertion in this file compares a default to itself in some form, and a
// self-comparison cannot notice a default being "tidied" to a rounder number — which is
// precisely what a mutation of each of these survived until this test existed.
//
// Two of these defaults have a live twin elsewhere, and neither twin is asserted here:
// app.notifyThrottle (TestNotifyThrottleDefaultMatchesConfig, in app) and ui's
// contextWarnPct/contextDangerPct fallback (TestContextDefaultsMatchTheConfigAccessors, in
// ui). Both guards live in the package holding the twin, because that is the package that
// can see it.
func TestCadenceDefaultsReproduceThePreConfigConstants(t *testing.T) {
	assert.Equal(t, 3, DefaultNotifyThrottleSeconds())
	assert.Equal(t, 75, DefaultContextWarnPercent())
	assert.Equal(t, 90, DefaultContextDangerPercent())
	assert.Equal(t, 30, DefaultPendingWatchdogMinutes())
	assert.Equal(t, 15, DefaultDiffRefreshSeconds())
}
