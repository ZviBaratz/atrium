package config

import "testing"

// GetMaxSessions falls back to the default for nil and non-positive values and
// honors an explicit positive cap.
func TestGetMaxSessions(t *testing.T) {
	var c Config
	if got := c.GetMaxSessions(); got != DefaultMaxSessions {
		t.Fatalf("nil MaxSessions: got %d, want %d", got, DefaultMaxSessions)
	}

	zero := 0
	c.MaxSessions = &zero
	if got := c.GetMaxSessions(); got != DefaultMaxSessions {
		t.Fatalf("zero MaxSessions: got %d, want %d", got, DefaultMaxSessions)
	}

	limit := 25
	c.MaxSessions = &limit
	if got := c.GetMaxSessions(); got != 25 {
		t.Fatalf("explicit MaxSessions: got %d, want 25", got)
	}
}
