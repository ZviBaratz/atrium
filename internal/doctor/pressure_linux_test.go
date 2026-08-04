//go:build linux

package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFixture writes content to a temp file and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestZramSwapBytesFromParsesProcSwaps covers the shapes the live file takes. The
// fixture is the real /proc/swaps from the host in #594 — a zram area plus a disk
// swapfile, which is the arrangement that makes "swap free" misleading.
func TestZramSwapBytesFromParsesProcSwaps(t *testing.T) {
	const mixed = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/swap.img                               file\t\t8388604\t\t3775556\t\t-1\n" +
		"/dev/zram0                              partition\t15901692\t10829852\t100\n"

	got, ok := zramSwapBytesFrom(writeFixture(t, "swaps", mixed))
	require.True(t, ok)
	require.Equal(t, uint64(15901692)*1024, got,
		"only the zram area counts, and /proc/swaps sizes are KiB")

	// Two zram devices sum; the header never contributes.
	const twoZram = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/dev/zram0                              partition\t100\t\t0\t\t100\n" +
		"/dev/zram1                              partition\t50\t\t0\t\t100\n"
	got, ok = zramSwapBytesFrom(writeFixture(t, "two", twoZram))
	require.True(t, ok)
	require.Equal(t, uint64(150)*1024, got)

	// A disk-only host reads successfully and reports none. The renderer relies on
	// this to tell "there is no zram" from "the file could not be read".
	const diskOnly = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/swap.img                               file\t\t8388604\t\t0\t\t-1\n"
	got, ok = zramSwapBytesFrom(writeFixture(t, "disk", diskOnly))
	require.True(t, ok)
	require.Zero(t, got)

	// A swapless host has a header and nothing else.
	got, ok = zramSwapBytesFrom(writeFixture(t, "empty",
		"Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"))
	require.True(t, ok)
	require.Zero(t, got)

	// An unreadable file is unknown, not zero.
	_, ok = zramSwapBytesFrom(filepath.Join(t.TempDir(), "absent"))
	require.False(t, ok)
}

// TestZramSwapBytesFromIgnoresALookalikeDevice: the prefix must not match a device that
// merely starts the same way, since counting a disk area as RAM-backed would
// under-report real headroom and warn on a healthy host.
func TestZramSwapBytesFromIgnoresALookalikeDevice(t *testing.T) {
	const lookalike = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/dev/zram0                              partition\t100\t\t0\t\t100\n" +
		"/dev/sda2                               partition\t900\t\t0\t\t-1\n" +
		"/var/zram-not-a-device                  file\t\t500\t\t0\t\t-2\n"
	got, ok := zramSwapBytesFrom(writeFixture(t, "swaps", lookalike))
	require.True(t, ok)
	require.Equal(t, uint64(100)*1024, got)
}

// TestMeminfoKBParsesTheKey checks the field the value actually lives in. A parser that
// took the wrong column would return the unit or a neighbouring key's number, and both
// are plausible-looking wrong answers rather than obvious failures.
func TestMeminfoKBParsesTheKey(t *testing.T) {
	const meminfo = "MemTotal:       31803988 kB\n" +
		"MemFree:         4892552 kB\n" +
		"MemAvailable:    8542960 kB\n" +
		"Buffers:          123456 kB\n"
	path := writeFixture(t, "meminfo", meminfo)

	got, ok := meminfoKB(path, "MemAvailable")
	require.True(t, ok)
	require.Equal(t, uint64(8542960), got)

	got, ok = meminfoKB(path, "MemTotal")
	require.True(t, ok)
	require.Equal(t, uint64(31803988), got)

	// A key that is a prefix of another must not match it: "Mem" is not "MemTotal".
	_, ok = meminfoKB(path, "Mem")
	require.False(t, ok)

	_, ok = meminfoKB(path, "Committed_AS")
	require.False(t, ok, "an absent key is unknown")

	_, ok = meminfoKB(filepath.Join(t.TempDir(), "absent"), "MemAvailable")
	require.False(t, ok)
}

// TestMeminfoKBRejectsAMalformedValue: a key present with an unparseable value is
// unknown, not zero. MemAvailable of 0 would render as a host with no memory left.
func TestMeminfoKBRejectsAMalformedValue(t *testing.T) {
	_, ok := meminfoKB(writeFixture(t, "bad", "MemAvailable:    lots kB\n"), "MemAvailable")
	require.False(t, ok)

	_, ok = meminfoKB(writeFixture(t, "empty", "MemAvailable:\n"), "MemAvailable")
	require.False(t, ok)
}

// TestStatfsPathClassifiesTmpfs pins the magic-number comparison against a directory
// the kernel really does back with tmpfs. /dev/shm is tmpfs on every Linux host; the
// test skips rather than fails if this one is unusual, since the assertion is about the
// classifier, not about the host's mount table.
func TestStatfsPathClassifiesTmpfs(t *testing.T) {
	shm, ok := statfsPath("/dev/shm")
	if !ok {
		t.Skip("/dev/shm not present")
	}
	require.True(t, shm.Tmpfs, "/dev/shm is tmpfs; TMPFS_MAGIC comparison is wrong")

	// A negative control, so the test cannot pass by always answering true. It has to
	// be a filesystem that *cannot* be tmpfs: procfs has its own magic and always
	// exists on Linux. The obvious candidate — the test binary's own directory — is
	// not safe, because `go test` builds under $TMPDIR and on the host in #594 that
	// is the 16 GiB tmpfs this whole section exists to report.
	proc, ok := statfsPath("/proc")
	if !ok {
		t.Skip("/proc not present")
	}
	require.False(t, proc.Tmpfs, "procfs is not tmpfs; the TMPFS_MAGIC comparison is too loose")
	require.NotEqual(t, shm.Dev, proc.Dev, "distinct filesystems must have distinct device ids")
}
