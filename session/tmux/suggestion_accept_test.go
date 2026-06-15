package tmux

import (
	"context"
	"os/exec"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ghostSuggestionPane is a raw capture (ANSI intact) of an idle claude box
// holding a dim ghost suggestion — the claude adapter's SuggestionVisible reads
// it as true. Mirrors the fixtures in session/agent/suggestion_test.go.
const ghostSuggestionPane = "transcript prose\n" +
	"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
	"\x1b[39m❯ \x1b[2mrun the failing test\x1b[0m\n" +
	"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
	"  ? for shortcuts"

func recordSendKeys(sent *[][]string, capture string) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			for i, arg := range cmd.Args {
				if arg == "send-keys" {
					*sent = append(*sent, cmd.Args[i+1:])
					break
				}
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			for _, arg := range cmd.Args {
				if arg == "capture-pane" {
					return []byte(capture), nil
				}
			}
			return []byte("%7\n"), nil
		},
	}
}

// TestAcceptSuggestion_SubmitsAfterTimeoutWhenCommitNotObserved pins the
// bounded-fallback branch of the accept→submit handshake: if the ghost never
// gives way to committed text within the timeout (here forced to 0, the capture
// always returns the same ghost frame), Enter is still sent. By the time the
// timeout elapses the async accept has long since rendered, so submitting is
// safe — and the suggestion must not be left inserted-but-unsent (the original
// bug). Right precedes Enter, as separate sends.
func TestAcceptSuggestion_SubmitsAfterTimeoutWhenCommitNotObserved(t *testing.T) {
	origTimeout, origPoll := suggestionAcceptTimeout, suggestionAcceptPollInterval
	suggestionAcceptTimeout, suggestionAcceptPollInterval = 0, 0
	t.Cleanup(func() {
		suggestionAcceptTimeout, suggestionAcceptPollInterval = origTimeout, origPoll
	})

	var sent [][]string
	sess := NewSessionWithDeps(context.Background(), "suggest-timeout", "claude",
		NewMockPtyFactory(t), recordSendKeys(&sent, ghostSuggestionPane))

	accepted, err := sess.AcceptSuggestion()
	require.NoError(t, err)
	require.True(t, accepted)

	require.Len(t, sent, 2, "Right and Enter must both be sent even when the commit isn't observed")
	assert.Equal(t, "Right", sent[0][len(sent[0])-1])
	assert.Equal(t, "Enter", sent[1][len(sent[1])-1])
}
