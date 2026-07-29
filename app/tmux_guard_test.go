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
