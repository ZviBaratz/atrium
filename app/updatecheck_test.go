package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapUpdateFakes replaces the package-level network/swap hooks for one test.
func swapUpdateFakes(t *testing.T,
	check func(context.Context, string) (*update.Release, error),
	apply func(context.Context, *update.Release) error) {
	t.Helper()
	origCheck, origApply := checkForUpdate, applyUpdate
	checkForUpdate = check
	applyUpdate = apply
	t.Cleanup(func() { checkForUpdate, applyUpdate = origCheck, origApply })
}

// swapResolved fakes the resolved-handle predicate: app tests cannot construct
// a network-resolved Release (its handles are unexported in internal/update).
func swapResolved(t *testing.T, resolved bool) {
	t.Helper()
	orig := releaseResolved
	releaseResolved = func(*update.Release) bool { return resolved }
	t.Cleanup(func() { releaseResolved = orig })
}

// newUpdateHome builds a home on a release version with the given mode. The
// window is sized so the Sessions panel border has room for the full update
// badge (list width = 30% of 120 = 36 columns).
func newUpdateHome(t *testing.T, mode string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.version = "0.6.0"
	h.appConfig.AutoUpdate = mode
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	return h
}

// listBadge returns the ANSI-stripped Sessions panel render, where the
// persistent update badge lives (its top border).
func listBadge(h *home) string {
	return xansi.Strip(h.list.String())
}

// Non-release builds have no release asset to update to; the command must be
// inert — including the bare-SHA stamp of a tagless `git describe --always`.
func TestUpdateCheckCmd_DevBuildIsInert(t *testing.T) {
	h := newCreateFormHome(t) // zero-value version ("")
	assert.Nil(t, h.updateCheckCmd())
	h.version = "dev"
	assert.Nil(t, h.updateCheckCmd())
	h.version = "1cd6ba3"
	assert.Nil(t, h.updateCheckCmd())
}

func TestUpdateCheckCmd_OffModeIsInert(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateOff)
	assert.Nil(t, h.updateCheckCmd())
}

// Notify mode: a newer release produces a hint naming the version and the
// update command; nothing is downloaded.
func TestUpdateCheckCmd_NotifyShowsHint(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)
	applied := false
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { applied = true; return nil },
	)

	cmd := h.updateCheckCmd()
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, updateCheckDoneMsg{}, msg)

	h.Update(msg)
	assert.False(t, applied, "notify mode must never download")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "9.9.9")
	assert.Contains(t, h.menu.String(), "atrium update")
	assert.Contains(t, listBadge(h), "⇡ v9.9.9", "the persistent panel badge must carry the available version")
}

// Auto mode with a network-resolved release: the check command reports the
// find, Update stages the download as its own command (so the "updating"
// notice renders during the transfer), and the final notice asks for a
// restart — the running TUI is never disturbed.
func TestUpdateCheckCmd_AutoInstallsAndAsksRestart(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	applied := false
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { applied = true; return nil },
	)
	swapResolved(t, true)

	msg := h.updateCheckCmd()()
	found, ok := msg.(updateFoundMsg)
	require.True(t, ok, "a resolved release in auto mode stages an install")
	assert.False(t, applied, "the check command itself must not download")

	_, cmd := h.Update(msg)
	require.NotNil(t, cmd, "Update must stage the install command")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "updating to v9.9.9")
	assert.Contains(t, listBadge(h), "⇡ v9.9.9", "the badge appears as soon as the download is staged")

	done := h.installUpdateCmd(found.release)()
	installed, ok := done.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.True(t, applied)
	assert.True(t, installed.installed)

	h.Update(done)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "restart")
	assert.Contains(t, listBadge(h), "⇡ restart", "the badge flips to the restart hint once the binary is swapped")
}

// Auto mode with a cache-served (unresolved) release: hint only — the install
// handle isn't there, and the install runs when the cache next expires.
func TestUpdateCheckCmd_AutoUnresolvedReleaseHintsOnly(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	applied := false
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil // no handles: cache-served
		},
		func(context.Context, *update.Release) error { applied = true; return nil },
	)

	msg := h.updateCheckCmd()()
	require.IsType(t, updateCheckDoneMsg{}, msg, "an unresolved release can only hint")
	assert.False(t, applied)
}

// A failed install (e.g. unwritable binary) degrades to the notify hint
// instead of surfacing an error: updater problems are log-only in the TUI.
func TestInstallUpdateCmd_FailureDegradesToNotify(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { return errors.New("read-only bin dir") },
	)

	msg := h.installUpdateCmd(&update.Release{Version: "9.9.9"})()
	done, ok := msg.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.False(t, done.installed)

	h.Update(msg)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "atrium update")
	assert.False(t, h.errBox.HasError(), "updater failures are never errors in the TUI")
	assert.Contains(t, listBadge(h), "⇡ v9.9.9", "a failed install leaves the available badge, not the restart one")
	assert.NotContains(t, listBadge(h), "restart")
}

// Up to date or check failure: the command resolves to a nil message and the
// UI shows nothing at all.
func TestUpdateCheckCmd_UpToDateAndErrorsAreSilent(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)

	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) { return nil, nil },
		func(context.Context, *update.Release) error { return nil },
	)
	assert.Nil(t, h.updateCheckCmd()(), "up to date yields no message")

	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) { return nil, errors.New("offline") },
		func(context.Context, *update.Release) error { return nil },
	)
	assert.Nil(t, h.updateCheckCmd()(), "a failed check yields no message")
	assert.False(t, h.menu.HasNotice())
}

// The hint quotes the binary name the user actually invoked (e.g. the atr
// alias), not a hardcoded "atrium".
func TestUpdateCheckDoneMsg_HintUsesInvokedBinName(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)
	h.binName = "atr"

	h.Update(updateCheckDoneMsg{version: "9.9.9"})

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "atr update")
}

// The startup check delivers its message exactly once; a notice that arrives
// while a modal overlay owns the screen must be buffered and re-delivered by
// the preview tick, not silently lost.
func TestUpdateNotice_BufferedWhileOverlayOpen(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)
	h.state = stateHelp // menuVisible() is false: the bar can't render

	h.Update(updateCheckDoneMsg{version: "9.9.9"})
	assert.False(t, h.menu.HasNotice(), "no notice while the overlay is up")
	assert.NotEmpty(t, h.pendingUpdateNotice, "the one-shot notice must be buffered")
	assert.Contains(t, listBadge(h), "⇡ v9.9.9",
		"the badge is set even while the overlay owns the screen — it is model state the overlay merely composites over")

	h.state = stateDefault
	h.Update(previewTickMsg{})
	require.True(t, h.menu.HasNotice(), "the tick re-delivers the buffered notice")
	assert.Contains(t, h.menu.String(), "9.9.9")
	assert.Empty(t, h.pendingUpdateNotice)
}

// The toast cannot render with the hint bar disabled, and stays buffered
// forever — the persistent panel badge is what carries the update signal to
// chrome-free setups (issue #108's core case).
func TestUpdateBadge_PersistsWithHintBarOff(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)
	off := false
	h.appConfig.HintBar = &off

	h.Update(updateCheckDoneMsg{version: "9.9.9", installed: true})

	assert.False(t, h.menu.HasNotice(), "no toast without the hint-bar row")
	assert.NotEmpty(t, h.pendingUpdateNotice, "the notice stays buffered in the chrome-free setup")
	assert.Contains(t, listBadge(h), "⇡ restart", "the badge must carry the restart hint regardless of hint_bar")
}
