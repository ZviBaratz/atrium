//go:build windows

package daemon

import (
	"golang.org/x/sys/windows"
	"os"
	"syscall"
)

// getSysProcAttr returns platform-specific process attributes for detaching the child process
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// acquireDaemonLock is a no-op on Windows, which has no flock, so the daemon runs
// without a single-instance lock. The exposure is small — Atrium's tmux-based
// runtime doesn't target Windows (the build exists for completeness) — and this
// mirrors internal/update/lock_windows.go.
func acquireDaemonLock(path string) (release func(), err error) {
	return func() {}, nil
}

// isDaemonLockHeld can't consult an flock on Windows, so it conservatively
// reports the lock as held: StopDaemon then falls back to its prior
// signal-by-PID behavior (terminateProcess's Kill), unchanged here.
func isDaemonLockHeld(path string) (bool, error) {
	return true, nil
}

// awaitDaemonStartupLock is unreachable on Windows: isDaemonLockHeld always
// reports the lock held, so StopDaemon never takes the present-but-unheld
// branch that calls this. The stub keeps the shared code compiling.
func awaitDaemonStartupLock(proc *os.Process, lockPath string) bool {
	return false
}

// terminateProcess stops the daemon. Windows has no SIGTERM equivalent that Go's
// os/signal delivers to a detached process group, so there is no graceful
// shutdown hook to trip; fall back to an immediate kill (the prior behavior).
// lockPath is unused here (no flock); it keeps the cross-platform signature.
func terminateProcess(proc *os.Process, lockPath string) error {
	return proc.Kill()
}
