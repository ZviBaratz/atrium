//go:build windows

package handover

// Hold is a no-op on Windows, which has no flock, so nothing records a terminal
// handover there and Held reports "unknown" forever. This mirrors acquireTUILock in
// lock_windows.go, acquireDaemonLock in daemon/daemon_windows.go and
// acquireUpdateLock in internal/update/lock_windows.go — and the exposure is the same
// one they name: Atrium's tmux-based runtime doesn't target Windows, and the build
// exists for completeness.
func Hold(Payload) (release func(), err error) {
	return func() {}, nil
}

// Held cannot answer on Windows: Hold is a no-op there, so a try-acquire would always
// succeed and report "no handover is in progress" even mid-attach. Reporting
// "unknown" keeps callers from printing a confidently wrong warning, which is the
// reason tuiRunning gives for the same choice.
func Held() (held bool, p Payload, known bool) {
	return false, Payload{}, false
}
