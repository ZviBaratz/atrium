package lifecycle

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCancelsSuspendsOnlyInterrupt is the whole scope argument, asserted rather than
// commented.
//
// main.go registers SIGHUP deliberately: it overrides Go's "terminate without running
// defers" disposition so closing the terminal cancels the lifecycle context and lets
// the deferred autoyes-daemon handoff run. A suspension that swallowed SIGHUP or
// SIGTERM would silently take that away — the process would keep running with its
// terminal gone, and the handoff would never happen.
func TestCancelsSuspendsOnlyInterrupt(t *testing.T) {
	require.True(t, cancels(os.Interrupt), "SIGINT cancels when nothing is suspended")

	resume := SuspendInterrupt()
	assert.False(t, cancels(os.Interrupt), "a suspended SIGINT belongs to the child")
	assert.True(t, cancels(syscall.SIGTERM),
		"SIGTERM must cancel while suspended — the suspension is SIGINT-scoped")
	assert.True(t, cancels(syscall.SIGHUP),
		"SIGHUP must cancel while suspended, or the daemon handoff never runs")

	resume()
	assert.True(t, cancels(os.Interrupt), "resuming restores SIGINT")
}

// A suspension nests, and its resume is idempotent: Run's deferred resume can be
// reached twice on no path today, but a counter that could go negative would leave
// SIGINT suppressed for the rest of the process — a TUI that no longer answers Ctrl+C
// from a non-TTY parent, with nothing to explain it.
func TestSuspendInterruptNestsAndResumesOnce(t *testing.T) {
	outer := SuspendInterrupt()
	inner := SuspendInterrupt()
	require.False(t, cancels(os.Interrupt))

	inner()
	inner() // idempotent: must not release the outer suspension
	assert.False(t, cancels(os.Interrupt), "the outer suspension is still held")

	outer()
	assert.True(t, cancels(os.Interrupt), "the last resume restores SIGINT")
	assert.Zero(t, interruptSuspended.Load(), "the depth must land back on zero")
}

// SuspendInterrupt is called from attachCommand.Run, which unit tests drive directly
// with no Watch anywhere — so it must be inert rather than panicking or blocking.
func TestSuspendInterruptWithoutAWatcher(t *testing.T) {
	resume := SuspendInterrupt()
	resume()
	assert.Zero(t, interruptSuspended.Load())
}
