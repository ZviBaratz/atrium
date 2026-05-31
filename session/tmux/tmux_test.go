package tmux

import (
	"fmt"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

type MockPtyFactory struct {
	t *testing.T

	// Array of commands and the corresponding file handles representing PTYs.
	cmds  []*exec.Cmd
	files []*os.File
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	filePath := filepath.Join(pt.t.TempDir(), fmt.Sprintf("pty-%s-%d", pt.t.Name(), rand.Int31()))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err == nil {
		pt.cmds = append(pt.cmds, cmd)
		pt.files = append(pt.files, f)
	}
	return f, err
}

func (pt *MockPtyFactory) Close() {}

func NewMockPtyFactory(t *testing.T) *MockPtyFactory {
	return &MockPtyFactory{
		t: t,
	}
}

func TestSanitizeName(t *testing.T) {
	session := NewTmuxSession("asdf", "program")
	require.Equal(t, TmuxPrefix()+"asdf", session.sanitizedName)

	session = NewTmuxSession("a sd f . . asdf", "program")
	require.Equal(t, TmuxPrefix()+"asdf__asdf", session.sanitizedName)
}

func TestIsReadyForPrompt(t *testing.T) {
	cases := []struct {
		name    string
		program string
		content string
		want    bool
	}{
		{
			name:    "claude trust screen is not ready",
			program: "claude",
			content: "Do you trust the files in this folder?\n  Yes  No",
			want:    false,
		},
		{
			name:    "claude new MCP server screen is not ready",
			program: "claude",
			content: "new MCP server detected. Approve?",
			want:    false,
		},
		{
			name:    "empty pane is not ready",
			program: "claude",
			content: "   \n\t\n",
			want:    false,
		},
		{
			name:    "claude idle input box is ready",
			program: "claude",
			content: "╭───╮\n│ > │  ? for shortcuts\n╰───╯",
			want:    true,
		},
		{
			name:    "non-claude doc-url gate is not ready",
			program: "aider",
			content: "Open documentation url for more info? (Y)es/(N)o",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ptyFactory := NewMockPtyFactory(t)
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error { return nil }, // session exists
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
					return []byte(tc.content), nil
				},
			}
			session := newTmuxSession("ready-test", tc.program, ptyFactory, cmdExec)
			require.Equal(t, tc.want, session.IsReadyForPrompt())
		})
	}
}

// Regression: after extracting containsStartupGate (shared by CheckAndHandleTrustPrompt
// and IsReadyForPrompt), it must still recognize each gate string.
func TestContainsStartupGate(t *testing.T) {
	cases := []struct {
		name    string
		program string
		content string
		want    bool
	}{
		{"claude trust folder", "claude", "Do you trust the files in this folder?", true},
		{"claude new MCP server", "claude", "A new MCP server was found", true},
		{"non-claude doc url", "aider", "Open documentation url for more info", true},
		{"claude idle box has no gate", "claude", "│ > │  ? for shortcuts", false},
		{"claude ignores non-claude gate string", "claude", "Open documentation url for more info", false},
		{"non-claude ignores claude gate string", "aider", "Do you trust the files in this folder?", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, containsStartupGate(tc.program, tc.content))
		})
	}
}

// A dead/missing tmux session must not be probed: the pollers should short-circuit
// without ever running capture-pane, so a single dead session can't flood the log
// and error box with "error capturing pane content: exit status 1" every tick.
func TestPollersSkipCaptureWhenSessionDead(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	captured := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			// has-session fails => the session no longer exists.
			return fmt.Errorf("can't find session")
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			captured = true
			return nil, fmt.Errorf("error capturing pane content: exit status 1")
		},
	}
	session := newTmuxSession("dead", "claude", ptyFactory, cmdExec)

	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

// The happy path must keep working: an alive session still captures. For a program with
// no busy marker, freshly seen content classifies as working (the content-change path),
// which the HasUpdated shim reports as updated.
func TestHasUpdatedCapturesWhenSessionAlive(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil }, // session exists
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("hello"), nil },
	}
	session := newTmuxSession("alive", "aider", ptyFactory, cmdExec)

	updated, _ := session.HasUpdated()
	require.True(t, updated, "first capture of new content should report updated")
}

// TestSessionDeathStopsProbing drives a REAL tmux session (not mocks) to reproduce the
// production flood: once a started session's pane is killed out from under cs, the
// pollers must report "not alive" and stop capturing, instead of running capture-pane
// and getting "exit status 1" on every tick.
func TestSessionDeathStopsProbing(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	name := fmt.Sprintf("death-%s-%d", t.Name(), rand.Int31())
	session := NewTmuxSession(name, "sleep 300")
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	// While alive: detectable, and a probe runs without panicking.
	require.True(t, session.DoesSessionExist())
	_, _ = session.HasUpdated()

	// Kill the session out from under cs (simulates a crash / external kill).
	// Must target the same dedicated socket the session was created on — bare
	// `tmux kill-session` hits tmux's default socket, where this session never
	// existed, so it would fail with "exit status 1".
	require.NoError(t, tmuxCommand("kill-session", "-t", session.sanitizedName).Run())

	// The pollers must now short-circuit cleanly rather than erroring every tick.
	require.False(t, session.DoesSessionExist())
	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
}

// pollSession builds a TmuxSession whose CapturePaneContent returns *content (or an
// error when *fail is true), so a test can drive Poll across ticks by mutating them.
// RunFunc reports the session as alive so Poll's liveness guard does not short-circuit.
func pollSession(t *testing.T, program string, content *string, fail *bool) *TmuxSession {
	t.Helper()
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil }, // session exists
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if fail != nil && *fail {
				return nil, fmt.Errorf("capture failed")
			}
			return []byte(*content), nil
		},
	}
	return newTmuxSession("poll-test", program, NewMockPtyFactory(t), cmdExec)
}

func TestCleanForDetection(t *testing.T) {
	require.Equal(t, "hello", cleanForDetection("\x1b[31mhel\x1b[0mlo"))
	require.Equal(t, "a\nb", cleanForDetection("a  \t\nb   "))
}

// A Claude pane showing the busy marker is PaneWorking, and stays PaneWorking even as the
// marker line's elapsed-time counter ticks — proving the counter no longer flips state.
func TestPollClaudeBusyMarkerIsStable(t *testing.T) {
	content := "✻ Cogitating… (5s · esc to interrupt)"
	c := content
	s := pollSession(t, "claude", &c, nil)

	require.Equal(t, PaneWorking, s.Poll())
	c = "✻ Cogitating… (6s · esc to interrupt)" // counter advanced, marker still present
	require.Equal(t, PaneWorking, s.Poll())
	c = "✻ Cogitating… (7s · esc to interrupt)"
	require.Equal(t, PaneWorking, s.Poll())
}

func TestPollClaudeIdleAndPrompt(t *testing.T) {
	idle := "╭───╮\n│ > │  ? for shortcuts\n╰───╯"
	c := idle
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(), "idle input box with no marker is idle immediately")

	c = "Do this? \n  No, and tell Claude what to do differently"
	require.Equal(t, PanePrompt, s.Poll(), "a tool-permission y/n prompt takes precedence")
}

// An interactive selection prompt (AskUserQuestion / plan approval) blocks on the user
// just like the permission dialog, even though it shows no permission text. Its footer
// is the signal — and the real idle/working footers must not trip it.
func TestPollClaudeSelectionPrompt(t *testing.T) {
	selection := "How do you want to be notified?\n  1. Telegram\n  2. Email\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel"
	c := selection
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "a selection prompt is a needs-input state")

	// Footers captured from live idle/working panes must classify as idle/working,
	// never as a prompt.
	for _, footer := range []string{
		"❯ \n⏵⏵ auto mode on · 1 shell · ctrl+t to hide tasks · ← for agents · ↓ to manage",
		"❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	} {
		c = footer
		s := pollSession(t, "claude", &c, nil)
		require.Equal(t, PaneIdle, s.Poll(), "idle footer must not be read as a prompt: %q", footer)
	}
}

// Regression: capture-pane includes the scrolled-back transcript, so the marker strings
// can appear in the agent's own words. Detection must look only at the bottom chrome, and
// the selection footer must require its structural tokens — a bare "Esc to cancel"
// sentence in prose must not trigger the prompt state.
func TestPollIgnoresMarkersInScrollback(t *testing.T) {
	body := "I added the \"esc to interrupt\" marker and matched the\n" +
		"\"Esc to cancel\" footer, plus the literal option text\n" +
		"\"No, and tell Claude what to do differently\".\n"
	pad := strings.Repeat("a normal line of build output\n", 20)
	idleFooter := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := body + pad + idleFooter
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"markers and a bare \"Esc to cancel\" in the scrolled-back body must be ignored")
}

// Hysteresis: working commits instantly; the first idle after working is held; the second
// consecutive idle commits; a new working observation resets the streak.
func TestPollHysteresis(t *testing.T) {
	busy := "working… esc to interrupt"
	idle := "│ > │ done"
	c := busy
	s := pollSession(t, "claude", &c, nil)

	require.Equal(t, PaneWorking, s.Poll())
	c = idle
	require.Equal(t, PaneWorking, s.Poll(), "first idle after working is held")
	require.Equal(t, PaneIdle, s.Poll(), "second consecutive idle commits")
	c = busy
	require.Equal(t, PaneWorking, s.Poll(), "working resets the streak")
	c = idle
	require.Equal(t, PaneWorking, s.Poll(), "held again after reset")
}

// Programs without a known marker use content-change detection on ANSI-stripped text, so
// color/cursor churn does not register as working.
func TestPollFallbackNormalization(t *testing.T) {
	c := "\x1b[32mthinking\x1b[0m"
	s := pollSession(t, "aider", &c, nil)

	require.Equal(t, PaneWorking, s.Poll(), "first observation is treated as active")
	// Same visible text, different ANSI only → not a change. Drain the hysteresis hold.
	c = "\x1b[33mthinking\x1b[0m"
	require.Equal(t, PaneWorking, s.Poll(), "held (idleStreak 1)")
	c = "\x1b[31mthinking\x1b[0m"
	require.Equal(t, PaneIdle, s.Poll(), "ANSI-only churn settles to idle")
	// A real text change flips back to working.
	c = "\x1b[31mthinking more\x1b[0m"
	require.Equal(t, PaneWorking, s.Poll())
}

func TestPollCaptureErrorIsUnknown(t *testing.T) {
	log.Initialize(false) // Poll logs on capture error; ErrorLog is otherwise nil in tests
	c := "anything"
	fail := false
	s := pollSession(t, "claude", &c, &fail)
	require.Equal(t, PaneIdle, s.Poll())
	fail = true
	require.Equal(t, PaneUnknown, s.Poll(), "capture failure yields PaneUnknown")
}

func TestHasUpdatedShim(t *testing.T) {
	busy := "esc to interrupt"
	c := busy
	s := pollSession(t, "claude", &c, nil)
	u, p := s.HasUpdated()
	require.True(t, u)
	require.False(t, p)

	c = "No, and tell Claude what to do differently"
	u, p = s.HasUpdated()
	require.False(t, u)
	require.True(t, p)
}

func TestTmuxCommandInjectsIsolationFlags(t *testing.T) {
	cmd := tmuxCommand("has-session", "-t=foo")
	// Args[0] is "tmux"; the socket flag must immediately follow and precede the
	// subcommand (tmux requires -L/-f before the command).
	require.Equal(t, "tmux", cmd.Args[0])
	require.Equal(t, "-L", cmd.Args[1])
	require.Equal(t, socketName(), cmd.Args[2])
	// The subcommand and its args must still be present and last.
	require.Contains(t, cmd.Args, "has-session")
	require.Equal(t, "-t=foo", cmd.Args[len(cmd.Args)-1])
}

func TestStartTmuxSession(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)

	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	workdir := t.TempDir()
	session := newTmuxSession("test-session", "claude", ptyFactory, cmdExec)

	err := session.Start(workdir)
	require.NoError(t, err)
	require.Equal(t, 2, len(ptyFactory.cmds))

	// Atrium runs on a dedicated socket with a bundled config, so every command is
	// prefixed with `-L <socket> -f <conf>`. The conf path is absolute and
	// machine-dependent, and the socket/prefix follow the active brand, so assert
	// the load-bearing parts via the same helpers rather than a literal string.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "-L "+socketName())
	require.Contains(t, newSession, "new-session -d -s "+TmuxPrefix()+"test-session")
	require.Contains(t, newSession, "-c "+workdir)
	require.Contains(t, newSession, "-n test-session")
	require.Contains(t, newSession, "claude")

	attach := cmd2.ToString(ptyFactory.cmds[1])
	require.Contains(t, attach, "-L "+socketName())
	require.Contains(t, attach, "attach-session -t "+TmuxPrefix()+"test-session")

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

func TestContinueProgram(t *testing.T) {
	cases := []struct {
		name    string
		program string
		want    string
	}{
		{"bare claude gets --continue", "claude", "claude --continue"},
		{"absolute claude path gets --continue", "/usr/local/bin/claude", "/usr/local/bin/claude --continue"},
		{"aider unchanged", "aider --model x", "aider --model x"},
		{"gemini unchanged", "gemini", "gemini"},
		// HasSuffix, not Contains: a binary merely containing "claude" is not matched.
		{"claude as non-suffix unchanged", "claude-wrapper", "claude-wrapper"},
		// A claude program carrying flags does not end in "claude", so it is not matched —
		// deliberately consistent with busyMarkers/detectPrompt/containsStartupGate, which
		// use the same HasSuffix predicate and likewise ignore flag-bearing claude profiles.
		{"claude with trailing flags unchanged", "claude --model opus", "claude --model opus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, continueProgram(tc.program))
		})
	}
}

// startMockExec mirrors TestStartTmuxSession's executor: the first has-session check
// reports "not found" so start's entry guard passes, and every later check succeeds so
// the poll loop sees the session and breaks.
func startMockExec() cmd_test.MockCmdExec {
	created := false
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}
}

func TestStartContinueAppendsContinueForClaude(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := newTmuxSession("cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	// cmds[0] is the new-session launch; cmds[1] is the trailing attach from Restore.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "claude --continue")
	// The session name is keyed off the session, not the program, so it is unchanged.
	require.Contains(t, newSession, "new-session -d -s "+TmuxPrefix()+"cont-test")
}

func TestStartContinueLeavesNonClaudeUnchanged(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := newTmuxSession("cont-test", "aider --model x", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.NotContains(t, newSession, "--continue")
	require.Contains(t, newSession, "aider --model x")
}

// Plain Start must never append --continue, even for claude — that is the first-time and
// PTY-reattach path, where there is nothing to continue.
func TestStartDoesNotAppendContinue(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := newTmuxSession("cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))
	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "--continue")
}
