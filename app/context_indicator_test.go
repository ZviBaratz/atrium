package app

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// contextHome assembles a real home around one instance carrying a context
// reading, through assembleHome — the constructor that seeds the chip mode from
// config (app_construct.go). Building the model by hand instead would leave that
// seeding line uncovered while every assertion below still passed, because the
// renderer's zero value happens to mean "percent": the default would look
// correct and a configured non-default would be ignored.
func contextHome(t *testing.T, mode string) (*home, *config.Config) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	cfg := config.DefaultConfig()
	cfg.ContextIndicator = mode
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "context-probe", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "u", Size: 1})

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage,
		[]*session.Instance{inst})
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	return h, cfg
}

// TestContextIndicatorSeededFromConfig covers app_construct.go's seeding call,
// which is the one link in the chain a renderer test cannot reach.
//
// The `off` case is the one that matters. The InstanceRenderer's zero value
// means "percent", so a home that never seeds the mode renders a percentage on
// every row — indistinguishable from a correctly seeded default, and wrong for
// exactly the user who turned the chip off in config.json. That user would see
// the chip on every row until they opened the settings panel and cycled the
// setting, which is when applySettingChange finally pushes a value.
func TestContextIndicatorSeededFromConfig(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		h, _ := contextHome(t, config.ContextIndicatorOff)
		out := ansi.Strip(h.list.String())
		require.NotContains(t, out, "28%", "a chip configured off must not be seeded on")
		require.NotContains(t, out, "283k")
	})

	t.Run("count", func(t *testing.T) {
		h, _ := contextHome(t, config.ContextIndicatorCount)
		require.Contains(t, ansi.Strip(h.list.String()), "283k",
			"a configured non-default mode must survive construction")
	})

	t.Run("default", func(t *testing.T) {
		h, _ := contextHome(t, "")
		require.Contains(t, ansi.Strip(h.list.String()), "28%",
			"an unset mode normalizes to percent (config.GetContextIndicator)")
	})
}

// TestContextIndicatorAppliesLive closes the one gap in the settings-row chain
// that has no guard of its own.
//
// A row declared timingLive promises the panel that its change takes effect
// without a restart, but nothing asserts that applySettingChange actually has a
// case for it — the reflection guards check the row exists and the README
// documents it, and both pass against a switch that silently ignores the key.
// (Keybindings had the same hole until TestEveryRegistryActionHasADispatchCase
// closed it; settings rows still do.) So this drives the real handler and then
// looks at the rendered list, rather than at the setter it hopes was called.
func TestContextIndicatorAppliesLive(t *testing.T) {
	h, cfg := contextHome(t, config.ContextIndicatorPercent)
	require.Contains(t, ansi.Strip(h.list.String()), "28%")

	cfg.ContextIndicator = config.ContextIndicatorCount
	_ = h.applySettingChange("context_indicator")
	require.Contains(t, ansi.Strip(h.list.String()), "283k",
		"applySettingChange must push the new mode to the list — a timingLive row with no case ships silently broken")

	cfg.ContextIndicator = config.ContextIndicatorOff
	_ = h.applySettingChange("context_indicator")
	out := ansi.Strip(h.list.String())
	require.NotContains(t, out, "283k")
	require.NotContains(t, out, "28%")
}
