//go:build unix

package discovery

import (
	"os"
	"syscall"
)

// deviceID returns the filesystem device id of path, letting the walk stop at
// mount boundaries. Best-effort: any failure disables the guard for that path.
func deviceID(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// The conversion is a no-op on linux (Dev is already uint64) but required
	// on darwin and the BSDs, where Dev is a smaller signed type.
	return uint64(st.Dev), true //nolint:unconvert
}
