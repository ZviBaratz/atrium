//go:build linux

package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/creack/pty"
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

	gotPath, _ := serverSocket(os.Getpid(), mustPathBoundSockets(t))
	require.Equal(t, path, gotPath,
		"a listening socket must be locatable by pid while its file exists")

	// The incident: the socket's directory tree is removed while the server lives.
	require.NoError(t, os.Remove(path))
	require.NoFileExists(t, path)

	gotPath, _ = serverSocket(os.Getpid(), mustPathBoundSockets(t))
	require.Equal(t, path, gotPath,
		"the kernel must still report the bound path after the file is unlinked — "+
			"without this, an unreachable orphan cannot be named at all")
}

// TestPathBoundSocketsSeparatesAListenerFromWhatItAccepted pins which side of a
// connection carries the duplicate path, which is what both halves of serverSocket
// stand on: the listener's row names the server's socket, and the rows that are *not*
// listening on that same path are the connections it accepted — one per client (#614).
//
// The comment on listeningFlags used to say the duplicate belonged to the *client*,
// and that a missing filter would let a client's pid be reported as owning the
// server's socket. Measured on the development host, it is the other way round: all 13
// rows for /tmp/tmux-1000/atrium resolved to the server pid and none of the twelve
// `tmux: client` processes held one. A stream client's socket is never bound; the
// socket accept() returns inherits the listener's address. The last assertion here is
// that claim, stated so a regression cannot restore the old reading: with one dialer
// per accepted connection in this same process, the path is carried N+1 times, not
// 2N+1.
func TestPathBoundSocketsSeparatesAListenerFromWhatItAccepted(t *testing.T) {
	const conns = 3
	path := filepath.Join(t.TempDir(), "probe.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	for range conns {
		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "unix", path)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		accepted, err := ln.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = accepted.Close() })
	}

	var listening, connected int
	for _, sock := range mustPathBoundSockets(t) {
		if sock.Path != path {
			continue
		}
		if sock.Listening {
			listening++
			continue
		}
		connected++
	}
	require.Equal(t, 1, listening, "exactly one socket is listening on %q", path)
	require.Equal(t, conns, connected,
		"the accepted sockets carry the listener's path, one per connection — and the dialers, "+
			"which are unbound, carry none; %d rows would mean both sides do", 2*conns)
}

// mustPathBoundSockets reads the socket table and fails the test unless the read itself
// succeeded.
//
// The ok result is load-bearing: pathBoundSockets used to return a nil map on a read
// error, which every caller then treated as "this host has no listening sockets" — so
// an unreadable /proc/net/unix made the whole scan report a clean host. A test that
// discarded ok would pass identically against that bug and against the fix.
func mustPathBoundSockets(t *testing.T) map[uint64]unixSocket {
	t.Helper()
	socks, ok := pathBoundSockets()
	require.True(t, ok, "/proc/net/unix must be readable for this assertion to mean anything")
	return socks
}

// TestServerSocketCountsTheClientsConnectedToIt drives the fact `reap --kill --yes`
// now refuses on, in the state it has to be right in: a server whose socket file is
// gone (#614).
//
// This is the discriminator the #614 inventory previously had nothing to offer. A live
// fleet whose socket file was deleted and a #547 orphan are both alive, both hold
// agents, and both answer nothing when probed by path; the difference is that the fleet
// still has clients on it, and the orphan's died with the run that made it. The count
// has to survive the unlink to be usable, since that is the only state either can be in
// — hence the second half.
//
// A connection to a *different* path in the same process is included so the count is
// shown to be per-socket rather than "how many connections does this pid hold".
func TestServerSocketCountsTheClientsConnectedToIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	other := filepath.Join(dir, "other.sock")
	otherLn, err := lc.Listen(t.Context(), "unix", other)
	require.NoError(t, err)
	t.Cleanup(func() { _ = otherLn.Close() })

	// Narrowed like every other call here, and for the reason tableFor gives: asked about the
	// whole table, this would be answered by whichever listener the fd walk reached first —
	// including one another test opened, whose own connections would then fail this.
	require.Zero(t, clientsOn(t, path), "a listener nothing has connected to holds no clients")

	dial := func(to string) {
		t.Helper()
		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "unix", to)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
	}
	dial(other)
	accepted, err := otherLn.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })

	const want = 2
	for range want {
		dial(path)
		accepted, err := ln.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = accepted.Close() })
	}

	// Asked about this listener by path, so the assertion is not at the mercy of which
	// of the two fds the walk reaches first.
	require.Equal(t, want, clientsOn(t, path),
		"every client connected to %q must be counted, and the one on the other socket must not", path)

	// ---- the #614 state: the socket file goes, the server and its clients do not ----
	require.NoError(t, os.Remove(path))
	require.NoFileExists(t, path)
	require.Equal(t, want, clientsOn(t, path),
		"the clients already on a server survive the unlink of its socket file — that is what "+
			"tells a live fleet from an orphan, and it is the only state either can be found in")
}

// tableFor reads the socket table and keeps only the rows naming path.
//
// serverSocket answers about the first listener it happens to reach in the fd walk, and
// the test binary holds listeners this test did not create. Narrowing the table is what
// makes the answer be about the socket the caller means. Only this process can appear in
// the result: path is under the test's own t.TempDir(), so no other process is bound to it.
func tableFor(t *testing.T, path string) map[uint64]unixSocket {
	t.Helper()
	only := map[uint64]unixSocket{}
	for inode, sock := range mustPathBoundSockets(t) {
		if sock.Path == path {
			only[inode] = sock
		}
	}
	return only
}

// clientsOn reports how many clients this process holds on the listener bound to path,
// checking the path serverSocket answered with so a miscount cannot pass as a count.
func clientsOn(t *testing.T, path string) int {
	t.Helper()
	got, clients := serverSocket(os.Getpid(), tableFor(t, path))
	require.Equal(t, path, got)
	return clients
}

// TestCandidatesInCarriesTheClientCount is the wiring assertion: the count reaches a
// candidate from the code that builds one, not only from serverSocket in isolation.
//
// It is the same shape as TestCandidatesInReportsAnUnreadableSocketTable and exists for
// the same reason — an assignment inside candidatesIn that no test drives is one a
// deletion leaves green (#593). The process it describes is this test binary, with a comm
// that passes the tmux prefilter, so the fd walk reads a real /proc entry.
func TestCandidatesInCarriesTheClientCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	const want = 2
	for range want {
		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "unix", path)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		accepted, err := ln.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = accepted.Close() })
	}

	uid := uint32(os.Getuid())
	self := os.Getpid()
	procs := map[int]procEntry{
		self: {stat: procStat{Comm: "tmux: server", State: "S", PPid: 1, StartTicks: 100}, uid: uid},
	}
	narrowed := tableFor(t, path)
	cands, gaps := candidatesIn(procs, uid, func() (map[uint64]unixSocket, bool) { return narrowed, true })
	require.False(t, gaps.IncompleteInventory(), "nothing about this fixture is unseen: %+v", gaps)
	require.Len(t, cands, 1)
	require.Equal(t, path, cands[0].SocketPath)
	require.Equal(t, want, cands[0].ConnectedClients,
		"candidatesIn must carry the client count, not just the path")
}

// TestCandidatesInReportsAnUnreadableSocketTable is the other half of what
// mustPathBoundSockets above only asserts as a premise: that the ok result is acted on.
//
// The bug was that an unreadable /proc/net/unix rendered as a clean host, and the fix
// is one assignment — gaps.SocketTableUnread = !ok. Nothing proved that assignment ran:
// every other test for this flag builds the ScanGaps struct itself, so deleting the
// line left the suite green. It cannot be driven against the real host either, since it
// needs a candidate process to exist *and* the table read to fail at the same moment,
// hence the injected table.
//
// The second case is the reason the flag is not simply set at the top of the scan: with
// no candidate, the table was never needed, and "no tmux process here" is a complete
// answer rather than a blind one.
func TestCandidatesInReportsAnUnreadableSocketTable(t *testing.T) {
	unreadable := func() (map[uint64]unixSocket, bool) { return nil, false }
	// One own-uid process whose comm passes the tmux prefilter — the minimum that makes
	// the socket table matter at all.
	uid := uint32(os.Getuid())
	server := map[int]procEntry{
		4242: {stat: procStat{Comm: "tmux: server", State: "S", PPid: 1, StartTicks: 100}, uid: uid},
	}

	cands, gaps := candidatesIn(server, uid, unreadable)
	require.True(t, gaps.SocketTableUnread,
		"a socket table that could not be read must be reported, not rendered as a host with no servers")
	require.False(t, gaps.ProcTableTruncated, "the walk itself was fine; only the socket table was not")
	for _, c := range cands {
		require.Empty(t, c.SocketPath,
			"with no table there is no path to attach, which is why this gap suppresses every row")
	}

	// A host with no tmux process at all: the table is never consulted, so its
	// readability is not a gap in the answer.
	other := map[int]procEntry{
		4242: {stat: procStat{Comm: "bash", State: "S", PPid: 1, StartTicks: 100}, uid: uid},
	}
	_, gaps = candidatesIn(other, uid, unreadable)
	require.False(t, gaps.IncompleteInventory(),
		"with nothing to identify, an unread socket table cannot make the answer incomplete")
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
			want: procStat{Comm: "sleep", State: "S", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a space", raw: "1234 (tmux: server)" + tail,
			want: procStat{Comm: "tmux: server", State: "S", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a close paren and a space", raw: "1234 (f) g)" + tail,
			want: procStat{Comm: "f) g", State: "S", PPid: 1, StartTicks: 19}, ok: true},
		{name: "comm with a trailing paren", raw: "1234 (weird))" + tail,
			want: procStat{Comm: "weird)", State: "S", PPid: 1, StartTicks: 19}, ok: true},
		{name: "a zombie", raw: "1234 (sleep) Z 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22",
			want: procStat{Comm: "sleep", State: "Z", PPid: 1, StartTicks: 19}, ok: true},
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

// TestProcessIsZombieSeesAKilledButUnreapedProcess.
//
// A zombie is why a liveness test cannot be signal 0 alone: the process has exited,
// but it keeps its pid until its parent collects it, so kill(pid, 0) succeeds
// indefinitely. Normally the window is microseconds; on the tmux 3.2 CI job an
// orphaned server re-parented to a container init that never wait()s stayed visible
// for the reaper's whole SIGTERM-then-SIGKILL budget, and the reaper concluded it had
// survived SIGKILL. Nothing survives SIGKILL.
//
// The fixture is exact: Start without Wait leaves the child unreaped by construction,
// so this is a real zombie rather than a simulated one.
func TestProcessIsZombieSeesAKilledButUnreapedProcess(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	// Reaped at the end, so the test leaves no zombie of its own behind.
	t.Cleanup(func() { _, _ = cmd.Process.Wait() })

	require.False(t, ProcessIsZombie(pid), "a running process is not a zombie")
	require.NoError(t, cmd.Process.Kill())

	require.Eventually(t, func() bool { return ProcessIsZombie(pid) },
		5*time.Second, 10*time.Millisecond,
		"a killed but unreaped child must be recognised as a zombie")

	// The property that actually matters, stated against the thing the reaper calls.
	require.False(t, errors.Is(syscall.Kill(pid, 0), syscall.ESRCH),
		"signal 0 still finds a zombie — which is exactly why it cannot be the whole test")
	require.False(t, processAliveForTest(pid), "a zombie must not read as alive")
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
	servers, supported, gaps := ScanServers(t.Context())
	require.True(t, supported, "Linux must support the scan")
	// A readable /proc is the premise of every assertion below: with a gap, an empty
	// servers slice would satisfy the loop vacuously.
	require.False(t, gaps.IncompleteInventory(), "the scan must be able to see /proc on the test host: %+v", gaps)

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
	servers, _, _ := ScanServers(t.Context())
	for _, s := range servers {
		rendered := fmt.Sprintf("%+v", s)
		for _, secret := range []string{"GH_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN", "gho_", "ghp_", "github_pat_"} {
			require.NotContains(t, rendered, secret,
				"a scan result carried %q; the socket name must come from the socket path, never from argv", secret)
		}
	}
}

// TestOrphanedServerIsFoundAndKillableAfterItsSocketRootIsDeleted reproduces the
// #547 incident with a real tmux server and then proves the scan reaches it.
//
// The shape of the incident: a run starts a server under its own TMUX_TMPDIR, the
// run ends, and its temp root is deleted while the server is still alive. The socket
// file goes with the root, so the path no longer resolves and every cleanup Atrium
// ships — `tmux ls`, `atrium reset`, clean.sh, clean_hard.sh — addresses a socket
// that is not there. The server keeps running, holding its agents, indefinitely.
//
// The negative control below is what makes the rest of this test mean anything.
// Without it the test would show that the scanner finds a server, but not that
// anything was broken.
func TestOrphanedServerIsFoundAndKillableAfterItsSocketRootIsDeleted(t *testing.T) {
	testutil.RequireTmux(t)

	// Short root, under /tmp: the socket path has to fit sockaddr_un's sun_path, and
	// t.TempDir() names the directory after the test. Same budget reasoning as
	// internal/testutil's installSandboxTmuxTmpdir and config_parse_test.go.
	tmuxTmp, err := os.MkdirTemp("/tmp", "atr")
	require.NoError(t, err)

	// A name the *production* predicate accepts, so this exercises the real
	// classifier rather than one injected for the test.
	sock := fmt.Sprintf("atrium-reaptest-%d", rand.Int31())
	sockPath := filepath.Join(tmuxTmp, fmt.Sprintf("tmux-%d", os.Getuid()), sock)

	// Teardown is armed before anything starts, and in two layers, because a test
	// that leaks a tmux server is the exact defect this file is about. The pid layer
	// is registered second so it runs first; the socket layer covers a start that
	// half-succeeded — server up, the pid lookup failed — while the root still exists.
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "tmux", "-S", sockPath, "kill-server").Run()
		_ = os.RemoveAll(tmuxTmp)
	})
	var serverPID int
	var childPIDs []int
	t.Cleanup(func() {
		for _, pid := range append([]int{serverPID}, childPIDs...) {
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	// TMUX_TMPDIR goes on cmd.Env, never os.Setenv: the package's TestMain installed
	// one for the whole binary, and reassigning the process-wide variable would move
	// every other test's sockets out from under the teardown that reaps them (#581).
	env := append(os.Environ(), "TMUX_TMPDIR="+tmuxTmp)
	startedBefore := time.Now().Add(-2 * time.Second)
	start := exec.CommandContext(t.Context(), "tmux", "-L", sock, "new-session", "-d", "sleep 600")
	start.Env = env
	out, err := start.CombinedOutput()
	require.NoError(t, err, "start the orphan-to-be: %s", out)
	startedAfter := time.Now().Add(2 * time.Second)

	pidOut, err := runWithEnv(t, env, "tmux", "-L", sock, "display-message", "-p", "#{pid}")
	require.NoError(t, err)
	serverPID, err = strconv.Atoi(strings.TrimSpace(pidOut))
	require.NoError(t, err)
	require.FileExists(t, sockPath, "the socket must exist while the root does")

	// ---- the incident: the root is deleted out from under the live server ----
	require.NoError(t, os.RemoveAll(tmuxTmp))
	require.NoFileExists(t, sockPath)
	require.True(t, processAliveForTest(serverPID), "the server outlives its socket file — that is the bug")

	// ---- negative control: existing tooling cannot reach it ----
	//
	// One of these commands is a kill-server, so where `-L` resolves matters. tmux
	// reads an empty or missing TMUX_TMPDIR as /tmp, which is the developer's live
	// socket directory; the control therefore runs with a TMUX_TMPDIR that is both
	// isolated and — the part easy to leave out — still EXISTS.
	//
	// To be exact about what is protecting what: the thing that makes a fallback
	// harmless here is the socket *name*, which is a per-run atrium-reaptest-<rand>
	// no live server ever binds, so `-L` cannot name Atrium's socket whatever it
	// resolves against. That is the same reasoning config_parse_test.go relies on.
	// The existing isolated root is the belt to that pair of braces, and it is
	// deliberate rather than incidental: a later edit that shortened this name to the
	// brand would otherwise turn a passing control into a live-fleet kill.
	control := append(os.Environ(), "TMUX_TMPDIR="+testutil.TmuxRoot(t))
	for _, args := range [][]string{
		{"tmux", "-L", sock, "list-sessions"},
		{"tmux", "-L", sock, "kill-server"},
	} {
		_, err := runWithEnv(t, control, args[0], args[1:]...)
		require.Error(t, err, "%v must not be able to reach the orphan; if it can, this test is not "+
			"reproducing #547 at all", args)
	}
	require.True(t, processAliveForTest(serverPID),
		"the orphan survived everything the existing toolbox can aim at it")

	// ---- the scan finds what nothing else can ----
	servers, supported, gaps := ScanServers(t.Context())
	require.True(t, supported)
	require.False(t, gaps.IncompleteInventory(), "a gap here would make the find below unprovable: %+v", gaps)

	found, ok := findServer(servers, serverPID)
	require.True(t, ok, "ScanServers did not find the orphan (pid %d); it found %v", serverPID, pidsOf(servers))
	require.Equal(t, sock, found.Socket)
	require.Equal(t, sockPath, found.SocketPath,
		"the bound path must be the deleted one — that is what the kernel still knows and what makes this solvable")
	require.True(t, found.ReachableKnown)
	require.False(t, found.Reachable, "nothing answers the deleted socket")
	require.WithinRange(t, found.Started, startedBefore, startedAfter)
	requireMatchesPS(t, serverPID, found.Started)
	require.NotEmpty(t, found.Children, "the server holds the process it was started with")
	// The other side of #614's discriminator, against the real thing: a genuine class-(c)
	// orphan has no client on it — `new-session -d` left none, and once the socket file is
	// gone nothing can connect. That is why counting clients can protect a live fleet
	// without refusing this, the case the whole command exists for: `reap --kill --yes`
	// still takes this server.
	require.Zero(t, found.ConnectedClients,
		"a real orphan holds no client, so the --yes refusal must not reach it")

	// ---- reap it, and verify rather than trust ----
	for _, kid := range found.Children {
		childPIDs = append(childPIDs, kid.PID)
	}
	// The PID-reuse guard production applies, applied here too: re-read the start
	// time and refuse if this is no longer the process that was scanned.
	reRead, ok := ProcessStartTime(serverPID)
	require.True(t, ok)
	require.Equal(t, found.Started, reRead)

	terminateLikeProduction(t, serverPID)

	// Killing the server takes its children down via the kernel's pty hangup, but
	// that is checked rather than assumed — the same reason `atrium reap` re-verifies
	// every captured child instead of trusting the mechanism.
	for _, kid := range found.Children {
		require.Eventually(t, func() bool { return !processAliveForTest(kid.PID) },
			5*time.Second, 20*time.Millisecond, "child pid %d (%s) outlived the server", kid.PID, kid.Comm)
	}

	// And it is gone from the scan, which is the property the user actually gets.
	after, _, _ := ScanServers(t.Context())
	_, stillThere := findServer(after, serverPID)
	require.False(t, stillThere, "a killed orphan must stop being reported")
}

// TestAServerWhoseSocketFileIsGoneStillReportsItsAttachedClient reproduces #614 against
// a real tmux server, which is the only place the whole chain can be checked at once:
// tmux's own accept()ed sockets, the /proc/net/unix rows they produce, and the count
// ScanServers puts on the row.
//
// The shape is the sibling test's, with one thing added and it is the thing that matters:
// a client is attached before the socket file goes. Everything the inventory can see is
// then identical to that test's orphan — alive, holding a child, answering nothing when
// probed by path, so ReachableKnown && !Reachable and a default `reap --kill` target — and
// the only difference between "an abandoned server" and "somebody's live session" is the
// client. The sibling asserts the count is 0 there; this asserts it is not 0 here, and
// between them the discriminator is shown to discriminate rather than merely to exist.
//
// The client is a genuine `tmux attach` in a pty, not a `tmux ls`: the fixture has to
// produce the connection tmux itself holds open for a session someone is looking at.
func TestAServerWhoseSocketFileIsGoneStillReportsItsAttachedClient(t *testing.T) {
	testutil.RequireTmux(t)

	// Short root under /tmp for sun_path's budget, and a per-run random socket *name* so
	// that no `-L` anywhere — here or in a later edit of this file — can resolve to a
	// socket the live fleet binds. Same reasoning as the sibling test and
	// config_parse_test.go.
	tmuxTmp, err := os.MkdirTemp("/tmp", "atr")
	require.NoError(t, err)
	sock := fmt.Sprintf("atrium-clienttest-%d", rand.Int31())
	sockPath := filepath.Join(tmuxTmp, fmt.Sprintf("tmux-%d", os.Getuid()), sock)

	// Teardown armed before anything starts, and by absolute path: `-L` resolves against
	// TMUX_TMPDIR, which tmux reads as /tmp when that root has gone — which by this test's
	// end it has.
	var serverPID int
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "tmux", "-S", sockPath, "kill-server").Run()
		if serverPID > 0 {
			_ = syscall.Kill(serverPID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(tmuxTmp)
	})

	env := append(os.Environ(), "TMUX_TMPDIR="+tmuxTmp)
	start := exec.CommandContext(t.Context(), "tmux", "-L", sock, "new-session", "-d", "sleep 600")
	start.Env = env
	out, err := start.CombinedOutput()
	require.NoError(t, err, "start the server: %s", out)

	pidOut, err := runWithEnv(t, env, "tmux", "-L", sock, "display-message", "-p", "#{pid}")
	require.NoError(t, err)
	serverPID, err = strconv.Atoi(strings.TrimSpace(pidOut))
	require.NoError(t, err)

	// ---- a real client, attached ----
	//
	// TMUX is dropped from the environment because the test binary may itself be running
	// inside tmux — this repo is developed in Atrium — and `attach` refuses to nest.
	attach := exec.CommandContext(t.Context(), "tmux", "-S", sockPath, "attach", "-t", "0")
	attach.Env = append(withoutTmuxVar(os.Environ()), "TMUX_TMPDIR="+tmuxTmp, "TERM=xterm")
	ptmx, err := pty.Start(attach)
	require.NoError(t, err, "attach a client in a pty")
	t.Cleanup(func() {
		_ = ptmx.Close()
		if attach.Process != nil {
			_ = attach.Process.Kill()
			_, _ = attach.Process.Wait()
		}
	})

	// tmux's own account of the client, so the premise is established before the socket
	// file goes: after that nothing can ask tmux anything about this server.
	require.Eventually(t, func() bool {
		out, err := runWithEnv(t, env, "tmux", "-L", sock, "list-clients")
		return err == nil && strings.Contains(out, "attached")
	}, 10*time.Second, 100*time.Millisecond, "no client attached, so this fixture is not #614's")

	// ---- the incident ----
	require.NoError(t, os.RemoveAll(tmuxTmp))
	require.NoFileExists(t, sockPath)
	require.True(t, processAliveForTest(serverPID), "the server outlives its socket file")

	servers, supported, gaps := ScanServers(t.Context())
	require.True(t, supported)
	require.False(t, gaps.IncompleteInventory(), "a gap would make the assertions below unprovable: %+v", gaps)

	found, ok := findServer(servers, serverPID)
	require.True(t, ok, "ScanServers did not find pid %d; it found %v", serverPID, pidsOf(servers))
	require.True(t, found.ReachableKnown)
	require.False(t, found.Reachable,
		"nothing answers the deleted socket — which is what makes this a default kill target and #614 a hazard")
	require.GreaterOrEqual(t, found.ConnectedClients, 1,
		"the client attached before the unlink is still connected, and counting it is the only thing "+
			"separating this row from the orphan the sibling test builds")
}

// withoutTmuxVar returns env with TMUX removed, so a `tmux attach` started from inside
// tmux is not refused as a nested session.
func withoutTmuxVar(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// terminateLikeProduction mirrors cli_reap.go's signal ladder: SIGTERM, a bounded
// wait, then SIGKILL.
//
// The escalation is load-bearing rather than belt-and-braces, and this test is how
// that was established. An earlier version sent only SIGTERM and required the server
// to be gone within five seconds; it passed against tmux 3.6 locally and failed on
// the tmux 3.2 floor job, where a server with a live session was still running when
// the budget expired. So "tmux exits on SIGTERM" is not a property to rely on across
// the versions Atrium supports — which is consistent with the design doc's finding
// that SIGTERM's role here was overstated in the first place.
func terminateLikeProduction(t *testing.T, pid int) {
	t.Helper()
	require.NoError(t, syscall.Kill(pid, syscall.SIGTERM))
	if waitGone(pid, 5*time.Second) {
		return
	}
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	require.True(t, waitGone(pid, 2*time.Second), "pid %d survived SIGTERM and SIGKILL", pid)
}

// waitGone polls until pid is gone or the budget expires.
func waitGone(pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !processAliveForTest(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runWithEnv runs a command with an explicit environment and returns its stdout.
func runWithEnv(t *testing.T, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Env = env
	out, err := cmd.Output()
	return string(out), err
}

// processAliveForTest mirrors cli_reap.go's liveness test, zombie check included.
// Signal 0 alone succeeds for a process that has exited and not yet been reaped, so
// a test using it would wait out both signal budgets and then report that something
// survived SIGKILL.
func processAliveForTest(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false
	}
	return !ProcessIsZombie(pid)
}

func findServer(servers []OrphanServer, pid int) (OrphanServer, bool) {
	for _, s := range servers {
		if s.PID == pid {
			return s, true
		}
	}
	return OrphanServer{}, false
}

func pidsOf(servers []OrphanServer) []int {
	pids := make([]int, 0, len(servers))
	for _, s := range servers {
		pids = append(pids, s.PID)
	}
	return pids
}

// requireMatchesPS checks the parsed start time against ps, an oracle that shares no
// code with the parser under test. LC_ALL=C pins lstart's format so the layout below
// is the one ps actually prints.
//
// ps reports whole seconds, so the comparison is to the second — which is ample: the
// misparse this guards against lands on the machine's boot time, hours or days away.
func requireMatchesPS(t *testing.T, pid int, got time.Time) {
	t.Helper()
	out, err := runWithEnv(t, append(os.Environ(), "LC_ALL=C"), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	require.NoError(t, err, "ps is the independent oracle for this assertion")
	want, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(out), time.Local)
	require.NoError(t, err, "parse ps lstart %q", strings.TrimSpace(out))
	require.WithinDuration(t, want, got, time.Second)
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

// TestScanServersReportsATruncatedProcWalk drives the real /proc reader with an
// already-cancelled context, which is the condition the 10 s reap budget can produce on
// a loaded host.
//
// It asserts against the production functions rather than a stubbed seam on purpose: the
// bug being fixed was that readProcTable returned its partial table with no way to say
// so, and every layer above then presented it as the whole host. Proving the flag is set
// by the code that actually walks /proc is the only version of this test that would have
// failed before the fix.
func TestScanServersReportsATruncatedProcWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	servers, supported, gaps := ScanServers(ctx)
	require.True(t, supported, "Linux still supports the scan; it just could not finish it")
	require.True(t, gaps.ProcTableTruncated,
		"a /proc walk cut short by the context must be reported as incomplete, not as a clean host")
	require.Empty(t, servers, "the walk never got far enough to classify anything")

	// The consequence that matters: this result must never render as "none".
	require.True(t, gaps.IncompleteInventory())
}
