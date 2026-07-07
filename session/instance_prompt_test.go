package session

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptSignature(t *testing.T) {
	cases := []struct {
		name, prompt, want string
	}{
		{"single line squashed", "do the thing", "dothething"},
		{"first non-empty line only", "\n\n  first real line\nsecond", "firstrealline"},
		{"capped to the max runes", strings.Repeat("a", promptSignatureMax+20), strings.Repeat("a", promptSignatureMax)},
		{"all blank yields empty", "   \n\t\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, promptSignature(c.prompt))
		})
	}
}

func TestIsSoftPromptError(t *testing.T) {
	for _, err := range []error{errPromptNotReady, errPromptNotLanded, errPromptNotSubmitted} {
		require.True(t, IsSoftPromptError(err), "%v must be a soft (retryable) outcome", err)
	}
	require.False(t, IsSoftPromptError(nil))
	require.False(t, IsSoftPromptError(assertHardErr()), "a hard tmux error must not be soft")
}

func assertHardErr() error { return exec.ErrNotFound }

// fakeAgentPane is a stateful executor that models an agent's composer end-to-end: it
// renders the input box on capture-pane, accepts literal typing (send-keys -l) and pastes
// (set-buffer + paste-buffer) into the box, and clears the box on a submitting Enter. This
// lets a full SendPrompt run be driven without a real tmux server, and is robust to how many
// times SendPrompt re-captures (no brittle fixed response sequence).
type fakeAgentPane struct {
	box     string // current composer text ("" = empty/submitted)
	pending string // text staged by set-buffer, applied on paste-buffer
	gate    bool   // a startup gate is up: no composer, keystrokes would be swallowed
	noLand  bool   // drop typed/pasted text on the floor (simulate a send that doesn't land)

	typed  []string // recorded send-keys -l payloads
	pasted []string // recorded paste-buffer applications
	enters int      // recorded submitting Enter taps
}

func (f *fakeAgentPane) render() string {
	if f.gate {
		return "  Do you trust the files in this folder?\n  ❯ 1. Yes, proceed\n    2. No, exit\n"
	}
	var b strings.Builder
	b.WriteString("╭──────────────────────────────────────────────╮\n")
	if f.box == "" {
		b.WriteString("│ ❯                                              │\n")
	} else {
		for i, ln := range strings.Split(f.box, "\n") {
			if i == 0 {
				b.WriteString("│ ❯ " + ln + " │\n")
			} else {
				b.WriteString("│   " + ln + " │\n")
			}
		}
	}
	b.WriteString("╰──────────────────────────────────────────────╯\n")
	b.WriteString("  ? for shortcuts\n")
	return b.String()
}

func (f *fakeAgentPane) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			args := cmd.Args
			switch {
			case slices.Contains(args, "send-keys") && slices.Contains(args, "Enter"):
				f.enters++
				f.box = "" // a submitting Enter clears the composer
			case slices.Contains(args, "send-keys") && slices.Contains(args, "-l"):
				text := lastArg(args)
				f.typed = append(f.typed, text)
				if !f.noLand {
					f.box += text
				}
			case slices.Contains(args, "set-buffer"):
				f.pending = lastArg(args)
			case slices.Contains(args, "paste-buffer"):
				f.pasted = append(f.pasted, f.pending)
				if !f.noLand {
					f.box += f.pending
				}
			}
			return nil // has-session etc.: alive
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			args := strings.Join(cmd.Args, " ")
			switch {
			case strings.Contains(args, "list-panes"):
				return []byte("%7\n"), nil
			case strings.Contains(args, "capture-pane"):
				return []byte(f.render()), nil
			default:
				return []byte("%7\n"), nil
			}
		},
	}
}

func lastArg(args []string) string { return args[len(args)-1] }

func newPromptInstance(t *testing.T, name string, fake *fakeAgentPane) *Instance {
	t.Helper()
	return &Instance{
		Title:       name,
		status:      Loading,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), name, "claude", tmux.MakePtyFactory(), fake.exec()),
	}
}

func TestSendPrompt_VerifiedDelivery(t *testing.T) {
	defer func(d any) { _ = d }(nil)
	prev := promptVerifyInterval
	promptVerifyInterval = 0 // don't sleep while polling for confirmation
	defer func() { promptVerifyInterval = prev }()

	t.Run("single-line prompt types, lands, and submits", func(t *testing.T) {
		fake := &fakeAgentPane{}
		inst := newPromptInstance(t, "single", fake)

		require.NoError(t, inst.SendPrompt("do the thing"))
		require.Equal(t, []string{"do the thing"}, fake.typed, "a single-line prompt is typed literally")
		require.Empty(t, fake.pasted, "a single-line prompt must not use the paste path")
		require.Equal(t, 1, fake.enters, "the prompt must be submitted exactly once")
		require.Equal(t, "", fake.box, "the composer must be empty after submission")
	})

	t.Run("multi-line prompt is pasted as one block and submitted once", func(t *testing.T) {
		fake := &fakeAgentPane{}
		inst := newPromptInstance(t, "multi", fake)

		require.NoError(t, inst.SendPrompt("line one\nline two\nline three"))
		require.Empty(t, fake.typed, "a multi-line prompt must not be typed with literal send-keys (early submit)")
		require.Equal(t, []string{"line one\nline two\nline three"}, fake.pasted,
			"a multi-line prompt must be pasted as a single bracketed-paste block")
		require.Equal(t, 1, fake.enters, "the whole block must be submitted by exactly one Enter")
	})

	t.Run("not awaiting input yields a soft error and never types", func(t *testing.T) {
		fake := &fakeAgentPane{gate: true} // a trust screen is up
		inst := newPromptInstance(t, "gated", fake)

		err := inst.SendPrompt("do the thing")
		require.True(t, IsSoftPromptError(err), "a gate up must defer (soft), got %v", err)
		require.Empty(t, fake.typed, "nothing may be typed onto a startup gate")
		require.Empty(t, fake.pasted)
		require.Equal(t, 0, fake.enters)
	})

	t.Run("text that does not land yields a soft error before submitting", func(t *testing.T) {
		fake := &fakeAgentPane{noLand: true} // typing is dropped on the floor
		inst := newPromptInstance(t, "noland", fake)

		err := inst.SendPrompt("do the thing")
		require.True(t, IsSoftPromptError(err), "an unconfirmed landing must defer (soft), got %v", err)
		require.NotEmpty(t, fake.typed, "it must have attempted to type")
		require.Equal(t, 0, fake.enters, "it must not submit when the text never landed")
	})

	t.Run("a retry after a staged-but-unsubmitted prompt does not double it", func(t *testing.T) {
		// Simulate a prior attempt that typed the prompt but could not confirm submission:
		// the box already holds the text. A fresh SendPrompt must skip typing and just submit.
		fake := &fakeAgentPane{box: "do the thing"}
		inst := newPromptInstance(t, "retry", fake)

		require.NoError(t, inst.SendPrompt("do the thing"))
		require.Empty(t, fake.typed, "an already-staged prompt must not be retyped (no doubling)")
		require.Empty(t, fake.pasted)
		require.Equal(t, 1, fake.enters, "the staged prompt must simply be submitted")
	})
}

func TestPendingPromptSurvivesRoundTrip(t *testing.T) {
	store := newTestStorage(t)

	a := newPausedInstance(t, "pending")
	// Plant the queue directly (same package) with a deliberately long-past queue time;
	// QueuePrompt would stamp it with now and defeat the clock-restart assertion.
	a.promptQueue = []queuedPrompt{{text: "finish the migration", queuedAt: time.Unix(1000, 0)}}

	require.NoError(t, store.SaveInstances([]*Instance{a}))
	got, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)

	require.Equal(t, "finish the migration", got[0].Prompt(),
		"an undelivered prompt must survive a restart so it can be re-delivered")
	require.False(t, got[0].PromptQueuedAt().IsZero(),
		"a restored pending prompt must have a delivery clock")
	require.True(t, got[0].PromptQueuedAt().After(time.Unix(1000, 0)),
		"the delivery timeout must restart from reload, not keep the stale wall-clock age")
}

func TestSendPrompt_NotStartedErrorsHard(t *testing.T) {
	fake := &fakeAgentPane{}
	inst := &Instance{Title: "unstarted", status: Ready,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "unstarted", "claude", tmux.MakePtyFactory(), fake.exec())}

	err := inst.SendPrompt("x")
	require.Error(t, err)
	assert.False(t, IsSoftPromptError(err), "an unstarted instance is a hard error, not a retryable soft one")
}
