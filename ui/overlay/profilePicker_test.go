package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The out-of-range guard returns the first profile — and must not itself panic
// when the picker holds no profiles at all (callers only construct pickers for
// non-empty lists, but a safety guard that panics on its own boundary is worse
// than none).
func TestProfilePicker_GetSelectedProfile(t *testing.T) {
	empty := NewProfilePicker(nil)
	assert.NotPanics(t, func() {
		assert.Equal(t, config.Profile{}, empty.GetSelectedProfile())
	})

	profiles := []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	pp := NewProfilePicker(profiles)
	assert.Equal(t, profiles[0], pp.GetSelectedProfile(), "first profile selected by default")

	pp.cursor = 99 // out of range falls back to the first profile
	assert.Equal(t, profiles[0], pp.GetSelectedProfile())
}

// The cursor wraps at both ends so one keypress reaches the opposite end, and a
// single-profile picker neither moves nor panics (the wrapIndex n<=1 guard).
func TestProfilePicker_WrapsAtEnds(t *testing.T) {
	profiles := []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	pp := NewProfilePicker(profiles)
	require.Equal(t, profiles[0], pp.GetSelectedProfile(), "first profile selected by default")

	pp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, profiles[1], pp.GetSelectedProfile(), "← from the first wraps to the last")

	pp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, profiles[0], pp.GetSelectedProfile(), "→ from the last wraps to the first")

	solo := NewProfilePicker([]config.Profile{{Name: "claude", Program: "claude"}})
	assert.NotPanics(t, func() { solo.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft}) })
	assert.Equal(t, "claude", solo.GetSelectedProfile().Name, "a single-option picker stays put")
}
