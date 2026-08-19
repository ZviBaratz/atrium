//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// acquireTUILock takes the exclusive single-instance lock at path and returns a
// release func the running TUI holds for its entire lifetime; closing the descriptor
// drops the flock (and so does process death). If another interactive atrium already
// still holds it once lockExclusive's retry budget is spent, it returns
// errTUIAlreadyRunning so RunE can refuse to start a duplicate — the retry being what
// keeps a passing tuiRunning probe from looking like one.
// Mirrors acquireDaemonLock (daemon/daemon_unix.go) and acquireUpdateLock
// (internal/update/lock_unix.go).
func acquireTUILock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
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
// this data dir, by asking for a SHARED lock on it and immediately releasing that.
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
//
// It answers half a question, and never on its own. This lock is held identically by a
// TUI whose event loop is running and one that has handed its terminal to a session and
// so is draining nothing, which is why both callers read it through drainState
// (drainstate.go) alongside internal/handover rather than directly.
//
// LOCK_SH, not the LOCK_EX acquireTUILock takes, and O_RDONLY without O_CREATE. A probe
// only needs to discover whether an exclusive holder exists, and a shared request is
// refused by one just the same — while two shared requests do not refuse each other, so
// two concurrent headless commands no longer read one another as a running TUI. That is
// handover.Held's reason, and it is the only one that belongs here: a shared lock still
// refuses a starting TUI's exclusive one, so this does nothing for that direction and
// acquireTUILock's retry is what covers it.
//
// Dropping O_CREATE is the other half. A probe has no business leaving a lock file in a
// data dir it only read, and the README's account of what a scripted loop touches is
// only true without it.
func tuiRunning() (running, known bool) {
	path, err := tuiLockPath()
	if err != nil {
		return false, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No interactive atrium has ever run in this data dir. That is an answer,
			// not a failure to get one — and reaching it without O_CREATE is what keeps
			// a probe from leaving a lock file behind in a dir it only read.
			return false, true
		}
		return false, false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, true
		}
		return false, false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, true
}

// lockAttempts and lockRetryDelay bound how long lockExclusive retries. Their product is
// how long a real TUI is willing to wait behind a lock it expects to be free, and how
// long a second TUI takes to be refused.
const (
	lockAttempts   = 20
	lockRetryDelay = 5 * time.Millisecond
)

// lockExclusive takes tui.lock exclusively, retrying briefly while someone else holds it.
//
// Without the retry, tuiRunning's own shared probe can deny a starting TUI its
// single-instance lock: acquireTUILockOrWarn maps EWOULDBLOCK to errTUIAlreadyRunning, so
// a user who launched atrium at the moment an `atrium new` was mid-probe would be told
// atrium was already running for this data directory and refused, naming an instance that
// does not exist. #760 made that likelier by probing on the --wait path too and again at
// each deadline. A shared probe holds the lock for two syscalls, so retrying past it is
// enough; only a lock still held after the whole budget is a real second TUI.
//
// A mirror of internal/handover's lockExclusive, which needs the same thing in the other
// direction. Duplicated rather than shared, as acquireDaemonLock and acquireUpdateLock
// already are — the two differ in what exhaustion means.
func lockExclusive(f *os.File) error {
	var err error
	for attempt := 0; attempt < lockAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(lockRetryDelay)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
	}
	return err
}
