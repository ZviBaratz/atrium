package update

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Only clean release versions self-update. "dev" (unstamped builds) and
// git-describe strings have no corresponding release asset, and a dev build
// usually outpaces the latest tag.
func TestIsUpdatableVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.6.0", true},
		{"1.2.3", true},
		{"dev", false},
		{"", false},
		{"0.6.0-5-gabc123", false},
		{"0.6.0-rc.1", false},
		{"0.6.0-dirty", false},
		{"0.6.0+meta", false},
		// `git describe --always` fallback in a tagless clone: a bare short SHA.
		// Hex SHAs would panic in the library's semver.MustParse; an all-digit
		// SHA would silently parse as version 1234567.0.0. Both must be inert.
		{"1cd6ba3", false},
		{"1234567", false},
		// Strict semver only: a clean X.Y.Z, nothing looser.
		{"0.6", false},
		{"v0.6.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			assert.Equal(t, tc.want, IsUpdatableVersion(tc.v), "IsUpdatableVersion(%q)", tc.v)
		})
	}
}

// isNewer drives the cache short-circuit; unparseable input on either side is
// never "newer" (no update prompt on bad data).
func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"0.7.0", "0.6.0", true},
		{"0.6.1", "0.6.0", true},
		{"0.6.0", "0.6.0", false},
		{"0.5.9", "0.6.0", false},
		{"v0.7.0", "0.6.0", true},
		{"0.7.0", "v0.6.0", true},
		{"v0.7.0", "v0.6.0", true},
		{"garbage", "0.6.0", false},
		{"0.7.0", "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.candidate+" vs "+tc.current, func(t *testing.T) {
			assert.Equal(t, tc.want, isNewer(tc.candidate, tc.current), "isNewer(%q, %q)", tc.candidate, tc.current)
		})
	}
}
