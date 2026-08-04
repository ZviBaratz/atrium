//go:build linux

package tmux

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSocketPathSurvivesUnlink pins the mechanism the whole orphan scan rests on: a
// bound unix socket keeps its exact path in /proc/net/unix after the file is
// unlinked. That is the only reason a class-(c) orphan — a tmux server whose
// TMUX_TMPDIR root was deleted out from under it — can be identified at all, and it
// is why none of this code has to reconstruct tmux's $TMUX_TMPDIR/tmux-<uid>/<name>
// layout (#547).
//
// Asserted with a plain unix listener rather than a tmux server, so it is hermetic,
// needs no tmux, and runs on every Linux job.
func TestSocketPathSurvivesUnlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.sock")

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	require.Equal(t, path, socketPathFor(os.Getpid(), listeningSockets()),
		"a listening socket must be locatable by pid while its file exists")

	// The incident: the socket's directory tree is removed while the server lives.
	require.NoError(t, os.Remove(path))
	require.NoFileExists(t, path)

	require.Equal(t, path, socketPathFor(os.Getpid(), listeningSockets()),
		"the kernel must still report the bound path after the file is unlinked — "+
			"without this, an unreachable orphan cannot be named at all")
}

// TestListeningSocketsExcludesConnectedPeers guards the Flags filter. /proc/net/unix
// lists every unix socket, and a *connected* endpoint of a path-bound socket can
// carry that same path — so without the SO_ACCEPTCON (00010000) filter, a client's
// pid would resolve to the server's socket path and be reported as owning it.
func TestListeningSocketsExcludesConnectedPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	accepted, err := ln.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })

	// Exactly one row survives the filter: the listener. The dialer and the accepted
	// endpoint are connected, not listening.
	var seen int
	for _, p := range listeningSockets() {
		if p == path {
			seen++
		}
	}
	require.Equal(t, 1, seen, "only the listening socket may be reported for %q", path)
}

// TestParseStatHandlesACommContainingASpace is the pure-function half of the
// start-time guard.
//
// /proc/<pid>/stat's second field is the comm in parentheses, and a tmux server's is
// "(tmux: server)". Its embedded space shifts every later field, so the naive
// strings.Fields(raw)[21] reads the wrong column — measured on a live tmux server it
// returns 0, which then formats to the machine's BOOT time. That is a
// plausible-looking wrong answer rather than a visible failure: it would make every
// orphan report an age of "up since boot", and it would defeat the PID-reuse guard,
// which compares a captured start time against a re-read one. Parse after the LAST
// ") " instead.
//
// PPid rides on the same parse and is asserted here too: it is what attributes a
// live agent to the server the reaper is about to kill, so a column off by one would
// name the wrong children in the confirmation prompt.
func TestParseStatHandlesACommContainingASpace(t *testing.T) {
	// Fields are 1-indexed: 3 is state, 4 is ppid, 22 is starttime. These lines carry
	// 1..N in the numeric columns after the state, so with state as field 3 the ppid
	// column reads 1 and the starttime column reads 19.
	const tail = " S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22"

	for _, tc := range []struct {
		name string
		raw  string
		want procStat
		ok   bool
	}{
		{name: "plain comm", raw: "1234 (sleep)" + tail,
			want: procStat{Comm: "sleep", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a space", raw: "1234 (tmux: server)" + tail,
			want: procStat{Comm: "tmux: server", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a close paren and a space", raw: "1234 (f) g)" + tail,
			want: procStat{Comm: "f) g", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a trailing paren", raw: "1234 (weird))" + tail,
			want: procStat{Comm: "weird)", PPid: 1, StartTicks: 19}, ok: true},
		{name: "no comm terminator", raw: "1234 (unterminated" + tail, ok: false},
		{name: "truncated after comm", raw: "1234 (sleep) S 1 2 3", ok: false},
		{name: "empty", raw: "", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStat(tc.raw)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

// TestStartTimeMatchesAProcessJustStarted is the positive control the synthetic
// lines above cannot be: a real /proc/<pid>/stat, written by the kernel, for a
// process whose start time this test knows to the second.
//
// The comms are chosen to be the shapes that break a naive parse — the tmux server's
// own, and one carrying the ") " the parser keys on. A parser that read the wrong
// column would land on boot time, which is outside the window by however long the
// host has been up.
func TestStartTimeMatchesAProcessJustStarted(t *testing.T) {
	for _, comm := range []string{"tmux: server", "f) g", "sleep"} {
		t.Run(comm, func(t *testing.T) {
			before := time.Now().Add(-2 * time.Second)
			pid := spawnNamed(t, comm)
			after := time.Now().Add(2 * time.Second)

			got, ok := procStartTime(pid)
			require.True(t, ok, "procStartTime(%d) failed for comm %q", pid, comm)
			require.WithinRange(t, got, before, after,
				"start time for a process spawned during this test must land in the window it was spawned in; "+
					"a misparse reports the machine's boot time instead")
		})
	}
}

// TestStartTimeOfADeadProcessFails: /proc/<pid> is gone, so there is nothing to
// parse. This is the branch the PID-reuse guard turns into "skip", so it must not
// quietly answer with a zero time.
func TestStartTimeOfADeadProcessFails(t *testing.T) {
	pid := spawnNamed(t, "sleep")
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	_, _ = proc.Wait()

	_, ok := procStartTime(pid)
	require.False(t, ok, "a reaped pid must not yield a start time")
}

// TestScanServersOnThisHostReportsOnlyOwnedSockets runs the real scan against the
// real /proc. It asserts invariants rather than a fleet state, since what is running
// on the host is not the test's to know: whatever comes back must have passed the
// ownership predicate, and must not be this test binary.
//
// It is the guard that the platform inventory and the portable classifier are wired
// to each other at all — the assembly tests stub the seams, so nothing else here
// would notice inventoryCandidates returning garbage.
func TestScanServersOnThisHostReportsOnlyOwnedSockets(t *testing.T) {
	servers, supported := ScanServers(t.Context())
	require.True(t, supported, "Linux must support the scan")

	for _, s := range servers {
		require.True(t, ownsSocketName(s.Socket),
			"pid %d was reported with socket %q, which is not Atrium's", s.PID, s.Socket)
		require.NotEqual(t, os.Getpid(), s.PID)
		require.False(t, s.Started.IsZero(), "pid %d has no start time to guard a signal with", s.PID)
		if s.SocketPath != "" {
			require.Equal(t, s.Socket, filepath.Base(s.SocketPath))
		}
	}
}

// TestScanServersNeverReportsArgv is the secret-hygiene guard at the scan's own
// boundary: no field of the result may carry a token, whatever the servers on this
// host were launched with. The report and the reap prompt render only these fields,
// so proving it here covers both.
func TestScanServersNeverReportsArgv(t *testing.T) {
	servers, _ := ScanServers(t.Context())
	for _, s := range servers {
		rendered := fmt.Sprintf("%+v", s)
		for _, secret := range []string{"GH_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN", "gho_", "ghp_", "github_pat_"} {
			require.NotContains(t, rendered, secret,
				"a scan result carried %q; the socket name must come from the socket path, never from argv", secret)
		}
	}
}

// spawnNamed starts a long-lived child process whose /proc/<pid>/comm is comm, by
// copying the `sleep` binary to a file of that name. comm is what the kernel derives
// from the executable's base name, so this produces a genuine stat line with the
// awkward characters in it — not a synthetic one.
//
// The teardown is armed before the process starts, so a Start that half-succeeds
// still has something to reap it. A test that leaks a process is the failure mode
// this whole package is about.
func spawnNamed(t *testing.T, comm string) int {
	t.Helper()

	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err, "sleep is needed to spawn a process with a controlled comm")

	src, err := os.Open(sleepPath)
	require.NoError(t, err)
	defer func() { _ = src.Close() }()

	dst := filepath.Join(t.TempDir(), comm)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(out, src)
	require.NoError(t, err)
	require.NoError(t, out.Close())

	cmd := exec.CommandContext(t.Context(), dst, "60")
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	require.NoError(t, cmd.Start())
	return cmd.Process.Pid
}
