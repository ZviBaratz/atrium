package config

import "runtime"

// cpuCount is the hardware-thread source for the host-derived session cap. It is a
// package var (not a direct runtime.NumCPU() call) purely so tests can pin it to a
// known value; production always reads the real thread count.
var cpuCount = runtime.NumCPU

// deriveSessionCap returns the host-derived default concurrent-session cap for a
// machine with numCPU hardware threads: max(2, numCPU/2). Halving leaves headroom
// for the user's own work and the agents' multi-core bursts; the floor of 2 keeps
// tiny hosts usable.
func deriveSessionCap(numCPU int) int {
	return max(2, numCPU/2)
}

// DefaultSessionCap is the cap an unset max_sessions resolves to: max(2, hardware
// threads / 2). Exported for the enforcement sites and `atrium doctor` to report
// the same recommendation.
func DefaultSessionCap() int {
	return deriveSessionCap(cpuCount())
}

// SessionCap is the effective session limit resolved from max_sessions. Limit == 0
// means unlimited (no cap). Soft distinguishes the host-derived default (true —
// exceeding it warns but is allowed) from an explicit user cap (false — exceeding a
// positive Limit is refused; a Limit of 0 with Soft false is an explicit
// "unlimited").
type SessionCap struct {
	Limit int
	Soft  bool
}

// SessionCap resolves the configured max_sessions into an effective cap:
//   - unset (nil field or nil receiver) → the host-derived soft default;
//   - explicit non-positive → unlimited, and explicit so no warning (the escape
//     hatch — set max_sessions to 0 to silence the host-capacity confirmation);
//   - explicit positive → a hard cap at that value.
func (c *Config) SessionCap() SessionCap {
	if c == nil || c.MaxSessions == nil {
		return SessionCap{Limit: DefaultSessionCap(), Soft: true}
	}
	if *c.MaxSessions < 1 {
		return SessionCap{Limit: 0, Soft: false}
	}
	return SessionCap{Limit: *c.MaxSessions, Soft: false}
}
