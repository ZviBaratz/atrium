package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

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
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	h := newWheelHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "context-probe", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "u", Size: 1})
	h.list.AddInstance(inst)

	// The default seeded by newHome shows a percentage.
	require.Contains(t, ansi.Strip(h.list.String()), "28%", "the chip must be seeded at construction")

	h.appConfig.ContextIndicator = config.ContextIndicatorCount
	_ = h.applySettingChange("context_indicator")
	require.Contains(t, ansi.Strip(h.list.String()), "283k",
		"applySettingChange must push the new mode to the list — a timingLive row with no case ships silently broken")

	h.appConfig.ContextIndicator = config.ContextIndicatorOff
	_ = h.applySettingChange("context_indicator")
	out := ansi.Strip(h.list.String())
	require.NotContains(t, out, "283k")
	require.NotContains(t, out, "28%")
}
