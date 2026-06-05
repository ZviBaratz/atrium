package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
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
