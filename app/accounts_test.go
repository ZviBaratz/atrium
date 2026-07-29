package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountsPanel_OpenAddPersistClose(t *testing.T) {
	resetSettingsTestState(t) // restores config.json on cleanup
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(textMsg("@"))
	require.Equal(t, stateAccounts, h.state)
	require.NotNil(t, h.accountsOverlay)
	assert.False(t, h.menuVisible(), "the modal renders its own hints")

	// n → type a name → tab → config dir → enter commits + persists.
	_, _ = h.handleKeyPress(textMsg("n"))
	for _, r := range "work" {
		_, _ = h.handleKeyPress(textMsg(string(r)))
	}
	_, _ = h.handleKeyPress(keyMsg("tab"))
	for _, r := range "~/.claude-work" {
		_, _ = h.handleKeyPress(textMsg(string(r)))
	}
	_, _ = h.handleKeyPress(keyMsg("enter"))

	require.Len(t, h.appConfig.ClaudeAccounts, 1)
	assert.Equal(t, "work", h.appConfig.ClaudeAccounts[0].Name)
	assert.Len(t, config.LoadConfig().ClaudeAccounts, 1, "the change reached disk immediately")

	_, _ = h.handleKeyPress(keyMsg("esc"))
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.accountsOverlay)
}
