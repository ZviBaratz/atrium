//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// acquireTUILock takes the exclusive single-instance lock at path and returns a
// release func the running TUI holds for its entire lifetime; closing the descriptor
// drops the flock (and so does process death). If another interactive atrium already
// holds it, it returns errTUIAlreadyRunning so RunE can refuse to start a duplicate.
// Mirrors acquireDaemonLock (daemon/daemon_unix.go) and acquireUpdateLock
// (internal/update/lock_unix.go).
func acquireTUILock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errTUIAlreadyRunning
		}
		return nil, err
	}
	// Closing the descriptor releases the flock.
	return func() { _ = f.Close() }, nil
}

// tuiRunning reports whether an interactive atrium currently holds tui.lock for
// this data dir, by try-acquiring the lock and immediately releasing it.
//
// The answer is advisory and inherently racy: a TUI can start or exit the
// instant after this returns. Callers must therefore use it only to phrase a
// message, never to decide whether an operation is safe. Both callers — `atrium
// send` and `atrium new` — are safe either way, and for the same reason: neither
// writes state.json, each only spools, and each has already spooled by the time it
// asks. What the answer changes is whether a warning is printed, never what was done.
//
// known is false when the question could not be answered at all (an unresolvable
// data dir, or a lock that cannot be opened), so a caller can stay silent rather
// than assert something wrong.
func tuiRunning() (running, known bool) {
	path, err := tuiLockPath()
	if err != nil {
		return false, false
	}
	release, err := acquireTUILock(path)
	if err != nil {
		if errors.Is(err, errTUIAlreadyRunning) {
			return true, true
		}
		return false, false
	}
	release()
	return false, true
}
