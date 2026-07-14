package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetEffortIndicator pins the normalization rule: only an explicit "off" hides the
// chip; everything else — empty (config files predating the key), unknown values, and a
// nil Config — normalizes to "on".
func TestGetEffortIndicator(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"", EffortIndicatorOn},
		{"garbage", EffortIndicatorOn},
		{EffortIndicatorOn, EffortIndicatorOn},
		{EffortIndicatorOff, EffortIndicatorOff},
	} {
		c := &Config{EffortIndicator: tc.value}
		assert.Equal(t, tc.want, c.GetEffortIndicator(), "EffortIndicator=%q", tc.value)
	}
	assert.Equal(t, EffortIndicatorOn, (*Config)(nil).GetEffortIndicator(), "nil Config")
}
