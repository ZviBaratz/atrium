package tmux

// TEMPORARY diagnostic for issue #305 — DO NOT MERGE.
//
// TestEnsureSessionReapsLegacyTermSession fails only on macOS because the
// terminal pane launches $SHELL (not a long-running `sleep`) and that session
// never appears within start()'s 2s existence poll. This probe reproduces the
// terminal-pane new-session invocation faithfully (same managed config, same
// -e args) but with `remain-on-exit on` appended, so a shell that exits leaves
// an inspectable corpse (pane_dead / pane_dead_status / captured output) instead
// of destroying its single-window session before we can look. A `sleep 300`
// control proves the harness itself is sound on the same runner.
//
// Gated on ATRIUM_DIAG_305=1 so it never runs in a normal suite. Pure logging,
// no assertions: run with `go test -v` and read the DIAG305 lines from CI logs.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDiag305TerminalShellStartup(t *testing.T) {
	if os.Getenv("ATRIUM_DIAG_305") != "1" {
		t.Skip("diagnostic gated on ATRIUM_DIAG_305=1")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	t.Logf("DIAG305 GOOS=%s SHELL=%q", runtime.GOOS, os.Getenv("SHELL"))
	if out, err := exec.Command("tmux", "-V").CombinedOutput(); err == nil {
		t.Logf("DIAG305 tmux -V: %s", bytes.TrimSpace(out))
	}

	// Render the managed config exactly as production, then append remain-on-exit
	// on so a dead shell's pane survives for inspection (production runs with
	// remain-on-exit off, which is why the real bug destroys the session).
	rendered, err := renderManagedConfig(false)
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	rendered = append(rendered, []byte("\nset-option -g remain-on-exit on\n")...)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "diag.conf")
	if err := os.WriteFile(confPath, rendered, 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}

	sock := socketName() + "-diag305"
	tmux := func(args ...string) (string, error) {
		full := append([]string{"-L", sock, "-f", confPath}, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", full...).CombinedOutput()
		return string(bytes.TrimSpace(out)), err
	}
	defer func() { _, _ = tmux("kill-server") }()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// probe mirrors terminal.go's new-session shape (the two -e vars a terminal
	// session always gets), then reports whether the session survived the 2s poll
	// and, via remain-on-exit, the pane's dead status + captured output.
	probe := func(name, program string) {
		t.Logf("---- DIAG305 probe %s: program=%q ----", name, program)
		out, err := tmux("new-session", "-d", "-s", name, "-c", dir, "-n", "term: diag",
			"-e", "ATRIUM=1", "-e", "ATRIUM_SESSION="+name, program)
		t.Logf("DIAG305 %s new-session err=%v out=%q", name, err, out)

		deadline := time.Now().Add(2 * time.Second)
		var alive bool
		for time.Now().Before(deadline) {
			if _, e := tmux("has-session", "-t="+name); e == nil {
				alive = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Logf("DIAG305 %s has-session-within-2s(remain-on-exit ON)=%v", name, alive)

		panes, _ := tmux("list-panes", "-t", name, "-F",
			"dead=#{pane_dead} status=#{pane_dead_status} pid=#{pane_pid}")
		t.Logf("DIAG305 %s list-panes: %q", name, panes)
		capd, _ := tmux("capture-pane", "-p", "-t", name)
		t.Logf("DIAG305 %s capture-pane:\n%s", name, capd)
		ds, _ := tmux("show-options", "-g", "default-shell")
		dc, _ := tmux("show-options", "-g", "default-command")
		t.Logf("DIAG305 %s default-shell=%q default-command=%q", name, ds, dc)
		_, _ = tmux("kill-session", "-t="+name)
	}

	probe("diag_term", shell)       // failing case: $SHELL
	probe("diag_ctrl", "sleep 300") // control: known-good long-runner
}
