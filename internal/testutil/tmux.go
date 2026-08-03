package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	// tmuxRootPIDFile records which process owns a root, so a later run can tell an
	// orphan from a sibling package's live root.
	tmuxRootPIDFile = "owner.pid"
	// tmuxRootGrace is how long a root is presumed live regardless of its marker.
	// It covers the window between MkdirTemp returning and the marker being
	// written, during which a concurrently starting package must not mistake a
	// brand-new root for an orphan. `go test ./...` runs package binaries in
	// parallel, so that window is genuinely raced.
	tmuxRootGrace = 5 * time.Minute
	// tmuxKillTimeout bounds one teardown `kill-server`, so a wedged tmux server
	// cannot hang the test binary's exit.
	tmuxKillTimeout = 5 * time.Second
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
// If /tmp is unusable — Windows, or a host with no writable /tmp — it installs
// nothing and leaves sandboxTmuxRoot empty. That is deliberately not a panic: it
// keeps every tmux-free package running, while requireSandboxedTmux still fails
// any test that would have driven a real tmux server unisolated.
func installSandboxTmuxTmpdir() func() {
	root, err := os.MkdirTemp(tmuxRootParent, tmuxRootPrefix)
	if err != nil {
		return func() {}
	}
	if err := os.Setenv("TMUX_TMPDIR", root); err != nil {
		_ = os.RemoveAll(root)
		panic("testutil: failed to set sandbox TMUX_TMPDIR: " + err.Error())
	}
	_ = os.WriteFile(filepath.Join(root, tmuxRootPIDFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600)
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
		// Kill first, empty second. A TMUX_TMPDIR root removed while its server
		// still lives produces a server no tooling can reach — the socket it is
		// listening on no longer has a path (#547).
		killTmuxServers(root)
		// The root itself outlives the contents on purpose: TMUX_TMPDIR still names
		// it, and tmux reads an empty *or missing* TMUX_TMPDIR as /tmp. Deleting the
		// directory here would leave every later `-L` call in this process — a stray
		// goroutine, a helper in the caller's TestMain — pointed at the developer's
		// live socket. The next run's sweep removes the empty shell.
		removeContents(root)
	}
}

// removeContents empties dir without removing dir itself.
func removeContents(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// sweepStaleTmuxRoots reaps the socket roots of test binaries that died before
// their teardown, skipping self and anything a live process still owns.
func sweepStaleTmuxRoots(self string) {
	roots, err := filepath.Glob(filepath.Join(tmuxRootParent, tmuxRootPrefix+"*"))
	if err != nil {
		return
	}
	for _, root := range roots {
		if root == self || !tmuxRootIsStale(root) {
			continue
		}
		killTmuxServers(root)
		_ = os.RemoveAll(root)
	}
}

// tmuxRootIsStale reports whether root belongs to a process that is gone. It
// answers "no" for anything younger than tmuxRootGrace whatever its marker says,
// which is what makes the sweep safe to run while sibling package binaries are
// starting up.
func tmuxRootIsStale(root string) bool {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || time.Since(info.ModTime()) < tmuxRootGrace {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(root, tmuxRootPIDFile))
	if err != nil {
		// Older than the grace window with no owner recorded: either a teardown
		// already emptied it, or the marker never got written.
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return true
	}
	return !processAlive(pid)
}

// processAlive reports whether pid names a running process. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// TmuxRoot returns the private tmux socket directory this test binary's sandbox
// installed, failing the test if there isn't one. Use it to assert where a socket
// landed; see session/tmux's socket-isolation tests.
func TmuxRoot(t *testing.T) string {
	t.Helper()
	requireSandboxedTmux(t)
	return sandboxTmuxRoot
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

// killTmuxServers reaps every tmux server whose socket lives under root.
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
func killTmuxServers(root string) {
	for _, sock := range tmuxSocketsUnder(root) {
		ctx, cancel := context.WithTimeout(context.Background(), tmuxKillTimeout)
		_ = exec.CommandContext(ctx, "tmux", "-S", sock, "kill-server").Run()
		cancel()
		// tmux never unlinks a socket when its server dies, so the file outlives the
		// kill; drop it here rather than leaving it for the caller to trip over.
		_ = os.Remove(sock)
	}
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
