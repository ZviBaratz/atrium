//go:build !linux && !darwin

package doctor

// hostMemBytes reports RAM as unknown on platforms without a supported reader; the
// doctor renders "unknown" rather than failing.
func hostMemBytes() (uint64, bool) {
	return 0, false
}
