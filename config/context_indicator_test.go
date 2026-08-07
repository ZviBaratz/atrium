package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetContextIndicator pins the normalization rule, which deliberately
// differs from the three on/off chip accessors beside it: a recognized mode
// passes through and everything else resolves to "percent", NOT to the first
// constant.
//
// The difference is the point of the test. With four modes, folding unknown
// values into "off" would make a hand-edited typo silently delete the chip, and
// a feature that vanishes with no explanation is the one failure a settings typo
// must not cause. Costing the user their preference is recoverable; costing them
// the feature looks like a bug in Atrium.
func TestGetContextIndicator(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", ContextIndicatorPercent},
		{"garbage", ContextIndicatorPercent},
		{ContextIndicatorPercent, ContextIndicatorPercent},
		{ContextIndicatorCount, ContextIndicatorCount},
		{ContextIndicatorBar, ContextIndicatorBar},
		{ContextIndicatorCost, ContextIndicatorCost},
		{ContextIndicatorOff, ContextIndicatorOff},
		// Near-misses a hand edit actually produces: none may resolve to "off".
		{"Off", ContextIndicatorPercent},
		{"on", ContextIndicatorPercent},
		{"percentage", ContextIndicatorPercent},
		{" off", ContextIndicatorPercent},
		{"spend", ContextIndicatorPercent},
	} {
		c := &Config{ContextIndicator: tc.value}
		assert.Equal(t, tc.want, c.GetContextIndicator(), "ContextIndicator=%q", tc.value)
	}
	assert.Equal(t, ContextIndicatorPercent, (*Config)(nil).GetContextIndicator(), "nil Config")
}

// TestGetContextIndicatorDefaultMatchesDefaultConfig pins that a fresh config
// resolves to the documented default. DefaultConfig() sets no literal for this
// field — the accessor does the work — and the settings panel's
// TestNoRowIsModifiedOnAFreshConfig compares exactly these two, so a divergence
// would show every new user a row flagged as "changed from default".
func TestGetContextIndicatorDefaultMatchesDefaultConfig(t *testing.T) {
	assert.Equal(t, ContextIndicatorPercent, DefaultConfig().GetContextIndicator())
	assert.Equal(t, ContextIndicatorPercent, (&Config{}).GetContextIndicator())
}
