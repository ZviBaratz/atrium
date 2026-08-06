//go:build !windows

package session

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the setup script in a process group of its own and points
// the command's cancellation at that GROUP rather than at the shell alone.
//
// Both halves exist for one reason: `sh -c` forks for anything compound
// (`npm ci && npm run build`) and for anything backgrounded, so the process a cancel
// can reach is usually not the process doing the work. Killing only the shell leaves
// the grandchild running — and, because it inherited the output pipe, leaves Cmd.Wait
// blocked until that grandchild exits on its own. That is precisely the hang
// AbortSetupScript exists to prevent: the drain times out, the session is "left as-is"
// with a worktree and branch that never reached state.json, and `npm ci` outlives the
// app that started it.
//
// Only the cancel path kills the group. A script that exits on its own having
// deliberately left something behind (`ollama serve &`) keeps it; there the bound is
// setupWaitDelay, which closes the pipes rather than killing anything.
func isolateProcessGroup(c *exec.Cmd) {
	// Setpgid with no Pgid makes the child its own group leader, so the group id is its
	// pid and every descendant that does not create a group of its own lands in it.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		// The negative pid addresses the whole group. SIGKILL rather than SIGTERM: this
		// runs on a shutdown path with a bounded drain to make, and a build tool that
		// traps SIGTERM to tidy up would spend that budget doing it.
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// Already gone, which is the outcome the cancel wanted. Reported as
			// ErrProcessDone because os/exec treats any other error from Cancel as the
			// command's result, replacing the process's own.
			return os.ErrProcessDone
		}
		return err
	}
}
