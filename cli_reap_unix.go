//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// signalProcess sends sig to pid. A process that is already gone is not an error:
// the reaper's goal is that it be gone, and it is.
func signalProcess(pid int, sig syscall.Signal) error {
	err := syscall.Kill(pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// processAlive reports whether pid still exists, by delivering signal 0 — the
// technique session/tmux/pty_reap_unix_test.go uses. It answers existence only, not
// identity: a caller acting on the answer must have already checked the start time,
// because a recycled pid is just as alive as the one it replaced.
func processAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
