package tmux

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
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

			// No '/' in the socket name: tmux reads -L as a path under /tmp/tmux-<uid>,
			// and a slash (t.Name carries the subtest path) would point at a missing dir.
			sock := fmt.Sprintf("cfgparse-%d", rand.Int31())
			ctx := context.Background()
			// Clean probe server (no -f) kept alive by a session so source-file has a
			// target. Never the live socket.
			if out, err := exec.CommandContext(ctx, "tmux", "-L", sock, "new-session", "-d", "sleep 60").CombinedOutput(); err != nil {
				t.Fatalf("start probe tmux server: %v: %s", err, out)
			}
			defer func() { _ = exec.CommandContext(ctx, "tmux", "-L", sock, "kill-server").Run() }()

			out, err := exec.CommandContext(ctx, "tmux", "-L", sock, "source-file", path).CombinedOutput()
			if msg := strings.TrimSpace(string(out)); err != nil || msg != "" {
				t.Fatalf("tmux rejected the rendered managed config (contextBar=%v): err=%v msg=%q\n---\n%s",
					contextBar, err, msg, rendered)
			}
		})
	}
}
