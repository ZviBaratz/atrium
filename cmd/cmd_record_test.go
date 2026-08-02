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

// ToString is documented as feeding "logging and error messages", which makes it a
// path from a raw argv to text a human reads — the same class as cmdlog.Redact and
// verbOf, and the reason it scrubs rather than joining the argv itself.
//
// Nothing routes a token-bearing argv here today: the one command carrying one is
// started on a pty, and this has a single caller. That is a fact about the caller
// set, so the guard drives the argv that WOULD leak rather than one a caller
// produces now.
//
// The second case is the negative control — an ordinary argv must survive intact,
// or "no secret in the output" would hold because nothing survives.
func TestToString_ScrubsASecretItWouldOtherwiseRender(t *testing.T) {
	c := exec.CommandContext(context.Background(),
		"tmux", "-L", "atrium", "new-session", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN=ghp_supersecretvalue")

	got := ToString(c)
	if strings.Contains(got, "ghp_supersecretvalue") {
		t.Errorf("ToString leaked the token verbatim: %q", got)
	}
	if want := "tmux -L atrium new-session -e GITHUB_PERSONAL_ACCESS_TOKEN=***"; got != want {
		t.Errorf("ToString = %q, want %q", got, want)
	}

	plain := exec.CommandContext(context.Background(), "git", "-C", "/repo", "status")
	if got, want := ToString(plain), "git -C /repo status"; got != want {
		t.Errorf("ToString = %q, want %q — an ordinary argv must pass through", got, want)
	}
	if got, want := ToString(nil), "<nil>"; got != want {
		t.Errorf("ToString(nil) = %q, want %q", got, want)
	}
}
