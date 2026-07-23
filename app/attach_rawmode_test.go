package app

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// When raw mode couldn't be set, the attach ran cooked (Ctrl+Q detach disabled), so
// the post-detach handler must surface the persistent info modal explaining it.
func TestAttachFinished_RawModeFailureOpensInfoModal(t *testing.T) {
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox()
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	_, _ = h.Update(attachFinishedMsg{rawModeFailed: true, killTarget: inst})

	assert.Equal(t, stateInfo, h.state, "a raw-mode failure must open the persistent modal")
	require.NotNil(t, h.textOverlay)
	plain := xansi.Strip(h.textOverlay.Render())
	// Assert on no-space tokens so word-wrap can't split the match.
	assert.Contains(t, plain, "Ctrl+Q", "the modal must name the broken detach key")
	assert.Contains(t, plain, "Ctrl-B", "the modal must offer tmux's own detach as the escape")
	assert.Contains(t, plain, "Enter", "cooked mode line-buffers input, so the escape must tell the user to press Enter")

	// Any key dismisses the modal back to the default screen.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
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
	cmd := &attachCommand{attach: func() (chan struct{}, error) { return ch, nil }}

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
	cmd := &attachCommand{attach: func() (chan struct{}, error) { return ch, nil }}

	require.NoError(t, cmd.Run())
	assert.False(t, cmd.rawModeFailed, "no terminal means no raw-mode attempt and no failure flag")
}
