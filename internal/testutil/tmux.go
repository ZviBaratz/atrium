package testutil

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// tmuxRootPrefix names a sandbox socket root. It has to be distinctive enough
	// to sweep by: sweepStaleTmuxRoots deletes directories matching it, so a prefix
	// short enough to collide with a developer's own scratch dir (/tmp/atr*, which
	// this machine has) would delete their work.
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
func installSandboxTmuxTmpdir() func() {
	root, err := os.MkdirTemp(tmuxRootParent, tmuxRootPrefix)
	if err != nil {
		return func() {}
	}
	// The owner marker goes down before TMUX_TMPDIR names the root, and a root that
	// cannot be marked is not a sandbox at all: unmarked, no future sweep can ever
	// identify it, so it and any server it holds outlive every run that might have
	// reclaimed them. Writing it first is also what keeps this failure path from
	// having to undo a TMUX_TMPDIR it had already installed, and a variable naming a
	// directory that no longer exists is the one state worse than no sandbox: tmux
	// reads it as /tmp.
	if err := os.WriteFile(filepath.Join(root, ownerMarkerFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		_ = os.RemoveAll(root)
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

	// Reap what earlier runs could not. A test binary killed mid-run (Ctrl-C, a
	// -timeout abort, a panicking process) never reaches the teardown below, and
	// what it strands here is worse than a stray directory: ui's terminal panes run
	// $SHELL (ui/terminal.go:303), which never exits, so the orphaned server lives
	// forever — and being under a private root, it is invisible to `tmux ls`,
	// `atrium` and `atrium reset`, all of which resolve -L against the *current*
	// TMUX_TMPDIR. The sandbox HOME beside it has leaked ~550 directories on this
	// developer's machine since July, so this is the expected case, not the
	// unlucky one.
	sweepStaleTmuxRoots(root)

	return func() {
		// Kill first, empty second, and only empty when the kill is confirmed. A
		// TMUX_TMPDIR root emptied while a server still lives produces a server no
		// tooling can reach — the socket it is listening on no longer has a path
		// (#547) — so a server that outlasted the kill keeps its socket, and the next
		// run's sweep gets another attempt at it.
		if !killTmuxServers(root) {
			return
		}
		// The root itself outlives the contents on purpose: TMUX_TMPDIR still names
		// it, and tmux reads an empty *or missing* TMUX_TMPDIR as /tmp. Deleting the
		// directory here would leave every later `-L` call in this process — a stray
		// goroutine, a helper in the caller's TestMain — pointed at the developer's
		// live socket. The next run's sweep removes the shell, recognising it by the
		// owner marker this deliberately leaves behind.
		removeContentsExceptMarker(root)
	}
}

// removeContentsExceptMarker empties dir without removing dir itself, and without
// removing its owner marker.
//
// Keeping the marker is load-bearing, not tidiness. rootIsStale reaps only roots
// whose marker names a dead process; a root stripped of its marker is one no sweep
// will ever touch again. This teardown is the normal end of every sandboxed test
// binary, so stripping it here would leak one directory per package per run — which
// is exactly what an age-based fallback used to paper over, at the cost of making
// every unmarked directory in the parent look reapable.
func removeContentsExceptMarker(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == ownerMarkerFile {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// sweepStaleTmuxRoots reaps the socket roots of test binaries that died before
// their teardown. The release callback is what makes it different from the HOME
// sweep: a root whose server survived the kill keeps its socket, because removing
// the directory out from under a live server is the unreachable orphan (#547), not
// the cure for it.
func sweepStaleTmuxRoots(self string) {
	sweepStaleRoots(tmuxRootParent, tmuxRootPrefix, self, killTmuxServers)
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
