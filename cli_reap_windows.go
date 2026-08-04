//go:build windows

package main

import (
	"errors"
	"syscall"
)

// errReapUnsupported is returned by the signalling stubs below. They are
// unreachable: runReap returns early when tmux.ScanServers reports the platform
// unsupported, which it does everywhere but Linux, and Windows is not Linux. The
// stubs exist to keep the shared code compiling — Atrium's tmux-based runtime does
// not target Windows, and the build exists for completeness (see
// daemon/daemon_windows.go).
var errReapUnsupported = errors.New("reaping tmux servers is not supported on Windows")

func signalProcess(int, syscall.Signal) error { return errReapUnsupported }

func processAlive(int) bool { return false }
