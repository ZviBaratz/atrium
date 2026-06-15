package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetPermissionIndicator pins the normalization rule: only an explicit
// "off" hides the chip; everything else — empty (old config files), unknown
// values, and a nil Config — normalizes to "on".
func TestGetPermissionIndicator(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"", PermissionIndicatorOn},
		{"garbage", PermissionIndicatorOn},
		{PermissionIndicatorOn, PermissionIndicatorOn},
		{PermissionIndicatorOff, PermissionIndicatorOff},
	} {
		c := &Config{PermissionIndicator: tc.value}
		assert.Equal(t, tc.want, c.GetPermissionIndicator(), "PermissionIndicator=%q", tc.value)
	}
	assert.Equal(t, PermissionIndicatorOn, (*Config)(nil).GetPermissionIndicator(), "nil Config")
}
