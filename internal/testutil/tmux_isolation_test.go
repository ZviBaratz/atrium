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

// TestSandboxInstallsAPrivateTmuxRoot pins the two properties every real-tmux test
// in the module leans on: TMUX_TMPDIR names a directory this binary owns, and it is
// short enough that the socket path underneath it fits sockaddr_un's sun_path.
func TestSandboxInstallsAPrivateTmuxRoot(t *testing.T) {
	root := TmuxRoot(t)

	if got := os.Getenv("TMUX_TMPDIR"); got != root {
		t.Fatalf("TMUX_TMPDIR = %q, want the sandbox root %q", got, root)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("sandbox root %q is not a directory: %v", root, err)
	}
	if !strings.HasPrefix(root, tmuxRootParent+"/") {
		t.Fatalf("sandbox root %q is not under %s: darwin's $TMPDIR is ~56 chars on its own, "+
			"which does not leave room for tmux-<uid>/<socket> inside sun_path", root, tmuxRootParent)
	}
	// Worst case Atrium can produce: the longest uid, and the longest socket name
	// (a legacy install's "-precheck" probe, session/tmux/config.go:187). 104 is
	// darwin's sun_path, the tighter of the two platforms.
	if longest := len(filepath.Join(root, "tmux-4294967295", "claudesquad-precheck")); longest >= 104 {
		t.Fatalf("worst-case socket path under %q is %d bytes, which will not fit sun_path (104 on darwin)",
			root, longest)
	}

	// The marker is not bookkeeping, it is what stops a sibling run's sweep reaping
	// this root while it is in use: without one, rootIsStale falls through to age
	// alone and a root outliving rootGrace is an orphan by that measure. So a root
	// that exists must carry it — which is why installSandboxTmuxTmpdir writes it
	// before it publishes TMUX_TMPDIR, and gives up on the whole sandbox if it cannot.
	raw, err := os.ReadFile(filepath.Join(root, ownerMarkerFile))
	if err != nil {
		t.Fatalf("sandbox root %q carries no %s: an unmarked root is swept by age alone, "+
			"so a sibling `go test ./...` package would reap this one mid-run", root, ownerMarkerFile)
	}
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("%s names %q, want this process (%d): a marker naming anything else is read as "+
			"an owner that has exited", ownerMarkerFile, got, os.Getpid())
	}
}

// TestTmuxSocketsUnderFindsOnlySockets is the discovery half of the teardown's
// safety argument: killTmuxServers can only ever address what this returns.
func TestTmuxSocketsUnderFindsOnlySockets(t *testing.T) {
	t.Run("empty root finds nothing", func(t *testing.T) {
		if got := tmuxSocketsUnder(""); got != nil {
			t.Fatalf("tmuxSocketsUnder(\"\") = %v, want nil — an empty root must never be globbed", got)
		}
	})

	t.Run("missing root finds nothing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), tmuxRootPrefix+"already-removed")
		if got := tmuxSocketsUnder(missing); got != nil {
			t.Fatalf("tmuxSocketsUnder(%q) = %v, want nil", missing, got)
		}
	})

	// The one that would cost the developer their fleet: /tmp holds the live
	// socket at /tmp/tmux-<uid>/atrium, and the first glob pattern matches it
	// exactly. Provenance is not a guard — the prefix check is.
	t.Run("a root we did not mint finds nothing", func(t *testing.T) {
		for _, root := range []string{tmuxRootParent, os.TempDir(), "/", t.TempDir()} {
			if got := tmuxSocketsUnder(root); got != nil {
				t.Fatalf("tmuxSocketsUnder(%q) = %v, want nil: this reaches the developer's live "+
					"Atrium socket at %s/tmux-<uid>/atrium", root, got, tmuxRootParent)
			}
		}
	})

	t.Run("regular files are not sockets", func(t *testing.T) {
		root := shortTempRoot(t)
		dir := filepath.Join(root, "tmux-1000")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "atrium"), nil, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := tmuxSocketsUnder(root); got != nil {
			t.Fatalf("tmuxSocketsUnder = %v, want nil — a regular file named like a socket is not one", got)
		}
	})

	t.Run("a socket is found", func(t *testing.T) {
		// A plain unix listener, so this half of the guard holds with no tmux.
		root := shortTempRoot(t)
		dir := filepath.Join(root, "tmux-1000")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, "atrium")
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "unix", path)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })

		got := tmuxSocketsUnder(root)
		if len(got) != 1 || got[0] != path {
			t.Fatalf("tmuxSocketsUnder = %v, want [%s]", got, path)
		}
	})
}

// TestKillTmuxServersReapsOnlyItsOwnRoot is the property the developer's live fleet
// depends on. A teardown runs with whatever root it was handed — and #547's whole
// point is that by then the root may already be gone. tmux treats `-L <name>` with
// an empty or missing TMUX_TMPDIR as /tmp, i.e. the live socket, so a teardown that
// reached for a name instead of a path would kill every running agent. This asserts
// the opposite: a vanished root reaps nothing, and only the matching root reaps.
func TestKillTmuxServersReapsOnlyItsOwnRoot(t *testing.T) {
	RequireTmux(t)

	root := shortTempRoot(t)
	sock := startProbeServer(t, root)

	// A root that is empty, one already removed, and /tmp itself — the states a
	// teardown actually runs in, plus the one that would be catastrophic. None may
	// touch the live server in root.
	removed := shortTempRoot(t)
	if err := os.RemoveAll(removed); err != nil {
		t.Fatalf("remove: %v", err)
	}
	killTmuxServers("")
	killTmuxServers(removed)
	killTmuxServers(tmuxRootParent)

	if !probeServerAlive(t, sock) {
		t.Fatalf("a server in %q died from a kill aimed at an empty, a removed and a shared root: "+
			"the teardown is reaching outside the root it was given (#581)", root)
	}

	// Asserted before the socket is gone, and asserted at all, because it is what the
	// callers gate their rm on: reporting "all reaped" while a server still listens is
	// what would let a teardown unlink a live server's only address (#547).
	if !killTmuxServers(root) {
		t.Fatalf("killTmuxServers(%q) reported an unreaped server after killing the only one there", root)
	}

	if probeServerAlive(t, sock) {
		t.Fatalf("server on %q survived killTmuxServers(%q): the teardown does not reap its own root", sock, root)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %q outlived its server: tmux never unlinks it, so the kill must", sock)
	}
}

// TestKillTmuxServersKeepsTheSocketOfAServerItCouldNotReap is the other half of the
// #547 guarantee, and the half a passing teardown hides. Unlinking the socket of a
// server that is still listening leaves a server nothing can address — not `tmux
// ls`, not `atrium reset`, not the next run's sweep — so the kill's *outcome*, never
// its exit status, has to decide whether the file goes.
//
// The failure is forced with no tmux on PATH at all: the kill cannot be sent, the
// server keeps running, and a teardown that removed the socket anyway would strand
// it. Same shape as a server that outlasts tmuxKillTimeout, without needing to wedge
// one.
func TestKillTmuxServersKeepsTheSocketOfAServerItCouldNotReap(t *testing.T) {
	RequireTmux(t)

	// Resolved before PATH is scrubbed, so the liveness check below still has a tmux
	// to ask. Asking through tmux rather than through tmuxServerAlive keeps the
	// assertion independent of the helper the code under test uses to decide.
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := shortTempRoot(t)
	sock := startProbeServer(t, root)

	// A PATH with no tmux in it; t.Setenv restores it when the test ends, which is
	// before the cleanups that reap the probe (they were registered earlier, so LIFO
	// runs them after).
	t.Setenv("PATH", t.TempDir())

	if killTmuxServers(root) {
		t.Fatal("killTmuxServers reported every server reaped while tmux was unreachable: " +
			"its callers take that as licence to delete the root")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket %q was unlinked from under a server that is still listening: "+
			"that is the unreachable orphan #547 is about, not a fix for it", sock)
	}
	if exec.CommandContext(context.Background(), tmuxBin, "-S", sock, "list-sessions").Run() != nil {
		t.Fatalf("probe server on %q is gone: with no tmux to send the kill it should have "+
			"survived, so this is not exercising the unreaped path", sock)
	}
}

// TestTmuxRootIsStale covers the sweep's decision. It is the difference between
// reaping a dead run's immortal $SHELL server and deleting the root out from under
// a sibling package that `go test ./...` started a moment ago.
//
// Its fixtures deliberately live under t.TempDir() rather than shortTempRoot's
// /tmp/atrium-tmux-*, because they are the one thing in this file that must NOT be
// sweepable: several of them are built to look stale — backdated, or marked with an
// exited pid — and a concurrent package's startup sweep globs exactly that namespace.
// One landing between the fixture and the assertion would delete it and invert the
// answer. rootIsStale never looks at the prefix (only sweepStaleTmuxRoots does,
// before calling it), so the shorter path costs the coverage nothing.
func TestTmuxRootIsStale(t *testing.T) {
	unswept := func(t *testing.T) string {
		t.Helper()
		return t.TempDir()
	}
	aged := func(t *testing.T, pid string) string {
		t.Helper()
		root := unswept(t)
		if pid != "" {
			if err := os.WriteFile(filepath.Join(root, ownerMarkerFile), []byte(pid), 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
		}
		old := time.Now().Add(-2 * rootGrace)
		if err := os.Chtimes(root, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		return root
	}

	t.Run("a fresh root with no marker is not stale", func(t *testing.T) {
		// The race the grace window exists for, and the only state it is for: MkdirTemp
		// has returned but the marker is not written yet, and a sibling binary is
		// sweeping right now.
		if rootIsStale(unswept(t)) {
			t.Fatal("a just-created root with no marker was swept: a concurrent `go test ./...` " +
				"package would lose its socket root mid-run")
		}
	})

	t.Run("a fresh root owned by a dead process is stale", func(t *testing.T) {
		// Age must not outrank a marker. A crashed run's root gets its mtime bumped by
		// whatever touched it last — its own teardown's removeContents, a tmux server
		// creating tmux-<uid> — so gating on age first would keep the immortal $SHELL
		// server this sweep exists for alive through every attempt at it.
		root := unswept(t)
		if err := os.WriteFile(filepath.Join(root, ownerMarkerFile),
			[]byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if !rootIsStale(root) {
			t.Fatal("a freshly touched root whose owner has exited was not reported stale: " +
				"a marker naming a dead pid is proof, and no amount of recent mtime unmakes it")
		}
	})

	t.Run("a marker naming another user's live process is not stale", func(t *testing.T) {
		// pid 1 always exists and (unless this runs as root) signal 0 to it returns
		// EPERM — "there, but not yours". Reading that as "gone" would let one user's
		// sweep kill and delete another user's live root.
		if !processAlive(1) {
			t.Fatal("processAlive(1) is false: EPERM from signal 0 means the process exists, " +
				"not that it is gone")
		}
		root := unswept(t)
		if err := os.WriteFile(filepath.Join(root, ownerMarkerFile), []byte("1"), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		old := time.Now().Add(-2 * rootGrace)
		if err := os.Chtimes(root, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if rootIsStale(root) {
			t.Fatal("a root owned by a live process this user cannot signal was reported stale")
		}
	})

	t.Run("an aged root whose marker cannot be read is not stale", func(t *testing.T) {
		// The other-user case, and the one an age-only fallback gets wrong. Another
		// user's root is 0700, so the read fails with EACCES rather than ENOENT — and
		// "unreadable" is not evidence of anything, least of all that the owner is gone.
		// Forced here with a directory in the marker's place (EISDIR), which fails the
		// read identically for every user including root; a chmod-000 file would not,
		// since root can read it and the assertion would invert in a root container.
		root := unswept(t)
		if err := os.Mkdir(filepath.Join(root, ownerMarkerFile), 0o700); err != nil {
			t.Fatalf("mkdir marker: %v", err)
		}
		old := time.Now().Add(-2 * rootGrace)
		if err := os.Chtimes(root, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if rootIsStale(root) {
			t.Fatal("a root whose owner marker could not be read was reported stale on age " +
				"alone: that is how one user's sweep decides another user's live root is an orphan")
		}
	})

	t.Run("an aged root owned by a live process is not stale", func(t *testing.T) {
		if rootIsStale(aged(t, strconv.Itoa(os.Getpid()))) {
			t.Fatal("a root owned by this very process was reported stale")
		}
	})

	t.Run("an aged root with no marker is stale", func(t *testing.T) {
		if !rootIsStale(aged(t, "")) {
			t.Fatal("an aged root with no owner recorded was not reported stale")
		}
	})

	t.Run("an aged root owned by a dead process is stale", func(t *testing.T) {
		if !rootIsStale(aged(t, strconv.Itoa(deadPID(t)))) {
			t.Fatal("a root owned by an exited process was not reported stale")
		}
	})

	t.Run("a missing root is not stale", func(t *testing.T) {
		// Nothing to reap, and reporting true would send killTmuxServers at a path
		// that no longer exists.
		if rootIsStale(filepath.Join(tmuxRootParent, tmuxRootPrefix+"gone")) {
			t.Fatal("a missing root was reported stale")
		}
	})
}

// deadPID returns the pid of a process that has exited, so processAlive has a
// definite negative to answer. Reusing a random number would risk hitting a live
// pid and inverting the assertion.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	return cmd.Process.Pid
}

// shortTempRoot mints a socket root the same way the sandbox does, rather than via
// t.TempDir(): a directory named after this test would push the socket path past
// sun_path, and a name without the sandbox prefix is refused by tmuxSocketsUnder.
// See installSandboxTmuxTmpdir.
func shortTempRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(tmuxRootParent, tmuxRootPrefix)
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// startProbeServer starts a throwaway tmux server whose socket lives under root,
// and returns that socket's absolute path. The root is passed through the child's
// environment rather than os.Setenv so this cannot disturb the sandbox the rest of
// the binary is running in, and the pane is short-lived so a failed assertion
// bounds what it strands.
func startProbeServer(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "tmux", "-L", "probe", "new-session", "-d", "sleep 10")
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start probe tmux server: %v: %s", err, out)
	}

	socks := tmuxSocketsUnder(root)
	if len(socks) != 1 {
		t.Fatalf("probe server left %v sockets under %q, want exactly 1", socks, root)
	}
	// Every later kill in this test addresses the socket by path, so the probe is
	// reaped even if an assertion fails first.
	t.Cleanup(func() { killTmuxServers(root) })
	return socks[0]
}

// probeServerAlive reports whether a server is still listening on sock, addressed
// by absolute path so the answer cannot come from the shared socket directory.
func probeServerAlive(t *testing.T, sock string) bool {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "tmux", "-S", sock, "list-sessions")
	// Empty TMUX_TMPDIR on purpose: with -S it changes nothing, and if that ever
	// stopped being true this test would start reporting on the shared directory.
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR=")
	return cmd.Run() == nil
}
