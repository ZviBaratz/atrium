//go:build !windows

package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keepSignalsCaught holds a notification registration for the whole test, so SIGINT is
// never back at its default disposition (terminate) while these tests raise it at this
// process. Watch's own stop() calls signal.Stop, and a signal still in flight when that
// lands would kill the test binary rather than fail a test.
//
// Not parallel: these raise process-global signals.
func keepSignalsCaught(t *testing.T) {
	t.Helper()
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, os.Interrupt, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })
}

// The mechanism, not just the decision: a real SIGINT delivered to this process cancels
// a Watch context.
func TestWatchCancelsOnInterrupt(t *testing.T) {
	keepSignalsCaught(t)
	ctx, stop := Watch(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGINT did not cancel the lifecycle context")
	}
}

// The reason this package exists. A `sh -c` child of a custom command runs COOKED, in
// Atrium's own process group, so the Ctrl+C that aborts it is delivered to Atrium too.
// Without the suspension that cancels the root context and quits the app: pressing
// Ctrl+C to stop a three-minute build would exit the TUI.
func TestWatchSwallowsInterruptWhileSuspended(t *testing.T) {
	keepSignalsCaught(t)
	ctx, stop := Watch(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shortGrace(t)
	resume := SuspendTerminalSignals()
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))

	select {
	case <-ctx.Done():
		t.Fatal("a suspended SIGINT must not cancel the lifecycle context")
	case <-time.After(250 * time.Millisecond):
	}

	// And the watcher is still watching. A swallowed signal must not retire it —
	// signal.NotifyContext's goroutine returns after the first signal it sees, which is
	// why this is not that function: one suspended Ctrl+C would leave the process
	// unable to shut down on any later signal for the rest of its life.
	resume()
	released(t)
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher stopped watching after swallowing one SIGINT")
	}
}

// SIGTERM cancels even while suspended, delivered for real. The suspension is about the
// keyboard's Ctrl+C reaching a child in our process group; `kill` is still `kill`.
func TestWatchStillCancelsOnTermWhileSuspended(t *testing.T) {
	keepSignalsCaught(t)
	ctx, stop := Watch(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shortGrace(t)
	defer SuspendTerminalSignals()()
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM must cancel the lifecycle context even while SIGINT is suspended")
	}
}

// stop() cancels as signal.NotifyContext's does — main.go defers it, and a stop that
// left the context live would leak every goroutine parked on it.
func TestWatchStopCancels(t *testing.T) {
	keepSignalsCaught(t)
	ctx, stop := Watch(context.Background(), os.Interrupt)
	stop()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stop must cancel the context, matching signal.NotifyContext")
	}
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// A parent already cancelled must not leave a watcher goroutine parked forever.
func TestWatchHonoursACancelledParent(t *testing.T) {
	keepSignalsCaught(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	ctx, stop := Watch(parent, os.Interrupt)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled parent must cancel the lifecycle context")
	}
}

// TestSuspendedInterruptStormCannotCostTheShutdownSignal is the fix for the dropped-signal
// defect, driven with real signals because the buffer is the whole subject.
//
// os/signal DROPS a signal delivered to a full channel. When SIGINT shared one cap-1
// channel with SIGTERM/SIGHUP, a user holding Ctrl+C against a stubborn build queued a
// swallowed SIGINT into the only slot — and the SIGHUP from the closing window was then
// discarded outright, leaving the process alive with its terminal gone and main.go's
// deferred autoyes-daemon handoff never run. signal.NotifyContext could not lose this way
// because its goroutine returned on the first signal; keeping the watcher alive is what
// made the buffering load-bearing.
func TestSuspendedInterruptStormCannotCostTheShutdownSignal(t *testing.T) {
	keepSignalsCaught(t)
	shortGrace(t)
	ctx, stop := Watch(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	defer SuspendTerminalSignals()()
	// Enough SIGINTs to overrun any single shared buffer, all of them swallowed.
	for range 20 {
		require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	}
	select {
	case <-ctx.Done():
		t.Fatal("a suspended SIGINT storm must not cancel the lifecycle context")
	case <-time.After(150 * time.Millisecond):
	}

	// The signal that must survive it.
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM was dropped behind a queue of swallowed SIGINTs — the SIGHUP " +
			"case of this loses the autoyes-daemon handoff")
	}
}

// Delivered for real: with a takeover held, a SIGQUIT at this process must not kill it.
// Only this direction is raiseable — a SIGQUIT after the registration is handed back dumps
// stacks and exits(2), which is why stopSignals is seamed and asserted through the seam.
func TestSuspendedSIGQUITDoesNotKillTheProcess(t *testing.T) {
	keepSignalsCaught(t)
	shortGrace(t)
	defer SuspendTerminalSignals()()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGQUIT))
	// Reaching the next statement at all is the assertion: an unignored SIGQUIT would have
	// taken the test binary down with a stack dump before it ran.
	time.Sleep(50 * time.Millisecond)
}
