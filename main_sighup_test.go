//go:build !windows

package main

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/lifecycle"

	"github.com/stretchr/testify/require"
)

// Beyond asserting membership (see TestQuitSignals), prove the mechanism: a real
// SIGHUP delivered to this process cancels a context wired with quitSignals
// (rather than killing the test binary). Watch registers before Kill, so
// Go catches the signal. Not parallel — it raises a process-global signal.
//
// Through lifecycle.Watch, which is what the root command calls — not
// signal.NotifyContext, which it no longer does. Watch can borrow SIGINT for a cooked
// terminal takeover, and the point of asserting SIGHUP here is that it can never borrow
// this one: the deferred autoyes-daemon handoff depends on it.
//
// Guarded to non-Windows: it uses syscall.Kill, which is undefined on Windows.
// The membership check in TestQuitSignals stays cross-platform.
func TestQuitSignalsCancelContextOnSIGHUP(t *testing.T) {
	ctx, stop := lifecycle.Watch(context.Background(), quitSignals...)
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGHUP))

	select {
	case <-ctx.Done():
		// pass: SIGHUP cancelled the lifecycle context, so defers (the daemon
		// handoff) get to run — it did not hard-kill the process.
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP did not cancel the lifecycle context wired with quitSignals")
	}
}
