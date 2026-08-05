//go:build linux

package doctor

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// pressureSupported gates the whole section: the readings that carry it — swap
// headroom and tmpfs classification — are read here through Linux interfaces.
const pressureSupported = true

// The two /proc files this section reads, named to keep the paths out of the parsing
// below. Not variables: the parsers are what the tests exercise, over a path they pass
// in, so nothing needs to repoint these.
const (
	procMeminfo = "/proc/meminfo"
	procSwaps   = "/proc/swaps"
)

// hostSwap returns total and free swap in bytes via sysinfo(2), the same call
// capacity_linux.go makes for total RAM — which already returns both of these and
// discards them. Totalswap and Freeswap are expressed in units of si.Unit bytes, so
// each is multiplied, exactly as Totalram is.
func hostSwap() (total, free uint64, ok bool) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, 0, false
	}
	unit := uint64(si.Unit)
	return si.Totalswap * unit, si.Freeswap * unit, true
}

// availRAMBytes returns MemAvailable in bytes.
//
// This is a /proc read rather than sysinfo's Freeram because they are different
// quantities and only one of them is meaningful: Freeram is MemFree, which counts
// reclaimable page cache as used, while MemAvailable is the kernel's own estimate of
// what a new allocation could actually get. sysinfo cannot report MemAvailable at all.
func availRAMBytes() (uint64, bool) {
	kb, ok := meminfoKB(procMeminfo, "MemAvailable")
	if !ok {
		return 0, false
	}
	return kb * 1024, true
}

// meminfoKB pulls one key's kB value out of a /proc/meminfo-shaped file, whose lines
// read "MemAvailable:    8542960 kB".
func meminfoKB(path, key string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(b)) {
		name, rest, found := strings.Cut(line, ":")
		if !found || name != key {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// zramSwapBytes returns how much of the configured swap is backed by zram.
//
// zram is a compressed block device that lives in RAM, so swap on it is not the relief
// valve that swap on a disk is: a tmpfs page evicted there is compressed, not freed.
// A host with mostly-zram swap can therefore run out of memory while reporting swap
// free, which is the state this whole section exists to make visible.
//
// Identified by device path — /proc/swaps names each area, and a zram area is
// /dev/zram<N>. ok is false only when the file cannot be read; a host with no zram
// legitimately returns (0, true), and the caller must not render a note for it.
func zramSwapBytes() (uint64, bool) {
	return zramSwapBytesFrom(procSwaps)
}

// zramSwapBytesFrom is zramSwapBytes over an explicit path, split out so the parsing is
// testable against a fixture. The shapes that matter are the header line, which has no
// numeric Size, and a mixed host with both a zram area and a disk swapfile.
func zramSwapBytesFrom(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var total uint64
	for line := range strings.Lines(string(b)) {
		fields := strings.Fields(line)
		// Filename Type Size Used Priority — the header line has no numeric Size and
		// falls out at ParseUint.
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "/dev/zram") {
			continue
		}
		kb, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			continue
		}
		total += kb * 1024
	}
	return total, true
}

// statfsPath reads one path's filesystem headroom.
//
// AvailBytes is f_bavail (what an unprivileged write can reach), not f_bfree, so a
// root-reserved disk is not reported as having headroom the user cannot use. Dev comes
// from a separate stat(2) rather than from statfs's f_fsid, which Linux reports as zero
// for every tmpfs — keying "same filesystem" on that would merge /tmp with /dev/shm.
func statfsPath(path string) (fsStat, bool) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return fsStat{}, false
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return fsStat{}, false
	}
	// A non-positive block size makes every byte figure below meaningless, and the
	// signed-to-unsigned widening that follows would turn it into an enormous one.
	// No reading at all beats a fabricated one.
	if fs.Bsize <= 0 {
		return fsStat{}, false
	}
	bsize := uint64(fs.Bsize)
	return fsStat{
		TotalBytes:  fs.Blocks * bsize,
		AvailBytes:  fs.Bavail * bsize,
		TotalInodes: fs.Files,
		FreeInodes:  fs.Ffree,
		// Compared against the untyped constant with no cast on either side, which is
		// what makes this build on every arch: Statfs_t.Type is int32 on 386 and arm
		// and int64 on amd64, and an untyped constant adopts whichever it meets.
		Tmpfs: fs.Type == unix.TMPFS_MAGIC,
		Dev:   st.Dev,
	}, true
}
