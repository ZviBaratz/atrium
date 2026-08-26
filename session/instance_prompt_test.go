package session

import (
	"context"
	"fmt"
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
	box      string // current composer text ("" = empty/submitted)
	pending  string // text staged by set-buffer, applied on paste-buffer
	gate     bool   // a startup gate is up: no composer, keystrokes would be swallowed
	noLand   bool   // drop typed/pasted text on the floor (simulate a send that doesn't land)
	collapse bool   // render a ≥4-line paste as claude's "[Pasted text +N lines]" chip, not the text

	// gateAfterCaptures raises the gate on the Nth capture-pane call and every one after it
	// (0 = never), so that capture itself already shows the dialog. It models a dialog
	// appearing DURING delivery: SendPrompt reads the pane several times, and each read is a
	// separate moment.
	gateAfterCaptures int

	typed    []string // recorded send-keys -l payloads
	pasted   []string // recorded paste-buffer applications
	enters   int      // recorded submitting Enter taps
	captures int      // recorded capture-pane calls
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
				switch {
				case f.noLand:
					// dropped on the floor
				case f.collapse && strings.Count(f.pending, "\n") >= 3:
					// claude collapses a ≥4-line bracketed paste into a placeholder chip whose
					// text is NOT the pasted content, defeating a first-line signature match.
					f.box += fmt.Sprintf("[Pasted text +%d lines]", strings.Count(f.pending, "\n"))
				default:
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
				f.captures++
				if f.gateAfterCaptures > 0 && f.captures >= f.gateAfterCaptures {
					f.gate = true
				}
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
		ident:       identity{title: name},
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

	t.Run("a collapsed multi-line paste is recognized as landed and submitted once", func(t *testing.T) {
		// claude collapses a ≥4-line bracketed paste to "[Pasted text +N lines]", so the
		// first-line signature never appears in the box. Delivery must treat the chip as landed:
		// submit once, paste once (never re-paste and accumulate chips across retries).
		fake := &fakeAgentPane{collapse: true}
		inst := newPromptInstance(t, "collapsed", fake)

		require.NoError(t, inst.SendPrompt("line one\nline two\nline three\nline four\nline five"))
		require.Len(t, fake.pasted, 1, "the prompt must be pasted exactly once (no re-paste accumulation)")
		require.Equal(t, 1, fake.enters, "a collapsed paste must be submitted exactly once")
		require.Equal(t, "", fake.box, "the composer must be empty after submission")
	})

	t.Run("a retry sees the collapsed chip already staged and does not re-paste", func(t *testing.T) {
		// A prior attempt pasted but could not confirm submission: the box already holds the chip.
		// A fresh SendPrompt must skip the paste and just submit — the anti-accumulation guard.
		fake := &fakeAgentPane{box: "[Pasted text +12 lines]", collapse: true}
		inst := newPromptInstance(t, "collapsed-retry", fake)

		require.NoError(t, inst.SendPrompt("line one\nline two\nline three\nline four"))
		require.Empty(t, fake.pasted, "an already-staged collapsed paste must not be re-pasted")
		require.Equal(t, 1, fake.enters, "the staged chip must simply be submitted")
	})
}

func TestPendingPromptSurvivesRoundTrip(t *testing.T) {
	store := newTestStorage(t)

	a := newPausedInstance(t, "pending")
	// Plant the queue directly (same package) with a deliberately long-past queue time;
	// QueuePrompt would stamp it with now and defeat the clock-restart assertion.
	a.promptQueue = []queuedPrompt{{text: "finish the migration", queuedAt: time.Unix(1000, 0)}}

	require.NoError(t, store.SaveInstances([]*Instance{a}))
	got, _, err := store.LoadInstances(context.Background())
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
	inst := &Instance{ident: identity{title: "unstarted"}, status: Ready,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "unstarted", "claude", tmux.MakePtyFactory(), fake.exec())}

	err := inst.SendPrompt("x")
	require.Error(t, err)
	assert.False(t, IsSoftPromptError(err), "an unstarted instance is a hard error, not a retryable soft one")
}

// Readiness is re-asserted at every point that sends keystrokes, not only at entry.
//
// SendPrompt reads the pane several times, and each read is a separate moment: the entry
// AwaitingInput check describes the pane as it was then, not as it is when the keystrokes go
// out. A dialog that rises in one of those gaps is where a queued prompt gets typed into a
// modal, or where Enter lands on the modal's pre-highlighted option — for copilot that option
// is "Yes, and add these directories to the allowed list", so the miss widens the agent's
// filesystem reach rather than merely losing the prompt.
//
// gateAfterCaptures raises the gate after the entry check has already passed, which is the
// shape a single entry gate cannot cover. Both cases must come back soft (retry next tick)
// having sent nothing.
func TestSendPrompt_ReAssertsReadinessBeforeActing(t *testing.T) {
	prev := promptVerifyInterval
	promptVerifyInterval = 0
	defer func() { promptVerifyInterval = prev }()

	t.Run("a gate that rises before typing types nothing", func(t *testing.T) {
		// Captures: 1 the entry check, 2 the boxHoldsPrompt readback, 3 the pre-typing check.
		fake := &fakeAgentPane{gateAfterCaptures: 3}
		inst := newPromptInstance(t, "gate-before-typing", fake)

		err := inst.SendPrompt("do the thing")
		require.ErrorIs(t, err, errPromptNotReady, "a gate mid-delivery is a soft retry")
		require.True(t, IsSoftPromptError(err))
		require.Empty(t, fake.typed, "nothing may be typed into a dialog")
		require.Empty(t, fake.pasted)
		require.Zero(t, fake.enters, "and nothing may be submitted")
	})

	t.Run("a gate that rises before Enter submits nothing", func(t *testing.T) {
		// The box already holds the prompt from an earlier attempt whose submission was not
		// confirmed, so typing is skipped and the very next act is TapEnter. Captures: 1 the
		// entry check, 2 the readback that finds it already staged, 3 the pre-Enter check.
		fake := &fakeAgentPane{box: "do the thing", gateAfterCaptures: 3}
		inst := newPromptInstance(t, "gate-before-enter", fake)

		err := inst.SendPrompt("do the thing")
		require.ErrorIs(t, err, errPromptNotReady)
		require.Zero(t, fake.enters,
			"Enter must not be tapped on a screen that replaced the composer — that is the tap "+
				"that selects a dialog's highlighted option")
		require.Empty(t, fake.typed, "and the staged text must not be retyped")
	})

	t.Run("an unbroken delivery still goes through", func(t *testing.T) {
		// The other direction: the extra checks must not have made delivery impossible.
		fake := &fakeAgentPane{}
		inst := newPromptInstance(t, "no-gate", fake)
		require.NoError(t, inst.SendPrompt("do the thing"))
		require.Equal(t, []string{"do the thing"}, fake.typed)
		require.Equal(t, 1, fake.enters)
	})
}
