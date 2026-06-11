package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetModelIndicator pins the normalization rule: only an explicit "off"
// hides the chip; everything else — empty (old config files), unknown values,
// the retired "pinned"/"always" modes, and a nil Config — normalizes to "on".
func TestGetModelIndicator(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"", ModelIndicatorOn},
		{"garbage", ModelIndicatorOn},
		{ModelIndicatorOn, ModelIndicatorOn},
		{ModelIndicatorOff, ModelIndicatorOff},
		// Retired modes from the pinned/observed era keep showing the chip.
		{"pinned", ModelIndicatorOn},
		{"always", ModelIndicatorOn},
	} {
		c := &Config{ModelIndicator: tc.value}
		assert.Equal(t, tc.want, c.GetModelIndicator(), "ModelIndicator=%q", tc.value)
	}
	assert.Equal(t, ModelIndicatorOn, (*Config)(nil).GetModelIndicator(), "nil Config")
}
