//go:build windows

package flock

import (
	"errors"
	"os"
	"time"
)

// LockExclusive cannot lock on Windows, which has no flock. Every caller of this package
// is already inside a !windows file, so this exists to keep the package buildable rather
// than to be reached — mirroring the stubs in lock_windows.go, daemon/daemon_windows.go,
// internal/update/lock_windows.go and internal/handover/lock_windows.go.
func LockExclusive(*os.File, int, time.Duration) error {
	return errors.New("advisory file locking is not supported on this platform")
}
