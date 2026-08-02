// Package cmd defines the Executor abstraction through which all tmux and git
// subprocesses are launched, so tests can substitute a fake executor instead of
// shelling out.
package cmd

import (
	"os/exec"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
)

// Executor runs external commands. The tmux and git layers depend on this
// interface rather than os/exec directly so tests can fake subprocess results.
type Executor interface {
	Run(cmd *exec.Cmd) error
	Output(cmd *exec.Cmd) ([]byte, error)
}

// Exec is the real Executor: it delegates straight to os/exec.
type Exec struct{}

// Run executes the command and waits for it to complete. Every launch is recorded
// into the command log as a side effect (#372); recording never changes the
// return value or blocks on I/O. Session attribution is empty at this generic
// seam — the tmux argv itself names the target session.
func (e Exec) Run(cmd *exec.Cmd) error {
	start := time.Now()
	err := cmd.Run()
	cmdlog.RecordCmd(cmd, "", start, nil, err)
	return err
}

// Output executes the command and returns its standard output. On an ExitError,
// the command log reads the OS-captured stderr for the failure tail.
func (e Exec) Output(cmd *exec.Cmd) ([]byte, error) {
	start := time.Now()
	out, err := cmd.Output()
	cmdlog.RecordCmd(cmd, "", start, nil, err)
	return out, err
}

// MakeExecutor returns the real subprocess-backed Executor.
func MakeExecutor() Executor {
	return Exec{}
}

// ToString renders a command as its space-joined argv, for logging and error
// messages.
//
// It goes through cmdlog.Redact rather than joining the argv itself, because
// "logging and error messages" is exactly the destination a secret must not reach
// verbatim. Nothing routes a token-bearing argv here today — the one command
// carrying one is started on a pty and this has a single caller (notify's failed
// desktop command) — but that is a fact about the caller set, not about this
// function, and a caller set is what the next call site changes.
func ToString(cmd *exec.Cmd) string {
	if cmd == nil {
		return "<nil>"
	}
	return cmdlog.Redact(cmd.Args)
}
