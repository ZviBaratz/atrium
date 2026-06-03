package config

import "testing"

// A nil SessionContextBar (older config files predating the feature) defaults to on,
// so the bar appears without users having to add the key — matching GetAutoAttach.
func TestGetSessionContextBar(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults on", nil, true},
		{"explicit true", &on, true},
		{"explicit false", &off, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{SessionContextBar: tc.val}
			if got := c.GetSessionContextBar(); got != tc.want {
				t.Fatalf("GetSessionContextBar() = %v, want %v", got, tc.want)
			}
		})
	}

	if !DefaultConfig().GetSessionContextBar() {
		t.Fatalf("DefaultConfig should enable the session context bar")
	}
}
