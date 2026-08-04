//go:build !windows

package main

import (
	"errors"
	"syscall"

	"github.com/ZviBaratz/atrium/session/tmux"
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

// processAlive reports whether pid is a process that is still running.
//
// Signal 0 — the technique session/tmux/pty_reap_unix_test.go uses — is necessary
// but not sufficient: it also succeeds for a zombie, which is a process that has
// already exited and is only waiting for its parent to collect it. A reaper built on
// signal 0 alone therefore reports a target it has just killed as having survived
// SIGKILL, which nothing does. Seen on the tmux 3.2 CI job, where the orphan was
// re-parented to a container init that never wait()s.
//
// It answers existence only, never identity: a caller acting on the answer must have
// already checked the start time, because a recycled pid is just as alive as the one
// it replaced.
func processAlive(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false
	}
	return !tmux.ProcessIsZombie(pid)
}
