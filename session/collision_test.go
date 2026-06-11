package session

import "testing"

// Duplicate detection must compare derived names, not raw titles: two distinct
// titles that sanitize to the same tmux segment or the same branch slug would
// still collide at the tmux or git layer.
func TestDerivedNamesCollide(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
		why  string
	}{
		{"same", "same", true, "identical titles"},
		{"Fix Bug", "fixbug", true, "tmux strips whitespace; case-insensitively equal segments"},
		{"x-y", "x y", true, "branch slug lowercases and dashes spaces"},
		{"v1.2", "v1 2", false, "dots and spaces sanitize differently in both layers"},
		{"a.b", "a_b", true, "tmux maps dots to underscores; segments equal"},
		{"foo", "bar", false, "unrelated titles"},
		{"alpha", "alpha2", false, "prefix of the other is not a collision"},
	}
	for _, c := range cases {
		if got := DerivedNamesCollide("zvi/", c.a, c.b); got != c.want {
			t.Errorf("DerivedNamesCollide(%q, %q) = %v, want %v (%s)", c.a, c.b, got, c.want, c.why)
		}
	}
}
