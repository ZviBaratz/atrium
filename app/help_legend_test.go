package app

import (
	"reflect"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestLegendCoversRowVocabulary pins the #378 legend-completeness contract: the
// '?' legend renders every status/git/badge glyph that can appear on a row,
// grouped, sourced from the live Glyphs table so it cannot drift. Reflection over
// Glyphs forces a decision for any field added later — a new glyph must land in
// the legend or in the documented exclusion set below, or this test fails.
func TestLegendCoversRowVocabulary(t *testing.T) {
	defer theme.SetGlyphSet(theme.GlyphSetPlain)()

	content := ansi.Strip(helpTypeGeneral{}.toContent())
	for _, header := range []string{"status", "git", "badges"} {
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
}
