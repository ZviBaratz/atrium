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

// tuiRunning cannot answer on Windows: acquireTUILock is a no-op there, so a
// try-acquire always succeeds and would report "no TUI is running" even while
// one is. Reporting "unknown" keeps callers from printing a confidently wrong
// warning on a platform that has no single-instance lock to consult.
func tuiRunning() (running, known bool) {
	return false, false
}
