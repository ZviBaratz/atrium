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
// succeed and report "no handover is in progress" even mid-attach. Reporting "unknown" is
// the same choice tuiRunning makes on Windows, for the same reason.
//
// Which is why nothing production reaches this: drainState asks tuiRunning first and stops
// at drainUnknown, so on Windows the answer is settled before this is called. It exists to
// keep the package compiling and to keep that the reason rather than a build error.
func Held() (held bool, p Payload, known bool) {
	return false, Payload{}, false
}
