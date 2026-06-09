package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClipboard records what the KeyCopyBranch handler tried to write, standing in
// for the host clipboard so the tests stay hermetic.
type fakeClipboard struct {
	called bool
	value  string
}

// withFakeClipboard swaps the package clipboard writer for a capturing fake and
// restores the real one when the test ends. retErr is what the fake returns, so the
// error path can be exercised without a real clipboard.
func withFakeClipboard(t *testing.T, retErr error) *fakeClipboard {
	t.Helper()
	orig := copyToClipboard
	t.Cleanup(func() { copyToClipboard = orig })
	fc := &fakeClipboard{}
	copyToClipboard = func(s string) error {
		fc.called = true
		fc.value = s
		return retErr
	}
	return fc
}

// newCopyBranchHome builds a minimal stateDefault home holding the given instances
// with the first one selected.
func newCopyBranchHome(t *testing.T, instances ...*session.Instance) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	for _, inst := range instances {
		l.AddInstance(inst)
	}
	return &home{
		ctx:       context.Background(),
		state:     stateDefault,
		list:      l,
		menu:      ui.NewMenu(),
		errBox:    ui.NewErrBox(),
		appConfig: config.DefaultConfig(),
	}
}

// newBranchInstance makes an unstarted instance with its Branch field set.
func newBranchInstance(t *testing.T, title, branch string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.Branch = branch
	return inst
}

// pressY drives the y keybinding and returns the command it produced.
func pressY(h *home) tea.Cmd {
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	return cmd
}

// Pressing y copies the selected session's branch name to the clipboard and
// acknowledges it: until this toast existed, success and failure were
// indistinguishable from the keyboard.
func TestKeyCopyBranch_CopiesSelectedBranchAndAcknowledges(t *testing.T) {
	h := newCopyBranchHome(t, newBranchInstance(t, "a", "feature/login"))
	fc := withFakeClipboard(t, nil)

	cmd := pressY(h)

	require.True(t, fc.called, "clipboard writer must be invoked")
	assert.Equal(t, "feature/login", fc.value)
	require.True(t, h.menu.HasNotice(), "success must be acknowledged")
	assert.Contains(t, h.menu.String(), "copied")
	assert.NotNil(t, cmd, "the toast schedules its own hide")
}

// A clipboard failure surfaces as a toast rather than dying in the log: the
// user needs to know the branch never reached the clipboard.
func TestKeyCopyBranch_ClipboardErrorSurfaces(t *testing.T) {
	h := newCopyBranchHome(t, newBranchInstance(t, "a", "feature/login"))
	fc := withFakeClipboard(t, errors.New("no clipboard utility"))

	cmd := pressY(h)

	require.True(t, fc.called)
	require.True(t, h.menu.HasNotice(), "the failure must be visible")
	assert.NotContains(t, h.menu.String(), "copied", "a failed copy must not read as success")
	assert.NotNil(t, cmd)
}

// With no session selected there is nothing to copy: the clipboard is left
// untouched and the key stays silent (the empty state is self-evident).
func TestKeyCopyBranch_NoOpWhenNoSelection(t *testing.T) {
	h := newCopyBranchHome(t) // empty list -> GetSelectedInstance() == nil
	fc := withFakeClipboard(t, nil)

	cmd := pressY(h)

	assert.False(t, fc.called, "clipboard must not be written when nothing is selected")
	assert.Nil(t, cmd)
}

// A session whose branch is not yet known (empty Branch) explains itself
// instead of copying an empty string or silently doing nothing.
func TestKeyCopyBranch_ExplainsWhenBranchEmpty(t *testing.T) {
	h := newCopyBranchHome(t, newBranchInstance(t, "a", ""))
	fc := withFakeClipboard(t, nil)

	cmd := pressY(h)

	assert.False(t, fc.called, "clipboard must not be written for an empty branch")
	require.True(t, h.menu.HasNotice(), "the no-op must explain itself")
	assert.NotNil(t, cmd)
}
