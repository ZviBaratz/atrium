//go:build !unix

package discovery

// deviceID has no portable implementation off unix; the mount-boundary guard
// is simply skipped there.
func deviceID(string) (uint64, bool) { return 0, false }
