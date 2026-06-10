package app

// Startup update check, per config.auto_update: notify shows a hint when a
// newer release exists; auto additionally downloads, verifies, and stages the
// new binary (applied on the next launch — the running TUI, daemon, and
// sessions are never disturbed). Every failure is log-only: the TUI never
// blocks on the network and never surfaces updater errors.

import (
	"context"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"

	tea "github.com/charmbracelet/bubbletea"
)

// checkForUpdate / applyUpdate are package vars so tests can fake the network
// and the binary swap (same pattern as copyToClipboard).
var (
	checkForUpdate = update.CheckCached
	applyUpdate    = func(ctx context.Context, r *update.Release) error { return r.Apply(ctx) }
)

// updateCheckDoneMsg reports a startup check that found a newer release.
// installed means auto mode already swapped the binary on disk, so the notice
// asks for a restart instead of pointing at `atrium update`. Up-to-date and
// failed checks never produce this message.
type updateCheckDoneMsg struct {
	version   string
	installed bool
}

// hintBinName returns the invoked binary name for user-facing update hints,
// defaulting to "atrium" for homes constructed without one (tests).
func (m *home) hintBinName() string {
	if m.binName == "" {
		return "atrium"
	}
	return m.binName
}

// updateCheckCmd returns the one-shot startup update command, or nil when the
// updater is inert (dev/unstamped build, or auto_update=off).
func (m *home) updateCheckCmd() tea.Cmd {
	mode := m.appConfig.GetAutoUpdateMode()
	if mode == config.AutoUpdateOff || !update.IsUpdatableVersion(m.version) {
		return nil
	}
	ctx, current := m.ctx, m.version
	return func() tea.Msg {
		rel, err := checkForUpdate(ctx, current)
		if err != nil {
			log.WarningLog.Printf("update check failed: %v", err)
			return nil
		}
		if rel == nil {
			return nil
		}
		if mode == config.AutoUpdateAuto {
			if err := applyUpdate(ctx, rel); err != nil {
				// Covers the unwritable-binary case: degrade to the notify hint.
				log.WarningLog.Printf("auto-update to v%s failed: %v", rel.Version, err)
				return updateCheckDoneMsg{version: rel.Version}
			}
			return updateCheckDoneMsg{version: rel.Version, installed: true}
		}
		return updateCheckDoneMsg{version: rel.Version}
	}
}
