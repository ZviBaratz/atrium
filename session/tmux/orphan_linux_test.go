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

	require.Equal(t, path, socketPathFor(os.Getpid(), mustListeningSockets(t)),
		"a listening socket must be locatable by pid while its file exists")

	// The incident: the socket's directory tree is removed while the server lives.
	require.NoError(t, os.Remove(path))
	require.NoFileExists(t, path)

	require.Equal(t, path, socketPathFor(os.Getpid(), mustListeningSockets(t)),
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
	for _, p := range mustListeningSockets(t) {
		if p == path {
			seen++
		}
	}
	require.Equal(t, 1, seen, "only the listening socket may be reported for %q", path)
}

// mustListeningSockets reads the listening-socket table and fails the test unless the
// read itself succeeded.
//
// The ok result is load-bearing: listeningSockets used to return a nil map on a read
// error, which every caller then treated as "this host has no listening sockets" — so
// an unreadable /proc/net/unix made the whole scan report a clean host. A test that
// discarded ok would pass identically against that bug and against the fix.
func mustListeningSockets(t *testing.T) map[uint64]string {
	t.Helper()
	socks, ok := listeningSockets()
	require.True(t, ok, "/proc/net/unix must be readable for this assertion to mean anything")
	return socks
}

// TestCandidatesInReportsAnUnreadableSocketTable is the other half of what
// mustListeningSockets above only asserts as a premise: that the ok result is acted on.
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
	unreadable := func() (map[uint64]string, bool) { return nil, false }
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
	require.False(t, gaps.Any(),
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
	require.False(t, gaps.Any(), "the scan must be able to see /proc on the test host: %+v", gaps)

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
	require.False(t, gaps.Any(), "a gap here would make the find below unprovable: %+v", gaps)

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
	require.True(t, gaps.Any())
}
