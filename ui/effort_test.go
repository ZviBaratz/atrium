package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// effortRow renders one claude row at a width wide enough that the right cluster is never
// dropped for space, with the effort chip in the given indicator mode.
func effortRow(t *testing.T, program, runtimeEffort, indicator string) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, effortIndicator: indicator}
	r.setWidth(120)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: program})
	require.NoError(t, err)
	if runtimeEffort != "" {
		inst.SetEffortMeta(runtimeEffort)
	}
	return ansi.Strip(r.Render(inst, 1, false, false))
}

// The chip renders each level's label, sourcing them from agent.ClaudeEffortLabel — so
// "medium" shows as "med", the same abbreviation the create form uses.
func TestRender_EffortChipPerLevel(t *testing.T) {
	for level, want := range map[string]string{
		"low":    "low",
		"medium": "med",
		"high":   "high",
		"xhigh":  "xhigh",
		"max":    "max",
	} {
		row := effortRow(t, "claude", level, "on")
		require.Contains(t, row, want, "level %q renders as %q", level, want)
	}
}

// The chip shows the hook-reported level, not the stale launch flag: a session launched at
// max but switched with /effort shows the new level. This is the feature's whole point —
// a flag-only chip would still say "max".
func TestRender_EffortChipUsesRuntimeTruth(t *testing.T) {
	row := effortRow(t, "claude --effort max", "low", "on")
	require.Contains(t, row, "low", "chip must reflect the live level")
	require.NotContains(t, row, "max", "chip must not show the stale launch flag")
}

// Before the first resolved turn there is no runtime value, so the flag fills the gap.
func TestRender_EffortChipFallsBackToFlag(t *testing.T) {
	require.Contains(t, effortRow(t, "claude --effort xhigh", "", "on"), "xhigh")
}

// Nothing known → no chip. A session with no --effort that has never run a tool has no
// effort to show, and inventing one (e.g. guessing the model default) would be a lie.
func TestRender_EffortChipHiddenWhenUnknown(t *testing.T) {
	row := effortRow(t, "claude", "", "on")
	for _, level := range []string{"low", "med", "high", "xhigh", "max"} {
		require.NotContains(t, row, level, "no effort known → no chip")
	}
}

// The config key hides it.
func TestRender_EffortChipOff(t *testing.T) {
	require.NotContains(t, effortRow(t, "claude --effort max", "low", "off"), "low")
}

// A non-claude session has no effort concept: no chip, no crash — even when its program
// string carries a lookalike flag.
func TestRender_EffortChipNonClaude(t *testing.T) {
	require.NotContains(t, effortRow(t, "aider --effort max", "", "on"), "max")
}

// TestEffortLabel_WidthCap is the row-safety backstop. The right cluster is fixed-width and
// never truncates — every chip eats directly from the name's budget — so an unexpected
// value (a future level, a malformed flag reaching PinnedEffort unvalidated) must not be
// able to blow the row open.
func TestEffortLabel_WidthCap(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := effortLabel(long)
	require.LessOrEqual(t, len([]rune(got)), effortChipMaxWidth, "an absurd level is capped")
	require.Equal(t, "max", effortLabel("max"), "a normal level is untouched")
}

// A row whose session carries an effort chip still fits its width: the name column
// truncates to absorb it, the chip does not.
func TestRender_EffortChipRowFitsWidth(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, effortIndicator: "on"}
	const width = 60
	r.setWidth(width)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: strings.Repeat("long-session-name", 5), Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetEffortMeta("xhigh")

	for _, line := range strings.Split(ansi.Strip(r.Render(inst, 1, false, false)), "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), width, "row line must fit the width: %q", line)
	}
}
