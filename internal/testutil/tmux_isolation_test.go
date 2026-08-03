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

	killTmuxServers(root)

	if probeServerAlive(t, sock) {
		t.Fatalf("server on %q survived killTmuxServers(%q): the teardown does not reap its own root", sock, root)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %q outlived its server: tmux never unlinks it, so the kill must", sock)
	}
}

// TestTmuxRootIsStale covers the sweep's decision. It is the difference between
// reaping a dead run's immortal $SHELL server and deleting the root out from under
// a sibling package that `go test ./...` started a moment ago.
func TestTmuxRootIsStale(t *testing.T) {
	aged := func(t *testing.T, pid string) string {
		t.Helper()
		root := shortTempRoot(t)
		if pid != "" {
			if err := os.WriteFile(filepath.Join(root, tmuxRootPIDFile), []byte(pid), 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
		}
		old := time.Now().Add(-2 * tmuxRootGrace)
		if err := os.Chtimes(root, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		return root
	}

	t.Run("a fresh root is never stale, marker or not", func(t *testing.T) {
		// The race the grace window exists for: MkdirTemp has returned but the
		// marker is not written yet, and a sibling binary is sweeping right now.
		if tmuxRootIsStale(shortTempRoot(t)) {
			t.Fatal("a just-created root with no marker was swept: a concurrent `go test ./...` " +
				"package would lose its socket root mid-run")
		}
	})

	t.Run("an aged root owned by a live process is not stale", func(t *testing.T) {
		if tmuxRootIsStale(aged(t, strconv.Itoa(os.Getpid()))) {
			t.Fatal("a root owned by this very process was reported stale")
		}
	})

	t.Run("an aged root with no marker is stale", func(t *testing.T) {
		if !tmuxRootIsStale(aged(t, "")) {
			t.Fatal("an aged root with no owner recorded was not reported stale")
		}
	})

	t.Run("an aged root owned by a dead process is stale", func(t *testing.T) {
		if !tmuxRootIsStale(aged(t, strconv.Itoa(deadPID(t)))) {
			t.Fatal("a root owned by an exited process was not reported stale")
		}
	})

	t.Run("a missing root is not stale", func(t *testing.T) {
		// Nothing to reap, and reporting true would send killTmuxServers at a path
		// that no longer exists.
		if tmuxRootIsStale(filepath.Join(tmuxRootParent, tmuxRootPrefix+"gone")) {
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
