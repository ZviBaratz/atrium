package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextRow renders one row at width 80 under the unicode theme with a context
// reading applied, and returns it ANSI-stripped.
func contextRow(t *testing.T, mode string, u transcript.Usage) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, contextIndicator: mode}
	r.setWidth(80)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "auth-refactor", Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetUsageMeta(u, transcript.Stamp{Path: "x", Size: 1})
	return ansi.Strip(r.Render(inst, 1, false, false))
}

// TestRender_ContextChipModes proves the chip reaches the row in each mode —
// the model/config plumbing can be right while the render call is not, and only
// this level sees that.
func TestRender_ContextChipModes(t *testing.T) {
	u := transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"}

	assert.Contains(t, contextRow(t, contextModePercent, u), "28%")
	assert.Contains(t, contextRow(t, contextModeCount, u), "283k")
	assert.Contains(t, contextRow(t, contextModeBar, u), "▃")

	off := contextRow(t, contextModeOff, u)
	assert.NotContains(t, off, "28%")
	assert.NotContains(t, off, "283k")

	// The zero value is what every directly-constructed renderer carries, so it
	// must mean the documented default rather than a silent fifth mode.
	assert.Contains(t, contextRow(t, "", u), "28%")
}

// TestRender_ContextChipAbsentWithoutAReading is acceptance criterion 2 at the
// row level: a session with nothing to report renders no chip, no zero, and the
// same two lines it always did.
func TestRender_ContextChipAbsentWithoutAReading(t *testing.T) {
	out := contextRow(t, contextModePercent, transcript.Usage{})
	assert.NotContains(t, out, "%")
	assert.NotContains(t, out, "0k")
	require.Len(t, strings.Split(out, "\n"), 2, "no reading must not add or remove a line")
}

// TestRender_ContextChipUnknownModelDegradesOnTheRow carries acceptance
// criterion 8 all the way to the rendered row, with an invented model id. The
// unit test on contextChip covers the same rule; this one covers the path a user
// actually sees, so a renderer that reached past contextChip for its own
// percentage could not pass both.
func TestRender_ContextChipUnknownModelDegradesOnTheRow(t *testing.T) {
	out := contextRow(t, contextModePercent, transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-99"})
	assert.Contains(t, out, "283k", "an unknown model must render a count on the row")
	assert.NotContains(t, out, "%", "…and never a percentage")
}

// fullyBadgedRow renders one row at `width` columns carrying the AC-7 badge set —
// account, AUTO, model, effort, permission and the context chip — under the given
// title, and returns it ANSI-stripped. `loaded` adds the two badges that only some
// sessions carry (muted, and a queue of two), for the worst-case cluster.
func fullyBadgedRow(t *testing.T, width int, title string, loaded bool) string {
	t.Helper()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(width)

	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetClaudeAccount("quantivly", "/home/x/.claude-quantivly", false)
	inst.AutoYes = true
	inst.SetModeMeta("acceptEdits")
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "u", Size: 1})
	if loaded {
		inst.SetModelMeta("claude-opus-4-8", transcript.Stamp{Path: "m", Size: 1})
		inst.SetEffortMeta("xhigh")
		inst.SetMuted(true)
		inst.QueuePrompt("first")
		inst.QueuePrompt("second")
	} else {
		inst.SetModelMeta("claude-opus-5", transcript.Stamp{Path: "m", Size: 1})
		inst.SetEffortMeta("max")
	}
	return ansi.Strip(r.Render(inst, 1, false, false))
}

// TestRender_ContextChipFitsTheEightyColumnRow is acceptance criterion 7, and the
// evidence behind the placement decision: line 1's right cluster already carries
// account, AUTO, model, effort and permission before this chip is added.
//
// It asserts three things a green suite would otherwise miss. Exact width, because
// composeLine pads to the column count and an overflow shows up as a wrap the
// height budget cannot see. That the name survives whole, because "fits" is
// trivially satisfiable by truncating the name to nothing — that is the failure
// the chip could actually cause. And the chip's own presence, so a row that "fit"
// by silently dropping it fails.
//
// t.Log prints the row so `go test -v` shows what the arithmetic describes: the
// column math can be right while the chip lands in the wrong place, and only
// looking at the row catches that.
func TestRender_ContextChipFitsTheEightyColumnRow(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const (
		width = 80
		name  = "context-window-chip"
	)

	plain := fullyBadgedRow(t, width, name, false)
	t.Logf("AC-7 row at %d columns (ANSI stripped):\n%s", width, plain)

	for i, line := range strings.Split(plain, "\n") {
		require.Equalf(t, width, ansi.StringWidth(line), "line %d must be exactly %d cells", i, width)
	}

	line1 := strings.Split(plain, "\n")[0]
	for _, want := range []string{"quantivly", "AUTO", "opus 5", "max", "accept-edits", "28%"} {
		require.Containsf(t, line1, want, "line 1 must carry %q", want)
	}
	require.Containsf(t, line1, name,
		"the name must survive un-truncated: the chip is supposed to cost slack, not the name")

	// The chip rides just inside the agent icon, after the brand-coloured
	// model/effort/permission phrase — asserted by position, because "the chip is
	// somewhere on the row" would pass with it wedged between the model and the
	// effort, which is what the placement argument rejects.
	require.Greater(t, strings.Index(line1, "28%"), strings.Index(line1, "accept-edits"),
		"the context chip must follow the permission chip, not interrupt the brand-coloured phrase")
}

// TestRender_ContextChipCostsSlackNotTheName pins the number the placement
// argument actually rests on: how many cells the name column still has at 80
// columns once the right cluster (48 cells) and the gutter are paid for.
//
// The AC-7 test above shows a 19-cell name surviving, which proves nothing about
// the budget — every name shorter than the budget survives, so that assertion
// holds just as well against a chip four times this size. This one walks the name
// up to the documented ceiling and one past it, so the pair fails the moment the
// cluster grows: below the ceiling the name is whole, above it composeLine
// truncates, and the boundary IS the budget.
func TestRender_ContextChipCostsSlackNotTheName(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const nameBudget = 28 // 79 usable - gutter(1) - space(1) - right cluster(48) - 1

	atBudget := strings.Repeat("n", nameBudget)
	row := fullyBadgedRow(t, 80, atBudget, false)
	require.Contains(t, strings.Split(row, "\n")[0], atBudget,
		"a name at the documented budget must survive whole")
	require.Contains(t, row, "28%", "…with the chip still on the row")

	overBudget := strings.Repeat("n", nameBudget+1)
	require.NotContains(t, strings.Split(fullyBadgedRow(t, 80, overBudget, false), "\n")[0], overBudget,
		"one cell past the budget must truncate — otherwise the budget above is not the real boundary")
}

// TestRender_ContextChipFullyLoadedRow is the case that actually risks eating the
// name, and the second number the placement argument quotes.
//
// The AC-7 row above carries the badges a typical session has. This one adds the
// two a loaded session also carries — muted, plus a queue of two — which is the
// widest the right cluster gets. The claim is that the name column still holds 21
// cells there, and it is asserted the same at-and-over-budget way, because "the
// row fits" is satisfied by truncating the name to nothing and 21 is the number
// that says it wasn't.
func TestRender_ContextChipFullyLoadedRow(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const loadedBudget = 21 // 28 minus muted + queue (3 cells + a gap) and the wider model/effort labels (4)

	atBudget := strings.Repeat("n", loadedBudget)
	row := fullyBadgedRow(t, 80, atBudget, true)
	line1 := strings.Split(row, "\n")[0]
	t.Logf("fully-loaded row at 80 columns (ANSI stripped):\n%s", row)

	for i, line := range strings.Split(row, "\n") {
		require.Equalf(t, 80, ansi.StringWidth(line), "line %d must be exactly 80 cells", i)
	}
	for _, want := range []string{"quantivly", "AUTO", "opus 4.8", "xhigh", "accept-edits", "28%"} {
		require.Containsf(t, line1, want, "the fully-loaded line 1 must still carry %q", want)
	}
	require.Contains(t, line1, atBudget, "a 21-cell name must survive the widest cluster whole")

	overBudget := strings.Repeat("n", loadedBudget+1)
	require.NotContains(t, strings.Split(fullyBadgedRow(t, 80, overBudget, true), "\n")[0], overBudget,
		"one cell past 21 must truncate — otherwise 21 is not the real fully-loaded budget")
}
