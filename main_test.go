package main

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exit-time daemon decision must re-read the persisted config rather than
// the autoyes value merged at startup, so an auto_yes toggle made in the
// settings panel during the run takes effect when the TUI exits.
func TestShouldLaunchDaemonOnExit(t *testing.T) {
	// Sandbox HOME so config reads/writes never touch the real data dir.
	t.Setenv("HOME", t.TempDir())

	t.Run("config off, no flag: no daemon", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.AutoYes = false
		require.NoError(t, config.SaveConfig(cfg))
		assert.False(t, shouldLaunchDaemonOnExit(false))
	})

	t.Run("config toggled on during the run wins over the startup value", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.AutoYes = true
		require.NoError(t, config.SaveConfig(cfg))
		assert.True(t, shouldLaunchDaemonOnExit(false))
	})

	t.Run("the -y flag wins for the run that received it", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.AutoYes = false
		require.NoError(t, config.SaveConfig(cfg))
		assert.True(t, shouldLaunchDaemonOnExit(true))
	})
}
