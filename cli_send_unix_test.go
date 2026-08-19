//go:build !windows

package main

import (
	"testing"
	"time"

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
	holdTUILock(t)

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err)
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestSendWarnsWhileAtriumIsParked is the prompt spool's half of #760. A queued prompt
// waiting for a detach is less surprising than a session that does not exist — the
// queue overlay lists it and `atrium ls` reports it as queued_prompts — but the same
// silence was there, and it is one predicate.
func TestSendWarnsWhileAtriumIsParked(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	holdParked(t, "fix-auth")

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "the prompt is durable; it lands on the detach")
	assert.Contains(t, stderr, "poll loop is parked")
	assert.Contains(t, stderr, "nothing is delivering this yet", "send's verb, not new's")
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestSendWaitNamesWhoIsNotDraining: the old deadline copy ended "will be delivered the
// next time one runs", which is plainly false while a TUI is up with its terminal handed
// away — it delivers on the detach. That was the one message in the pair that asserted
// something untrue rather than merely vague.
func TestSendWaitNamesWhoIsNotDraining(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	holdParked(t, "fix-auth")

	_, _, err := send(t, "fix-auth", "", "hello", 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still queued", "the message survives a timeout; say so")
	assert.Contains(t, err.Error(), "poll loop is parked")
	assert.NotContains(t, err.Error(), "the next time one runs")
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
