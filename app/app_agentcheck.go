package app

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"
	tea "github.com/charmbracelet/bubbletea"
)

type agentCheckDoneMsg struct {
	newAgents []string
}

func (m *home) agentCheckCmd() tea.Cmd {
	return func() tea.Msg {
		detected := config.DetectAgentProfiles()
		var newAgents []string

		for _, d := range detected {
			exists := false
			for _, p := range m.appConfig.GetProfiles() {
				if p.Name == d.Name {
					exists = true
					break
				}
			}
			if !exists {
				newAgents = append(newAgents, d.Name)
			}
		}

		if len(newAgents) > 0 {
			return agentCheckDoneMsg{newAgents: newAgents}
		}
		return nil
	}
}

func (m *home) handleAgentNotice(text string) tea.Cmd {
	if !m.appConfig.GetHintBar() {
		m.pendingAgentNotice = text
		return nil
	}
	if cmd := m.showMenuNotice(text, ui.NoticeInfo); cmd != nil {
		m.pendingAgentNotice = ""
		return cmd
	}
	m.pendingAgentNotice = text
	return nil
}

func (m *home) flushPendingAgentNotice() tea.Cmd {
	if m.pendingAgentNotice == "" {
		return nil
	}
	return m.handleAgentNotice(m.pendingAgentNotice)
}
