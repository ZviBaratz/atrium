package main

import (
	"errors"
	"github.com/ZviBaratz/atrium/config"
	"path/filepath"
)

// tuiLockFilename is the advisory-lock file a running interactive atrium holds for
// its whole lifetime, sitting next to daemon.lock / daemon.pid / update.lock in the
// data dir. It enforces one TUI per data dir: two TUIs sharing a state.json would
// let one's exit-time autoyes daemon snapshot clobber the other's instances and
// non-instance state (issue #230). The kernel frees an flock when its owning process
// dies — cleanly or by crash — so a dead TUI never wedges the next one and no
// stale-lock recovery is needed (unlike a PID file).
const tuiLockFilename = "tui.lock"

// errTUIAlreadyRunning is returned by acquireTUILock when another interactive atrium
// already holds the lock, so RunE can refuse to start a second one. It lives here
// (not in lock_unix.go) so RunE can compare it with errors.Is on every platform.
var errTUIAlreadyRunning = errors.New("another atrium TUI is already running")

// tuiLockPath returns <data dir>/tui.lock. It is kept separate from acquireTUILock so
// the latter takes an explicit path that tests can point at a temp dir — mirroring
// daemonLockPath / acquireDaemonLock in the daemon package.
func tuiLockPath() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tuiLockFilename), nil
}
