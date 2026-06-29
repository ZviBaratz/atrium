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
