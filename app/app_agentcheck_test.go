package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withDetectAgents temporarily swaps the detectAgents seam and restores it on
// cleanup (mirrors the pattern used in app_welcome_test.go).
func withDetectAgents(t *testing.T, fn func() []config.Profile) {
	t.Helper()
	orig := detectAgents
	detectAgents = fn
	t.Cleanup(func() { detectAgents = orig })
}

// TestAgentCheckCmd_AllExistReturnsNil confirms that when every detected agent
// is already present in the config profiles, agentCheckCmd produces nil (no
// notice to show).
func TestAgentCheckCmd_AllExistReturnsNil(t *testing.T) {
	h := newCreateFormHome(t)
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	withDetectAgents(t, func() []config.Profile {
		return []config.Profile{
			{Name: "claude", Program: "claude"},
			{Name: "codex", Program: "codex"},
		}
	})

	cmd := h.agentCheckCmd()
	require.NotNil(t, cmd, "agentCheckCmd must return a command (runs background probe)")
	msg := cmd()
	assert.Nil(t, msg, "all agents already known → no message")
}

// TestAgentCheckCmd_NewAgentFound confirms that when a newly detected agent is
// not yet in the config profiles, agentCheckCmd returns an agentCheckDoneMsg
// carrying the new agent's name.
func TestAgentCheckCmd_NewAgentFound(t *testing.T) {
	h := newCreateFormHome(t)
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
	}
	withDetectAgents(t, func() []config.Profile {
		return []config.Profile{
			{Name: "claude", Program: "claude"},
			{Name: "opencode", Program: "opencode"}, // newly installed
		}
	})

	cmd := h.agentCheckCmd()
	require.NotNil(t, cmd)
	msg := cmd()
	got, ok := msg.(agentCheckDoneMsg)
	require.True(t, ok, "expected agentCheckDoneMsg, got %T", msg)
	assert.Equal(t, []string{"opencode"}, got.newAgents)
}

// TestAgentCheckDoneMsg_SingleAgent checks that a single new agent produces
// the singular notice text ("Run `atrium profiles detect` to add it.").
func TestAgentCheckDoneMsg_SingleAgent(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	h.Update(agentCheckDoneMsg{newAgents: []string{"opencode"}})

	require.True(t, h.menu.HasNotice(), "a single new agent must produce a notice")
	assert.Contains(t, h.menu.String(), "opencode")
	assert.Contains(t, h.menu.String(), "to add it", "singular form")
}

// TestAgentCheckDoneMsg_MultiAgent checks that multiple new agents produce the
// plural notice text ("Run `atrium profiles detect` to add them.").
func TestAgentCheckDoneMsg_MultiAgent(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	h.Update(agentCheckDoneMsg{newAgents: []string{"opencode", "amp"}})

	require.True(t, h.menu.HasNotice(), "multiple new agents must produce a notice")
	assert.Contains(t, h.menu.String(), "opencode")
	assert.Contains(t, h.menu.String(), "amp")
	assert.Contains(t, h.menu.String(), "to add them", "plural form")
}

// TestAgentNotice_BufferedWhileOverlayOpen mirrors TestUpdateNotice_BufferedWhileOverlayOpen:
// a notice that arrives while an overlay owns the screen is buffered and
// flushed on the next preview tick.
func TestAgentNotice_BufferedWhileOverlayOpen(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = stateHelp // menuVisible() is false: the bar can't render

	h.Update(agentCheckDoneMsg{newAgents: []string{"opencode"}})
	assert.False(t, h.menu.HasNotice(), "no notice while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingAgentNotice, "notice must be buffered for later delivery")

	h.state = stateDefault
	h.Update(previewTickMsg{})
	assert.True(t, h.menu.HasNotice(), "the tick re-delivers the buffered notice")
	assert.Empty(t, h.pendingAgentNotice, "buffer is cleared after delivery")
}

// TestAgentNotice_HintBarOff_StaysSilent confirms that with hint_bar disabled,
// the agent detection notice is silently dropped (not buffered).
func TestAgentNotice_HintBarOff_StaysSilent(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	off := false
	h.appConfig.HintBar = &off

	h.Update(agentCheckDoneMsg{newAgents: []string{"opencode"}})

	assert.False(t, h.menu.HasNotice(), "hint_bar=false suppresses the notice")
	assert.Empty(t, h.pendingAgentNotice, "notice is not buffered when hint_bar is off")
}
