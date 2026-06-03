package tmux

import (
	"context"
	"os/exec"
	"time"
)

// Subprocess budgets for tmux commands. Short operations (capture-pane,
// has-session, kill-session, rename) run under tmuxOpTimeout so a hung tmux
// server can't wedge the poll loop; long-lived pty clients (new-session,
// attach-session) run under the session's bare base context instead.
const (
	tmuxOpTimeout = 10 * time.Second
	// probeTimeout bounds one-shot feature-detection subprocesses (claude --help).
	probeTimeout = 10 * time.Second
)

// tmuxCommand builds a tmux *exec.Cmd under ctx with cs's isolation flags prepended
// ahead of the subcommand: `tmux -L <socket> [-f <conf>] <args...>`. -L and -f are
// global (server) options and must precede the subcommand, so they are always
// prepended, never appended. The returned command works for both execution paths in
// this package: ptyFactory.Start(cmd) and cmdExec.Run/Output(cmd). Cancelling ctx
// kills the subprocess, so callers pick the lifetime: opContext for short
// operations, the bare base context for pty clients that must outlive the call.
func tmuxCommand(ctx context.Context, args ...string) *exec.Cmd {
	full := []string{"-L", socketName()}
	if conf := tmuxConfigPath(); conf != "" {
		full = append(full, "-f", conf)
	}
	return exec.CommandContext(ctx, "tmux", append(full, args...)...)
}
