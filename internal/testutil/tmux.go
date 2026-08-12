package testutil

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireTmuxEnv is the environment variable that flips RequireTmux from a
// friendly local skip into a hard failure. CI sets it to "1" on the jobs that
// install tmux (see .github/workflows/build.yml and issue #274).
const requireTmuxEnv = "ATRIUM_CI_REQUIRE_TMUX"

const (
	// tmuxRootParent is where sandbox socket roots live. Hardcoded rather than
	// os.TempDir() because the socket path has a hard length budget — see
	// installSandboxTmuxTmpdir.
	tmuxRootParent = "/tmp"
	// tmuxRootPrefix names a sandbox socket root. It is doing safety work, not just
	// naming: tmuxSocketsUnder refuses any root whose basename lacks it, which is what
	// stops a stray "/tmp" from resolving to /tmp/tmux-<uid>/atrium — the socket a
	// running Atrium is on. It is also long enough not to collide with a developer's
	// own scratch dirs (this machine had /tmp/atr*).
	tmuxRootPrefix = "atrium-tmux-"
	// tmuxKillTimeout bounds one teardown `kill-server`, so a wedged tmux server
	// cannot hang the test binary's exit.
	tmuxKillTimeout = 5 * time.Second
	// tmuxDialTimeout bounds the liveness probe that decides whether a socket file
	// still has a server behind it. A connect to a local unix socket either lands
	// or is refused immediately; the timeout only covers a full listen backlog.
	tmuxDialTimeout = time.Second
	// tmuxReapTimeout is how long a killed server is given to close its socket before
	// it counts as one that outlasted the kill. Only a server that is genuinely stuck
	// spends it: a healthy one is gone in a poll or two.
	tmuxReapTimeout = 2 * time.Second
	// tmuxReapPoll is the gap between liveness probes while waiting out tmuxReapTimeout.
	tmuxReapPoll = 10 * time.Millisecond
)

// sandboxTmuxRoot is the private TMUX_TMPDIR that SandboxHomeMain mints for this
// test binary, and the empty string until it has run. Recording it — rather than
// trusting the environment variable alone — is what lets the isolation check tell
// "the sandbox is installed" apart from "the developer's shell happened to export
// a TMUX_TMPDIR", which would be isolated from Atrium's live socket only by luck.
var sandboxTmuxRoot string

// installSandboxTmuxTmpdir gives this test binary a tmux socket directory of its
// own and returns the teardown for it. Called by SandboxHomeMain; see that
// function for why the socket directory is the only thing that isolates tmux.
//
// The root has to stay short, and it has to be under /tmp. tmux binds the socket
// at $TMUX_TMPDIR/tmux-<uid>/<sock>, and that path has to fit sockaddr_un's
// sun_path (104 bytes on darwin, 108 on linux) or the server dies with "File name
// too long". So neither t.TempDir() (names the directory after the test) nor
// $TMPDIR works as the base — and note tmux ignores TMPDIR entirely, so pointing
// that at somewhere short is not an option either. /tmp is where tmux would have
// put the socket anyway; this only makes the directory unique and removable.
//
// If the sandbox cannot be installed — /tmp unusable on Windows or a host with no
// writable one, or the TMUX_TMPDIR assignment itself refused — it installs nothing
// and leaves sandboxTmuxRoot empty. Neither path panics: that keeps every tmux-free
// package running, while requireSandboxedTmux still fails any test that would have
// driven a real tmux server unisolated.
//
// Nothing reaps a root whose run never reached the teardown — a signal, a -timeout
// abort — and for a run that had started a server that costs more than the directory:
// ui's terminal panes run $SHELL (ui/terminal.go), which never exits, and every -L
// lookup (`tmux ls`, and `atrium reset` via CleanupSessions) resolves against the
// *current* TMUX_TMPDIR rather than that dead run's, so the survivor is invisible to
// both.
//
// On Linux, `atrium doctor` is what can name it (#547). Its "Orphaned tmux servers:"
// section identifies a server from /proc rather than from a socket directory
// (ScanServers), so a sandbox socket is in range wherever its root sits, and the name
// it binds is the bare brand, which ownsSocketName claims. A root the teardown never
// reached is still there, so the socket under it still resolves and the server answers
// — the row reads `reachable` and carries the exact path-named command that stops it:
//
//	pid 4242  socket atrium  up 2h11m  reachable  holds 1 process (bash)
//	    → tmux -S /tmp/atrium-tmux-<rand>/tmux-<uid>/atrium kill-server
//
// Run what it prints. Read the row rather than this example, though: with no live
// fleet answering the ambient socket, that same command arrives under a caution that
// the server may itself be a live fleet under another TMUX_TMPDIR, and if tmux could
// not be probed at all the row offers no command — three branches, one per class of
// what the scan established (renderOrphanServer, internal/doctor/orphans.go).
//
// `atrium reap` reports the identical list, but it is not the verb for *this* leak.
// Reachable is the class `--kill` spares by default (reapTargets, cli_reap.go) —
// deliberately, since a reachable server may be a second live Atrium — and the `--all`
// that would select it refuses whenever no live fleet was identified, which is exactly
// the state a bare-brand reachable orphan puts the scan in (EmptyFleetUnproven). Reap
// is for the shape whose root *was* removed while it lived: unreachable, nothing can
// address it by path any more, and `atrium reap --kill` is the only thing that stops it.
//
// Off Linux there is no process inventory, so neither command can see any of this:
// doctor renders the section unavailable and reap errors out (orphanScanSupported in
// session/tmux/orphan_other.go). Kill it by the path you can see under the leaked root
// — `tmux -S <that path> kill-server`, the path spelled out, not matched.
//
// Neither command accounts for the leftover directory itself, nor a socket file with
// no server behind it inside one: ScanStaleSockets reads only Atrium's own socket dir.
// That part is litter — remove the root by its exact path, never by a glob.
//
// Sweeping this package's roots automatically is what it tried and reverted: the glob
// that finds them is one wrong prefix away from /tmp/tmux-<uid>/atrium, and that
// prefix going missing during a mutation test is what killed the developer's live
// fleet (#584). Reaping by hand is rare; a sweep is a standing hazard.
func installSandboxTmuxTmpdir() func() {
	root, err := os.MkdirTemp(tmuxRootParent, tmuxRootPrefix)
	if err != nil {
		return func() {}
	}
	if err := os.Setenv("TMUX_TMPDIR", root); err != nil {
		// Not a panic, for the reason SandboxHomeMain documents: an uninstallable
		// socket sandbox must leave sandboxTmuxRoot empty so requireSandboxedTmux
		// fails exactly the tests that needed it, rather than taking the whole
		// package's run down with it.
		_ = os.RemoveAll(root)
		return func() {}
	}
	sandboxTmuxRoot = root

	return func() {
		// Kill first, remove second, and only remove when the kill is confirmed. A
		// TMUX_TMPDIR root removed while a server still lives produces a server no
		// tooling can reach — the socket it is listening on no longer has a path
		// (#547) — so a server that outlasted the kill keeps its socket and its root.
		// That leaves a directory behind, which is the right way round: a stray
		// directory is litter, a server nothing can address is not.
		if !killTmuxServers(root) {
			return
		}
		_ = os.RemoveAll(root)
		// TMUX_TMPDIR now names a directory that is gone, and tmux reads that as /tmp
		// — the developer's live socket. Nothing reaches it today: this runs after
		// m.Run has returned, so every test and cleanup is finished, and no test in
		// app, ui or session/tmux calls Attach (the only path with goroutines that
		// outlive their caller). Clearing the recorded root is the belt: anything that
		// does run later and goes through RequireTmux/RequireSandboxedTmux hard-fails
		// instead of quietly binding /tmp.
		sandboxTmuxRoot = ""
	}
}

// TmuxRoot returns the private tmux socket directory this test binary's sandbox
// installed, failing the test if there isn't one. Use it to assert where a socket
// landed; see session/tmux's socket-isolation tests.
func TmuxRoot(t *testing.T) string {
	t.Helper()
	requireSandboxedTmux(t)
	return sandboxTmuxRoot
}

// RequireSandboxedTmux fails the test unless this package's tmux socket directory
// is sandboxed. RequireTmux already calls it, so use this only where RequireTmux
// cannot be: a real-tmux test that must keep a plain skip when tmux is missing (see
// session/tmux's TestSessionDeathStopsProbing, which CI -skips by name and so must
// never hard-fail under ATRIUM_CI_REQUIRE_TMUX=1). Isolation is not the same gate —
// a test that does drive a real server has to be isolated whatever its skip policy,
// or it drives the developer's live fleet (#581).
func RequireSandboxedTmux(t *testing.T) {
	t.Helper()
	requireSandboxedTmux(t)
}

// requireSandboxedTmux fails the test unless TMUX_TMPDIR still points at the root
// SandboxHomeMain minted, and that root still exists. All three checks matter, and
// the third is the one that is easy to leave out: tmux reads a TMUX_TMPDIR naming
// a *missing* directory as /tmp, silently, so a variable that merely holds the
// right string proves nothing about where the next socket lands.
func requireSandboxedTmux(t *testing.T) {
	t.Helper()
	if sandboxTmuxRoot == "" {
		t.Fatal("this package's tmux socket is not sandboxed: its TestMain must call " +
			"testutil.SandboxHomeMain, which points TMUX_TMPDIR at a private root (and if it " +
			"does, /tmp was unusable). Without one, tmux falls back to /tmp and a real-tmux " +
			"test binds the developer's live Atrium socket (#581)")
	}
	if got := os.Getenv("TMUX_TMPDIR"); got != sandboxTmuxRoot {
		t.Fatalf("TMUX_TMPDIR is %q, not the sandbox root %q: sockets created now escape the "+
			"teardown that reaps them (#581)", got, sandboxTmuxRoot)
	}
	if info, err := os.Stat(sandboxTmuxRoot); err != nil || !info.IsDir() {
		t.Fatalf("sandbox root %q is gone: tmux reads a missing TMUX_TMPDIR as /tmp, so a "+
			"session started now lands on the developer's live socket (#581)", sandboxTmuxRoot)
	}
}

// tmuxSocketsUnder returns the tmux server sockets living under root, and nothing
// at all for an empty root, a missing root, or any path that is not a sandbox root
// this package minted. Those refusals are the safety-critical part: they are what
// make the teardown a no-op rather than a live-fleet kill when its root has already
// been removed, and what stops a caller-supplied "/tmp" from resolving to
// /tmp/tmux-<uid>/atrium — the socket a running Atrium is on.
//
// `man tmux` on -L: "the sockets are all created in a directory tmux-UID under the
// directory given by TMUX_TMPDIR". The second pattern is belt-and-braces against a
// layout that puts one directly in the root; the ModeSocket filter means a
// surprise there can only ever cost an extra stat.
func tmuxSocketsUnder(root string) []string {
	if root == "" || !strings.HasPrefix(filepath.Base(root), tmuxRootPrefix) {
		return nil
	}
	var socks []string
	for _, pattern := range []string{
		filepath.Join(root, "tmux-*", "*"),
		filepath.Join(root, "*"),
	} {
		// Glob errors only on a malformed pattern; a missing root is no matches.
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			info, err := os.Lstat(match)
			if err != nil || info.Mode()&os.ModeSocket == 0 {
				continue
			}
			socks = append(socks, match)
		}
	}
	return socks
}

// killTmuxServers reaps every tmux server whose socket lives under root, and
// reports whether every one of them is confirmed gone. A false answer means
// something is still listening — a wedged server that outlasted tmuxKillTimeout, or
// no tmux on PATH to send the kill at all — and the caller must then leave root's
// contents in place: unlinking a live server's socket is what produces a server no
// tooling can address, which is #547 rather than a fix for it.
//
// It addresses each server by absolute socket path (`tmux -S`), never by name
// (`tmux -L`). -L resolves against TMUX_TMPDIR, and tmux silently falls back to
// /tmp when that variable is empty or names a missing directory — so `-L atrium`
// in a teardown whose root had gone would destroy the developer's live fleet.
// Measured on tmux 3.6: `TMUX_TMPDIR= tmux -L atrium list-sessions` lists the live
// sessions, while `tmux -S <missing path> kill-server` is an error with no
// fallback. With tmuxSocketsUnder refusing every root but a sandbox one, and
// reading only that directory, no argument to this function reaches
// /tmp/tmux-<uid>/atrium.
func killTmuxServers(root string) bool {
	reaped := true
	for _, sock := range tmuxSocketsUnder(root) {
		ctx, cancel := context.WithTimeout(context.Background(), tmuxKillTimeout)
		_ = exec.CommandContext(ctx, "tmux", "-S", sock, "kill-server").Run()
		cancel()
		// The exit status is not the answer: `kill-server` fails identically for "the
		// server was already gone" (the stale socket file below) and "the kill did not
		// land", and only the second must stop the unlink. Ask the socket instead.
		if !awaitTmuxServerGone(sock) {
			reaped = false
			continue
		}
		// tmux never unlinks a socket when its server dies, so the file outlives the
		// kill; drop it here rather than leaving it for the caller to trip over.
		_ = os.Remove(sock)
	}
	return reaped
}

// awaitTmuxServerGone reports whether nothing is listening on sock, waiting up to
// tmuxReapTimeout for a server that has been told to die to finish dying.
//
// The wait is what makes the answer usable rather than a coin flip. `kill-server`
// returns once the server has acknowledged the command, not once it has closed its
// listening socket, so a single probe immediately after it frequently still
// connects — measured here as a flaky "reported an unreaped server after killing the
// only one there". Reading that as "still alive" would leave every reaped root
// un-emptied.
func awaitTmuxServerGone(sock string) bool {
	deadline := time.Now().Add(tmuxReapTimeout)
	for {
		if !tmuxServerAlive(sock) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(tmuxReapPoll)
	}
}

// tmuxServerAlive reports whether a server is still listening on sock. The file's
// existence proves nothing — tmux leaves it behind when the server dies — so this
// connects: a refused or absent socket is a dead server, a completed connect is a
// live one. The connection is closed immediately and speaks no protocol, which tmux
// handles the same way it handles any client that hangs up.
func tmuxServerAlive(sock string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxDialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RequireTmux gates a real-tmux integration test on tmux being available, and on
// this package's tmux socket being sandboxed. When tmux is absent it skips the
// test — keeping local runs friendly on machines without tmux — unless
// ATRIUM_CI_REQUIRE_TMUX=1, in which case a missing tmux is a hard failure instead
// of a silent skip.
//
// The regression guards these tests provide (e.g. the atrium.conf bad-escape bug
// that shipped and broke every new session) are only useful if they actually
// run. CI installs tmux and sets ATRIUM_CI_REQUIRE_TMUX=1 so that a broken tmux
// install can never let those guards silently re-skip and go dark. See #274.
//
// The isolation check is not a skip: a real-tmux test that runs unisolated writes
// into the developer's live Atrium fleet, so the only safe outcome is to fail
// loudly. It is also the guard on the sandbox itself — remove the TMUX_TMPDIR line
// from SandboxHomeMain and every caller here goes red, which is what keeps this
// from silently regressing to what #581 describes.
func RequireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(requireTmuxEnv) == "1" {
			t.Fatalf("tmux not found but %s=1: %v", requireTmuxEnv, err)
		}
		t.Skipf("tmux not available: %v", err)
	}
	requireSandboxedTmux(t)
}
