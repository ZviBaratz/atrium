//go:build !windows

package daemon

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reapAndWatch starts cmd, reaps it in the background (so an exited child does
// not linger as a zombie that still answers signal 0), and returns a function
// reporting whether it has exited yet.
func reapAndWatch(t *testing.T, cmd *exec.Cmd) (exited func() bool) {
	t.Helper()
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}

func TestTerminateProcess_GracefulOnSigterm(t *testing.T) {
	// `sleep` has the default SIGTERM disposition (terminate), so it stops well
	// before the timeout and terminateProcess returns via the liveness poll.
	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	exited := reapAndWatch(t, cmd)

	start := time.Now()
	require.NoError(t, terminateProcess(cmd.Process))

	// Returned promptly (graceful path), not after the full SIGKILL timeout.
	assert.Less(t, time.Since(start), gracefulStopTimeout)
	assert.Eventually(t, exited, time.Second, 10*time.Millisecond,
		"process should have exited after SIGTERM")
}

func TestTerminateProcess_FallsBackToKill(t *testing.T) {
	// Shrink the timeout so the SIGKILL fallback path is fast to exercise.
	origTimeout, origPoll := gracefulStopTimeout, gracefulStopPoll
	gracefulStopTimeout, gracefulStopPoll = 150*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { gracefulStopTimeout, gracefulStopPoll = origTimeout, origPoll })

	// A shell that ignores SIGTERM, forcing escalation to SIGKILL.
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "trap '' TERM; sleep 60")
	exited := reapAndWatch(t, cmd)

	require.NoError(t, terminateProcess(cmd.Process))
	assert.Eventually(t, exited, time.Second, 10*time.Millisecond,
		"process should have been SIGKILLed after the timeout")
}

func TestTerminateProcess_AlreadyDead(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	exited := reapAndWatch(t, cmd)
	require.Eventually(t, exited, time.Second, 10*time.Millisecond, "process should exit on its own")

	// An already-stopped daemon is success, not an error: terminateProcess
	// recognizes the "process gone" signal result and returns nil.
	assert.NoError(t, terminateProcess(cmd.Process))
}
