package config

// defaultOOMMargin is the oom_score_adj delta an unset agent_oom_margin resolves
// to. At 300, an agent adds 300/1000 × total RAM of "virtual badness" over the
// server it inherits from — far more than any plausible agent-vs-server RSS
// difference, so the kernel OOM killer reliably picks a single agent (one
// recoverable session) before the shared tmux server (every session). It stays
// well under the kernel's +1000 ceiling, leaving room to clamp.
const defaultOOMMargin = 300

// DefaultOOMMargin is the margin an unset agent_oom_margin resolves to. Exported so
// the spawn path and `atrium doctor` report the same number.
func DefaultOOMMargin() int { return defaultOOMMargin }

// GetAgentOOMMargin resolves the configured agent_oom_margin into an effective
// oom_score_adj delta:
//   - unset (nil field or nil receiver) → the default margin (feature on);
//   - explicit non-positive → 0 (disabled — the opt-out);
//   - explicit positive → that margin.
//
// A returned 0 means "apply no OOM wrapper"; any positive value is the number of
// points to raise each agent above the tmux server.
func (c *Config) GetAgentOOMMargin() int {
	if c == nil || c.AgentOOMMargin == nil {
		return DefaultOOMMargin()
	}
	if *c.AgentOOMMargin < 1 {
		return 0
	}
	return *c.AgentOOMMargin
}
