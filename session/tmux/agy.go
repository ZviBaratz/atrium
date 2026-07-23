package tmux

import (
	"fmt"
	"os/exec"

	"github.com/ZviBaratz/atrium/log"
)

// agyGeminiConfigPath is the Antigravity CLI's own config directory, kept as a
// double-quoted shell word so $HOME expands in the pane's `sh` at launch — not at
// the time Atrium builds the command, when HOME may differ (e.g. the daemon) or be
// unset. bwrap bind-mounts a routed account's dir over this path.
const agyGeminiConfigPath = `"$HOME/.gemini/antigravity-cli"`

// wrapAgyBwrap wraps program in a bwrap (bubblewrap) invocation that bind-mounts
// configDir over the Antigravity CLI's own config directory
// ($HOME/.gemini/antigravity-cli), so a session routed to an agy_account runs under
// that account's config instead of the ambient one. `--dev-bind / /` keeps the rest
// of the filesystem (with device nodes) visible; only the one path is overlaid.
//
// It returns program unchanged when there is nothing to isolate (configDir is ""),
// the host is not Linux (bwrap is a Linux user-namespace tool that does not exist on
// macOS — a no-op there, mirroring wrapOOMScore's own GOOS guard), or bwrap is not
// installed. Like the OOM and gh-token wraps in start(), isolation can never block a
// session from starting: a missing bwrap logs a warning and launches without
// isolation rather than failing the launch outright.
//
// It must run on the bare agent program BEFORE wrapOOMScore, which rewrites program
// into a shell snippet (`…; exec <program>`) whose leading token is no longer the
// agent — wrapping (or matching) after that never fires. The OOM snippet then wraps
// this bwrap command and exec's it, while the raised oom_score_adj (written pre-exec
// and inherited across execve and the user namespace) still protects the agent.
func wrapAgyBwrap(program, configDir, goos string) string {
	if configDir == "" || goos != "linux" {
		return program
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		log.WarningLog.Printf("agy: bwrap not found on PATH; launching without config-dir isolation for %q (%v)", configDir, err)
		return program
	}
	return fmt.Sprintf("%s --dev-bind / / --bind %s %s %s",
		bwrapPath, shellSingleQuote(configDir), agyGeminiConfigPath, program)
}
