package tmux

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestManagedConfigParsesUnderRealTmux feeds the rendered managed config to a real
// tmux via `source-file` and asserts a clean parse. This is the regression guard for
// the `\E`/`\7` clipboard-override bug (`atrium.conf:NN: invalid octal escape`): the
// pre-existing string-only render test never asked tmux to parse the file, so the bad
// escape shipped.
//
// It must use `source-file`, NOT `new-session -d -f <conf>`. A detached new-session
// returns success and defers any config parse error until a client attaches, so a
// new-session-based check would false-pass the broken config. We build the tmux
// commands directly (rather than via tmuxCommand) precisely because we need a
// throwaway socket and an explicit `-f`-less probe server that we control.
func TestManagedConfigParsesUnderRealTmux(t *testing.T) {
	testutil.RequireTmux(t)
	for _, contextBar := range []bool{true, false} {
		t.Run(fmt.Sprintf("contextBar=%v", contextBar), func(t *testing.T) {
			rendered, err := renderManagedConfig(contextBar)
			if err != nil {
				t.Fatalf("renderManagedConfig(%v): %v", contextBar, err)
			}
			path := filepath.Join(t.TempDir(), "atrium.conf")
			if err := os.WriteFile(path, rendered, 0o644); err != nil {
				t.Fatalf("write rendered config: %v", err)
			}

			// tmux puts the socket under $TMUX_TMPDIR (default /tmp — it ignores
			// TMPDIR), and never unlinks the file when the server dies, so a probe
			// socket outlives kill-server. The package's TestMain points TMUX_TMPDIR
			// at a private root and reaps it (testutil.SandboxHomeMain), which takes
			// this socket with it instead of leaving it in the shared /tmp/tmux-<uid>
			// next to Atrium's live one. That root is where the sun_path budget is
			// documented; this test only asserts it took effect.
			tmuxTmp := testutil.TmuxRoot(t)

			// No '/' in the socket name: tmux reads -L as a path under
			// $TMUX_TMPDIR/tmux-<uid>, and a slash (t.Name carries the subtest path)
			// would point at a missing dir.
			//
			// Brand-prefixed so ownsSocketName claims it, which is what puts a *live*
			// server left on this socket inside `atrium doctor` and `atrium reap` (#602):
			// both read the /proc-based ScanServers, which finds a server by its socket
			// path wherever that path is. The bare "cfgparse-" it used to be matched
			// neither brand exactly nor a brand followed by "-", so such a server was
			// invisible to the very tooling #547 built to find strays. Note the limit of
			// what this buys: the leftover socket *file* stays out of the stale-socket
			// list whatever it is called, since ScanStaleSockets reads only SocketDir and
			// never this sandbox root. So the exposure closed is bounded by the server's
			// life — `sleep 60` under tmux's default exit-empty, holding nothing. Small,
			// but a test socket Atrium's own predicate disowns is a gap in the predicate's
			// coverage, not a property worth keeping. The cost is the mirror image, and
			// intended: a `reap` run concurrent with this suite can now claim this probe,
			// which fails the test run rather than damaging anything.
			//
			// Derived from socketName() rather than a literal "atrium-", per CLAUDE.md's
			// rule for anything naming the socket, and matching probeSocketName's
			// "<brand>-precheck-<pid>-<n>" next door. Under a sandbox HOME that resolves
			// to "atrium"; a legacy brand only lengthens it, and ownsSocketName claims
			// both unconditionally.
			//
			// The per-run random suffix is what still makes `-L` safe here, and it has
			// to stay: the prefix is now one a live server could answer to, so the
			// randomness is the whole of the separation. See CLAUDE.md.
			sock := fmt.Sprintf("%s-cfgparse-%d", socketName(), rand.Int31())
			// Pins the prefix rather than trusting the comment above it. Dropping it
			// leaves every other assertion in this file passing, which is how the socket
			// came to be disowned in the first place.
			if !ownsSocketName(sock) {
				t.Fatalf("socket name %q is not one ownsSocketName claims, so a server left "+
					"on it would be invisible to `atrium doctor` and `atrium reap` (#602)", sock)
			}
			ctx := context.Background()
			// Armed before the server starts, so a new-session that half-succeeds —
			// server up, command failed — still has something to tear it down. Nothing
			// asserts this ordering; it only pays out on a path the test cannot force,
			// and the rule it follows is CLAUDE.md's rather than a guard's.
			defer func() { _ = exec.CommandContext(ctx, "tmux", "-L", sock, "kill-server").Run() }()
			// Clean probe server (no -f) kept alive by a session so source-file has a
			// target. Never the live socket.
			if out, err := exec.CommandContext(ctx, "tmux", "-L", sock, "new-session", "-d", "sleep 60").CombinedOutput(); err != nil {
				t.Fatalf("start probe tmux server: %v: %s", err, out)
			}

			// Prove TMUX_TMPDIR took effect rather than being silently ignored: the
			// live server's socket must sit somewhere under the sandbox root.
			// Searching by name keeps this off tmux's socket-dir layout, which the -L
			// comment above deliberately leaves to tmux.
			//
			// Since the root is now the package's rather than this test's, this is the
			// guard that the shared isolation is real end to end — testutil's own
			// tests prove the variable is set, and this proves tmux honours it.
			if !containsFile(t, tmuxTmp, sock) {
				t.Fatalf("probe socket %q not found under TMUX_TMPDIR %q: the socket is leaking into the shared socket dir", sock, tmuxTmp)
			}

			out, err := exec.CommandContext(ctx, "tmux", "-L", sock, "source-file", path).CombinedOutput()
			if msg := strings.TrimSpace(string(out)); err != nil || msg != "" {
				t.Fatalf("tmux rejected the rendered managed config (contextBar=%v): err=%v msg=%q\n---\n%s",
					contextBar, err, msg, rendered)
			}
		})
	}
}

// containsFile reports whether any entry named name exists anywhere under root.
func containsFile(t *testing.T, root, name string) bool {
	t.Helper()
	found := false
	if err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = true
			return fs.SkipAll
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}
