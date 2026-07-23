package config

import "testing"

// An unset agent_oom_margin resolves to the default margin: the feature is on by
// default, so a fresh install protects the shared tmux server without configuration.
func TestGetAgentOOMMargin_UnsetIsDefault(t *testing.T) {
	var c Config
	if got := c.GetAgentOOMMargin(); got != DefaultOOMMargin() {
		t.Fatalf("nil AgentOOMMargin: got %d, want DefaultOOMMargin() %d", got, DefaultOOMMargin())
	}
}

// A nil receiver behaves like an unset field (the default margin).
func TestGetAgentOOMMargin_NilReceiverIsDefault(t *testing.T) {
	var c *Config
	if got := c.GetAgentOOMMargin(); got != DefaultOOMMargin() {
		t.Fatalf("nil Config: got %d, want DefaultOOMMargin() %d", got, DefaultOOMMargin())
	}
}

// An explicit non-positive value disables the feature (the opt-out): no OOM
// wrapper is applied at spawn.
func TestGetAgentOOMMargin_ExplicitNonPositiveIsDisabled(t *testing.T) {
	for _, v := range []int{0, -1, -100} {
		val := v
		c := Config{AgentOOMMargin: &val}
		if got := c.GetAgentOOMMargin(); got != 0 {
			t.Errorf("AgentOOMMargin=%d: got %d, want 0 (disabled)", v, got)
		}
	}
}

// An explicit positive value is used verbatim as the margin.
func TestGetAgentOOMMargin_ExplicitPositive(t *testing.T) {
	for _, v := range []int{1, 300, 900} {
		val := v
		c := Config{AgentOOMMargin: &val}
		if got := c.GetAgentOOMMargin(); got != v {
			t.Errorf("AgentOOMMargin=%d: got %d, want %d", v, got, v)
		}
	}
}

// DefaultConfig must not write the margin: absence of the key in config.json is
// what resolves to the default (see GetAgentOOMMargin).
func TestDefaultConfigHasNoAgentOOMMargin(t *testing.T) {
	if c := DefaultConfig(); c.AgentOOMMargin != nil {
		t.Fatalf("DefaultConfig().AgentOOMMargin = %d, want nil", *c.AgentOOMMargin)
	}
}

// DefaultOOMMargin is a positive, sub-1000 value (a valid oom_score_adj delta).
func TestDefaultOOMMargin_Sane(t *testing.T) {
	if d := DefaultOOMMargin(); d <= 0 || d >= 1000 {
		t.Fatalf("DefaultOOMMargin() = %d, want in (0,1000)", d)
	}
}
