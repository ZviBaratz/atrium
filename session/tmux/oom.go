package tmux

import (
	"fmt"
	"sync/atomic"
)

// agentOOMMargin is the process-wide OOM margin every session's launch command
// applies (see wrapOOMScore). It is a package var rather than a per-session
// parameter because the margin is one global policy — config.GetAgentOOMMargin —
// that every launch (new, resume, recreate) must pick up uniformly. start() reads
// it afresh on each launch, so a mid-run Settings change reaches any session the
// user then relaunches (pause → resume, or a pane recreate), not only brand-new
// ones. Its zero value is the disabled state, so a process that never sets it is
// safely unprotected rather than broken. It is atomic because the Settings panel
// may re-set it on the UI thread while a session's start() reads it on a background
// goroutine.
var agentOOMMargin atomic.Int64

// SetAgentOOMMargin sets the process-wide OOM margin sessions apply at launch. Call
// once at startup (from config.GetAgentOOMMargin) and again whenever the setting
// changes; every launch reads the current value in start().
func SetAgentOOMMargin(margin int) { agentOOMMargin.Store(int64(margin)) }

// wrapOOMScore prefixes program with a POSIX-sh snippet that raises the agent's
// Linux oom_score_adj by margin points before exec'ing the agent, so under memory
// pressure the kernel OOM killer sheds a single (recoverable) agent before the
// shared tmux server — which holds every session and would otherwise be next in
// line because its tiny RSS is dwarfed by the oom_score_adj it inherited.
//
// The snippet runs inside the pane's `sh -c`, so /proc/self is that shell, which
// inherited the server's oom_score_adj by fork — reading it yields the server's
// current value, and adding a fixed margin ranks the agent above the server
// regardless of the baseline the launcher happened to hand us. The write can only
// ever raise (an unprivileged process may not lower oom_score_adj), so it never
// hits EACCES. The final `exec` replaces the shell with the agent, preserving the
// pane's process topology; the raised score survives execve and is inherited by
// the agent's own children.
//
// margin is a delta, not an absolute target: the resolved config.GetAgentOOMMargin.
// It returns program unchanged when margin <= 0 (the feature is disabled) or the
// host is not Linux (no /proc/<pid>/oom_score_adj exists). Every step of the
// snippet tolerates failure (2>/dev/null, ||) so an OOM tweak can never block a
// session from starting.
func wrapOOMScore(program string, margin int, goos string) string {
	if margin <= 0 || goos != "linux" {
		return program
	}
	return fmt.Sprintf(
		`a=$(cat /proc/self/oom_score_adj 2>/dev/null||echo 0); t=$((a+%d)); `+
			`[ "$t" -gt 1000 ] && t=1000; echo "$t" >/proc/self/oom_score_adj 2>/dev/null; exec %s`,
		margin, program)
}
