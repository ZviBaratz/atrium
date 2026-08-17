//go:build !windows

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewWarnsWhenNoTUIRunning: the request genuinely succeeded — it is durable
// and will be created — so this exits 0, but silence would let an orchestrator
// believe a session existed when nothing was listening. That matters more here
// than for `send`: a caller that goes on to `atrium send` the session it thinks it
// just made would get "no session named …" with no clue why.
//
// Unix-only: acquireTUILock is a no-op on Windows, so tuiRunning reports
// "unknown" there and deliberately says nothing rather than guess.
func TestNewWarnsWhenNoTUIRunning(t *testing.T) {
	sandboxDataDir(t)

	stdout, stderr, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err, "a queued request is a success, not a failure")
	assert.Contains(t, stdout, "fix-auth")
	assert.Contains(t, stderr, "no atrium TUI is running")
	assert.Len(t, spooledCreates(t), 1, "and it is durable")
}

// TestNewStaysQuietWhileATUIHoldsTheLock is the other side of that warning: the
// normal case, creating a session while watching the fleet, must not nag.
func TestNewStaysQuietWhileATUIHoldsTheLock(t *testing.T) {
	sandboxDataDir(t)

	// Stand in for a running TUI by holding tui.lock for the duration.
	path, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(path)
	require.NoError(t, err)
	defer release()

	_, stderr, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestNewWaitSkipsTheNoTUIWarning: --wait already reports the outcome, including
// a timeout that says the request is still queued, so the warning would only
// duplicate it — and it must not be printed before the wait even begins.
func TestNewWaitSkipsTheNoTUIWarning(t *testing.T) {
	sandboxDataDir(t)

	_, stderr, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), wait: 150 * time.Millisecond,
	})
	require.Error(t, err, "nothing is draining, so this times out")
	assert.NotContains(t, stderr, "no atrium TUI is running")
}
