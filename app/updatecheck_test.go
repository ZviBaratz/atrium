package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"

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

// newUpdateHome builds a home on a release version with the given mode.
func newUpdateHome(t *testing.T, mode string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.version = "0.6.0"
	h.appConfig.AutoUpdate = mode
	return h
}

// Dev builds have no release asset to update to; the command must be inert.
func TestUpdateCheckCmd_DevBuildIsInert(t *testing.T) {
	h := newCreateFormHome(t) // zero-value version ("")
	assert.Nil(t, h.updateCheckCmd())
	h.version = "dev"
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
}

// Auto mode: the binary is swapped in the background and the notice asks for a
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

	msg := h.updateCheckCmd()()
	done, ok := msg.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.True(t, applied)
	assert.True(t, done.installed)

	h.Update(msg)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "restart")
}

// A failed auto-install (e.g. unwritable binary) degrades to the notify hint
// instead of surfacing an error: updater problems are log-only in the TUI.
func TestUpdateCheckCmd_AutoApplyFailureDegradesToNotify(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { return errors.New("read-only bin dir") },
	)

	msg := h.updateCheckCmd()()
	done, ok := msg.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.False(t, done.installed)

	h.Update(msg)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "atrium update")
	assert.False(t, h.errBox.HasError(), "updater failures are never errors in the TUI")
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
