package app

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// stubYieldTabs swaps the tab-delay seam for counters and reports whether the field
// was in the child's hands at the moment the attach func ran. Seamed rather than
// driven for real because attachCommand.Run reads os.Stdin, which under `go test`
// from a terminal is the developer's own tty.
func stubYieldTabs(t *testing.T, yielded, retaken *int) {
	t.Helper()
	orig := yieldTabs
	t.Cleanup(func() { yieldTabs = orig })
	yieldTabs = func(*os.File) func() {
		*yielded++
		return func() { *retaken++ }
	}
}

// A cooked child owns the terminal with OPOST on, so it must NOT inherit the
// tab-delay field app.Run set: the driver would expand the child's tabs itself,
// counting its ANSI escape bytes as printable columns and misaligning colourized,
// tab-aligned output (`git diff --color=always`, `go test`) that was correct before
// #796. Asserted from inside the attach func, which is the only window the child is
// alive in — a yield taken after it, or given back before it, protects nothing.
func TestAttachCommandRun_CookedYieldsHardTabs(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return true }

	borrowed, resumed := 0, 0
	defer stubSuspendInterrupt(t, &borrowed, &resumed)()

	yielded, retaken := 0, 0
	stubYieldTabs(t, &yielded, &retaken)

	heldDuringChild := false
	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: false, attach: func() (chan struct{}, error) {
		heldDuringChild = yielded == 1 && retaken == 0
		return ch, nil
	}}

	require.NoError(t, cmd.Run())
	assert.True(t, heldDuringChild, "the tab-delay field must be handed back before the child starts")
	assert.Equal(t, 1, retaken, "and taken again before Run returns — bubbletea's RestoreTerminal "+
		"re-reads the field on the way back in, so a yield that leaked would re-enable the tab "+
		"bytes TestFrameEmitsNoRawTab forbids for the rest of the session")
}

// A raw takeover that GOT raw mode must not yield: makeRaw clears OPOST, so the
// driver never consults the field, and moving it would be a tty write for nothing on
// the tmux-attach path that runs on every session open.
//
// makeRaw is stubbed to SUCCEED rather than isTerminal stubbed to false, because the
// reason this branch is exempt is that makeRaw ran — an exemption asserted over a
// path where makeRaw was never called is asserting it for a different reason than
// the one the code gives.
func TestAttachCommandRun_RawKeepsHardTabs(t *testing.T) {
	origIsTerminal, origMakeRaw, origRestore := isTerminal, makeRaw, restoreTerm
	t.Cleanup(func() { isTerminal, makeRaw, restoreTerm = origIsTerminal, origMakeRaw, origRestore })
	isTerminal = func(int) bool { return true }
	makeRaw = func(int) (*term.State, error) { return &term.State{}, nil }
	restoreTerm = func(int, *term.State) error { return nil }

	yielded, retaken := 0, 0
	stubYieldTabs(t, &yielded, &retaken)

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	require.False(t, cmd.rawModeFailed, "precondition: this is the branch where raw mode was obtained")
	assert.Zero(t, yielded, "a raw takeover has OPOST off and nothing to yield")
	assert.Zero(t, retaken)
}

// A takeover that ASKED for raw mode and could not get it runs cooked — OPOST on, the
// constrained Docker/SSH tty TestAttachCommandRun_RawModeFailureStillAttaches exists
// for. It needs the yield exactly as much as a cooked request does, so this branch is
// keyed on the outcome and not on a.raw.
//
// Note the asymmetry with suspendInterrupt, whose own condition in Run is deliberately
// NOT extended to rawModeFailed. That is a judgement about who should receive a Ctrl+C;
// this is only a question of what the tty driver is doing.
func TestAttachCommandRun_FailedRawYieldsHardTabs(t *testing.T) {
	origIsTerminal, origMakeRaw := isTerminal, makeRaw
	t.Cleanup(func() { isTerminal, makeRaw = origIsTerminal, origMakeRaw })
	isTerminal = func(int) bool { return true }
	makeRaw = func(int) (*term.State, error) { return nil, errors.New("no raw mode on this tty") }

	yielded, retaken := 0, 0
	stubYieldTabs(t, &yielded, &retaken)

	heldDuringChild := false
	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) {
		heldDuringChild = yielded == 1 && retaken == 0
		return ch, nil
	}}

	require.NoError(t, cmd.Run())
	require.True(t, cmd.rawModeFailed, "precondition: raw mode was asked for and refused")
	assert.True(t, heldDuringChild,
		"an attach that fell back to cooked mode keeps OPOST, so its child must not inherit "+
			"the tab-delay field either (#796)")
	assert.Equal(t, 1, retaken, "and it must come back before RestoreTerminal re-reads it")
}
