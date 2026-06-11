//go:build !windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ZviBaratz/atrium/config"
)

// lockFileName sits next to the update cache in the data dir.
const lockFileName = "update.lock"

// acquireUpdateLock takes an exclusive cross-process lock for the duration of
// a binary swap (a TUI auto-install racing `atrium update`, or two TUIs
// starting together). flock is advisory, but every applier is this code; the
// kernel drops the lock with the owning process, so a crash mid-update never
// wedges future updates. The caller must invoke the returned release func.
func acquireUpdateLock() (release func(), err error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another update is already in progress")
	}
	// Closing the descriptor releases the flock.
	return func() { _ = f.Close() }, nil
}
