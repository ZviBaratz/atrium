//go:build !linux

package doctor

// pressureSupported is false where the readings that carry the section cannot be
// taken, and RenderPressure says so rather than rendering an empty or half-true
// report — the same gate RenderOOM and RenderOrphans use.
//
// Not a permanent verdict on other platforms: unix.Statfs and its Fstypename exist on
// darwin, so filesystem headroom and tmpfs detection could be added there. Swap needs
// a different source (sysctl vm.swapusage), and a section where only some rows can
// answer is a bigger change than adding the rows. Whole-section unavailability is the
// honest first landing (#594).
const pressureSupported = false

// hostSwap reports swap as unknown; the doctor renders "unavailable" rather than failing.
func hostSwap() (total, free uint64, ok bool) {
	return 0, 0, false
}

// availRAMBytes reports available memory as unknown.
func availRAMBytes() (uint64, bool) {
	return 0, false
}

// zramSwapBytes reports zram-backed swap as unknown. zram is a Linux facility, so
// there is nothing here to detect even where the file could be read.
func zramSwapBytes() (uint64, bool) {
	return 0, false
}

// statfsPath reports filesystem headroom as unknown.
func statfsPath(string) (fsStat, bool) {
	return fsStat{}, false
}
