package app

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestLegendCoversRowVocabulary pins the #378 legend-completeness contract: the
// '?' legend renders every status/git/badge/agent glyph that can appear on a row,
// grouped, sourced from the live theme so it cannot drift. Reflection over Glyphs
// forces a decision for any field added later — a new glyph must land in the legend
// or in the documented exclusion set below, or this test fails.
//
// A theme carries TWO glyph tables, and for #672's whole life this test could see
// only one: agent identity is a map keyed by agent name, not a Glyphs field, so
// adding "✜" for agy tripped nothing here and the mark reached no legend. The second
// half below closes that, walking theme.AgentKeys — see the comment there for why it
// asserts the glyph and its label together, and why it runs on two rungs.
func TestLegendCoversRowVocabulary(t *testing.T) {
	defer theme.SetGlyphSet(theme.GlyphSetPlain)()

	content := ansi.Strip(helpTypeGeneral{}.toContent())
	for _, header := range []string{"status", "git", "badges", "agents"} {
		require.Containsf(t, content, header, "legend must render the %q group", header)
	}

	// Glyphs fields that are legitimately NOT row status/git/badge vocabulary,
	// each with the reason it is excluded from the legend. Adding a Glyphs field
	// without categorizing it (here or in the legend) fails the loop below.
	excluded := map[string]string{
		"SpinnerFPS":    "timing, not a glyph",
		"SpinnerFrames": "represented by the working spinner entry (frame 0)",
		"FoldOpen":      "repo-group fold marker (list chrome, not a row status)",
		"FoldClosed":    "repo-group fold marker (list chrome, not a row status)",
		"SelectionMark": "cursor selection bar (affordance, not a row status)",
		"MarkChecked":   "multi-select mark (affordance, not a row status)",
		"TextCursor":    "text-input caret (not a row status)",
		"Modified":      "settings-panel modified marker (panel chrome, not a row status)",
		// Handoff's "→" happens to occur elsewhere in the legend prose, so the loop below
		// would pass without this entry — by coincidence, not by design. It is categorized
		// here anyway: the glyph is panel chrome, and a copy edit that drops that arrow
		// would otherwise turn this into a surprise failure in an unrelated PR.
		"Handoff": "settings-rail handoff arrow (panel chrome, not a row status)",
		// The accounts panel's availability marks say something about a Claude ACCOUNT,
		// not about a session row, and the @ overlay's own legend line names them
		// ("l limited <mark>"). Both would pass the loop below by coincidence anyway —
		// AcctAvailable's "●" is Ready's glyph and the ascii rung's "*"/"x" are shared
		// too — which is exactly why they are categorized here instead.
		"AcctAvailable": "accounts-panel availability mark (panel chrome, not a row status)",
		"AcctLimited":   "accounts-panel availability mark (panel chrome, not a row status)",
		// A []string, so the loop's string branch cannot measure it — the same
		// reason SpinnerFrames is here. It IS row vocabulary and it IS in the
		// legend: the badges group carries its full rung, asserted explicitly
		// below rather than by the reflection loop.
		"ContextRamp": "a []string ramp, represented in the badges group by its full rung (asserted below)",
	}

	g := theme.Current().Glyphs
	rv := reflect.ValueOf(g)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, skip := excluded[f.Name]; skip {
			continue
		}
		if f.Type.Kind() != reflect.String {
			t.Fatalf("non-string Glyphs field %s is neither in the legend nor documented as excluded", f.Name)
		}
		val := rv.Field(i).String()
		require.Containsf(t, content, val, "row-vocabulary glyph %s (%q) must appear in the legend", f.Name, val)
	}

	// The working spinner's first frame stands in for the SpinnerFrames field.
	require.Contains(t, content, g.SpinnerFrames[0], "the working spinner frame must appear in the legend")

	// The context meter's full rung stands in for the ContextRamp field. Asserted
	// on the rung the legend actually renders, not on any rung: the entry exists
	// so a user who turns on `bar` mode can identify the mark, and a legend
	// showing a different rung than the row draws at 100% would not do that.
	require.Contains(t, content, g.ContextRamp[len(g.ContextRamp)-1],
		"the context meter's full rung must appear in the legend")
	require.Contains(t, content, "context", "the context meter's legend entry must be labelled")

	assertLegendCoversAgents(t)
}

// agentExcludedFromLegend names the agent identity keys allowed not to reach the '?'
// legend, each with the reason. It is empty, and the exact-set assertion below is what
// keeps it that way: the agent column is pinned to the far right of the row so "which
// CLI is this" is answerable at a glance, and an agent the legend cannot decode leaves
// that question answerable only by guessing.
var agentExcludedFromLegend = map[string]string{}

// assertLegendCoversAgents is the half of the completeness contract the reflection
// above structurally cannot state. Agent identity lives in a map in ui/theme keyed by
// agent name (ui/theme/agent.go), not in the Glyphs struct, so a seventh agent adds no
// field for reflect to find — #672 added a sixth and nothing here noticed.
//
// theme.AgentKeys is the enumeration, which is what makes this a guard rather than a
// hand-kept list: package app cannot read the table, but it can be told what is in it.
//
// It asserts the glyph and its label TOGETHER. A bare glyph containment check is what
// the excluded map above has to apologise for twice — Handoff's "→" and the accounts
// pair pass by coincidence, because some other entry happens to paint the same mark —
// and an agent glyph is the worst case for that, since the ascii rung spells these as
// plain letters that occur all over the help text.
//
// Two rungs, because the legend is a projection of the ACTIVE tables and #674 was
// precisely a table that ignored the rung: plain, and the ascii floor where every
// agent glyph is a different character.
func assertLegendCoversAgents(t *testing.T) {
	t.Helper()

	excluded := make([]string, 0, len(agentExcludedFromLegend))
	for k := range agentExcludedFromLegend {
		excluded = append(excluded, k)
	}
	sort.Strings(excluded)
	require.Equal(t, []string{}, excluded,
		"the set of agents allowed to stay out of the ? legend changed; the legend decodes "+
			"every other glyph the row paints, so an addition here needs an argued reason in "+
			"review, not just a map entry")

	for _, set := range []string{theme.GlyphSetPlain, theme.GlyphSetASCII} {
		restore := theme.SetGlyphSet(set)
		content := ansi.Strip(helpTypeGeneral{}.toContent())
		keys := theme.Current().AgentKeys()
		require.NotEmptyf(t, keys, "%s rung: no agent keys, so the loop below would say nothing", set)
		for _, key := range keys {
			if _, skip := agentExcludedFromLegend[key]; skip {
				continue
			}
			glyph, _ := theme.Current().AgentGlyph(key)
			require.Containsf(t, content, glyph+" "+key,
				"%s rung: agent %q paints %q on the row's far-right column, and the ? legend "+
					"must decode it — add it to legendGroups' agents entry (app/help.go) or to "+
					"agentExcludedFromLegend with a reason", set, key, glyph)
		}
		restore()
	}
	// Back to the known default rather than to whatever was current on entry: CI runs
	// -shuffle=on, so "whatever was there" is not a fixed value.
	t.Cleanup(func() { theme.SetGlyphSet(theme.GlyphSetPlain) })
}

// TestLegendLinesFit is the width invariant nothing used to check, and the
// reason a fifth badges entry shipped as a mid-word wrap.
//
// The legend is a columnar table, not prose: the ? overlay will happily reflow
// an over-long line to its box, but it breaks between words, so "AUTO
// auto-accepting" lands as "AUTO auto-" over an unindented "accepting". The
// group is also the one part of the help that GROWS — every glyph added to the
// row vocabulary must appear here (TestLegendCoversRowVocabulary above), so a
// hand-checked "it fits today" is a claim with a short shelf life.
//
// Measured across all three fidelity rungs because the widths differ: nerd's
// icons are the same one cell but its labels ride different glyphs, and the
// ascii rung is the one a terminal without a font falls back to.
func TestLegendLinesFit(t *testing.T) {
	for _, set := range []string{theme.GlyphSetPlain, theme.GlyphSetNerd, theme.GlyphSetASCII} {
		restore := theme.SetGlyphSet(set)
		for i, line := range legendLines() {
			require.LessOrEqualf(t, ansi.StringWidth(line), legendMaxWidth,
				"legend line %d under %q is %d cells, past the ? overlay's %d-cell inner width at 80 columns:\n%s",
				i, set, ansi.StringWidth(line), legendMaxWidth, ansi.Strip(line))
		}
		restore()
	}
}

// TestLegendWrapsUnderABlankTitle pins the shape of the overflow, not just the
// bound: a group too wide for one line continues under a blank title column, so
// its entries stay in one column instead of restarting at the left margin.
//
// Driven on the badges group, which is over the limit today — if a future edit
// brings it back under, this test stops proving anything, so it asserts the
// premise first.
func TestLegendWrapsUnderABlankTitle(t *testing.T) {
	defer theme.SetGlyphSet(theme.GlyphSetPlain)()

	var badges []string
	found := false
	for _, line := range legendLines() {
		plain := ansi.Strip(line)
		switch {
		case strings.HasPrefix(plain, "  badges"):
			found = true
			badges = append(badges, plain)
		case found && strings.HasPrefix(plain, strings.Repeat(" ", legendTitleCol)):
			badges = append(badges, plain)
		case found:
			found = false
		}
	}
	require.Greater(t, len(badges), 1,
		"the badges group must still overflow one line, or this test proves nothing")
	require.Equal(t, strings.Repeat(" ", legendTitleCol), badges[1][:legendTitleCol],
		"a continuation line must be indented to the entry column, not to the margin")
	require.NotContains(t, badges[0], "auto-accepting",
		"the entry that did not fit must move whole")
	require.Contains(t, badges[1], "auto-accepting",
		"…and land intact on the continuation line, not split across the break")
}
