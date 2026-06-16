package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// The cursor wraps at both ends so one keypress reaches the opposite end.
func TestAccountPicker_WrapsAtEnds(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	require.Equal(t, "personal", ap.GetSelectedAccount().Name, "first account selected by default")

	ap.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "← from the first wraps to the last")

	ap.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "→ from the last wraps to the first")
}

// touched distinguishes an auto-routed preselection (which the form may revise as
// the target project changes) from a deliberate user override (which must stick).
func TestAccountPicker_TouchedTracksInteraction(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.False(t, ap.Touched(), "a fresh picker is untouched")

	ap.SelectByName("quantivly")
	assert.False(t, ap.Touched(), "programmatic preselect does not count as a user touch")

	ap.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.True(t, ap.Touched(), "a navigation keypress marks the picker touched")
}

// Once the user has taken control, auto-routing's preselect must not clobber it.
func TestAccountPicker_PreselectNoopAfterTouch(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	ap.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // user picks quantivly
	require.Equal(t, "quantivly", ap.GetSelectedAccount().Name)

	ap.SelectByName("personal") // a later auto-route attempt
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "explicit choice survives auto-route")
}
