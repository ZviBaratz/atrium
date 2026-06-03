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

// Start launches cmd inside a new pty and returns its master end.
func (pt Pty) Start(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}

// Close is a no-op: the real pty's lifetime is tied to the file returned by
// Start, which callers close themselves.
func (pt Pty) Close() {}

// MakePtyFactory returns the real pty-backed PtyFactory.
func MakePtyFactory() PtyFactory {
	return Pty{}
}
