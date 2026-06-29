//go:build !windows

package main

import (
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acquire → second-acquire-refused (separate fd) → release → re-acquire. Mirrors
// TestDaemonLock (daemon/daemon_unix_test.go). Hermetic: explicit temp path, no
// HOME / GetConfigDir needed.
func TestAcquireTUILock(t *testing.T) {
	path := filepath.Join(t.TempDir(), tuiLockFilename)

	release, err := acquireTUILock(path)
	require.NoError(t, err)

	// flock conflicts across open file descriptions even within one process, so a
	// second acquire is refused while the first is held.
	if _, err := acquireTUILock(path); !assert.ErrorIs(t, err, errTUIAlreadyRunning) {
		t.Fatalf("expected errTUIAlreadyRunning, got %v", err)
	}

	release()

	// Released → the next TUI can acquire it.
	release2, err := acquireTUILock(path)
	require.NoError(t, err)
	release2()
}

// tuiLockPath resolves <data dir>/tui.lock. HOME is sandboxed per the repo test
// convention because it reaches config.GetConfigDir.
func TestTUILockPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path, err := tuiLockPath()
	require.NoError(t, err)
	assert.Equal(t, tuiLockFilename, filepath.Base(path))

	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	assert.Equal(t, dir, filepath.Dir(path))
}
