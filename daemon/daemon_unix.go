//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// processGone reports whether a signal error means the target is already gone —
// ESRCH for an unrelated PID (the production case: the daemon is not our child)
// or os.ErrProcessDone for a reaped child (the test case).
func processGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

// getSysProcAttr returns platform-specific process attributes for detaching the child process
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true, // Create a new session
	}
}

// terminateProcess stops the daemon gracefully: it sends SIGTERM (which trips the
// daemon's signal.NotifyContext shutdown, letting it persist its in-memory
// instances before exiting), waits for the process to disappear, and only
// escalates to SIGKILL if it overstays gracefulStopTimeout. The daemon is not a
// child of this process, so liveness is probed with signal 0 rather than Wait.
func terminateProcess(proc *os.Process) error {
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if processGone(err) {
			return nil // already stopped — nothing to do
		}
		// Some other failure (e.g. no permission) — Kill is the best fallback.
		return proc.Kill()
	}

	deadline := time.Now().Add(gracefulStopTimeout)
	for time.Now().Before(deadline) {
		// signal 0 performs error checking without delivering a signal: a
		// non-nil error means the process is gone (or its PID was reused).
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return nil
		}
		time.Sleep(gracefulStopPoll)
	}
	return proc.Kill()
}
