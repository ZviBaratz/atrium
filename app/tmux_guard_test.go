package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
)

// When tmux is not installed, pressing n must NOT open the create form. Instead
// the friendly sentinel is surfaced (routed to the persistent info modal), so the
// user never fills in a form only to hit the raw exec-not-found error at launch.
func TestOpenCreateForm_BlockedWhenTmuxMissing(t *testing.T) {
	orig := tmuxAvailable
	t.Cleanup(func() { tmuxAvailable = orig })
	tmuxAvailable = func() error { return tmux.ErrNotInstalled }

	h := newCreateFormHome(t)

	h.handleKeyPress(textMsg("n"))

	assert.NotEqual(t, statePrompt, h.state, "a missing tmux must block the create form from opening")
	assert.Nil(t, h.textInputOverlay, "no form overlay should be built when tmux is missing")
}

// A tmux too old for `new-session -e` must block the create form exactly as a missing
// one does — the session would fail either way, just with a poll timeout that names
// nothing instead of an actionable message.
func TestOpenCreateForm_BlockedWhenTmuxTooOld(t *testing.T) {
	orig := tmuxAvailable
	t.Cleanup(func() { tmuxAvailable = orig })
	tmuxAvailable = func() error { return tmux.ErrTooOldFor("3.1") }

	h := newCreateFormHome(t)

	h.handleKeyPress(textMsg("n"))

	assert.NotEqual(t, statePrompt, h.state, "a too-old tmux must block the create form from opening")
	assert.Nil(t, h.textInputOverlay, "no form overlay should be built when tmux is too old")
}

// The too-old error must reach the persistent info modal, not a one-line toast that
// truncates it. handleError decides on WIDTH alone (ui.ErrBox.Fits), so this asserts a
// real measured width: with the default zero width Fits returns true and the test would
// pass whatever the message said. That makes it the guard against someone shortening
// the sentinel into something a toast would swallow.
func TestTooOldErrorRoutesToInfoModal(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateDefault
	h.errBox.SetSize(80, 1)

	h.handleError(tmux.ErrTooOldFor("3.1"))

	assert.Equal(t, stateInfo, h.state,
		"the too-old message must be wide enough to route to the persistent modal, not a truncated toast")
}
