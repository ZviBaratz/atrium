package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetMouse pins the default-on rule for the mouse-capture opt-out: nil (an
// older config file with no such key), and a nil Config, both keep the mouse on;
// only an explicit false turns it off.
func TestGetMouse(t *testing.T) {
	assert.True(t, (&Config{}).GetMouse(), "absent mouse key defaults to on")
	assert.True(t, (*Config)(nil).GetMouse(), "nil Config defaults to on")

	on := true
	off := false
	assert.True(t, (&Config{Mouse: &on}).GetMouse(), "explicit true stays on")
	assert.False(t, (&Config{Mouse: &off}).GetMouse(), "explicit false turns capture off")
}

// The built-in defaults ship with the mouse enabled, so a fresh install has
// clickable rows/tabs/hint-bar and wheel scrolling out of the box.
func TestDefaultConfigSeedsMouseOn(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg.Mouse, "DefaultConfig must seed an explicit mouse value")
	assert.True(t, *cfg.Mouse, "the default is mouse on")
	assert.True(t, cfg.GetMouse())
}
