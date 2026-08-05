package lifecycle

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortGrace shrinks the post-takeover borrow window so tests do not wait it out, and
// restores it afterwards.
func shortGrace(t *testing.T) {
	t.Helper()
	prev := interruptGrace
	interruptGrace = 5 * time.Millisecond
	t.Cleanup(func() { interruptGrace = prev })
}

// released waits for the grace-delayed release, so a test asserting the resumed state is
// not racing the timer that performs it.
func released(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return interruptSuspended.Load() == 0 },
		2*time.Second, time.Millisecond, "the borrow must be released after the grace")
}

// TestCancelsNeverTouchesAShutdownSignal is the whole scope argument, asserted rather than
// commented.
//
// main.go registers SIGHUP deliberately: it overrides Go's "terminate without running
// defers" disposition so closing the terminal cancels the lifecycle context and lets the
// deferred autoyes-daemon handoff run. A suspension that swallowed SIGHUP or SIGTERM
// would silently take that away — the process would keep running with its terminal gone,
// and the handoff would never happen.
func TestCancelsNeverTouchesAShutdownSignal(t *testing.T) {
	shortGrace(t)
	require.True(t, cancels(os.Interrupt), "SIGINT cancels when nothing is suspended")

	resume := SuspendTerminalSignals()
	assert.False(t, cancels(os.Interrupt), "a suspended SIGINT belongs to the child")
	assert.True(t, cancels(syscall.SIGTERM),
		"SIGTERM must cancel while suspended — the suspension never borrows a shutdown signal")
	assert.True(t, cancels(syscall.SIGHUP),
		"SIGHUP must cancel while suspended, or the daemon handoff never runs")

	resume()
	released(t)
	assert.True(t, cancels(os.Interrupt), "resuming restores SIGINT")
}

// A suspension nests, and its resume is idempotent: a resume that could run twice would
// drive the depth negative and leave SIGINT suppressed for the rest of the process — a
// TUI that no longer answers Ctrl+C from a non-TTY parent, with nothing to explain it.
func TestSuspendNestsAndResumesOnce(t *testing.T) {
	shortGrace(t)
	outer := SuspendTerminalSignals()
	inner := SuspendTerminalSignals()
	require.False(t, cancels(os.Interrupt))

	inner()
	inner() // idempotent: must not release the outer suspension
	time.Sleep(20 * time.Millisecond)
	assert.False(t, cancels(os.Interrupt), "the outer suspension is still held")

	outer()
	released(t)
	assert.True(t, cancels(os.Interrupt), "the last resume restores SIGINT")
}

// TestBorrowOutlivesTheChildByTheGrace is the fix for the observe-after-resume race.
//
// A signal has no arrival time we can read: cancels() runs when the watcher OBSERVES the
// SIGINT, which can be after the child died and the resume already fired. Without the
// grace that read sees depth 0 and quits the TUI on the very keypress this package exists
// to make survivable, and it is a scheduling coin flip — worst for exactly the
// short-lived commands a user is most likely to interrupt.
func TestBorrowOutlivesTheChildByTheGrace(t *testing.T) {
	prev := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = prev })

	resume := SuspendTerminalSignals()
	resume() // the child exited and Run returned

	assert.False(t, cancels(os.Interrupt),
		"a SIGINT observed just after the child exited still belongs to the child")
	released(t)
	assert.True(t, cancels(os.Interrupt), "and the borrow does not leak past the grace")
}

// SuspendTerminalSignals is called from attachCommand.Run, which unit tests drive directly
// with no Watch anywhere — so it must be inert rather than panicking or blocking, and must
// leave the process's SIGQUIT disposition as it found it.
func TestSuspendWithoutAWatcher(t *testing.T) {
	shortGrace(t)
	resume := SuspendTerminalSignals()
	resume()
	released(t)
}

// TestSuspendIgnoresAndRestoresSIGQUIT is what makes the SIGQUIT half real rather than a
// list nobody consults.
//
// Both directions matter and both were unguarded. Without the ignore, Ctrl+\ during a
// cooked takeover dumps every goroutine stack and exits(2), skipping main.go's deferred
// autoyes-daemon handoff — the whole fleet stops being answered with goroutine traces
// where the screen was. Without the reset, Go's stack-dump aid stays disabled for the rest
// of the process after the first custom command.
func TestSuspendIgnoresAndRestoresSIGQUIT(t *testing.T) {
	shortGrace(t)
	prevNotify, prevStop := notifySignals, stopSignals
	t.Cleanup(func() { notifySignals, stopSignals = prevNotify, prevStop })
	var registered [][]os.Signal
	stops := 0
	notifySignals = func(_ chan<- os.Signal, s ...os.Signal) { registered = append(registered, s) }
	stopSignals = func(chan<- os.Signal) { stops++ }

	resume := SuspendTerminalSignals()
	require.Len(t, registered, 1, "the takeover must take SIGQUIT off Go's default")
	assert.Equal(t, swallowedDuringTakeover, registered[0])
	assert.Zero(t, stops, "and not hand it back before the command has finished")

	resume()
	assert.Equal(t, 1, stops, "SIGQUIT's stack-dump default must come back")
	resume()
	assert.Equal(t, 1, stops, "and handing it back is idempotent, like the release")
	released(t)
}

// The list must not include SIGINT: the lifecycle watcher already receives that one, and a
// second registration would give two consumers a claim on the same delivery — whichever
// won, the depth check in cancels() would stop being what decides.
func TestSwallowedDuringTakeoverExcludesInterrupt(t *testing.T) {
	require.NotEmpty(t, swallowedDuringTakeover)
	for _, s := range swallowedDuringTakeover {
		assert.NotEqual(t, os.Interrupt, s,
			"SIGINT is already on the watcher's channel; registering it twice here would "+
				"race two consumers for the same signal")
	}
	assert.Contains(t, swallowedDuringTakeover, os.Signal(syscall.SIGQUIT),
		"cooked mode arms Ctrl+\\, whose default dumps stacks and exits without running "+
			"main.go's deferred daemon handoff")
}
