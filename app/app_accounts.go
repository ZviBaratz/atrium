package app

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"

	tea "charm.land/bubbletea/v2"
)

// handleAccountsState routes a key to the accounts overlay, persists on change, and
// reclaims the menu row when the panel closes. A running session's injected env is
// still never re-resolved — CLAUDE_CONFIG_DIR can only be set at session birth — but
// its account LABELS are: an edit here re-derives the badge and cluster of every
// open session from that dir, so a rename lands immediately instead of splitting the
// account across two group headers until the next launch (#470).
func (m *home) handleAccountsState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	closed, dirty := m.accountsOverlay.HandleKeyPress(msg)
	if dirty {
		if err := config.SaveConfig(m.appConfig); err != nil {
			log.WarningLog.Printf("failed to persist accounts: %v", err)
		}
		// A stashed create-form draft cached its account list at build time; drop it so
		// the next open rebuilds from live config and can't pin a just-deleted account.
		m.stashedDraft = nil
		// The regrouping is held over rather than announced now: the panel still covers
		// the list it describes, and a toast behind a modal would expire unseen. A visit
		// can commit several edits, so the passes accumulate — one visit, one notice
		// covering all of them, rather than the last edit erasing its predecessors.
		m.pendingAccountSync.merge(m.resyncAccountStamps())
	}
	if closed {
		m.accountsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout()
		return m, tea.Batch(m.flushAccountNotice(), tea.RequestWindowSize)
	}
	return m, nil
}

// flushAccountNotice announces everything the visit's edits moved, now that the
// panel no longer covers the list it explains. nil when there is nothing to say.
func (m *home) flushAccountNotice() tea.Cmd {
	sync := m.pendingAccountSync
	m.pendingAccountSync = accountStampSync{}
	if !sync.changed() {
		return nil
	}
	return m.handleInfoNotice(accountSyncNotice(sync))
}
