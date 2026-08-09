package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

const boxPane = "" +
	"╭──────────────────────────────────────────────╮\n" +
	"│ ❯                                              │\n" +
	"╰──────────────────────────────────────────────╯\n" +
	"  ? for shortcuts\n"

const gatePane = "" +
	"  Do you trust the files in this folder?\n" +
	"  ❯ 1. Yes, proceed\n" +
	"    2. No, exit\n"

// captureExec answers list-panes with a fixed pane id and capture-pane with the supplied
// content, and records every send-keys / set-buffer / paste-buffer invocation's args.
func captureExec(content string, sent *[][]string) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			a := strings.Join(cmd.Args, " ")
			if strings.Contains(a, "send-keys") || strings.Contains(a, "set-buffer") || strings.Contains(a, "paste-buffer") {
				*sent = append(*sent, cmd.Args)
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			a := strings.Join(cmd.Args, " ")
			if strings.Contains(a, "capture-pane") {
				return []byte(content), nil
			}
			return []byte("%7\n"), nil // list-panes
		},
	}
}

func TestAwaitingInput(t *testing.T) {
	t.Run("true when the composer is on screen", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "box", "claude", NewMockPtyFactory(t), captureExec(boxPane, &sent))
		require.True(t, s.AwaitingInput())
	})

	t.Run("false when a startup gate is up", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "gate", "claude", NewMockPtyFactory(t), captureExec(gatePane, &sent))
		require.False(t, s.AwaitingInput(), "a trust gate must never read as ready to receive a prompt")
	})

	t.Run("false on a blank pane", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "blank", "claude", NewMockPtyFactory(t), captureExec("   \n", &sent))
		require.False(t, s.AwaitingInput())
	})
}

// #512's consequence, at the level the bug was actually reported at. agy's selection
// pointer is a plain ASCII ">", the same character its composer uses, so isInputBoxLine
// reports a live input box on BOTH of its dialogs. While the adapter carried no Gates and
// no Prompts, AwaitingInput reduced to that box check alone and returned true — and
// app/app_poll.go's promptDeliveryReady then let a queued prompt be typed into a dialog
// whose highlighted row is "> 1. Yes". That failed OPEN: Enter approves the command.
//
// So the matchers are only half the fix; this is the half that proves it. Each dialog must
// read false, and — the control that keeps "fixed" from meaning "delivery broken outright"
// — the real composer must still read true.
func TestAwaitingInputAgyDialogs(t *testing.T) {
	agyIdle := "────\n>\n────\n? for shortcuts                          Gemini 3.1 Pro · high"

	t.Run("true on the composer", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "agy-idle", "agy", NewMockPtyFactory(t), captureExec(agyIdle, &sent))
		require.True(t, s.AwaitingInput(), "a real agy composer must still receive queued prompts")
	})

	t.Run("false while a tool confirmation is up", func(t *testing.T) {
		pane := "Do you want to proceed?\n> 1. Yes\n  4. No\n\n" +
			"  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\nesc to cancel"
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "agy-confirm", "agy", NewMockPtyFactory(t), captureExec(pane, &sent))
		require.False(t, s.AwaitingInput(),
			"the dialog's \"> 1. Yes\" still reads as an input box, so only the prompt matcher "+
				"keeps a queued prompt from being typed into it")
	})

	t.Run("false while the trust gate is up", func(t *testing.T) {
		pane := "Do you trust the contents of this project?\n\n> Yes, I trust this folder\n" +
			"  No, exit\n\n  ↑/↓ Navigate · enter Confirm"
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "agy-gate", "agy", NewMockPtyFactory(t), captureExec(pane, &sent))
		require.False(t, s.AwaitingInput(),
			"a trust gate must never read as ready to receive a prompt")
	})
}

// #510's consequence, at the level the bug was actually reported at, and the exact mirror
// of #512 above. Codex draws its composer with "›" (U+203A), which isInputBoxLine did not
// accept, so InputBoxVisible was permanently false for every codex session — and with it
// AwaitingInput, and with that app/app_poll.go's promptDeliveryReady, whose awaitingInput
// precondition its 60s valve never bypasses. A queued prompt was therefore never delivered
// AND never expired. That failed CLOSED, where agy's failed open.
//
// app_test.go's TestPromptDeliveryReady cannot cover this: it passes awaitingInput in as a
// bool, so it never runs the line that computes it. This does.
//
// Panes are verbatim tmux captures from a live codex-cli 0.147.0 (2026-08-09); the full
// width ladder and the readback live in session/agent/codex_pane_test.go.
func TestAwaitingInputCodex(t *testing.T) {
	codexComposer := strings.Join([]string{
		"╭─────────────────────────────────────────────╮",
		"│ >_ OpenAI Codex (v0.147.0)                  │",
		"│                                             │",
		"│ model:     gpt-5.6-terra   /model to change │",
		"│ directory: /tmp/cx510/repo                  │",
		"╰─────────────────────────────────────────────╯",
		"",
		"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
		"",
		"",
		"› Run this exact shell command and nothing else: rm -rf /tmp/cx510/repo/build",
		"",
		"",
		"✔ You approved codex to run rm -rf /tmp/cx510/repo/build this time",
		"",
		"• Ran rm -rf /tmp/cx510/repo/build",
		"  └ (no output)",
		"",
		"─ Worked for 1m 39s ────────────────────────────────────────────────────────────────────────────────────────────────────",
		"",
		"",
		"› refactor the parser and add a regression test",
		"",
		"  gpt-5.6-terra default · /tmp/cx510/repo",
	}, "\n")
	codexGate := strings.Join([]string{
		"> You are in /tmp/cx510/fresh",
		"",
		"  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt",
		"  injection. Trusting the directory allows project-local config, hooks, and exec policies to load.",
		"",
		"› 1. Yes, continue",
		"  2. No, quit",
		"",
		"  Press enter to continue",
	}, "\n")
	codexGateAt20 := strings.Join([]string{
		"> You are in /tmp/cx",
		"",
		"  Do you trust the",
		"  contents of this",
		"  directory? Working",
		"  with untrusted",
		"  contents comes",
		"  with higher risk",
		"  of prompt",
		"  injection.",
		"  Trusting the",
		"  directory allows",
		"  project-local",
		"  config, hooks, and",
		"  exec policies to",
		"  load.",
		"",
		"› 1. Yes, continue",
		"  2. No, quit",
		"",
		"  Press enter to con",
	}, "\n")
	codexApproval := strings.Join([]string{
		"╭─────────────────────────────────────────────╮",
		"│ >_ OpenAI Codex (v0.147.0)                  │",
		"│                                             │",
		"│ model:     gpt-5.6-terra   /model to change │",
		"│ directory: /tmp/cx510/repo                  │",
		"╰─────────────────────────────────────────────╯",
		"",
		"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
		"",
		"",
		"› Run this exact shell command and nothing else: rm -rf /tmp/cx510/repo/build",
		"",
		"",
		"• Running rm -rf /tmp/cx510/repo/build",
		"",
		"",
		"  Would you like to run the following command?",
		"",
		"  Environment: local",
		"",
		"  $ rm -rf /tmp/cx510/repo/build",
		"",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for commands that start with `rm -rf /tmp/cx510/repo/build` (p)",
		"  3. No, and tell Codex what to do differently (esc)",
		"",
		"  Press enter to confirm or esc to cancel",
	}, "\n")

	t.Run("true on the composer", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex-idle", "codex", NewMockPtyFactory(t), captureExec(codexComposer, &sent))
		require.True(t, s.AwaitingInput(),
			"a real codex composer must receive queued prompts; while this was false no codex "+
				"session could ever be handed one (#510)")
	})

	t.Run("the readback confirms a landing", func(t *testing.T) {
		// AwaitingInput alone does not prove delivery COMPLETES: session/prompt.go types the
		// prompt, then requires boxHoldsPrompt to read it back before submitting. That is a
		// second consumer of the same predicate, and it needs the "›" stripped off.
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex-read", "codex", NewMockPtyFactory(t), captureExec(codexComposer, &sent))
		text, ok := s.InputBoxText()
		require.True(t, ok)
		require.Equal(t, "refactor the parser and add a regression test", text)
	})

	t.Run("false while the trust gate is up", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex-gate", "codex", NewMockPtyFactory(t), captureExec(codexGate, &sent))
		require.False(t, s.AwaitingInput(), "a trust gate must never read as ready to receive a prompt")
	})

	t.Run("false on the trust gate at width 20, where GateUp itself misses", func(t *testing.T) {
		// The rung that justifies Adapter.SelectorSharesPromptChar. Codex wraps the gate body
		// rather than truncating it, so at 20 columns the body pushes the headline out of the
		// flatten budget and GateUp returns false. The gate's "› 1. Yes, continue" is then the
		// only composer-shaped line on screen, and the selector-row rule is all that stands
		// between a queued prompt and a trust screen.
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex-gate-20", "codex", NewMockPtyFactory(t), captureExec(codexGateAt20, &sent))
		require.False(t, s.AwaitingInput())
	})

	t.Run("false while an approval overlay is up", func(t *testing.T) {
		// Excluded by DetectPrompt, not by the box check: codex echoes the submitted message
		// into its transcript with the same "›", so this frame still carries a line that reads
		// as a composer. Driving codex 0.147.0, typing "hey there" at this overlay approved the
		// command outright — "y" is the accelerator for "1. Yes, proceed (y)" and confirms with
		// no Enter — which is what the approval matcher is holding back (#347).
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex-approval", "codex", NewMockPtyFactory(t), captureExec(codexApproval, &sent))
		require.False(t, s.AwaitingInput())
	})
}

func TestSendPasted_UsesBracketedPasteBuffer(t *testing.T) {
	var sent [][]string
	s := NewSessionWithDeps(context.Background(), "paste", "claude", NewMockPtyFactory(t), captureExec(boxPane, &sent))

	require.NoError(t, s.SendPasted("line one\nline two"))

	var setBuffer, pasteBuffer []string
	for _, args := range sent {
		switch {
		case contains(args, "set-buffer"):
			setBuffer = args
		case contains(args, "paste-buffer"):
			pasteBuffer = args
		}
	}
	require.NotNil(t, setBuffer, "the text must be staged with set-buffer")
	require.Equal(t, "line one\nline two", setBuffer[len(setBuffer)-1], "the staged value must be the verbatim multi-line text")
	require.NotNil(t, pasteBuffer, "the staged buffer must be pasted")
	require.True(t, contains(pasteBuffer, "-p"), "paste must use -p (bracketed paste) so the agent does not submit per line")
	require.True(t, contains(pasteBuffer, "-d"), "paste must use -d so the buffer is cleaned up")
}

// collapsedPastePane is claude's composer showing a collapsed-paste chip (its render of a
// ≥4-line bracketed paste) instead of the literal pasted text.
const collapsedPastePane = "" +
	"  some earlier transcript line\n" +
	"────────────────────────────────────────────────\n" +
	"❯ [Pasted text #1 +29 lines]\n" +
	"────────────────────────────────────────────────\n" +
	"  ? for shortcuts\n"

func TestInputBoxCollapsedPaste(t *testing.T) {
	t.Run("true when the composer shows a collapsed-paste chip", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "chip", "claude", NewMockPtyFactory(t), captureExec(collapsedPastePane, &sent))
		require.True(t, s.InputBoxCollapsedPaste())
	})

	t.Run("false for an empty composer", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "empty", "claude", NewMockPtyFactory(t), captureExec(boxPane, &sent))
		require.False(t, s.InputBoxCollapsedPaste())
	})

	t.Run("false for an agent without a PasteCollapsed predicate", func(t *testing.T) {
		var sent [][]string
		s := NewSessionWithDeps(context.Background(), "codex", "codex", NewMockPtyFactory(t), captureExec(collapsedPastePane, &sent))
		require.False(t, s.InputBoxCollapsedPaste(), "codex renders pastes inline; the chip text must not be mistaken for one")
	})
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
