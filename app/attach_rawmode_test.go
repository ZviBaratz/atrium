package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// When raw mode couldn't be set, the attach ran cooked (the detach key disabled),
// so the post-detach handler must surface the persistent info modal explaining it.
func TestAttachFinished_RawModeFailureOpensInfoModal(t *testing.T) {
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox()
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	_, _ = h.Update(attachFinishedMsg{rawModeFailed: true, killTarget: inst})

	assert.Equal(t, stateInfo, h.state, "a raw-mode failure must open the persistent modal")
	require.NotNil(t, h.textOverlay)
	plain := xansi.Strip(h.textOverlay.Render())
	// Assert on no-space tokens so word-wrap can't split the match.
	// Read from the registry, not spelled here: the modal names whichever key
	// currently detaches, and a literal would pass while the modal told the user
	// to press a key they had rebound away.
	assert.Contains(t, plain, keys.LabelOf(keys.KeyAttachToggle),
		"the modal must name the broken detach key")
	assert.Contains(t, plain, "Ctrl-B",
		"the modal must offer tmux's own detach as the escape — a literal on purpose, "+
			"since the tmux prefix is not Atrium's to rebind")
	assert.Contains(t, plain, "Enter", "cooked mode line-buffers input, so the escape must tell the user to press Enter")

	// Any key dismisses the modal back to the default screen.
	_, _ = h.handleKeyPress(textMsg("x"))
	assert.Equal(t, stateDefault, h.state, "any key must dismiss the info modal")
}

// A normal detach (raw mode worked) must not pop the modal — it would be noise.
func TestAttachFinished_NoRawModeFailureNoModal(t *testing.T) {
	h, inst := newUnreadHome(t)

	_, _ = h.Update(attachFinishedMsg{killTarget: inst})

	assert.Equal(t, stateDefault, h.state, "a clean detach must not open the modal")
	assert.Nil(t, h.textOverlay)
}

// Run records the failure and STILL proceeds with the attach (cooked mode) rather
// than hard-failing — the core requirement for constrained Docker/SSH ttys.
func TestAttachCommandRun_RawModeFailureStillAttaches(t *testing.T) {
	origIsTerminal, origMakeRaw := isTerminal, makeRaw
	t.Cleanup(func() { isTerminal, makeRaw = origIsTerminal, origMakeRaw })
	isTerminal = func(int) bool { return true }
	makeRaw = func(int) (*term.State, error) { return nil, errors.New("inappropriate ioctl for device") }

	ch := make(chan struct{})
	close(ch) // the attach returns immediately, so Run doesn't block
	// raw: true is what a tmux attach passes, and what this test is about — see
	// TestAttachCommandRun_CookedNeverAttemptsRawMode for the other polarity.
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run(), "attach must proceed in cooked mode, not hard-fail")
	assert.True(t, cmd.rawModeFailed, "the raw-mode failure must be recorded for the handler")
}

// Without a controlling terminal, Run skips the raw-mode attempt entirely, so the
// failure flag stays false (there's no detach key to break).
func TestAttachCommandRun_NotATerminalSkipsRawMode(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	assert.False(t, cmd.rawModeFailed, "no terminal means no raw-mode attempt and no failure flag")
}

// The positive test for a deliberate omission (#375 stage C).
//
// A `sh -c` child of a custom command must run COOKED. Raw mode is not merely
// unnecessary for it — it is wrong: term.MakeRaw also clears OPOST/ONLCR, so every
// newline the command prints arrives as a bare LF and the output staircases down the
// screen. An interactive child (lazygit, an editor) sets its own termios anyway.
//
// Skipping MakeRaw is therefore a decision, and a decision with no test is a comment.
// The second assertion is the one that carries weight downstream: rawModeFailed staying
// false is what stops handleAttachFinished's "Ctrl+Q detach didn't work" modal — advice
// about a key the mode has no equivalent of — from firing after every custom command.
func TestAttachCommandRun_CookedNeverAttemptsRawMode(t *testing.T) {
	origIsTerminal, origMakeRaw := isTerminal, makeRaw
	t.Cleanup(func() { isTerminal, makeRaw = origIsTerminal, origMakeRaw })
	isTerminal = func(int) bool { return true } // a real tty, so only `raw` can decide
	attempted := false
	makeRaw = func(int) (*term.State, error) {
		attempted = true
		return nil, errors.New("must not be called")
	}

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: false, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	assert.False(t, attempted, "a cooked child must not have the terminal put in raw mode")
	assert.False(t, cmd.rawModeFailed,
		"a mode that never attempts raw mode cannot have failed at it — otherwise every "+
			"custom command ends with a modal explaining a detach key it does not have")
}

// A cooked takeover borrows SIGINT for its duration, and gives it back.
//
// The whole reason internal/lifecycle exists. Cooked mode leaves ISIG on and the child
// is in Atrium's process group, so the Ctrl+C that aborts the command is delivered to
// Atrium too — where it cancels the root context that app.Run passes to
// tea.WithContext. Without the borrow, pressing Ctrl+C to stop a three-minute `just ci`
// exits the TUI.
//
// Asserted from inside the attach func: that is the window the child is alive in, and a
// borrow taken after it (or released before it) protects nothing.
func TestAttachCommandRun_CookedBorrowsInterrupt(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return true }

	borrowed, resumed := 0, 0
	restore := stubSuspendInterrupt(t, &borrowed, &resumed)
	defer restore()

	heldDuringChild := false
	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: false, attach: func() (chan struct{}, error) {
		heldDuringChild = borrowed == 1 && resumed == 0
		return ch, nil
	}}

	require.NoError(t, cmd.Run())
	assert.True(t, heldDuringChild, "SIGINT must be borrowed before the child starts")
	assert.Equal(t, 1, resumed, "and given back before Run returns — a borrow that leaked "+
		"would leave the TUI silently unable to shut down on Ctrl+C")
}

// A raw tmux attach does NOT borrow SIGINT: ISIG is off, so Ctrl+C cannot be a signal
// there, and a `kill -INT atrium` from outside must still shut the app down as it always
// has. Stage C changed a shared seam; this is the assertion that it changed nothing here.
func TestAttachCommandRun_RawAttachDoesNotBorrowInterrupt(t *testing.T) {
	origIsTerminal, origMakeRaw, origRestore := isTerminal, makeRaw, restoreTerm
	t.Cleanup(func() { isTerminal, makeRaw, restoreTerm = origIsTerminal, origMakeRaw, origRestore })
	isTerminal = func(int) bool { return true }
	// The raw-mode SUCCESS path, which no test drove before this one — hence the
	// restoreTerm seam. A fake makeRaw has no real *term.State, and the unseamed
	// term.Restore would either nil-deref or (given a fabricated zeroed State) apply
	// that termios to the terminal `go test` was launched from.
	makeRaw = func(int) (*term.State, error) { return nil, nil } //nolint:nilnil // the seamed restore never reads it
	restored := false
	restoreTerm = func(int, *term.State) error { restored = true; return nil }
	defer func() { assert.True(t, restored, "the success path must restore the terminal state") }()

	borrowed, resumed := 0, 0
	restore := stubSuspendInterrupt(t, &borrowed, &resumed)
	defer restore()

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	assert.Zero(t, borrowed, "raw mode makes Ctrl+C a byte; there is no signal to borrow")
}

// The case that looks like it should borrow and must not.
//
// An attach whose MakeRaw failed IS running cooked, with ISIG on, so extending the borrow
// to it reads as closing a gap. It is the opposite: tmux sets SIGINT to SIG_IGN in both
// client and server, so a borrowed Ctrl+C there reaches nobody — and this is precisely the
// attach where Ctrl+Q is swallowed by IXON, input is line-buffered, and the post-detach
// modal admits tmux's own prefix may not register. Ctrl+C quitting Atrium is that user's
// last escape from a session they cannot detach from, and it stays theirs.
func TestAttachCommandRun_FailedRawModeKeepsInterruptQuittable(t *testing.T) {
	origIsTerminal, origMakeRaw := isTerminal, makeRaw
	t.Cleanup(func() { isTerminal, makeRaw = origIsTerminal, origMakeRaw })
	isTerminal = func(int) bool { return true }
	makeRaw = func(int) (*term.State, error) { return nil, errors.New("inappropriate ioctl for device") }

	borrowed, resumed := 0, 0
	restore := stubSuspendInterrupt(t, &borrowed, &resumed)
	defer restore()

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{raw: true, attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	require.True(t, cmd.rawModeFailed, "the fixture must have failed at raw mode")
	assert.Zero(t, borrowed,
		"a raw attach that fell back to cooked must NOT borrow SIGINT: tmux ignores it, so "+
			"the borrow would leave the user with no key that quits and no key that detaches")
	assert.Zero(t, resumed)
}

// stubSuspendInterrupt replaces the lifecycle seam and counts borrows and returns, so
// Run's SIGINT handling is assertable without raising a process-global signal — the
// same reason isTerminal and makeRaw are seamed.
func stubSuspendInterrupt(t *testing.T, borrowed, resumed *int) func() {
	t.Helper()
	prev := suspendInterrupt
	suspendInterrupt = func() func() {
		*borrowed++
		return func() { *resumed++ }
	}
	return func() { suspendInterrupt = prev }
}
