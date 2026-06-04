package tmux

import (
	"context"
	"fmt"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	session := NewSession(context.Background(), "asdf", "program")
	require.Equal(t, Prefix()+"asdf", session.sanitizedName)

	session = NewSession(context.Background(), "a sd f . . asdf", "program")
	require.Equal(t, Prefix()+"asdf__asdf", session.sanitizedName)
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
			session := newSession(context.Background(), "ready-test", tc.program, ptyFactory, cmdExec)
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
	session := newSession(context.Background(), "dead", "claude", ptyFactory, cmdExec)

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
	session := newSession(context.Background(), "alive", "aider", ptyFactory, cmdExec)

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
	session := NewSession(context.Background(), name, "sleep 300")
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	// While alive: detectable, and a probe runs without panicking.
	require.True(t, session.DoesSessionExist())
	_, _ = session.HasUpdated()

	// Kill the session out from under cs (simulates a crash / external kill).
	// Must target the same dedicated socket the session was created on — bare
	// `tmux kill-session` hits tmux's default socket, where this session never
	// existed, so it would fail with "exit status 1".
	require.NoError(t, tmuxCommand(context.Background(), "kill-session", "-t", session.sanitizedName).Run())

	// The pollers must now short-circuit cleanly rather than erroring every tick.
	require.False(t, session.DoesSessionExist())
	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
}

// pollSession builds a Session whose CapturePaneContent returns *content (or an
// error when *fail is true), so a test can drive Poll across ticks by mutating them.
// RunFunc reports the session as alive so Poll's liveness guard does not short-circuit.
func pollSession(t *testing.T, program string, content *string, fail *bool) *Session {
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
	return newSession(context.Background(), "poll-test", program, NewMockPtyFactory(t), cmdExec)
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

	// The live AskUserQuestion footer carries extra hints ("n to add notes") between the
	// navigate and cancel tokens; it must still classify as a prompt.
	c = "Server restart?\n  1. Relaunch\n❯ 2. Restart now\n  3. Nav only\n" +
		"Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel"
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "selection footer with extra hints is a prompt")

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

// At a narrow pane width Claude hard-wraps its chrome, splitting a prompt's footer (and the
// permission dialog's decline option) across physical lines. Detection must survive the wrap:
// the navigate/select token and "Esc to cancel" can land on separate lines, and the decline
// sentence can break mid-phrase. The bottom-chrome confinement still holds, so a wrapped footer
// is recognized while scrolled-back prose is not.
func TestPollClaudePromptWrapTolerant(t *testing.T) {
	// Selection footer wrapped so "Esc to cancel" is on a different line than the nav/select
	// tokens — the case the old same-line check missed.
	wrappedFooter := "Server restart?\n  1. Relaunch\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes · Esc to cancel"
	c := wrappedFooter
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "a wrapped selection footer is still a prompt")

	// Permission dialog whose decline option wraps mid-sentence.
	wrappedDialog := "Do you want to proceed?\n  Yes\n  No, and tell Claude what to do\ndifferently"
	c = wrappedDialog
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "a wrapped permission dialog is still a prompt")

	// Footer wrapped across three physical lines, with a filler line between the nav/select
	// token and "Esc to cancel". This pins footerChromeLines at its current width: any window
	// narrower than 3 would drop the nav/select token and silently misclassify the prompt.
	threeLineFooter := "Server restart?\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes\n· Esc to cancel"
	c = threeLineFooter
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "a footer wrapped across the full footer window is still a prompt")
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

// Fix 2 (agent-team layout): the busy marker lives in the footer below the input box's
// bottom border, and the variable-height team selector (one line per teammate) renders below
// the marker — pushing it outside a fixed bottom-N window. markerWorking must anchor to the
// box border so it still finds the marker no matter how many teammates the selector lists.
func TestMarkerWorkingAnchorsBelowInputBox(t *testing.T) {
	c := ""
	s := pollSession(t, "claude", &c, nil)

	working := strings.Join([]string{
		"⏺ Running the build…",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
		"  Running 2 agents…",
		"  ● main",
		"  ◯ general-purpose",
	}, "\n")
	require.True(t, s.markerWorking(working),
		"the footer marker is found even when a team selector renders below it")

	// Regression: the same marker text sitting in the scrolled-back transcript (above the
	// last box border) must not count — only the live footer below the border does.
	scrollback := strings.Join([]string{
		"  I will add the \"esc to interrupt\" marker check now.",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"  ● main",
	}, "\n")
	require.False(t, s.markerWorking(scrollback),
		"a marker above the input box (in the transcript) is ignored")
}

// Hysteresis (content-change fallback, e.g. aider): a content change reads as working;
// once the pane goes quiet the indicator is held until it has been unchanged for
// idleSettleTicks, then commits idle. This path is only for programs without a busy marker;
// the marker-driven Claude path is covered by the hook/marker tests.
func TestPollHysteresis(t *testing.T) {
	busy := "running… building target"
	idle := "$ done"
	c := busy
	s := pollSession(t, "aider", &c, nil)

	require.Equal(t, PaneWorking, s.Poll(), "first content read → working")
	// The content changing to idle is itself a change (working), then the pane must stay
	// quiet for idleSettleTicks observations before idle commits.
	c = idle
	for i := 0; i < idleSettleTicks; i++ {
		require.Equal(t, PaneWorking, s.Poll(), "held while the pane settles (observation %d)", i)
	}
	require.Equal(t, PaneIdle, s.Poll(), "commits once the pane has been quiet for idleSettleTicks")
	c = busy
	require.Equal(t, PaneWorking, s.Poll(), "a content change resets to working")
}

// Regression for the status oscillation: at an auto-accept turn boundary Claude briefly
// drops the "esc to interrupt" marker (model spin-up) while the pane keeps repainting
// (spinner elapsed ticking, output rendering). A *moving* pane must never settle to idle —
// it holds "working" until the marker returns, so there is no Ready→Running flicker.
func TestPollBridgesAutoAcceptTurnBoundary(t *testing.T) {
	working := "⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ctrl+t to hide tasks"
	// Same footer minus the marker, plus a spinner whose elapsed counter advances each tick.
	gap := func(i int) string {
		return fmt.Sprintf("✻ Cogitating… (%ds)\n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", i)
	}
	c := working
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	// A churning gap (well past idleSettleTicks) is held the whole time, then the marker
	// returns: the indicator never flipped to idle.
	for i := 1; i < idleConfirmTicks; i++ {
		c = gap(i)
		require.Equal(t, PaneWorking, s.Poll(), "churning turn-boundary gap held (observation %d)", i)
	}
	c = working
	require.Equal(t, PaneWorking, s.Poll(), "marker returning resumes working without a blip")
}

// Safety cap: if the marker stays absent while the pane keeps changing (an agent UI we
// don't model, or a missed marker), the idleConfirmTicks cap eventually commits idle rather
// than holding "working" forever.
func TestPollChurnHitsSafetyCap(t *testing.T) {
	c := "esc to interrupt"
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	for i := 1; i < idleConfirmTicks; i++ {
		c = fmt.Sprintf("repainting %d, no marker", i) // changes every tick → never settles
		require.Equal(t, PaneWorking, s.Poll(), "held under churn before the cap (observation %d)", i)
	}
	c = "repainting final, no marker"
	require.Equal(t, PaneIdle, s.Poll(), "commits at the idleConfirmTicks safety cap")
}

// PollNow classifies at face value with no hysteresis — for the one-shot refresh on detach,
// where the stalled stream left the smoothing state stale. An idle pane reads idle at once
// even though the monitor last reported working; a marker pane reads working; a markerless
// program can't be classified from a single snapshot and yields PaneUnknown.
func TestPollNow(t *testing.T) {
	idle := "│ > │  ? for shortcuts"
	c := "working… esc to interrupt"
	s := pollSession(t, "claude", &c, nil)

	// Leave the monitor mid working→idle hold, the way a stalled stream would.
	require.Equal(t, PaneWorking, s.Poll())
	c = idle
	require.Equal(t, PaneWorking, s.Poll(), "normal Poll holds working via hysteresis")

	require.Equal(t, PaneIdle, s.PollNow(), "PollNow ignores the hold and commits idle at once")

	c = "thinking… esc to interrupt"
	require.Equal(t, PaneWorking, s.PollNow(), "a marker pane is working")

	// A markerless program has no level signal, so a single snapshot is inconclusive.
	c = "some output"
	a := pollSession(t, "aider", &c, nil)
	require.Equal(t, PaneUnknown, a.PollNow(), "no marker → unknown, left to the tick loop")
}

// Poll is driven by the metadata tick and, off-cadence, by the UI when the selection
// changes or a session is detached. monitorMu must make concurrent calls on one session
// race-free; run under -race to exercise it.
func TestPollConcurrentIsRaceFree(t *testing.T) {
	c := "✻ Cogitating… (5s · esc to interrupt)"
	s := pollSession(t, "claude", &c, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Poll()
			}
		}()
	}
	wg.Wait()
}

// Programs without a known marker use content-change detection on ANSI-stripped text, so
// color/cursor churn does not register as working.
func TestPollFallbackNormalization(t *testing.T) {
	c := "\x1b[32mthinking\x1b[0m"
	s := pollSession(t, "aider", &c, nil)

	require.Equal(t, PaneWorking, s.Poll(), "first observation is treated as active")
	// Same visible text, different ANSI only → not a change, so the pane reads as quiet and
	// settles to idle after idleSettleTicks. Vary the ANSI each tick to prove a raw byte
	// comparison would (wrongly) see motion.
	for i := 1; i < idleSettleTicks; i++ {
		c = fmt.Sprintf("\x1b[%dmthinking\x1b[0m", 31+i)
		require.Equal(t, PaneWorking, s.Poll(), "ANSI-only churn held (observation %d)", i)
	}
	c = fmt.Sprintf("\x1b[%dmthinking\x1b[0m", 31+idleSettleTicks)
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
	cmd := tmuxCommand(context.Background(), "has-session", "-t=foo")
	// Args[0] is "tmux"; the socket flag must immediately follow and precede the
	// subcommand (tmux requires -L/-f before the command).
	require.Equal(t, "tmux", cmd.Args[0])
	require.Equal(t, "-L", cmd.Args[1])
	require.Equal(t, socketName(), cmd.Args[2])
	// The subcommand and its args must still be present and last.
	require.Contains(t, cmd.Args, "has-session")
	require.Equal(t, "-t=foo", cmd.Args[len(cmd.Args)-1])
}

func TestStartSession(t *testing.T) {
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
	session := newSession(context.Background(), "test-session", "claude", ptyFactory, cmdExec)

	err := session.Start(workdir)
	require.NoError(t, err)
	require.Equal(t, 2, len(ptyFactory.cmds))

	// Atrium runs on a dedicated socket with a bundled config, so every command is
	// prefixed with `-L <socket> -f <conf>`. The conf path is absolute and
	// machine-dependent, and the socket/prefix follow the active brand, so assert
	// the load-bearing parts via the same helpers rather than a literal string.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "-L "+socketName())
	require.Contains(t, newSession, "new-session -d -s "+Prefix()+"test-session")
	require.Contains(t, newSession, "-c "+workdir)
	require.Contains(t, newSession, "-n test-session")
	require.Contains(t, newSession, "claude")

	attach := cmd2.ToString(ptyFactory.cmds[1])
	require.Contains(t, attach, "-L "+socketName())
	require.Contains(t, attach, "attach-session -t "+Prefix()+"test-session")

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

// Regression: a session title with a shell metacharacter (e.g. "Surya's comment") flows
// into the hook settings path, which is appended to the launch command — a string tmux
// hands to `sh -c`. Unquoted, the apostrophe opened an unterminated quote, the window's
// shell died instantly, and the start poll timed out ("timed out waiting for tmux
// session claudesquad_Surya'scomment"). The path must be single-quoted so the launch
// command stays valid shell for any session name.
func TestStartQuotesHookSettingsPath(t *testing.T) {
	forceSettingsFlag(t, true)
	ptyFactory := NewMockPtyFactory(t)
	session := newSession(context.Background(), "Surya's comment", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	// The launch command is the final argument of the new-session invocation; tmux runs
	// it via the shell, so it must parse cleanly (sh -n parses without executing).
	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.Contains(t, program, "--settings")
	parseOnly := exec.CommandContext(context.Background(), "sh", "-n", "-c", program)
	require.NoError(t, parseOnly.Run(), "launch command must be valid shell syntax: %q", program)

	// The settings path (which embeds the apostrophe-bearing session name) is quoted.
	dir, err := hookSessionDir(session.sanitizedName)
	require.NoError(t, err)
	settingsPath := filepath.Join(dir, "settings.json")
	require.Contains(t, program, " --settings "+shellSingleQuote(settingsPath))
}

// Regression: the start poll's timeout branch wrapped a nil error with %w, rendering as
// the unreadable "timed out waiting for tmux session X: %!w(<nil>) (cleanup error: …)".
// The timeout must produce a clean message whether or not the cleanup also fails.
func TestStartTimeoutErrorOmitsNilWrap(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	// has-session never succeeds (the session died at launch) and kill-session fails too
	// (nothing to kill) — the exact shape of the real failure.
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}
	session := newSession(context.Background(), "timeout-test", "prog", ptyFactory, cmdExec)

	err := session.Start(t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out waiting for tmux session")
	require.Contains(t, err.Error(), "cleanup error", "the failed kill is still reported")
	require.NotContains(t, err.Error(), "%!w", "a nil error must never be wrapped")
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
		// Detection is on the binary basename containing "claude", so a launcher wrapper that
		// exec's claude (the default_program many setups use) and a flag-bearing claude are
		// both recognized — the wrapper forwards the appended flag through to claude.
		{"claude launcher wrapper gets --continue", "/home/u/.claude-squad/launch-claude.sh", "/home/u/.claude-squad/launch-claude.sh --continue"},
		{"claude-wrapper gets --continue", "claude-wrapper", "claude-wrapper --continue"},
		{"claude with trailing flags gets --continue", "claude --model opus", "claude --model opus --continue"},
		// A non-claude binary under a claude-containing directory is NOT matched (basename wins).
		{"non-claude binary in claude dir unchanged", "/home/u/.claude-squad/bin/aider", "/home/u/.claude-squad/bin/aider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, continueProgram(tc.program))
		})
	}
}

// startMockExec mirrors TestStartSession's executor: the first has-session check
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
	session := newSession(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	// cmds[0] is the new-session launch; cmds[1] is the trailing attach from Restore.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "claude --continue")
	// The session name is keyed off the session, not the program, so it is unchanged.
	require.Contains(t, newSession, "new-session -d -s "+Prefix()+"cont-test")
}

func TestStartContinueLeavesNonClaudeUnchanged(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := newSession(context.Background(), "cont-test", "aider --model x", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.NotContains(t, newSession, "--continue")
	require.Contains(t, newSession, "aider --model x")
}

// Plain Start must never append --continue, even for claude — that is the first-time and
// PTY-reattach path, where there is nothing to continue.
func TestStartDoesNotAppendContinue(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := newSession(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))
	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "--continue")
}
