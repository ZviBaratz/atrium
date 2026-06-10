// app/hints_test.go
package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpener stands in for the browser opener, mirroring fakeClipboard.
type fakeOpener struct {
	called bool
	target string
}

func withFakeOpener(t *testing.T, retErr error) *fakeOpener {
	t.Helper()
	orig := openInBrowser
	t.Cleanup(func() { openInBrowser = orig })
	fo := &fakeOpener{}
	openInBrowser = func(s string) error {
		fo.called = true
		fo.target = s
		return retErr
	}
	return fo
}

// newHintsHome builds a minimal home with a tabbed window (hint mode renders
// into the preview pane) and the given instances, first one selected.
func newHintsHome(t *testing.T, instances ...*session.Instance) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	for _, inst := range instances {
		l.AddInstance(inst)
	}
	return &home{
		ctx:            context.Background(),
		state:          stateDefault,
		list:           l,
		menu:           ui.NewMenu(),
		errBox:         ui.NewErrBox(),
		appConfig:      config.DefaultConfig(),
		appState:       config.LoadState(),
		tabbedWindow:   ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		welcomeChecked: true,
	}
}

func pressRunes(h *home, s string) tea.Cmd {
	var last tea.Cmd
	for _, r := range s {
		_, last = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return last
}

// f with a live selection but an empty preview explains itself instead of
// silently doing nothing.
func TestHints_NothingToHintExplains(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	fc := withFakeClipboard(t, nil)

	pressRunes(h, "f")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, fc.called)
	require.True(t, h.menu.HasNotice())
}

// Content with no pattern matches: stay in default state with an explanation.
func TestHints_NoMatchesExplains(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()

	_, _ = h.startHints(inst, "thinking about words\nnothing actionable")

	assert.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice())
}

// The core flow: one match -> label "a" -> pressing a copies it and returns
// to default with an acknowledgment. The opener must NOT run for lowercase.
func TestHints_LowercaseCopies(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "PR: https://github.com/x/y/pull/9\n")
	require.Equal(t, stateHints, h.state)
	require.True(t, h.tabbedWindow.InPreviewHintMode())

	pressRunes(h, "a")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
	require.True(t, fc.called)
	assert.Equal(t, "https://github.com/x/y/pull/9", fc.value)
	assert.False(t, fo.called, "lowercase must not open")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "copied")
}

// Uppercase = copy + open for URLs.
func TestHints_UppercaseOpensURL(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "PR: https://github.com/x/y/pull/9\n")
	pressRunes(h, "A")

	require.True(t, fc.called)
	require.True(t, fo.called)
	assert.Equal(t, "https://github.com/x/y/pull/9", fo.target)
}

// Uppercase on a URL the browser can't usefully open (ssh/git/scp-style)
// degrades to plain copy: xdg-open on git@... is useless-to-surprising.
func TestHints_UppercaseNonWebURLJustCopies(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "clone git@github.com:x/y.git now\n")
	pressRunes(h, "A")

	require.True(t, fc.called)
	assert.Equal(t, "git@github.com:x/y.git", fc.value)
	assert.False(t, fo.called, "non-web URL must not reach the opener")
}

// Uppercase on a non-URL degrades to plain copy (v1).
func TestHints_UppercaseNonURLJustCopies(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "edit /tmp/notes.md:12 please\n")
	pressRunes(h, "A")

	require.True(t, fc.called)
	assert.Equal(t, "/tmp/notes.md:12", fc.value)
	assert.False(t, fo.called)
}

// esc cancels: no copy, overlay gone, back to default.
func TestHints_EscCancels(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	require.Equal(t, stateHints, h.state)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
	assert.False(t, fc.called)
}

// A key outside the hint alphabet (or with no matching label) exits without
// acting — any non-hint key is an exit, per the spec.
func TestHints_InvalidKeyExits(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	pressRunes(h, "5")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, fc.called)
}

// Two-character labels: the first key narrows (mode stays up), the second
// acts. 27 distinct matches force expansion past single chars.
func TestHints_TwoCharNarrowing(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("abcdef%02x", i))
	}
	_, _ = h.startHints(inst, strings.Join(lines, "\n"))
	require.Equal(t, stateHints, h.state)

	pressRunes(h, "n")
	assert.Equal(t, stateHints, h.state, "valid prefix keeps the mode up")
	assert.False(t, fc.called)

	pressRunes(h, "a")
	assert.Equal(t, stateDefault, h.state)
	require.True(t, fc.called)
	assert.Equal(t, "abcdef01", fc.value,
		"label na = 26th from the bottom = row 1")
}

// If the pane drops the overlay out from under the state machine (owner
// paused externally, or any future drop vector), the next key must
// self-heal — exit instead of copying from a stale frozen screen.
func TestHints_StaleStateSelfHeals(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	require.Equal(t, stateHints, h.state)
	h.tabbedWindow.ClearPreviewHintOverlay() // simulate an external drop

	pressRunes(h, "a")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, fc.called, "a stale hint screen must not be acted on")
}

// A resize invalidates the frozen geometry: exit hint mode.
func TestHints_ResizeExits(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	require.Equal(t, stateHints, h.state)

	_, _ = h.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
}
