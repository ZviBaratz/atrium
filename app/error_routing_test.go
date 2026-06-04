package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A short, single-line error stays on the transient toast: state is untouched
// and the error box carries the message.
func TestHandleError_ShortErrorUsesToast(t *testing.T) {
	h := newCreateFormHome(t)
	h.errBox.SetSize(80, 1)

	h.handleError(fmt.Errorf("title cannot be empty"))

	assert.Equal(t, stateDefault, h.state)
	assert.True(t, h.errBox.HasError())
	assert.Nil(t, h.textOverlay)
}

// A multi-line error (e.g. git output from a failed push) cannot be conveyed by
// the one-line toast, so from stateDefault it is routed to the persistent info
// modal instead.
func TestHandleError_MultilineErrorOpensInfoModal(t *testing.T) {
	h := newCreateFormHome(t)
	h.errBox.SetSize(80, 1)

	h.handleError(fmt.Errorf("failed to push\nhint: updates were rejected"))

	assert.Equal(t, stateInfo, h.state)
	assert.NotNil(t, h.textOverlay)
	assert.False(t, h.errBox.HasError(), "modal-routed errors must not also claim the toast row")
}

// A single-line error wider than the error box would be truncated into
// uselessness, so it also goes to the modal.
func TestHandleError_OverwideErrorOpensInfoModal(t *testing.T) {
	h := newCreateFormHome(t)
	h.errBox.SetSize(40, 1)

	h.handleError(fmt.Errorf("branch 'zvi/some-very-long-branch-name' is checked out at '/home/user/projects/atrium'"))

	assert.Equal(t, stateInfo, h.state)
	assert.NotNil(t, h.textOverlay)
}

// Outside stateDefault the modal would clobber whatever overlay is open (e.g. a
// form validation error while the create form is up), so every error uses the
// toast there — even one the toast must truncate.
func TestHandleError_OverlayStatesAlwaysUseToast(t *testing.T) {
	h := newCreateFormHome(t)
	h.errBox.SetSize(40, 1)
	h.state = statePrompt

	h.handleError(fmt.Errorf("a\nb\n%s", strings.Repeat("x", 100)))

	assert.Equal(t, statePrompt, h.state, "an overlay state must never be replaced by the error modal")
	assert.True(t, h.errBox.HasError())
}

// With no measured width yet (startup, tests), a single-line error defaults to
// the toast rather than guessing it won't fit.
func TestHandleError_UnsizedBoxDefaultsToToast(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleError(fmt.Errorf("%s", strings.Repeat("x", 200)))

	assert.Equal(t, stateDefault, h.state)
	assert.True(t, h.errBox.HasError())
}
