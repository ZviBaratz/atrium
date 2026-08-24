package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/internal/handover"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandboxHandover points the data dir at a fresh temp dir so the lock this package's
// tests take is never the one a running Atrium is on. The package TestMain already
// sandboxes HOME; this narrows it per test so a leaked hold cannot leak between them.
func sandboxHandover(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// handoverProbe records what a concurrent reader would have seen while Run was blocked.
// The observation is taken from inside the attach itself, which is the only vantage
// point a test can reach mid-Run — and the only one that proves the lock spans the
// suspension rather than merely being taken and dropped somewhere inside it.
type handoverProbe struct {
	held    bool
	payload handover.Payload
}

// observeDuringRun wraps cmd.attach so the probe samples the lock while Run is still
// blocked on it.
func observeDuringRun(t *testing.T, cmd *attachCommand) *handoverProbe {
	t.Helper()
	p := &handoverProbe{}
	orig := cmd.attach
	cmd.attach = func() (chan struct{}, error) {
		ch, err := orig()
		p.held, p.payload, _ = handover.Held()
		return ch, err
	}
	return p
}

// TestAttachRunPublishesTheHandover is the writer half of #760: while the event loop is
// blocked, another process must be able to see that nothing is draining the outbox.
//
// Asserted from inside the attach rather than after it, because "taken and released"
// is not the property — the lock has to be held for the span in which the tick is not
// running, and a Hold/release pair anywhere inside Run would satisfy a weaker check.
func TestAttachRunPublishesTheHandover(t *testing.T) {
	sandboxHandover(t)
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{
		attach:   func() (chan struct{}, error) { return ch, nil },
		handover: handover.Payload{Kind: handover.KindAttach, Label: "fix-auth"},
	}
	probe := observeDuringRun(t, cmd)
	require.NoError(t, cmd.Run())

	assert.True(t, probe.held, "the lock must be held for the whole of Run")
	assert.Equal(t, handover.Payload{Kind: handover.KindAttach, Label: "fix-auth"}, probe.payload,
		"and it must name what the terminal went to, so the warning can say which session to detach from")

	nowHeld, _, known := handover.Held()
	assert.True(t, known)
	assert.False(t, nowHeld, "and released on return, or every later `atrium new` would be told a lie")
}

// TestAttachRunReleasesTheHandoverWhenTheAttachFails: a Run that never suspended
// anything must not leave the lock behind. Left held, the next `atrium new` would
// report a parked TUI forever — the failure mode a marker file has and a flock is
// supposed to be immune to, reintroduced by hand.
func TestAttachRunReleasesTheHandoverWhenTheAttachFails(t *testing.T) {
	sandboxHandover(t)
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	cmd := &attachCommand{
		attach:   func() (chan struct{}, error) { return nil, errors.New("attach failed") },
		handover: handover.Payload{Kind: handover.KindAttach, Label: "fix-auth"},
	}
	require.Error(t, cmd.Run())

	held, _, known := handover.Held()
	assert.True(t, known)
	assert.False(t, held)
}

// TestAttachRunProceedsWhenTheHandoverCannotBeRecorded: handing over the terminal must
// never depend on a lock file. Failing open costs only the warning — a headless command
// reads the lock as free and stays quiet, which is what happened before this existed.
func TestAttachRunProceedsWhenTheHandoverCannotBeRecorded(t *testing.T) {
	sandboxHandover(t)
	origIsTerminal, origHold := isTerminal, handoverHold
	t.Cleanup(func() { isTerminal, handoverHold = origIsTerminal, origHold })
	isTerminal = func(int) bool { return false }
	handoverHold = func(handover.Payload) (func(), error) { return nil, errors.New("read-only data dir") }

	attached := false
	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{attach: func() (chan struct{}, error) { attached = true; return ch, nil }}
	require.NoError(t, cmd.Run())
	assert.True(t, attached, "the attach happens whatever the lock did")
}

// TestBothBuildersWireTheHandover is the assertion the rest of this file was missing.
// Every other test here constructs an attachCommand literally, so deleting the `handover:`
// line from either builder shipped green — and an attach would then publish the zero
// Payload, leaving `atrium new` unable to name what to detach from for every real attach
// while every test around it still passed.
//
// It goes through the constructors rather than the tea.Cmd they are wrapped in for the
// reason terminalCustomCommandExec's doc gives: tea.Exec's message type is unexported, so
// the wiring is invisible from the far side of it.
func TestBothBuildersWireTheHandover(t *testing.T) {
	h, inst := newCustomCommandHome(t, nil)

	cmd, _ := h.attachExecCommand(func() (chan struct{}, error) { return nil, nil }, inst, nil)
	assert.Equal(t, handover.Payload{Kind: handover.KindAttach, Label: inst.Title()}, cmd.handover,
		"a session attach must publish the session it handed the terminal to")

	termCmd, _ := h.terminalCustomCommandExec(customCommandSpec{key: "g", desc: "lazygit"})
	assert.Equal(t, handover.Payload{Kind: handover.KindCommand, Label: "lazygit"}, termCmd.handover,
		"a terminal command must publish its own name and kind, since Resumes phrases the two differently")
}

// titledInstance builds an unstarted session with nothing but a title, which is all
// the label and notice helpers below read. It goes through the real constructor rather
// than a struct literal because the identity fields are unexported and guarded (#795).
func titledInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	return inst
}

// TestAttachExecLabelsTheSession pins where the label comes from for each attach shape.
// killTarget is nil only for the terminal tab, and that tab shows the selected row —
// every terminal-tab site selects before attaching — so the selection answers for it.
func TestAttachExecLabelsTheSession(t *testing.T) {
	killTarget := titledInstance(t, "kill-target")
	selected := titledInstance(t, "selected")

	assert.Equal(t, "kill-target", attachLabel(killTarget, selected),
		"a session attach names the instance it bound up front")
	assert.Equal(t, "selected", attachLabel(nil, selected),
		"the terminal tab has no kill target, so the selection is the session it belongs to")
	assert.Equal(t, "", attachLabel(nil, nil),
		"and with neither, no label rather than a guess")
}
