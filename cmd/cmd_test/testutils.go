// Package cmd_test provides a mock cmd.Executor shared by tests that need to
// fake tmux/git subprocess results instead of shelling out.
package cmd_test

import (
	"os/exec"
)

// MockCmdExec implements cmd.Executor with caller-supplied functions, letting
// each test script the result of every subprocess invocation.
type MockCmdExec struct {
	RunFunc    func(cmd *exec.Cmd) error
	OutputFunc func(cmd *exec.Cmd) ([]byte, error)
}

// Run invokes the test-supplied RunFunc.
func (e MockCmdExec) Run(cmd *exec.Cmd) error {
	return e.RunFunc(cmd)
}

// Output invokes the test-supplied OutputFunc.
func (e MockCmdExec) Output(cmd *exec.Cmd) ([]byte, error) {
	return e.OutputFunc(cmd)
}
