package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestAccountPicker_SelectionAndPreselect(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"}, // no matches → inferred default
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "first account selected by default")
	assert.True(t, ap.HasMultiple())

	ap.SelectByName("quantivly")
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "preselect by name")

	ap.Focus()
	ap.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "Up moves to previous")

	var empty AccountPicker
	assert.Equal(t, config.ClaudeAccount{}, empty.GetSelectedAccount(), "zero picker is safe")
}
