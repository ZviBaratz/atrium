//go:build windows

package session

import "os/exec"

// isolateProcessGroup is a no-op on Windows, which has no POSIX process group to
// signal: os/exec's default cancel (Process.Kill on the shell alone) stands, and
// setupWaitDelay is what keeps Wait from blocking forever on an output pipe a
// surviving descendant still holds open.
func isolateProcessGroup(*exec.Cmd) {}
