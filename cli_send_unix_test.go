//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendWarnsWhenNoTUIRunning: the send genuinely succeeded — the message is
// durable and will be delivered — so this exits 0, but silence would let an
// orchestrator believe an agent had been prompted when nothing was listening.
//
// Unix-only: acquireTUILock is a no-op on Windows, so tuiRunning reports
// "unknown" there and deliberately says nothing rather than guess.
func TestSendWarnsWhenNoTUIRunning(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "a deferred delivery is not a failure")
	assert.Contains(t, stderr, "no atrium TUI is running")
	assert.Len(t, spooled(t), 1)
}

// TestSendStaysQuietWhileATUIHoldsTheLock is the other side of that warning: the
// normal case, sending while watching the fleet, must not nag.
func TestSendStaysQuietWhileATUIHoldsTheLock(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	// Stand in for a running TUI by holding tui.lock for the duration.
	path, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(path)
	require.NoError(t, err)
	defer release()

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err)
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestTUIRunningDetectsHeldLock pins the probe itself, independent of send.
func TestTUIRunningDetectsHeldLock(t *testing.T) {
	sandboxDataDir(t)

	running, known := tuiRunning()
	require.True(t, known)
	assert.False(t, running, "nothing holds the lock yet")

	path, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(path)
	require.NoError(t, err)

	running, known = tuiRunning()
	assert.True(t, known)
	assert.True(t, running, "a held lock means a TUI is up")

	release()
	running, _ = tuiRunning()
	assert.False(t, running, "releasing the lock frees the probe again")
}
