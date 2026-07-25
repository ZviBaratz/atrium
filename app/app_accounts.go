package app

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"

	tea "github.com/charmbracelet/bubbletea"
)

// handleAccountsState routes a key to the accounts overlay, persists on change, and
// reclaims the menu row when the panel closes. A running session's injected env is
// still never re-resolved — CLAUDE_CONFIG_DIR can only be set at session birth — but
// its account LABELS are: an edit here re-derives the badge and cluster of every
// open session from that dir, so a rename lands immediately instead of splitting the
// account across two group headers until the next launch (#470).
func (m *home) handleAccountsState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	closed, dirty := m.accountsOverlay.HandleKeyPress(msg)
	if dirty {
		if err := config.SaveConfig(m.appConfig); err != nil {
			log.WarningLog.Printf("failed to persist accounts: %v", err)
		}
		// A stashed create-form draft cached its account list at build time; drop it so
		// the next open rebuilds from live config and can't pin a just-deleted account.
		m.stashedDraft = nil
		// The regrouping is held over rather than announced now: the panel still covers
		// the list it describes, and a toast behind a modal would expire unseen.
		if notice := m.resyncAccountStamps(); notice != "" {
			m.pendingAccountNotice = notice
		}
	}
	if closed {
		m.accountsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout()
		return m, tea.Batch(m.flushAccountNotice(), tea.WindowSize())
	}
	return m, nil
}

// flushAccountNotice shows the notice an accounts edit held over, now that the panel
// no longer covers the list it explains. nil when there is nothing to say.
func (m *home) flushAccountNotice() tea.Cmd {
	if m.pendingAccountNotice == "" {
		return nil
	}
	text := m.pendingAccountNotice
	m.pendingAccountNotice = ""
	return m.handleInfoNotice(text)
}
