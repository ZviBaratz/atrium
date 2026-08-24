package config

// The four exposed cadence/threshold knobs (#799), and the recorded decision
// about the rest.
//
// Atrium's behaviour is shaped by around thirty displayed-but-hardcoded constants
// (the #793 program's domain inventory). Exactly four are user-configurable, and
// the boundary is not "which ones were easy to plumb" — it is whether the value is
// a *preference* or a *correctness tuning*:
//
//   - The context chip's warn/danger bands say when a session is close enough to
//     compaction to be worth looking at. "Close enough" is a working style.
//   - The notification throttle is how often the user consents to be
//     interrupted. That is theirs by definition.
//   - The pending watchdog is how long a background sub-agent is allowed to run
//     before Atrium stops believing it. That depends on the user's workload, not
//     on any property of tmux.
//   - The diff-refresh floor trades staleness in a background row's +/- chip
//     against a git walk per session per sweep. Which side to err on depends on
//     fleet size and disk.
//
// Everything else stays hardcoded: the 500ms metadata poll and its 4-tick full
// sweep, the 100ms frame tick, the PR status TTLs, the 1.5s read dwell, the
// 2/6-tick idle hysteresis, the 30s hook heartbeat. Those are tuned against a
// single shared tmux server, a network round trip, or a classifier's failure
// modes — a wrong value does not produce a differently-shaped UI, it produces a
// wrong one. Exposing them would sell a correctness constraint as a taste.
//
// Adding a fifth knob is a decision to record, not a drive-by: it belongs in a
// program design record, the way these four do.

import "time"

// Cadence knob defaults and bounds. Each is the value the corresponding
// hardcoded constant held before it became configurable, so an unset config
// reproduces the pre-#799 behaviour exactly.
const (
	defaultNotifyThrottleSeconds = 3
	maxNotifyThrottleSeconds     = 3600

	defaultContextWarnPercent   = 75
	defaultContextDangerPercent = 90

	defaultPendingWatchdogMinutes = 30
	maxPendingWatchdogMinutes     = 1440

	defaultDiffRefreshSeconds = 15
	maxDiffRefreshSeconds     = 3600
)

// Each knob's default and bound is also exported, so a settings row can name the value it
// stands in for and the two cannot disagree (mirroring DefaultProjectSearchDepth /
// MaxProjectSearchDepth).

// DefaultNotifyThrottleSeconds is the spacing an unset notify_throttle_seconds uses.
func DefaultNotifyThrottleSeconds() int { return defaultNotifyThrottleSeconds }

// MaxNotifyThrottleSeconds is the ceiling GetNotifyThrottleSeconds clamps to.
func MaxNotifyThrottleSeconds() int { return maxNotifyThrottleSeconds }

// DefaultContextWarnPercent is the occupancy an unset context_warn_percent uses.
func DefaultContextWarnPercent() int { return defaultContextWarnPercent }

// DefaultContextDangerPercent is the occupancy an unset context_danger_percent uses.
func DefaultContextDangerPercent() int { return defaultContextDangerPercent }

// DefaultPendingWatchdogMinutes is the cap an unset pending_watchdog_minutes uses.
func DefaultPendingWatchdogMinutes() int { return defaultPendingWatchdogMinutes }

// MaxPendingWatchdogMinutes is the ceiling GetPendingWatchdogMinutes clamps to.
func MaxPendingWatchdogMinutes() int { return maxPendingWatchdogMinutes }

// DefaultDiffRefreshSeconds is the floor an unset diff_refresh_seconds uses.
func DefaultDiffRefreshSeconds() int { return defaultDiffRefreshSeconds }

// MaxDiffRefreshSeconds is the ceiling GetDiffRefreshSeconds clamps to.
func MaxDiffRefreshSeconds() int { return maxDiffRefreshSeconds }

// clampInt returns n bounded to [lo,hi].
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// GetNotifyThrottleSeconds returns the per-edge notification spacing in seconds.
// A nil field — or a nil Config — defaults to 3. Zero is honoured as "no
// throttle" rather than clamped up, because a user who wants every edge is
// asking for something coherent; negatives and oversized values clamp.
func (c *Config) GetNotifyThrottleSeconds() int {
	if c == nil || c.NotifyThrottleSeconds == nil {
		return defaultNotifyThrottleSeconds
	}
	return clampInt(*c.NotifyThrottleSeconds, 0, maxNotifyThrottleSeconds)
}

// GetContextWarnPercent returns the occupancy at which the context chip turns
// Attention-coloured, clamped to [1,100] and never above the danger threshold.
//
// The collapse is deliberate and one-directional: an inverted pair would paint a
// chip Attention *above* the point it should read Danger, so the top rung wins
// and the warn band narrows to nothing. Reporting an error instead is not
// available here — this is a render-path accessor with nowhere to report to, and
// the settings row refuses the inversion up front where there is a user to tell.
func (c *Config) GetContextWarnPercent() int {
	danger := c.GetContextDangerPercent()
	if c == nil || c.ContextWarnPercent == nil {
		return min(defaultContextWarnPercent, danger)
	}
	return min(clampInt(*c.ContextWarnPercent, 1, 100), danger)
}

// GetContextDangerPercent returns the occupancy at which the context chip turns
// Danger-coloured. A nil field — or a nil Config — defaults to 90; values
// outside [1,100] clamp.
func (c *Config) GetContextDangerPercent() int {
	if c == nil || c.ContextDangerPercent == nil {
		return defaultContextDangerPercent
	}
	return clampInt(*c.ContextDangerPercent, 1, 100)
}

// GetPendingWatchdogMinutes returns the wall-clock cap a session may sit Pending
// before the watchdog reconciles it to done. A nil field — or a nil Config —
// defaults to 30; values outside [1,1440] clamp. The floor is 1 rather than 0
// because there is no "off": a zero cap would reconcile every Pending row on its
// first poll, which is not "no watchdog" but a permanently-firing one.
func (c *Config) GetPendingWatchdogMinutes() int {
	if c == nil || c.PendingWatchdogMinutes == nil {
		return defaultPendingWatchdogMinutes
	}
	return clampInt(*c.PendingWatchdogMinutes, 1, maxPendingWatchdogMinutes)
}

// GetDiffRefreshSeconds returns how stale a background session's +/- chip may
// get. A nil field — or a nil Config — defaults to 15; values outside [1,3600]
// clamp. The floor is 1 rather than 0 because zero means "recompute every
// session's diff on every sweep", which is the load the floor exists to bound.
func (c *Config) GetDiffRefreshSeconds() int {
	if c == nil || c.DiffRefreshSeconds == nil {
		return defaultDiffRefreshSeconds
	}
	return clampInt(*c.DiffRefreshSeconds, 1, maxDiffRefreshSeconds)
}

// PendingWatchdogOverride returns the user's Pending cap as a duration, or 0 when
// pending_watchdog_minutes is unset.
//
// It is deliberately not GetPendingWatchdogMinutes. That accessor resolves an absent
// field to the built-in 30 minutes, so a caller that installed its result would call
// session.SetPendingWatchdog with a positive value on every launch — pinning the
// fleet-wide cap and leaving agent.Adapter.PendingWatchdog, the middle rung of that
// package's three-rung ladder, permanently inert. The zero returned here is exactly what
// SetPendingWatchdog reads as "the user has expressed no opinion", which is what hands
// the decision on to the adapter and then the default.
func (c *Config) PendingWatchdogOverride() time.Duration {
	if c == nil || c.PendingWatchdogMinutes == nil {
		return 0
	}
	return time.Duration(c.GetPendingWatchdogMinutes()) * time.Minute
}
