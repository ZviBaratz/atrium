//go:build windows

package daemon

import (
	"golang.org/x/sys/windows"
	"os"
	"syscall"
)

// getSysProcAttr returns platform-specific process attributes for detaching the child process
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// terminateProcess stops the daemon. Windows has no SIGTERM equivalent that Go's
// os/signal delivers to a detached process group, so there is no graceful
// shutdown hook to trip; fall back to an immediate kill (the prior behavior).
func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}
