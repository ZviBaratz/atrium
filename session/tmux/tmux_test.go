package tmux

import (
	"context"
	"fmt"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
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

	// StartErr, when non-nil, makes Start fail without allocating a pty. Tests use
	// it to simulate a Restore/pty failure (e.g. the Detach degraded path).
	StartErr error
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	if pt.StartErr != nil {
		return nil, pt.StartErr
	}
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
			session := NewSessionWithDeps(context.Background(), "ready-test", tc.program, ptyFactory, cmdExec)
			require.Equal(t, tc.want, session.IsReadyForPrompt())
		})
	}
}

// Regression: the per-agent startup gates (now adapter data, shared by
// CheckAndHandleTrustPrompt and IsReadyForPrompt) must still recognize each gate string —
// and only for their own agent, so a gate is never dismissed with another agent's key.
func TestStartupGates(t *testing.T) {
	cases := []struct {
		name    string
		program string
		content string
		want    bool
	}{
		{"claude trust folder", "claude", "Do you trust the files in this folder?", true},
		{"claude new MCP server (lowercase)", "claude", "new MCP server found in this project", true},
		{"claude new MCP server (capital-N)", "claude", "New MCP server found in this project: nanoclaw", true},
		{"aider doc url", "aider", "Open documentation url for more info", true},
		{"claude idle box has no gate", "claude", "│ > │  ? for shortcuts", false},
		{"claude ignores aider gate string", "claude", "Open documentation url for more info", false},
		{"aider ignores claude gate string", "aider", "Do you trust the files in this folder?", false},
		// Pre-adapter, every non-claude program matched aider's documentation gate and
		// received its stray 'D' keystroke; an unknown agent must match nothing.
		{"unknown agent has no gates", "someagent", "Open documentation url for more info", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionWithDeps(context.Background(), "gate-test", tc.program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
			_, ok := s.adapter.GateUp(tc.content)
			require.Equal(t, tc.want, ok)
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
	session := NewSessionWithDeps(context.Background(), "dead", "claude", ptyFactory, cmdExec)

	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

// A dead/missing session must classify as PaneDead (distinct from the PaneUnknown a
// transient capture failure yields), so the metadata loop can flag it lost from this one
// has-session check instead of forking its own. Neither poller may run capture-pane.
func TestPollersReturnDeadWhenSessionDead(t *testing.T) {
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
	s := NewSessionWithDeps(context.Background(), "dead", "claude", NewMockPtyFactory(t), cmdExec)

	require.Equal(t, PaneDead, s.Poll(), "a dead session must classify as PaneDead")
	require.Equal(t, PaneDead, s.PollNow(), "a dead session must classify as PaneDead")
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
	session := NewSessionWithDeps(context.Background(), "alive", "aider", ptyFactory, cmdExec)

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
	return NewSessionWithDeps(context.Background(), "poll-test", program, NewMockPtyFactory(t), cmdExec)
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

// Poll feeds the live permission mode from the footer into the session, end to
// end: real capture → cleanForDetection (ANSI strip) → adapter detection. The
// box-rule + below-the-box footer mirror a live claude pane (see Step-0 capture).
func TestPollDetectsPermissionMode(t *testing.T) {
	box := func(footer string) string {
		rule := strings.Repeat("─", 40)
		return rule + "\n❯ \n" + rule + "\n" + footer
	}
	cases := []struct{ name, footer, want string }{
		{"plan", "  ⏸ plan mode on (shift+tab to cycle) · ← for agents", "plan"},
		{"acceptEdits", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents", "acceptEdits"},
		{"auto", "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", "auto"},
		{"bypass", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", "bypassPermissions"},
		{"default", "  ? for shortcuts · ← for agents", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := box(c.footer)
			s := pollSession(t, "claude", &content, nil)
			s.Poll()
			require.Equal(t, c.want, s.RuntimePermissionMode())
		})
	}

	// A real capture carries ANSI; cleanForDetection must strip it so footerRegion
	// and the detector still fire (the rule line would fail isHorizontalRule raw).
	ansi := "\x1b[2m" + strings.Repeat("─", 40) + "\x1b[0m\n❯ \n" +
		strings.Repeat("─", 40) + "\n\x1b[39m  ⏵⏵ auto mode on (shift+tab to cycle)\x1b[39m"
	s := pollSession(t, "claude", &ansi, nil)
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode(), "ANSI-wrapped footer must still detect")

	// Sticky: an indeterminate (busy, no indicator) footer leaves the last mode in
	// place rather than blanking it, so the chip doesn't flicker mid-turn.
	c := box("  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents")
	s = pollSession(t, "claude", &c, nil)
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode())
	c = "✻ Cogitating… (6s · esc to interrupt)" // no box, no mode indicator
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode(), "indeterminate footer must keep the last mode")
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

// The plan-approval dialog classifies as PanePromptManual — its auto-answer is
// destructive (Enter accepts the plan AND enables auto mode), so autoyes paths
// must surface it instead of tapping. The HasUpdated shim's hasPrompt must stay
// false for it: the daemon's legacy tap path keyed on that bit, so excluding the
// manual state there is the fail-safe for any caller not yet switched to Poll.
// Pane content mirrors a live 2.1.170 capture (see agent.TestClaudePlanPrompt).
func TestPollClaudePlanPrompt(t *testing.T) {
	plan := strings.Join([]string{
		"   Claude has written up a plan and is ready to execute. Would you like to proceed?",
		"",
		"   ❯ 1. Yes, and use auto mode",
		"     2. Yes, manually approve edits",
		"     3. No, refine with Ultraplan on Claude Code on the web",
		"     4. Tell Claude what to change",
		"        shift+tab to approve with this feedback",
		"",
		"   ctrl+g to edit in  VS Code  · ~/.claude/plans/make-a-plan.md",
	}, "\n")
	c := plan
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "plan approval is a manual-only prompt")

	_, hasPrompt := s.HasUpdated()
	require.False(t, hasPrompt, "the HasUpdated shim must not report a manual prompt as tappable")
}

// A session launched with a bad --model stays alive showing claude's error and
// an idle input box; Poll must surface it as a manual prompt (needs-input),
// never auto-tap — there is nothing for autoyes to answer. Pane content mirrors
// a live 2.1.170 capture (see agent.TestClaudeModelErrorPrompt).
func TestPollClaudeModelError(t *testing.T) {
	pane := strings.Join([]string{
		"❯ say hi",
		"",
		"● There's an issue with the selected model (atrium-bogus-model-check). It may not exist or you may",
		"  not have access to it. Run /model to pick a different model.",
		"",
		"✻ Cogitated for 0s",
		"",
		strings.Repeat("─", 100),
		"❯ ",
		strings.Repeat("─", 100),
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "a bad-model launch must surface as needs-input")

	_, hasPrompt := s.HasUpdated()
	require.False(t, hasPrompt, "the HasUpdated shim must not report a manual prompt as tappable")
}

// A custom Claude Code statusLine renders below the selection-prompt footer (captured live:
// the overlay draws a horizontal rule, then "6. Chat about this", the key-hint footer, blank
// padding, and finally the user's multi-line statusLine). The footer is then several non-empty
// lines above the pane bottom, so a fixed bottom-N window misses it. The rule-delimited
// segment scan (selectionFooterVisible) keeps it visible regardless of the statusLine's
// height.
func TestPollClaudeSelectionPromptBelowStatusLine(t *testing.T) {
	rule := strings.Repeat("─", 80)
	pane := strings.Join([]string{
		"  4. IMP-1573: midday API exhaustion gate",
		"  5. Type something.",
		rule,
		"  6. Chat about this",
		"",
		"Enter to select · ↑/↓ to navigate · Esc to cancel",
		"", "", "", "", "", "", "", "", "", "",
		"  2 tasks (0 done, 2 open)",
		"  ◻ Session ID: c706f0e8-d7a3-413e-85bf-9b74bd725e0b",
		"  ◻ Worktree mode: inplace",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(),
		"a selection prompt whose footer sits above a multi-line statusLine is still a prompt")
}

// Regression (review): a custom statusLine may draw its own horizontal divider — a pure-─
// separator is a common powerline/boxed statusLine idiom. Anchoring the footer match to
// "below the last rule" would re-anchor past the footer onto the statusLine's divider and
// miss the prompt: the very displacement bug the statusLine fix addresses, reintroduced by
// fancier statusLines. Detection must survive any number of rules below the footer.
func TestPollClaudeSelectionPromptAboveStatusLineDivider(t *testing.T) {
	rule := strings.Repeat("─", 80)
	for _, tc := range []struct {
		name       string
		statusLine []string
	}{
		{"divider", []string{"────────────", "  main · opus · 12% ctx"}},
		{"boxed", []string{"──────────", "  main · opus · 12% ctx", "──────────"}},
		{"tall sectioned", []string{
			"────────────",
			"  main · opus · 12% ctx",
			"  2 tasks (0 done, 2 open)",
			"────────────",
			"  ◻ Session ID: c706f0e8-d7a3-413e-85bf-9b74bd725e0b",
			"  ◻ Worktree mode: inplace",
		}},
	} {
		pane := strings.Join(append([]string{
			"  5. Type something.",
			rule,
			"  6. Chat about this",
			"",
			"Enter to select · ↑/↓ to navigate · Esc to cancel",
			"", "",
		}, tc.statusLine...), "\n")
		c := pane
		s := pollSession(t, "claude", &c, nil)
		require.Equal(t, PanePrompt, s.Poll(),
			"a selection prompt above a %s statusLine is still a prompt", tc.name)
	}
}

// FP-safety: the footer's co-occurring tokens must appear within one rule-delimited segment.
// Hint text spread across different segments — Claude's own hint line plus an unrelated
// statusLine line below a divider — must not combine into a false footer.
func TestPollClaudeHintTokensAcrossSegmentsStayIdle(t *testing.T) {
	pane := strings.Join([]string{
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on · ↑/↓ to navigate history · ← for agents",
		"────────────",
		"  press Esc to cancel the current task",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"footer tokens split across rule-delimited segments must not be read as a live prompt")
}

// FP-safety: a transcript quote of the footer stays excluded even when the statusLine draws
// its own divider below the input box — the upward scan stops at the box interior and never
// reaches the quote.
func TestPollClaudeFooterQuoteAboveBoxWithDividerStatusLine(t *testing.T) {
	pane := strings.Join([]string{
		"  The selection footer looks like:",
		"  Enter to select · ↑/↓ to navigate · Esc to cancel",
		"╭────────────────────────────────────────╮",
		"│ ❯                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"────────────",
		"  main · opus · 12% ctx",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"a quoted footer above the input box must stay idle regardless of statusLine rules")
}

// FP-safety: an idle pane whose scrolled-back transcript quotes the full footer line must
// stay idle. The quote sits above the input box, so the upward segment scan stops at the
// box interior before reaching it — where a merely-wider bottom-N window would re-admit it
// and flip the session to a spurious needs-input.
func TestPollClaudeFooterQuoteInScrollbackStaysIdle(t *testing.T) {
	rule := strings.Repeat("─", 80)
	pane := strings.Join([]string{
		"  The selection footer looks like:",
		"  Enter to select · ↑/↓ to navigate · Esc to cancel",
		"  (that is what we match on).",
		"",
		rule,
		"❯ ",
		rule,
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"a footer quoted in the transcript above the input box must not be read as a live prompt")
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
	// token and "Esc to cancel". This pane has no horizontal rule, so it pins the no-rule
	// fallback window (workChromeLines) at its current width: any window narrower than 3
	// would drop the nav/select token and silently misclassify the prompt.
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

// Codex renders its status row ("Working (12s • esc to interrupt)") *above* the
// composer, outside claude's below-the-box footer anchor; the adapter's bottom-window
// confinement must still find it, hold across counter ticks, and read its approval
// overlay as a prompt.
func TestPollCodex(t *testing.T) {
	working := "• Fixing the failing test.\n\n▌ Working (12s • esc to interrupt)\n\n› \n\n  ? for shortcuts"
	c := working
	s := pollSession(t, "codex", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())
	c = "• Fixing the failing test.\n\n▌ Working (13s • esc to interrupt)\n\n› \n\n  ? for shortcuts"
	require.Equal(t, PaneWorking, s.Poll(), "counter ticking does not flip the state")

	c = "Would you like to run the following command?\n\n  rm -rf build/\n\n" +
		"› 1. Yes, proceed\n  3. No, and tell Codex what to do differently"
	require.Equal(t, PanePrompt, s.Poll(), "an approval overlay is a needs-input state")

	c = "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	require.Equal(t, PaneIdle, s.Poll(), "marker gone after a prompt commits idle at face value")
}

// Gemini's loading row ("(esc to cancel, 12s)") also renders above its input box; it is
// now a marker-bearing program, and its tool confirmation must classify as a prompt on
// the current upstream strings (the pre-adapter "Yes, allow once" no longer exists).
func TestPollGemini(t *testing.T) {
	working := "✦ Refactoring the parser.\n\n⠏ Thinking... (esc to cancel, 12s)\n\n" +
		"╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	c := working
	s := pollSession(t, "gemini", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	c = "Apply this change?\n  1. Allow once\n  2. Allow always\n  3. No, suggest changes (esc)"
	require.Equal(t, PanePrompt, s.Poll(), "a tool confirmation is a needs-input state")

	// PollNow (the post-detach face-value refresh): gemini is marker-bearing, so —
	// unlike aider's PaneUnknown — an absent marker with no hook file reads as idle,
	// and a present marker as working.
	c = "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	require.Equal(t, PaneIdle, s.PollNow(), "no marker at face value is idle")
	c = working
	require.Equal(t, PaneWorking, s.PollNow(), "a live marker at face value is working")
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

// idleConfirmCap returns the adapter override when set (> 0), else the package default.
func TestIdleConfirmCap(t *testing.T) {
	c := "x"
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, idleConfirmTicks, s.idleConfirmCap(), "claude sets no override → package default")

	s.adapter = &agent.Adapter{IdleConfirmTicks: 3}
	require.Equal(t, 3, s.idleConfirmCap(), "a positive adapter override is honored")

	s.adapter = nil
	require.Equal(t, idleConfirmTicks, s.idleConfirmCap(), "nil adapter falls back to the default")
}

// An adapter that raises IdleConfirmTicks holds working past the package default, and a
// lowered one commits idle earlier — proving Poll reads the per-adapter cap, not the const.
func TestPollHonorsAdapterIdleConfirmCap(t *testing.T) {
	c := "esc to interrupt"
	s := pollSession(t, "claude", &c, nil)
	// Clone the resolved adapter by value and change only the cap. Mutating the shared
	// registry adapter in place would leak the lowered cap into the default-cap tests.
	ad := *s.adapter
	ad.IdleConfirmTicks = 3
	s.adapter = &ad

	require.Equal(t, PaneWorking, s.Poll())
	// idleStreak climbs 1,2 under churn (held), then 3 trips the lowered cap — well before
	// the default idleConfirmTicks (6) would have.
	for i := 1; i < 3; i++ {
		c = fmt.Sprintf("repainting %d, no marker", i)
		require.Equal(t, PaneWorking, s.Poll(), "held under churn before the per-adapter cap (observation %d)", i)
	}
	c = "repainting final, no marker"
	require.Equal(t, PaneIdle, s.Poll(), "commits at the per-adapter cap (3), earlier than the default 6")
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
	session := NewSessionWithDeps(context.Background(), "test-session", "claude", ptyFactory, cmdExec)

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
	session := NewSessionWithDeps(context.Background(), "Surya's comment", "claude", ptyFactory, startMockExec())

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
	session := NewSessionWithDeps(context.Background(), "timeout-test", "prog", ptyFactory, cmdExec)

	err := session.Start(t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out waiting for tmux session")
	require.Contains(t, err.Error(), "cleanup error", "the failed kill is still reported")
	require.NotContains(t, err.Error(), "%!w", "a nil error must never be wrapped")
}

// forceHelpProbe installs canned --help outputs so the capability probes never exec a
// real binary in tests. The override is set and cleared under helpProbeMu, since
// binHelpContains reads it under the same lock from production goroutines.
func forceHelpProbe(t *testing.T, outputs map[string]string) {
	t.Helper()
	helpProbeMu.Lock()
	helpProbeOverride = outputs
	helpProbeMu.Unlock()
	t.Cleanup(func() {
		helpProbeMu.Lock()
		helpProbeOverride = nil
		helpProbeMu.Unlock()
	})
}

func TestResumeCommand(t *testing.T) {
	forceHelpProbe(t, map[string]string{
		"gemini": "-r, --resume   Resume a previous session",
		"codex":  "Commands:\n  resume  Resume a previous interactive session",
		// The canonical binary at an absolute path is probed at that path (it may
		// not be on PATH at all); keyed by path so a bare-name probe would miss.
		"/opt/agents/gemini": "-r, --resume   Resume a previous session",
	})

	cases := []struct {
		name    string
		program string
		want    string
	}{
		{"bare claude gets --continue", "claude", "claude --continue"},
		{"absolute claude path gets --continue", "/usr/local/bin/claude", "/usr/local/bin/claude --continue"},
		{"aider unchanged", "aider --model x", "aider --model x"},
		// Resume parity: gemini and codex now relaunch into their prior conversation.
		{"gemini gets --resume latest", "gemini", "gemini --resume latest"},
		{"codex gets resume --last", "codex", "codex resume --last"},
		// The codex subcommand cannot be spliced into an argv with flags; relaunch blank.
		{"codex with flags unchanged", "codex --model o3", "codex --model o3"},
		// An off-PATH absolute install still resumes: the probe targets the
		// program's own path because its basename is the canonical binary.
		{"absolute gemini path probes itself", "/opt/agents/gemini", "/opt/agents/gemini --resume latest"},
		// Detection is on the binary basename containing "claude", so a launcher wrapper that
		// exec's claude (the default_program many setups use) and a flag-bearing claude are
		// both recognized — the wrapper forwards the appended flag through to claude.
		{"claude launcher wrapper gets --continue", "/home/u/.claude-squad/launch-claude.sh", "/home/u/.claude-squad/launch-claude.sh --continue"},
		{"claude-wrapper gets --continue", "claude-wrapper", "claude-wrapper --continue"},
		{"claude with trailing flags gets --continue", "claude --model opus", "claude --model opus --continue"},
		// A non-claude binary under a claude-containing directory is NOT matched (basename wins).
		{"non-claude binary in claude dir unchanged", "/home/u/.claude-squad/bin/aider", "/home/u/.claude-squad/bin/aider"},
		{"unknown agent unchanged", "someagent", "someagent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionWithDeps(context.Background(), "resume-test", tc.program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
			require.Equal(t, tc.want, s.resumeCommand())
		})
	}
}

// An installed binary that predates its resume flag (probe finds no support) must
// relaunch blank rather than fail on an unknown flag. Codex's needle additionally pins
// the subcommand *listing* — help text that merely mentions resuming must not pass.
func TestResumeCommandProbeGate(t *testing.T) {
	forceHelpProbe(t, map[string]string{
		"gemini": "old gemini help with no such flag",
		"codex":  "old codex; sessions resume automatically on restart",
	})

	for _, program := range []string{"gemini", "codex"} {
		s := NewSessionWithDeps(context.Background(), "resume-test", program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
		require.Equal(t, program, s.resumeCommand(), "probe must fail closed for %s", program)
	}
}

// probeTarget picks which binary's --help the resume probe runs: the program's own first
// token when it is the canonical binary (wherever it lives), the canonical name otherwise —
// a wrapper's side effects must never run on a probe.
func TestProbeTarget(t *testing.T) {
	cases := []struct {
		program string
		key     agent.Key
		want    string
	}{
		{"gemini", agent.KeyGemini, "gemini"},
		{"/opt/agents/gemini", agent.KeyGemini, "/opt/agents/gemini"},
		{"/opt/agents/gemini --yolo", agent.KeyGemini, "/opt/agents/gemini"},
		{"launch-gemini.sh", agent.KeyGemini, "gemini"},
		{"/usr/local/bin/codex", agent.KeyCodex, "/usr/local/bin/codex"},
		{"codex-nightly", agent.KeyCodex, "codex"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, probeTarget(tc.program, tc.key), "program %q", tc.program)
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
	session := NewSessionWithDeps(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	// cmds[0] is the new-session launch; cmds[1] is the trailing attach from Restore.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "claude --continue")
	// The session name is keyed off the session, not the program, so it is unchanged.
	require.Contains(t, newSession, "new-session -d -s "+Prefix()+"cont-test")
}

func TestStartContinueLeavesNonClaudeUnchanged(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "cont-test", "aider --model x", ptyFactory, startMockExec())

	require.NoError(t, session.StartContinue(t.TempDir()))

	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.NotContains(t, newSession, "--continue")
	require.Contains(t, newSession, "aider --model x")
}

// Plain Start must never append --continue, even for claude — that is the first-time and
// PTY-reattach path, where there is nothing to continue.
func TestStartDoesNotAppendContinue(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))
	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "--continue")
}

func TestStartSessionInjectsClaudeConfigDir(t *testing.T) {
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
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "acct-session", "claude", ptyFactory, cmdExec)
	session.SetClaudeConfigDir("/home/tester/.claude-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e CLAUDE_CONFIG_DIR=/home/tester/.claude-quantivly")
	// The -e flag must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "CLAUDE_CONFIG_DIR"),
		strings.LastIndex(newSessionCmd, "claude"))
}

func TestStartSessionNoConfigDirNoEnvFlag(t *testing.T) {
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
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "plain-session", "claude", ptyFactory, cmdExec)
	require.NoError(t, session.Start(t.TempDir()))

	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "CLAUDE_CONFIG_DIR")
}

// TestStartSessionConfigDirReachesPane drives a real tmux server on Atrium's
// dedicated socket and asserts the injected CLAUDE_CONFIG_DIR is actually present
// in the session environment — the end-to-end proxy for the acceptance criterion
// (`tmux show-environment` shows the var). Self-skips when tmux is unavailable.
func TestStartSessionConfigDirReachesPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	name := fmt.Sprintf("acctenv-%d", rand.Int31())
	dir := t.TempDir()
	session := NewSession(context.Background(), name, "sleep 300")
	session.SetClaudeConfigDir(dir)
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	out, err := tmuxCommand(context.Background(), "show-environment", "-t", session.sanitizedName).Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "CLAUDE_CONFIG_DIR="+dir)
}

func TestStartSessionInjectsGHConfigDir(t *testing.T) {
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
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "gh-session", "claude", ptyFactory, cmdExec)
	session.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e GH_CONFIG_DIR=/home/tester/.config/gh-quantivly")
	// The -e flag must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "GH_CONFIG_DIR"),
		strings.LastIndex(newSessionCmd, "claude"))
}

func TestStartSessionNoGHConfigDirNoEnvFlag(t *testing.T) {
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
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "plain-gh-session", "claude", ptyFactory, cmdExec)
	require.NoError(t, session.Start(t.TempDir()))

	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "GH_CONFIG_DIR")
}

// TestStartSessionInjectsBothConfigDirs asserts CLAUDE_CONFIG_DIR and
// GH_CONFIG_DIR coexist as independent -e flags, both ahead of the program word.
func TestStartSessionInjectsBothConfigDirs(t *testing.T) {
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
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "both-session", "claude", ptyFactory, cmdExec)
	session.SetClaudeConfigDir("/home/tester/.claude-quantivly")
	session.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e CLAUDE_CONFIG_DIR=/home/tester/.claude-quantivly")
	require.Contains(t, newSessionCmd, "-e GH_CONFIG_DIR=/home/tester/.config/gh-quantivly")
	programIdx := strings.LastIndex(newSessionCmd, "claude")
	require.Less(t, strings.Index(newSessionCmd, "CLAUDE_CONFIG_DIR"), programIdx)
	require.Less(t, strings.Index(newSessionCmd, "GH_CONFIG_DIR"), programIdx)
}
