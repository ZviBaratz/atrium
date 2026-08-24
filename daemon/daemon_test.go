package daemon

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
)

// A non-positive configured interval would panic time.NewTicker; effectivePollInterval
// must fall back to the built-in default so the daemon keeps running. A positive value
// passes through unchanged.
func TestEffectivePollInterval(t *testing.T) {
	def := time.Duration(config.DefaultDaemonPollIntervalMs) * time.Millisecond
	assert.Equal(t, def, effectivePollInterval(0), "zero must fall back to the default")
	assert.Equal(t, def, effectivePollInterval(-100), "negative must fall back to the default")
	assert.Equal(t, 250*time.Millisecond, effectivePollInterval(250), "positive passes through unchanged")
}
