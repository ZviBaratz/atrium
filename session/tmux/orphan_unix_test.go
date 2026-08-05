//go:build !windows

package tmux

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStaleSocketsInReportsOnlyUnansweredAtriumSockets covers the class-(a) list:
// socket files tmux left behind when their servers died.
//
// The live socket sits in the same directory, so "a file is here" can never be the
// test — a socket that answers belongs to a running server, and reporting it would
// put the live fleet on a list headed "remove these". Everything not positively
// proven dead stays off the list.
func TestStaleSocketsInReportsOnlyUnansweredAtriumSockets(t *testing.T) {
	dir := staleSocketFixture(t)

	stubSocketOwner(t, func(_ context.Context, path string) (int, bool) {
		switch filepath.Base(path) {
		case "atrium":
			return 4242, true // the live server answers here
		case "atrium-unprobeable":
			return 0, false // tmux could not run
		}
		return 0, true // probed, nothing there
	})

	stale, gaps := staleSocketsIn(t.Context(), dir)
	require.Len(t, stale, 1)
	require.Equal(t, filepath.Join(dir, "atrium-precheck-9-0"), stale[0].Path,
		"only a socket positively proven to have no server behind it may be listed")
	require.False(t, stale[0].ModTime.IsZero())

	// The unprobeable file is the reason this count exists: it is off the list above,
	// and without saying so the caller cannot tell this directory from one where every
	// file was read and judged healthy.
	require.Equal(t, 1, gaps.Unprobed,
		"a file whose probe could not run must be counted, not silently dropped")
	require.False(t, gaps.DirUnread, "the directory itself listed fine")
}

// staleSocketFixture builds a directory holding one socket of every kind this filter
// has to tell apart, and returns its path.
//
// A short root under /tmp rather than t.TempDir(): a unix socket path has to fit
// sockaddr_un's sun_path (104 bytes on darwin, 108 on linux), and t.TempDir() names the
// directory after the test — which on macOS sits under a long /var/folders/… prefix and
// overflows it. Same budget internal/testutil documents for the tmux socket root, and
// the reason this file is not on Windows, which has no /tmp.
func staleSocketFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "atr")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for _, name := range []string{"atrium", "atrium-precheck-9-0", "atrium-unprobeable", "default"} {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "unix", filepath.Join(dir, name))
		require.NoError(t, err)
		// Keep the file after Close; a tmux server's socket outlives its server, and
		// that is exactly the artifact under test.
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		require.NoError(t, ln.Close())
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atrium-notasocket"), nil, 0o600))
	return dir
}

// TestStaleSocketsInCountsEveryUnprobeableFile is route 3 of #598, the reachable one:
// with tmux off PATH — or the scan's budget spent — no file can be classified, every
// one is skipped, and the pass returns the same empty list a genuinely clean directory
// does. The section then reported "none in <dir>" having probed nothing.
//
// The count is asserted exactly, not merely as non-zero. Only a file that reached a
// probe may be counted: `default` belongs to another tool and `atrium-notasocket` is a
// regular file, and counting either would report a gap about a directory that is in
// fact fully understood.
func TestStaleSocketsInCountsEveryUnprobeableFile(t *testing.T) {
	dir := staleSocketFixture(t)

	stubSocketOwner(t, func(context.Context, string) (int, bool) {
		return 0, false // tmux is not runnable: nothing here can be classified
	})

	stale, gaps := staleSocketsIn(t.Context(), dir)
	require.Empty(t, stale, "nothing was proven dead, so nothing may be listed for removal")
	require.Equal(t, 3, gaps.Unprobed,
		"every Atrium socket that reached a probe must be counted, and only those: a "+
			"foreign socket name or a regular file cannot make this directory unknown")
	require.False(t, gaps.DirUnread)
	require.True(t, gaps.Any(), "an empty list this pass never established must not read as clean")
}

// TestStaleSocketsInReportsAnUnlistableDirectory is route 2: os.ReadDir failed, so the
// pass knows nothing about the directory's contents. Returning a bare nil made that
// indistinguishable from having read it and found it clean.
func TestStaleSocketsInReportsAnUnlistableDirectory(t *testing.T) {
	// A path under a real temp root that does not exist, so the failure is ReadDir's
	// and not a permission quirk of the host.
	dir := filepath.Join(staleSocketFixture(t), "no-such-subdir")

	stale, gaps := staleSocketsIn(t.Context(), dir)
	require.Nil(t, stale)
	require.True(t, gaps.DirUnread,
		"a directory that could not be listed must be reported, not rendered as one holding no stale sockets")
	require.Zero(t, gaps.Unprobed, "nothing was listed, so nothing reached a probe to be counted")
}
