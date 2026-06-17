package app

// Startup agent-heuristic drift check: probes installed agent CLIs and, when one
// has drifted past Atrium's verified version (and the user hasn't already
// acknowledged that version), shows a one-line hint pointing at `atrium doctor`.
// Like the update check, it runs in a background tea.Cmd and never blocks.

import (
	"context"

	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
)

// checkDrift is a package var so tests can fake the probe (same pattern as
// checkForUpdate).
var checkDrift = doctor.CheckInstalled

// driftFoundMsg reports drifted agents the user has not yet acknowledged at
// their current installed version.
type driftFoundMsg struct {
	agents []doctor.Result
}

// driftCheckCmd probes installed agents and emits driftFoundMsg for any drifted,
// unacknowledged agent, or nil when there is nothing to surface. The ack map is
// read on the main thread and captured, so the goroutine never touches appState.
func (m *home) driftCheckCmd() tea.Cmd {
	acked := m.appState.GetAckedDrift()
	appCtx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(appCtx, doctor.ProbeTimeout)
		defer cancel()
		var fresh []doctor.Result
		for _, r := range doctor.Drifted(checkDrift(ctx)) {
			if acked[string(r.Key)] != r.Installed {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		return driftFoundMsg{agents: fresh}
	}
}

// driftBadgeText is the persistent Sessions-panel badge shown as a fallback when
// the startup drift hint could not be delivered. Short so the panel can degrade
// it word-by-word when narrow ("⚠ stale" -> "⚠"), like updateBadgeText.
func driftBadgeText() string {
	return theme.Current().Glyphs.Warn + " stale"
}
