package app

// Startup update check, per config.auto_update: notify shows a hint when a
// newer release exists; auto additionally downloads, verifies, and stages the
// new binary (applied on the next launch — the running TUI, daemon, and
// sessions are never disturbed). Every failure is log-only: the TUI never
// blocks on the network and never surfaces updater errors.

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// updateCheckTimeout bounds the metadata query. The check already runs in
	// a background tea.Cmd, but its context is otherwise the app's lifetime:
	// a blackholed connection (captive portal, dropped packets) would silently
	// pin the goroutine — and with it the hint, the install, and the cache
	// write — for the whole session.
	updateCheckTimeout = 15 * time.Second
	// updateInstallTimeout bounds the auto-mode download+swap. Generous:
	// release archives are several megabytes on possibly slow links.
	updateInstallTimeout = 5 * time.Minute
)

// checkForUpdate / applyUpdate / releaseResolved are package vars so tests can
// fake the network and the binary swap (same pattern as app.copyToClipboard).
var (
	checkForUpdate  = update.CheckCached
	applyUpdate     = func(ctx context.Context, r *update.Release) error { return r.Apply(ctx) }
	releaseResolved = (*update.Release).Resolved
)

// updateCheckDoneMsg reports a startup check that found a newer release.
// installed means auto mode already swapped the binary on disk, so the notice
// asks for a restart instead of pointing at `atrium update`. Up-to-date and
// failed checks never produce this message.
type updateCheckDoneMsg struct {
	version   string
	installed bool
}

// updateFoundMsg reports a network-resolved newer release in auto mode. Update
// reacts by staging the download as its own command (installUpdateCmd) so the
// "updating" notice renders while the transfer runs, rather than the whole
// download hiding inside one silent command.
type updateFoundMsg struct {
	release *update.Release
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
	appCtx, current := m.ctx, m.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(appCtx, updateCheckTimeout)
		defer cancel()
		// Auto mode asks for a resolved release (the handles Apply needs), so
		// a pending install re-queries the network instead of waiting out the
		// cache TTL; notify mode is served from the cache while it is fresh.
		rel, err := checkForUpdate(ctx, current, mode == config.AutoUpdateAuto)
		if err != nil {
			log.WarningLog.Printf("update check failed: %v", err)
			return nil
		}
		if rel == nil {
			return nil
		}
		if mode == config.AutoUpdateAuto && releaseResolved(rel) {
			return updateFoundMsg{release: rel}
		}
		// Notify mode — or auto mode with a cache-served (unresolved) release,
		// which can hint but not install: the resolving re-query failed or is
		// inside the failure backoff, so the install retries on a later launch.
		return updateCheckDoneMsg{version: rel.Version}
	}
}

// installUpdateCmd downloads, verifies, and stages the resolved release in the
// background. A failure (e.g. an unwritable binary) degrades to the notify
// hint: updater problems are log-only in the TUI.
func (m *home) installUpdateCmd(rel *update.Release) tea.Cmd {
	appCtx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(appCtx, updateInstallTimeout)
		defer cancel()
		if err := applyUpdate(ctx, rel); err != nil {
			log.WarningLog.Printf("auto-update to v%s failed: %v", rel.Version, err)
			return updateCheckDoneMsg{version: rel.Version}
		}
		return updateCheckDoneMsg{version: rel.Version, installed: true}
	}
}

// updateBadgeText is the persistent Sessions-panel badge for an update state:
// the available version in notify mode (or while an auto-install runs), and a
// restart hint once the binary on disk has been swapped. It is deliberately
// short — the panel degrades it word-by-word when the list is narrow.
func updateBadgeText(version string, installed bool) string {
	g := theme.Current().Glyphs
	if installed {
		return g.Ahead + " restart"
	}
	return g.Ahead + " v" + version
}

// handleUpdateNotice shows an update notice on the hint bar, like
// handleInfoNotice — but where ordinary notices acknowledge a user action and
// may drop when the bar can't render (a modal overlay owns the screen), the
// startup check delivers each message exactly once, so an undeliverable notice
// is buffered and re-delivered by the preview tick when the bar returns. With
// the hint bar disabled in config it stays buffered indefinitely — acceptable
// because the toast is only the attention-getter: the durable signal is the
// Sessions-panel badge (updateBadgeText), which renders regardless of
// overlays and hint_bar.
func (m *home) handleUpdateNotice(text string) tea.Cmd {
	if cmd := m.handleInfoNotice(text); cmd != nil {
		m.pendingUpdateNotice = ""
		return cmd
	}
	m.pendingUpdateNotice = text
	return nil
}

// flushPendingUpdateNotice re-attempts a buffered update notice; nil when
// there is none or the bar still can't show it.
func (m *home) flushPendingUpdateNotice() tea.Cmd {
	if m.pendingUpdateNotice == "" {
		return nil
	}
	return m.handleUpdateNotice(m.pendingUpdateNotice)
}
