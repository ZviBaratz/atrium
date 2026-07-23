package tmux

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// On Linux with a routed config dir (and bwrap installed), the program is wrapped in
// a bwrap invocation that bind-mounts the account dir over the Antigravity CLI's own
// config path and runs the original program inside.
func TestWrapAgyBwrap_LinuxWrapsWithBind(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("bwrap is Linux-only; GOOS=%s", runtime.GOOS)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	const program = "agy --continue"
	got := wrapAgyBwrap(program, "/home/u/.agy-work", "linux")

	if got == program {
		t.Fatal("expected the program to be wrapped on linux with a config dir")
	}
	if !strings.Contains(got, "bwrap") {
		t.Errorf("wrapper does not invoke bwrap: %q", got)
	}
	if !strings.Contains(got, "--bind '/home/u/.agy-work'") {
		t.Errorf("wrapper does not bind the account config dir: %q", got)
	}
	if !strings.Contains(got, `"$HOME/.gemini/antigravity-cli"`) {
		t.Errorf("wrapper does not overlay the Antigravity config path (left unquoted so $HOME expands at launch): %q", got)
	}
	// The original program must be the command bwrap runs, verbatim, at the end.
	if !strings.HasSuffix(got, program) {
		t.Errorf("wrapper does not run the original program inside bwrap: %q", got)
	}
}

// The wrapper is a no-op off Linux (bwrap does not exist on macOS/Windows — the
// macOS regression this guards) or without a routed dir: the program is returned
// exactly as given, so a launch is never turned into an unrunnable `bwrap …` there.
func TestWrapAgyBwrap_NonLinuxOrNoDirUnchanged(t *testing.T) {
	const program = "agy"
	cases := []struct {
		name string
		dir  string
		goos string
	}{
		{"darwin with dir", "/home/u/.agy-work", "darwin"},
		{"windows with dir", "/home/u/.agy-work", "windows"},
		{"linux without dir", "", "linux"},
		{"darwin without dir", "", "darwin"},
	}
	for _, c := range cases {
		if got := wrapAgyBwrap(program, c.dir, c.goos); got != program {
			t.Errorf("%s: got %q, want unchanged %q", c.name, got, program)
		}
	}
}

// A single-quote in the config dir is escaped so the wrapped command stays valid,
// injection-safe shell (the path is a start()-time value, but defense in depth).
func TestWrapAgyBwrap_QuotesConfigDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("bwrap is Linux-only; GOOS=%s", runtime.GOOS)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	got := wrapAgyBwrap("agy", "/tmp/a'b", "linux")
	parseOnly := exec.CommandContext(context.Background(), "sh", "-n", "-c", got)
	require.NoError(t, parseOnly.Run(), "wrapped command must be valid shell: %q", got)
}

// End to end through start(): an agy session with a routed config dir wraps its
// launch command in bwrap, and — crucially — the wrap survives the default OOM
// margin (the regression: the OOM snippet used to rewrite `program` before the old
// bwrap check ran, so it never matched). The OOM raise must end up OUTSIDE the bwrap
// command (`… exec <bwrap> …`), so the agent still inherits the raised score.
func TestStartWrapsAgyInBwrapUnderDefaultOOMMargin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("bwrap is Linux-only; GOOS=%s", runtime.GOOS)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	prev := agentOOMMargin.Load()
	t.Cleanup(func() { agentOOMMargin.Store(prev) })
	SetAgentOOMMargin(300) // the on-by-default margin that used to defeat the bwrap check

	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "agy-on", "agy", ptyFactory, startMockExec())
	session.SetAgyConfigDir("/home/u/.agy-work")

	require.NoError(t, session.Start(t.TempDir()))

	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.Contains(t, program, "bwrap", "agy launch must be bwrap-wrapped even with the default OOM margin on")
	require.Contains(t, program, "/home/u/.agy-work")
	require.Contains(t, program, "/proc/self/oom_score_adj", "the OOM raise must still apply")
	// The OOM raise wraps the bwrap command (exec's it), not the other way round.
	require.Regexp(t, `exec .*bwrap`, program, "OOM snippet must exec the bwrap command")
	parseOnly := exec.CommandContext(context.Background(), "sh", "-n", "-c", program)
	require.NoError(t, parseOnly.Run(), "wrapped launch command must be valid shell: %q", program)
}

// A non-agy session with no routed dir is never bwrap-wrapped.
func TestStartDoesNotWrapNonAgy(t *testing.T) {
	prev := agentOOMMargin.Load()
	t.Cleanup(func() { agentOOMMargin.Store(prev) })
	SetAgentOOMMargin(0)

	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "claude-plain", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.NotContains(t, program, "bwrap")
}
