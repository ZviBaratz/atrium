package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigDirResolution locks in the "prefer-new, fall back to legacy, never
// move" contract. The data dir holds worktrees and a state.json of absolute paths,
// so an existing legacy dir must be used in place and never relocated.
func TestConfigDirResolution(t *testing.T) {
	t.Run("fresh install uses ~/.atrium", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		dir, err := GetConfigDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".atrium"), dir)
		assert.Equal(t, "atrium", RuntimeName())
	})

	t.Run("legacy-only uses ~/.claude-squad and never moves it", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		legacy := filepath.Join(home, ".claude-squad")
		require.NoError(t, os.MkdirAll(legacy, 0o755))
		sentinel := filepath.Join(legacy, "sentinel")
		require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o644))

		dir, err := GetConfigDir()
		require.NoError(t, err)
		assert.Equal(t, legacy, dir)
		assert.Equal(t, "claudesquad", RuntimeName())

		// Never moved: the legacy dir and its contents survive untouched, and the
		// new dir is not conjured into existence as a side effect of resolution.
		assert.FileExists(t, sentinel)
		assert.NoDirExists(t, filepath.Join(home, ".atrium"))
	})

	t.Run("both present prefers ~/.atrium", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude-squad"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))

		dir, err := GetConfigDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".atrium"), dir)
		assert.Equal(t, "atrium", RuntimeName())
	})
}
