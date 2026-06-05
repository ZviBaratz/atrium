package config

import "testing"

// GetMaxSessions returns 0 (unlimited) for nil and non-positive values and
// honors an explicit positive cap.
func TestGetMaxSessions(t *testing.T) {
	var c Config
	if got := c.GetMaxSessions(); got != 0 {
		t.Fatalf("nil MaxSessions: got %d, want 0 (unlimited)", got)
	}

	zero := 0
	c.MaxSessions = &zero
	if got := c.GetMaxSessions(); got != 0 {
		t.Fatalf("zero MaxSessions: got %d, want 0 (unlimited)", got)
	}

	negative := -5
	c.MaxSessions = &negative
	if got := c.GetMaxSessions(); got != 0 {
		t.Fatalf("negative MaxSessions: got %d, want 0 (unlimited)", got)
	}

	limit := 25
	c.MaxSessions = &limit
	if got := c.GetMaxSessions(); got != 25 {
		t.Fatalf("explicit MaxSessions: got %d, want 25", got)
	}
}

// DefaultConfig must not write a session cap: absence of the key in
// config.json is how "unlimited" is persisted.
func TestDefaultConfigHasNoSessionCap(t *testing.T) {
	if c := DefaultConfig(); c.MaxSessions != nil {
		t.Fatalf("DefaultConfig().MaxSessions = %d, want nil", *c.MaxSessions)
	}
}
