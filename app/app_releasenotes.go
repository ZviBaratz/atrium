package app

import (
	"context"
	"strings"

	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
)

// fetchReleaseNotes is a package var so tests can fake the network (same pattern
// as checkForUpdate).
var fetchReleaseNotes = update.FetchVersion

// releaseNotesFetchedMsg carries the landed version's notes back to the UI. An
// empty notes body still arrives (so the version is recorded and we stop
// refetching); a fetch failure produces no message at all.
type releaseNotesFetchedMsg struct {
	version string
	notes   string
	url     string
}

// releaseNotesCmd is the startup "what's new" command. It is distinct from
// updateCheckCmd: that one finds the *next* available release to nudge forward,
// while this one fetches the notes for the version the user just *landed on*
// after an auto-update. Returns nil (inert) when there is nothing to show.
func (m *home) releaseNotesCmd() tea.Cmd {
	if !m.appConfig.GetShowReleaseNotesAfterUpdate() || !update.IsUpdatableVersion(m.version) {
		return nil
	}
	last := m.appState.GetLastNotesVersion()
	if last == "" {
		// First run with this feature (or a fresh install): there is no
		// transition to celebrate, and the Welcome modal owns first launch.
		// Seed the current version so the next genuine upgrade is detected.
		if err := m.appState.SetLastNotesVersion(m.version); err != nil {
			log.WarningLog.Printf("failed to seed release-notes version: %v", err)
		}
		return nil
	}
	if !update.IsNewer(m.version, last) {
		return nil
	}
	appCtx, current := m.ctx, m.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(appCtx, updateCheckTimeout)
		defer cancel()
		rel, err := fetchReleaseNotes(ctx, current)
		if err != nil {
			// Best-effort: a failed fetch (e.g. offline after an auto-update)
			// is silent, and LastNotesVersion is left unchanged so the notes
			// retry on the next launch — never surfaced as a UI error.
			log.WarningLog.Printf("release-notes fetch failed: %v", err)
			return nil
		}
		if rel == nil {
			return nil
		}
		return releaseNotesFetchedMsg{version: rel.Version, notes: rel.Notes, url: rel.URL}
	}
}

// showReleaseNotes opens the landed version's notes in the dismissible modal
// (the same TextOverlay the help screen and showInfo use). Unlike showInfo this
// is informational, not an error, so it does not log to ErrorLog.
func (m *home) showReleaseNotes(version, notes, url string) tea.Cmd {
	log.InfoLog.Printf("showing release notes for v%s", version)
	var b strings.Builder
	b.WriteString(helpTitleStyle().Render("What's new in v" + version))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(notes))
	if url != "" {
		b.WriteString("\n\nFull release notes: " + url)
	}
	m.textOverlay = overlay.NewTextOverlay(b.String())
	m.textOverlay.SetHint("press any key to close")
	m.state = stateInfo
	// Size the overlay now rather than waiting for the next resize.
	m.recomputeLayout()
	return nil
}

// flushPendingReleaseNotes opens notes that arrived while another overlay owned
// the screen, once the screen is free. nil when there is nothing buffered or an
// overlay is still up (mirrors flushPendingUpdateNotice).
func (m *home) flushPendingReleaseNotes() tea.Cmd {
	if m.pendingReleaseNotes == nil || m.state != stateDefault {
		return nil
	}
	msg := *m.pendingReleaseNotes
	m.pendingReleaseNotes = nil
	return m.showReleaseNotes(msg.version, msg.notes, msg.url)
}
