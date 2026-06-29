//go:build !windows

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
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
	require.NoError(t, terminateProcess(cmd.Process, ""))

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

	require.NoError(t, terminateProcess(cmd.Process, ""))
	assert.Eventually(t, exited, time.Second, 10*time.Millisecond,
		"process should have been SIGKILLed after the timeout")
}

func TestTerminateProcess_AlreadyDead(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	exited := reapAndWatch(t, cmd)
	require.Eventually(t, exited, time.Second, 10*time.Millisecond, "process should exit on its own")

	// An already-stopped daemon is success, not an error: terminateProcess
	// recognizes the "process gone" signal result and returns nil.
	assert.NoError(t, terminateProcess(cmd.Process, ""))
}

// The daemon lock is the liveness+ownership signal StopDaemon trusts: held while a
// daemon owns it, free once released, and exclusive against a second daemon.
func TestDaemonLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), daemonLockFilename)

	// No file yet → indeterminate, reported as errDaemonLockAbsent (distinct from a
	// present-but-free lock) so the stopper falls back to a direct probe instead of
	// assuming the recorded PID is stale.
	if _, err := isDaemonLockHeld(path); !assert.ErrorIs(t, err, errDaemonLockAbsent) {
		t.Fatalf("expected errDaemonLockAbsent for missing file, got %v", err)
	}

	release, err := acquireDaemonLock(path)
	require.NoError(t, err)

	// A second acquire must be refused while the first is held (single-instance).
	if _, err := acquireDaemonLock(path); !assert.ErrorIs(t, err, errDaemonAlreadyRunning) {
		t.Fatalf("expected errDaemonAlreadyRunning, got %v", err)
	}

	// flock conflicts across open file descriptions even in one process, so the
	// stopper's check sees the held lock.
	held, err := isDaemonLockHeld(path)
	require.NoError(t, err)
	assert.True(t, held, "lock must read as held while owned")

	release()

	held, err = isDaemonLockHeld(path)
	require.NoError(t, err)
	assert.False(t, held, "lock must read as not held after release")
}

// The core fix: a stale daemon.pid (the daemon died but the OS recycled its PID
// onto an unrelated process) must not get signaled. The dead daemon left its lock
// file on disk but no longer holds it; that present-but-free lock is the signal
// StopDaemon trusts to skip signaling and clear the stale PID file instead of
// killing the innocent process now living at that PID.
func TestStopDaemon_SkipsStalePIDWhenLockNotHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// Stand in for the innocent process that inherited the recycled PID.
	victim := exec.CommandContext(context.Background(), "sleep", "60")
	exited := reapAndWatch(t, victim)

	pidFile := filepath.Join(dir, "daemon.pid")
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(victim.Process.Pid)), 0o644))
	// The dead daemon left its lock file behind but holds no flock on it: a
	// present-but-free lock, which proves the PID is stale.
	lockFile := filepath.Join(dir, daemonLockFilename)
	require.NoError(t, os.WriteFile(lockFile, nil, 0o644))

	require.NoError(t, StopDaemon())

	// The victim must be untouched, and the stale PID file cleared.
	assert.NoError(t, victim.Process.Signal(syscall.Signal(0)), "victim must not be signaled")
	assert.False(t, exited(), "victim should still be running (was not signaled)")
	_, statErr := os.Stat(pidFile)
	assert.True(t, os.IsNotExist(statErr), "stale PID file should be removed")
}

// Regression guard for the pre-lock upgrade path: when daemon.pid points at a
// LIVE daemon but there is no lock file (a daemon from a build predating the lock
// never created one), StopDaemon must still stop it rather than mistake the absent
// file for a dead daemon and orphan it. An absent lock file is not proof of death;
// only a present-but-free lock is.
func TestStopDaemon_SignalsLegacyDaemonWithoutLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// Stand in for a still-running pre-lock daemon. `sleep` takes the default
	// SIGTERM disposition, so StopDaemon's graceful path terminates it.
	legacy := exec.CommandContext(context.Background(), "sleep", "60")
	exited := reapAndWatch(t, legacy)

	pidFile := filepath.Join(dir, "daemon.pid")
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(legacy.Process.Pid)), 0o644))
	// Deliberately create NO daemon.lock: a pre-lock daemon never made one.

	require.NoError(t, StopDaemon())

	assert.Eventually(t, exited, time.Second, 10*time.Millisecond,
		"legacy daemon should be stopped, not orphaned")
	_, statErr := os.Stat(pidFile)
	assert.True(t, os.IsNotExist(statErr), "PID file should be removed after stopping")
}
