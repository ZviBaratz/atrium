package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetModelIndicator pins the normalization rule: empty (old config files),
// unknown values, and a nil Config all fall back to the conservative default.
func TestGetModelIndicator(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"", ModelIndicatorPinned},
		{"garbage", ModelIndicatorPinned},
		{ModelIndicatorPinned, ModelIndicatorPinned},
		{ModelIndicatorAlways, ModelIndicatorAlways},
		{ModelIndicatorOff, ModelIndicatorOff},
	} {
		c := &Config{ModelIndicator: tc.value}
		assert.Equal(t, tc.want, c.GetModelIndicator(), "ModelIndicator=%q", tc.value)
	}
	assert.Equal(t, ModelIndicatorPinned, (*Config)(nil).GetModelIndicator(), "nil Config")
}
