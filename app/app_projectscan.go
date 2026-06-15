package app

// Background git-repo discovery for the new-session project picker: a bounded
// walk of the configured search roots feeds the picker repos the user has
// never opened in Atrium. Runs at startup (the persisted cache covers the gap
// until it lands) and again on form open when the last result has gone stale.

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/discovery"
	"github.com/ZviBaratz/atrium/log"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// projectScanTimeout bounds a single walk — huge homedirs and network
	// mounts must never pin a goroutine indefinitely. The scan returns
	// partial results on expiry.
	projectScanTimeout = 30 * time.Second
	// projectScanTTL is how old a completed scan may be before opening the
	// create form kicks a refresh (long-running TUIs would otherwise serve
	// launch-time results forever).
	projectScanTTL = 15 * time.Minute
)

// projectScanDoneMsg carries a completed repo scan back to Update, stamped
// with the generation it was started under so a superseded scan's result is
// dropped.
type projectScanDoneMsg struct {
	repos []string
	gen   uint64
}

// startProjectScan kicks the background repo scan and returns its tea.Cmd, or
// nil when the feature is disabled (project_search_depth ≤ 0) or a scan is
// already in flight.
func (m *home) startProjectScan() tea.Cmd {
	depth := m.appConfig.GetProjectSearchDepth()
	if depth <= 0 || m.scanInFlight {
		return nil
	}
	m.scanInFlight = true
	m.scanGen++
	gen := m.scanGen
	opts := discovery.Options{
		Roots:    m.appConfig.GetProjectSearchRoots(),
		MaxDepth: depth,
	}
	// Exclude the data dir so Atrium's own session worktrees never surface as
	// candidates. Derived, never hardcoded: GetConfigDir also covers a legacy
	// ~/.claude-squad install (see config.RuntimeName).
	if dir, err := config.GetConfigDir(); err == nil {
		opts.SkipPaths = []string{dir}
	}
	ctx := m.ctx
	return func() tea.Msg {
		sctx, cancel := context.WithTimeout(ctx, projectScanTimeout)
		defer cancel()
		return projectScanDoneMsg{repos: discovery.Scan(sctx, opts), gen: gen}
	}
}

// handleProjectScanDone stores and persists a completed scan and live-updates
// an open create form without disturbing the user's typed filter or cursor.
func (m *home) handleProjectScanDone(msg projectScanDoneMsg) tea.Cmd {
	if msg.gen != m.scanGen {
		return nil // superseded by a newer scan
	}
	m.scanInFlight = false
	m.lastScanAt = time.Now()
	m.scannedRepos = msg.repos
	// Best-effort cache: a failed write only costs the instant first paint on
	// the next launch.
	if err := m.appState.SetScannedRepos(msg.repos); err != nil {
		log.WarningLog.Printf("failed to persist repo scan: %v", err)
	}
	if m.state != statePrompt || m.textInputOverlay == nil || !m.textInputOverlay.IsCreateForm() {
		return nil
	}
	m.textInputOverlay.UpdateDirCandidates(m.candidateRepoPaths())
	// Cursor preservation makes a selection change rare, but if the refresh did
	// move it (e.g. the previous selection vanished), re-scope exactly like a
	// key-driven path change.
	if newPath := m.textInputOverlay.GetSelectedPath(); newPath != "" && newPath != m.newSessionPath {
		return m.retargetNewSession(newPath)
	}
	return nil
}

// retargetNewSession re-scopes the open create form to a newly selected target
// path: invalidate in-flight branch results for the old repo, then schedule a
// fresh (debounced) branch search and an async target-state re-check. The
// check is async because filesystem browsing changes the selected path almost
// every keystroke, and a synchronous git subprocess per keystroke would
// stutter the UI; ClearTargetValidity resets the indicator to "unknown" up
// front so the previous path's verdict isn't asserted for the new one during
// the debounce window.
func (m *home) retargetNewSession(newPath string) tea.Cmd {
	m.newSessionPath = newPath
	m.textInputOverlay.ClearTargetValidity()
	version := m.textInputOverlay.InvalidateBranchSearch()
	// The old target's branch verdict no longer applies; the validity result
	// re-points the group scope and re-runs the title duplicate check.
	m.resetTitleCheck()
	m.refreshTitleError()
	return tea.Batch(
		m.scheduleBranchSearch(m.textInputOverlay.BranchFilter(), version),
		m.scheduleValidityCheck(newPath),
		m.scheduleTitleCheck(m.textInputOverlay.GetTitle(), newPath),
	)
}
