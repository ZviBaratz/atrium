package tmux

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// PtyFactory creates the pseudo-terminal a tmux client (attach) runs in. It
// exists so tests can substitute a fake pty instead of allocating a real one.
type PtyFactory interface {
	Start(cmd *exec.Cmd) (*os.File, error)
	Close()
}

// Pty starts a "real" pseudo-terminal (PTY) using the creack/pty package.
type Pty struct{}

// Start launches cmd inside a new pty and returns its master end. It also
// spawns a goroutine that Waits on cmd so the tmux client is reaped instead of
// parking as a zombie (#362). The goroutine is fire-and-forget: it is not
// tracked by any WaitGroup and self-terminates when the client exits (normally
// once a caller closes the returned master). Wait's error is discarded because
// the exit status carries no signal here — a closed master routinely surfaces
// as "signal: hangup".
func (pt Pty) Start(cmd *exec.Cmd) (*os.File, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return ptmx, nil
}

// Close is a no-op: the real pty's lifetime is tied to the file returned by
// Start, which callers close themselves.
func (pt Pty) Close() {}

// MakePtyFactory returns the real pty-backed PtyFactory.
func MakePtyFactory() PtyFactory {
	return Pty{}
}
