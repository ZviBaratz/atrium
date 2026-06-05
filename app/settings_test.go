package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSettingsTestHome builds the minimal home model the settings paths touch.
// HOME is sandboxed by TestMain, so config persistence stays hermetic.
func newSettingsTestHome() *home {
	return &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		menu:      ui.NewMenu(),
	}
}

// resetSettingsTestState restores the on-disk config and active theme that
// settings tests mutate, so sibling tests in the package see defaults.
func resetSettingsTestState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = config.SaveConfig(config.DefaultConfig())
		theme.Set(theme.DefaultThemeName)
	})
}

func TestSettingsPanel_OpenEditPersistClose(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	// ',' opens the settings panel.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	require.NotNil(t, h.settingsOverlay)

	// Toggling a value persists it to config.json immediately, not on close.
	require.True(t, h.settingsOverlay.SelectRow("auto_attach"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.False(t, h.appConfig.GetAutoAttach())
	assert.False(t, config.LoadConfig().GetAutoAttach(),
		"a change must reach disk immediately so it survives a crash")

	// Esc closes the panel and returns to the list.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.settingsOverlay)
}

func TestSettingsPanel_ThemeChangeAppliesLive(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	require.True(t, h.settingsOverlay.SelectRow("theme"))

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.NotEqual(t, theme.DefaultThemeName, h.appConfig.Theme)
	assert.Equal(t, h.appConfig.Theme, theme.Current().Name,
		"the active theme must follow the config change without a restart")
	assert.Equal(t, h.appConfig.Theme, config.LoadConfig().Theme)
	assert.NotNil(t, cmd, "a repaint command must be issued for the new palette")
}

func TestSettingsPanel_AutoYesTogglePropagatesToHomeFlag(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.SelectRow("auto_yes"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})

	assert.True(t, h.autoYes, "the home flag gates AutoYes on newly created instances")
	assert.True(t, config.LoadConfig().AutoYes,
		"the persisted flag is what the exit-time daemon decision reads")
}

func TestSettingsPanel_HidesHintBarLikeOtherModals(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	assert.False(t, h.menuVisible(), "the panel renders its own key hints; the bar would be redundant")
}
