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

// acquireDaemonLock takes the exclusive daemon lock at path and returns a release
// func the running daemon must hold for its entire lifetime. Closing the
// descriptor drops the flock, and so does process death — so the lock doubles as
// a liveness signal the stopper can trust (see isDaemonLockHeld). If another
// daemon already holds it, it returns errDaemonAlreadyRunning so RunDaemon can
// decline to start a duplicate. Mirrors internal/update/lock_unix.go.
func acquireDaemonLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errDaemonAlreadyRunning
		}
		return nil, err
	}
	// Closing the descriptor releases the flock.
	return func() { _ = f.Close() }, nil
}

// isDaemonLockHeld reports whether a live daemon currently holds the lock at path.
// It trylocks without delivering anything:
//   - trylock blocked → a live daemon holds it → (true, nil).
//   - trylock succeeds → the file exists but nobody holds it, so the daemon that
//     created it has died and the recorded PID is stale → (false, nil).
//   - the file is absent → (false, errDaemonLockAbsent). This is deliberately
//     distinct from "present but free": an absent file is not proof of death (a
//     pre-lock daemon never created one), so callers fall back to a direct
//     signal/probe instead of treating the PID as stale.
//
// flock conflicts across separate open file descriptions even within a single
// process, so this answers correctly no matter who calls it.
func isDaemonLockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, errDaemonLockAbsent
		}
		return false, err
	}
	// Closing the descriptor releases the lock if our trylock below succeeded.
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil // a live daemon holds it
		}
		return false, err
	}
	return false, nil // we acquired it, so nobody held it
}

// awaitDaemonStartupLock resolves the one ambiguity a present-but-unheld lock
// leaves: a just-launched daemon records its PID (LaunchDaemon) before it
// reaches acquireDaemonLock in RunDaemon, so for a brief startup window it is
// alive without holding its lock — indistinguishable at a glance from a dead
// daemon's PID recycled onto an unrelated process. It polls for up to
// daemonStartupGrace: the lock turning held proves a live daemon (true); the
// process dying proves the PID stale (false); outliving the grace without ever
// locking means the PID was recycled — never signal it (false). The common
// stale case (process gone, PID not recycled) returns on the first probe, so
// the grace costs nothing there.
func awaitDaemonStartupLock(proc *os.Process, lockPath string) bool {
	deadline := time.Now().Add(daemonStartupGrace)
	for {
		if held, err := isDaemonLockHeld(lockPath); err == nil && held {
			return true
		}
		if proc.Signal(syscall.Signal(0)) != nil {
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(gracefulStopPoll)
	}
}

// terminateProcess stops the daemon gracefully: it sends SIGTERM (which trips the
// daemon's signal.NotifyContext shutdown, letting it persist its in-memory
// instances before exiting), waits for the process to disappear, and only
// escalates to SIGKILL if it overstays gracefulStopTimeout. The daemon is not a
// child of this process, so liveness is probed externally rather than with Wait.
//
// PID-reuse safety: liveness is probed via the daemon lock at lockPath (see
// daemonGone), which the kernel frees only when the real daemon dies — so a
// recycled PID can never be mistaken for a still-running daemon and SIGKILLed.
// StopDaemon also confirms the lock is held before calling this, so the initial
// SIGTERM targets a confirmed-live daemon. An empty lockPath (tests) falls back
// to a signal-0 probe.
func terminateProcess(proc *os.Process, lockPath string) error {
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if processGone(err) {
			return nil // already stopped — nothing to do
		}
		// Some other failure (e.g. no permission) — Kill is the best fallback.
		return proc.Kill()
	}

	deadline := time.Now().Add(gracefulStopTimeout)
	for time.Now().Before(deadline) {
		if daemonGone(proc, lockPath) {
			return nil
		}
		time.Sleep(gracefulStopPoll)
	}
	return proc.Kill()
}

// daemonGone reports whether the daemon has exited. It prefers the lock
// (reuse-proof: the kernel frees it only when the real daemon process dies). It
// falls back to a signal-0 probe — where any error means the PID is gone — when
// no lock path is supplied (tests), on a lock-check error, or when the lock file
// is absent (a pre-lock daemon never created one), since none of those let the
// lock answer authoritatively.
func daemonGone(proc *os.Process, lockPath string) bool {
	if lockPath != "" {
		if held, err := isDaemonLockHeld(lockPath); err == nil {
			return !held
		}
	}
	return proc.Signal(syscall.Signal(0)) != nil
}
