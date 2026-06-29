//go:build windows

package main

// acquireTUILock is a no-op on Windows, which has no flock, so interactive atrium
// runs without a single-instance lock and a second TUI is never refused. The
// exposure is small — Atrium's tmux-based runtime doesn't target Windows (the build
// exists for completeness) — and this mirrors acquireDaemonLock in
// daemon/daemon_windows.go and internal/update/lock_windows.go.
func acquireTUILock(path string) (release func(), err error) {
	return func() {}, nil
}
