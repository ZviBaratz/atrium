package config

import "testing"

// withCPUCount pins the hardware-thread source for a test and restores it after,
// so host-derived-cap assertions do not depend on the machine running the suite.
func withCPUCount(t *testing.T, n int) {
	t.Helper()
	prev := cpuCount
	cpuCount = func() int { return n }
	t.Cleanup(func() { cpuCount = prev })
}

// deriveSessionCap is max(2, numCPU/2): halve the threads for headroom, but never
// drop below a floor of 2 so tiny hosts stay usable.
func TestDeriveSessionCap(t *testing.T) {
	cases := map[int]int{1: 2, 2: 2, 4: 2, 5: 2, 6: 3, 8: 4, 16: 8, 32: 16}
	for numCPU, want := range cases {
		if got := deriveSessionCap(numCPU); got != want {
			t.Errorf("deriveSessionCap(%d) = %d, want %d", numCPU, got, want)
		}
	}
}

// DefaultSessionCap derives the cap from the live hardware-thread count.
func TestDefaultSessionCap_UsesCPUCount(t *testing.T) {
	withCPUCount(t, 16)
	if got := DefaultSessionCap(); got != 8 {
		t.Fatalf("DefaultSessionCap() = %d, want 8 for 16 threads", got)
	}
}

// An unset max_sessions resolves to the host-derived soft cap: exceeding it warns
// but is allowed.
func TestSessionCap_UnsetIsSoftHostDerived(t *testing.T) {
	withCPUCount(t, 8)
	var c Config
	sc := c.SessionCap()
	if sc.Limit != 4 || !sc.Soft {
		t.Fatalf("nil MaxSessions: got %+v, want {Limit:4 Soft:true}", sc)
	}
}

// A nil receiver behaves like an unset field (host-derived soft cap).
func TestSessionCap_NilReceiverIsSoftHostDerived(t *testing.T) {
	withCPUCount(t, 8)
	var c *Config
	sc := c.SessionCap()
	if sc.Limit != 4 || !sc.Soft {
		t.Fatalf("nil Config: got %+v, want {Limit:4 Soft:true}", sc)
	}
}

// An explicit non-positive value is the "unlimited" escape hatch: no cap and,
// being explicit, no warning (Soft is false).
func TestSessionCap_ExplicitNonPositiveIsUnlimited(t *testing.T) {
	withCPUCount(t, 8) // must be ignored: an explicit value wins over the host default
	for _, v := range []int{0, -1, -100} {
		val := v
		c := Config{MaxSessions: &val}
		sc := c.SessionCap()
		if sc.Limit != 0 || sc.Soft {
			t.Errorf("MaxSessions=%d: got %+v, want {Limit:0 Soft:false} (unlimited)", v, sc)
		}
	}
}

// An explicit positive value is a hard cap: exceeding it is refused (Soft false),
// down to a cap of 1.
func TestSessionCap_ExplicitPositiveIsHardCap(t *testing.T) {
	withCPUCount(t, 8)
	for _, v := range []int{1, 3, 25} {
		val := v
		c := Config{MaxSessions: &val}
		sc := c.SessionCap()
		if sc.Limit != v || sc.Soft {
			t.Errorf("MaxSessions=%d: got %+v, want {Limit:%d Soft:false}", v, sc, v)
		}
	}
}

// DefaultConfig must not write a session cap: absence of the key in config.json is
// what resolves to the host-derived default (see SessionCap).
func TestDefaultConfigHasNoSessionCap(t *testing.T) {
	if c := DefaultConfig(); c.MaxSessions != nil {
		t.Fatalf("DefaultConfig().MaxSessions = %d, want nil", *c.MaxSessions)
	}
}
