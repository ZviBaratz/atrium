//go:build !windows

package flock

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// LockExclusive takes an exclusive advisory lock on f, retrying while another process
// holds a conflicting one. It returns nil once the lock is held, the last EWOULDBLOCK
// if the budget ran out, or any other flock error immediately — a failure that is not
// contention will not become a success by waiting.
//
// attempts < 1 is treated as one attempt. Returning nil without holding the lock would
// convert a single-instance guard into a silent no-op, so there is no input for which
// this reports success on a lock it did not take.
//
// Non-blocking by construction: it never issues a blocking LOCK_EX, because the callers
// are an interactive startup and a terminal handover, and neither may wait indefinitely
// on a peer that has wedged.
func LockExclusive(f *os.File, attempts int, delay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
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
