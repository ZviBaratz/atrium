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

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
