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
	// A short root under /tmp rather than t.TempDir(): a unix socket path has to fit
	// sockaddr_un's sun_path (104 bytes on darwin, 108 on linux), and t.TempDir()
	// names the directory after the test — which on macOS sits under a long
	// /var/folders/… prefix and overflows it. Same budget internal/testutil documents
	// for the tmux socket root, and the reason this file is not on Windows, which has
	// no /tmp.
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

	stubSocketOwner(t, func(_ context.Context, path string) (int, bool) {
		switch filepath.Base(path) {
		case "atrium":
			return 4242, true // the live server answers here
		case "atrium-unprobeable":
			return 0, false // tmux could not run
		}
		return 0, true // probed, nothing there
	})

	stale := staleSocketsIn(t.Context(), dir)
	require.Len(t, stale, 1)
	require.Equal(t, filepath.Join(dir, "atrium-precheck-9-0"), stale[0].Path,
		"only a socket positively proven to have no server behind it may be listed")
	require.False(t, stale[0].ModTime.IsZero())
}
