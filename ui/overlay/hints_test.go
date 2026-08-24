package overlay

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hintLadders is every ladder fitHint is called with, by the name of the var that
// holds it. Listing them as data is what lets the two contract tests below hold for
// all of them at once rather than for whichever one someone remembered.
var hintLadders = map[string][]string{
	"createFormHelp":  createFormHelp,
	"promptFocusHelp": promptFocusHelp,
	// promptFocusHelpLegacy adds no coverage today — it is promptFocusHelp[1:], so
	// every rung in it is already swept above through its parent. It is listed for
	// the day that stops being true: the derivation is what
	// TestPromptFocusHelpLegacy_IsDerivedFromTheOptimisticLadder holds, and if a
	// later change re-authors this ladder as its own literal, this entry is what
	// makes it a guarded one from the first commit rather than the second.
	"promptFocusHelpLegacy": promptFocusHelpLegacy,
	"modelCustomHelp":       modelCustomHelp,
	"variantFocusHelp":      variantFocusHelp,
	"checkpointFooterHints": checkpointFooterHints,
	// The prompt placeholder's two ladders. They are fitted by fitPlaceholder rather
	// than fitHint, which is fitHint plus an ellipsizing tail — the rung selection
	// these two tests are about is the same, so registering them here is what buys
	// them the ordering and floor-budget guards (#690/#797).
	"promptPlaceholderOptionalRungs": promptPlaceholderOptionalRungs,
	"promptPlaceholderForkRungs":     promptPlaceholderForkRungs,
}

// TestFitHint_PicksWidestThatFits pins the helper's contract at its edges, because
// every call site's behaviour is this function plus a prefix.
func TestFitHint_PicksWidestThatFits(t *testing.T) {
	wide, narrow := "0123456789", "01234"

	assert.Equal(t, wide, fitHint(10, "", wide, narrow), "an exact fit takes the wide rung")
	assert.Equal(t, narrow, fitHint(9, "", wide, narrow), "one cell short drops a rung")
	assert.Equal(t, wide, fitHint(0, "", wide, narrow), "unsized renders the widest rung")
	assert.Equal(t, narrow, fitHint(1, "", wide, narrow), "when nothing fits, the narrowest is the least bad")
	assert.Equal(t, narrow, fitHint(12, "abc", wide, narrow), "the prefix comes out of the budget")
	assert.Equal(t, "", fitHint(10, ""), "no rungs is not a panic")
}

// TestHintLadders_OrderedWidestFirst is the invariant fitHint documents but cannot
// enforce: it returns the *first* rung that fits, so a ladder whose rungs are not
// descending silently skips the ones behind a wider sibling. Adding a rung in the
// wrong place is the natural way to get that wrong, and it is invisible at any
// single width.
func TestHintLadders_OrderedWidestFirst(t *testing.T) {
	for name, rungs := range hintLadders {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, rungs)
			for i := 1; i < len(rungs); i++ {
				assert.Lessf(t, lipgloss.Width(rungs[i]), lipgloss.Width(rungs[i-1]),
					"rung %d (%q) must be narrower than rung %d (%q)", i, rungs[i], i-1, rungs[i-1])
			}
		})
	}
}

// TestHintLadders_NarrowestRungFitsTheFloor is the other half: a ladder is only
// worth its complexity if its last rung actually fits the narrowest terminal
// Atrium supports. Prefixes are excluded here — this is the per-site budget check
// the composed-line guards (TestCreateForm_ComposesWithinInnerWidth,
// TestModelField_CustomHintLadder) make exact.
func TestHintLadders_NarrowestRungFitsTheFloor(t *testing.T) {
	for name, rungs := range hintLadders {
		last := rungs[len(rungs)-1]
		assert.LessOrEqualf(t, lipgloss.Width(last), claudeFieldInnerWidth,
			"%s's narrowest rung is %d cells, past the %d an 80-col terminal gives: %q",
			name, lipgloss.Width(last), claudeFieldInnerWidth, last)
	}
}

// TestModelField_CustomHintLadder is #464 itself: the custom-mode hint was one
// static 61-cell line against the 42 cells an 80-col terminal gives, so
// "· checked at launch" — the clause explaining that a model name is validated by
// the launched session rather than rejected as you type — was cut with nothing to
// say it had been.
//
// The last case is the negative control. Every other assertion here would still
// pass if the ladder's rungs were made identical; that one fails, because it is
// what makes the ladder load-bearing rather than decoration.
func TestModelField_CustomHintLadder(t *testing.T) {
	full, short := modelCustomHelp[0], modelCustomHelp[1]
	lineWidth := func(hint string) int { return lipgloss.Width(modelLabel) + lipgloss.Width(hint) }

	for _, tc := range []struct {
		name  string
		width int
		want  string
	}{
		{"80-col terminal", claudeFieldInnerWidth, short},
		{"exactly the full line", lineWidth(full), full},
		{"one cell short of it", lineWidth(full) - 1, short},
		{"unsized", 0, full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := NewModelField()
			mf.SetWidth(tc.width)
			assert.Equal(t, tc.want, mf.customModeHint())
		})
	}

	assert.LessOrEqual(t, lineWidth(short), claudeFieldInnerWidth,
		"the narrow rung must fit the 80-col budget — it is the one that has to")
	assert.Greater(t, lineWidth(full), claudeFieldInnerWidth,
		"the full rung must NOT fit it, or the ladder is dead code and a static string would do")
}

// TestModelField_CustomInputRowFitsWidth covers the arithmetic half of the same
// budget. A bubbles text input renders one column past its Width for the
// end-of-line cursor cell, so the field handed the overlay's full inner width drew
// 43 cells against 42 and fitOverlay stamped an "…" over the last column on every
// render of custom mode — a permanent artifact rather than a lost tail.
func TestModelField_CustomInputRowFitsWidth(t *testing.T) {
	mf := NewModelField()
	mf.SetWidth(claudeFieldInnerWidth)
	mf.Focus()
	mf.HandleKeyPress(textMsg(strings.Repeat("a", 64))) // the field's CharLimit

	lines := strings.Split(mf.Render(), "\n")
	row := lines[len(lines)-1]
	assert.LessOrEqualf(t, lipgloss.Width(row), claudeFieldInnerWidth,
		"the custom-mode input row must fit the inner width, cursor cell included: %q", row)
}
