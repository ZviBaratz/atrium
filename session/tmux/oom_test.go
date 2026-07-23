package tmux

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// On Linux with a positive margin, the program is wrapped in a shell snippet that
// raises /proc/self/oom_score_adj and then execs the original program unchanged.
func TestWrapOOMScore_LinuxWrapsWithRaiseAndExec(t *testing.T) {
	const program = "claude --settings '/tmp/x y'"
	got := wrapOOMScore(program, 300, "linux")

	if got == program {
		t.Fatal("expected the program to be wrapped on linux with a positive margin")
	}
	if !strings.Contains(got, "/proc/self/oom_score_adj") {
		t.Errorf("wrapper does not touch oom_score_adj: %q", got)
	}
	if !strings.Contains(got, "+300") {
		t.Errorf("wrapper does not add the margin 300: %q", got)
	}
	if !strings.Contains(got, "1000") {
		t.Errorf("wrapper does not clamp to the kernel max 1000: %q", got)
	}
	// The original program (args and quoting intact) must be exec'd verbatim so the
	// agent stays the pane's direct process and inherits the raised score.
	if !strings.HasSuffix(got, "exec "+program) {
		t.Errorf("wrapper does not exec the original program last: %q", got)
	}
}

// The wrapper is a no-op when disabled (margin <= 0) or off Linux (no
// oom_score_adj): the program is returned exactly as given.
func TestWrapOOMScore_DisabledOrNonLinuxUnchanged(t *testing.T) {
	const program = "claude --settings '/tmp/x'"
	cases := []struct {
		name   string
		margin int
		goos   string
	}{
		{"zero margin", 0, "linux"},
		{"negative margin", -5, "linux"},
		{"darwin", 300, "darwin"},
		{"windows", 300, "windows"},
	}
	for _, c := range cases {
		if got := wrapOOMScore(program, c.margin, c.goos); got != program {
			t.Errorf("%s: got %q, want unchanged %q", c.name, got, program)
		}
	}
}

// End to end: the emitted snippet must be valid POSIX sh that actually raises the
// running process's oom_score_adj by the margin (clamped to 1000) before exec.
func TestWrapOOMScore_RaisesActualScore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("oom_score_adj is Linux-only; GOOS=%s", runtime.GOOS)
	}
	raw, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		t.Skipf("cannot read own oom_score_adj: %v", err)
	}
	base, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse baseline %q: %v", raw, err)
	}
	// A restrictive container (seccomp/AppArmor) can forbid writing oom_score_adj,
	// which the snippet swallows by design — leaving the score unchanged and the
	// assertion unmeetable. Probe with a no-op self-raise (writing the current value
	// back is permitted whenever writes work at all) and skip if it's blocked, so
	// this proves the snippet's arithmetic without depending on a permissive sandbox.
	if err := os.WriteFile("/proc/self/oom_score_adj", raw, 0); err != nil {
		t.Skipf("environment forbids writing oom_score_adj: %v", err)
	}

	const margin = 137
	// The exec'd program prints the score this very process now carries.
	wrapped := wrapOOMScore("cat /proc/self/oom_score_adj", margin, "linux")
	out, err := exec.CommandContext(context.Background(), "sh", "-c", wrapped).CombinedOutput()
	if err != nil {
		t.Fatalf("running wrapped snippet: %v\noutput: %s", err, out)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse snippet output %q: %v", out, err)
	}

	want := base + margin
	if want > 1000 {
		want = 1000
	}
	if got != want {
		t.Fatalf("raised oom_score_adj = %d, want %d (baseline %d + margin %d, clamped)", got, want, base, margin)
	}
}

// A session created with a positive OOM margin wraps its launch command so the
// agent raises its own oom_score_adj at start; the wrapped command stays valid
// shell and still execs the original program.
func TestStartWrapsLaunchCommandWithOOMRaise(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the OOM wrapper only applies on linux; GOOS=%s", runtime.GOOS)
	}
	prev := agentOOMMargin.Load()
	t.Cleanup(func() { agentOOMMargin.Store(prev) })
	SetAgentOOMMargin(300)

	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "oom-on", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.Contains(t, program, "/proc/self/oom_score_adj")
	require.Contains(t, program, "+300")
	require.Contains(t, program, "exec claude")
	// tmux hands the launch command to `sh -c`, so it must parse cleanly.
	parseOnly := exec.CommandContext(context.Background(), "sh", "-n", "-c", program)
	require.NoError(t, parseOnly.Run(), "wrapped launch command must be valid shell: %q", program)
}

// With the feature disabled (margin 0), the launch command carries no OOM wrapper.
func TestStartWithoutOOMMarginIsUnwrapped(t *testing.T) {
	prev := agentOOMMargin.Load()
	t.Cleanup(func() { agentOOMMargin.Store(prev) })
	SetAgentOOMMargin(0)

	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "oom-off", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.NotContains(t, program, "oom_score_adj")
}

// The margin is read from the process-wide default at each launch, not baked in at
// construction. A session constructed under one margin but launched under another
// (as happens when the user changes the setting mid-run and then relaunches the
// session via pause → resume) applies the value current at launch — the guarantee
// the doctor's "pause and resume those sessions" remedy depends on.
func TestStartAppliesCurrentMarginNotConstructionTime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the OOM wrapper only applies on linux; GOOS=%s", runtime.GOOS)
	}
	prev := agentOOMMargin.Load()
	t.Cleanup(func() { agentOOMMargin.Store(prev) })

	SetAgentOOMMargin(100)
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "oom-relaunch", "claude", ptyFactory, startMockExec())
	// Change the process-wide margin after construction but before launch.
	SetAgentOOMMargin(400)

	require.NoError(t, session.Start(t.TempDir()))

	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.Contains(t, program, "+400", "start must apply the margin current at launch")
	require.NotContains(t, program, "+100", "start must not apply the margin from construction time")
}
