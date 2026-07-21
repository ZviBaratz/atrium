//go:build linux

package doctor

import "golang.org/x/sys/unix"

// hostMemBytes returns total physical RAM in bytes via sysinfo(2). Totalram is
// expressed in units of si.Unit bytes, so the two are multiplied.
func hostMemBytes() (uint64, bool) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, false
	}
	return si.Totalram * uint64(si.Unit), true
}
