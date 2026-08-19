//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"

	"github.com/ZviBaratz/atrium/internal/flock"
)

// acquireTUILock takes the exclusive single-instance lock at path and returns a
// release func the running TUI holds for its entire lifetime; closing the descriptor
// drops the flock (and so does process death). If another interactive atrium already
// still holds it once flock.LockExclusive's budget is spent, it returns
// errTUIAlreadyRunning so RunE can refuse to start a duplicate. The retry is what keeps a
// passing tuiRunning probe from looking like one; it shrinks that window rather than
// closing it, since a shared holder outlasting the budget lands in this same arm and is
// reported as a second TUI. Refusing is the safe side of that (issue #230), and #771
// tracks telling the two apart.
// Mirrors acquireDaemonLock (daemon/daemon_unix.go) and acquireUpdateLock
// (internal/update/lock_unix.go) in shape, but no longer in behaviour: neither of those
// retries, and the daemon's own probe is still exclusive, so #772 tracks giving them this.
func acquireTUILock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flock.LockExclusive(f, flock.Attempts, flock.Delay); err != nil {
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
// known is false when the question could not be answered at all — an unresolvable data
// dir, or a lock that cannot be opened — so a caller can stay silent rather than assert
// something wrong. A data dir with no lock file in it is NOT one of those: it answers
// "no TUI", for the reason the ErrNotExist arm below gives.
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
