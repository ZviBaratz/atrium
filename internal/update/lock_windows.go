//go:build windows

package update

// acquireUpdateLock is a no-op on Windows, which has no flock. The exposure is
// small there: go-update already special-cases the platform (a running binary
// can't be renamed over, and the old file is hidden rather than removed), and
// Atrium's tmux-based runtime doesn't target Windows — the build exists for
// completeness.
func acquireUpdateLock() (release func(), err error) {
	return func() {}, nil
}
