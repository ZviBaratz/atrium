package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
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

// acquireTUILockOrWarn applies the single-instance policy for a top-level
// command: it resolves and try-acquires tui.lock, returning a release func
// (never nil on a nil error) the caller must defer. Only a lock held by another
// atrium refuses, with a user-facing error ending in refuseHint. Failing to
// resolve or open the lock is deliberately non-fatal — warn and proceed with a
// no-op release, matching RunDaemon — since a data dir broken enough to refuse
// the flock will fail the command's real work anyway. verb names the caller's
// action in those warnings ("running", "resetting").
func acquireTUILockOrWarn(verb, refuseHint string) (release func(), err error) {
	lockPath, err := tuiLockPath()
	if err != nil {
		log.WarningLog.Printf("could not resolve TUI lock path: %v; %s without single-instance lock", err, verb)
		return func() {}, nil
	}
	release, err = acquireTUILock(lockPath)
	if err != nil {
		if errors.Is(err, errTUIAlreadyRunning) {
			return nil, fmt.Errorf("atrium is already running for this data directory (%s); %s", filepath.Dir(lockPath), refuseHint)
		}
		log.WarningLog.Printf("could not acquire TUI lock %s: %v; %s without it", lockPath, err, verb)
		return func() {}, nil
	}
	return release, nil
}
