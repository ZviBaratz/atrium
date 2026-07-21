//go:build darwin

package doctor

import "golang.org/x/sys/unix"

// hostMemBytes returns total physical RAM in bytes via sysctl hw.memsize.
func hostMemBytes() (uint64, bool) {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, false
	}
	return n, true
}
