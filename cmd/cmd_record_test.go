package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmdlog"
)

// The real executor records every subprocess it runs into the command log (#372),
// without changing the return value. This is the seam that covers the tmux layer.
func TestExec_RecordsSubprocesses(t *testing.T) {
	cmdlog.Reset()
	e := MakeExecutor()

	if err := e.Run(exec.CommandContext(context.Background(), "true")); err != nil {
		t.Fatalf("Run(true): %v", err)
	}
	out, err := e.Output(exec.CommandContext(context.Background(), "echo", "hi"))
	if err != nil {
		t.Fatalf("Output(echo): %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hi" {
		t.Errorf("Output returned %q, want %q — recording must not alter the result", got, "hi")
	}

	snap := cmdlog.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 recorded commands, got %d: %+v", len(snap), snap)
	}
	// Newest first.
	if !strings.Contains(snap[0].Argv, "echo hi") {
		t.Errorf("newest record argv = %q, want it to contain %q", snap[0].Argv, "echo hi")
	}
	if !strings.Contains(snap[1].Argv, "true") {
		t.Errorf("oldest record argv = %q, want it to contain %q", snap[1].Argv, "true")
	}
}
