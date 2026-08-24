package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// floorFormHeight is the terminal height these fixtures are sized at — the 24-row
// floor the project documents, paired with createOverlayWidth (the overlay's share
// of an 80-col terminal). Both defects #690 reports are defects *at that shape*,
// so every fixture below renders there and nowhere else.
const floorFormHeight = 24

// floorForm builds a create form at the 80×24 floor with two profiles, one claude
// and one not, and hands it to prepare before returning it. It is the shared
// fixture for the collapse guards and the goldens, so the two can never disagree
// about what "a non-claude form" means.
func floorForm(t *testing.T, prepare func(*TextInputOverlay)) *TextInputOverlay {
	t.Helper()
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
	o.SetBranchResults([]string{"main", "develop"}, o.BranchFilterVersion())
	o.SetSize(createOverlayWidth, floorFormHeight)
	if prepare != nil {
		prepare(o)
	}
	return o
}

// composedRows returns the form's composed (pre-fitOverlay) rows with styling
// stripped. Composed, not rendered, for the reason
// TestCreateForm_ComposesWithinInnerWidth states: fitOverlay is a safety net for
// content Atrium does not control, and a count taken after it cannot tell copy
// that fit from copy that was cut to fit.
func composedRows(t *testing.T, o *TextInputOverlay) []string {
	t.Helper()
	content, _, _ := o.compose()
	rows := strings.Split(xansi.Strip(content), "\n")
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	return rows
}

// rowsContaining counts the composed rows carrying marker.
func rowsContaining(t *testing.T, o *TextInputOverlay, marker string) int {
	t.Helper()
	n := 0
	for _, r := range composedRows(t, o) {
		if strings.Contains(r, marker) {
			n++
		}
	}
	return n
}

// TestCollapsedClaudeFields_TwoRowsAtTheFloor is #690's headline, as a number: a
// non-claude create form spent NINE of the twenty content rows an 80×24 terminal
// gives it — three label rows, three copies of one sentence, three rules — saying
// what it cannot configure. It now spends two.
//
// The count is asserted on claudeFieldNA rather than on the form's total height,
// deliberately. Height moves for a dozen unrelated reasons (a profile, an account
// section, fitOverlay shedding a blank); the number of times the refusal is
// repeated is the defect itself, and it is the one thing a later re-baseline of
// the golden below cannot quietly launder away.
func TestCollapsedClaudeFields_TwoRowsAtTheFloor(t *testing.T) {
	o := floorForm(t, selectOnlyNonClaude)

	require.True(t, o.claudeFieldsCollapsed(),
		"the fixture must actually be a non-claude form, or this measures nothing")
	assert.Equal(t, 1, rowsContaining(t, o, claudeFieldNA),
		"the refusal must be stated once, not once per field")
	assert.Equal(t, 1, rowsContaining(t, o, collapsedClaudeLabel),
		"the three labels must share one row")

	// The two rows are adjacent and in that order — a label above the sentence it
	// labels, the shape every other section in this form has.
	rows := composedRows(t, o)
	label := -1
	for i, r := range rows {
		if strings.Contains(r, collapsedClaudeLabel) {
			label = i
		}
	}
	require.GreaterOrEqual(t, label, 0)
	require.Less(t, label+1, len(rows))
	assert.Contains(t, rows[label+1], strings.TrimSpace(claudeFieldNA),
		"the n/a sentence must sit directly under the labels")
}

// TestCollapsedClaudeFields_ClaudeFormKeepsThreeLiveSections is the other half of
// the contract, and the reason the collapse is keyed on Disabled() rather than on
// the fields' presence: a claude profile still gets three separate, live sections.
// Without this, "collapse the n/a rows" and "delete the claude fields" pass the
// same tests.
func TestCollapsedClaudeFields_ClaudeFormKeepsThreeLiveSections(t *testing.T) {
	o := floorForm(t, nil) // the default selection is the claude profile

	require.False(t, o.claudeFieldsCollapsed())
	assert.Zero(t, rowsContaining(t, o, claudeFieldNA),
		"a claude form refuses nothing")
	assert.Zero(t, rowsContaining(t, o, collapsedClaudeLabel),
		"the collapsed line belongs to the non-claude form only")
	for _, label := range []string{modelLabel, effortLabel, modeLabel} {
		assert.Equal(t, 1, rowsContaining(t, o, label),
			"%q must still have its own section", label)
	}
}

// TestCollapsedClaudeFields_CollapseSurvivesTheProfileRoundTrip pins that the
// collapse tracks the live selection rather than the state the form was built in.
// The fields go inert and live again as the variant counts move
// (syncClaudeFieldsEnabled), and a form that collapsed once and stayed collapsed
// would hide three live controls — the worst failure this change could have.
func TestCollapsedClaudeFields_CollapseSurvivesTheProfileRoundTrip(t *testing.T) {
	o := floorForm(t, selectOnlyNonClaude)
	require.Equal(t, 1, rowsContaining(t, o, claudeFieldNA))

	selectClaude(o)
	require.False(t, o.claudeFieldsCollapsed(), "claude is back in the batch")
	assert.Zero(t, rowsContaining(t, o, claudeFieldNA))
	assert.Equal(t, 1, rowsContaining(t, o, modelLabel), "the Model section is live again")
}

// TestCollapsedClaudeFields_LabelNamesEveryCollapsedField holds the collapsed line
// to the labels the three fields RENDER, not to the consts it is built from.
//
// The distinction is the whole value of the test. Asserting that
// collapsedClaudeLabel contains modelLabel cannot fail — both sides are the same
// const, which is exactly why the line is composed from those consts rather than
// written out. What can still drift is a field whose label ROW is not just its
// label: a suffix, a marker, a second word added to ModeField.Render and nowhere
// else. So the fields are rendered and their label rows read back.
func TestCollapsedClaudeFields_LabelNamesEveryCollapsedField(t *testing.T) {
	labelRow := func(render string) string {
		return xansi.Strip(strings.Split(render, "\n")[0])
	}
	rendered := []string{
		labelRow(NewModelField().Render()),
		labelRow(NewEffortField().Render()),
		labelRow(NewModeField().Render()),
	}
	assert.Equal(t, strings.Join(rendered, " · "), collapsedClaudeLabel,
		"the collapsed line must be the three fields' own label rows, in the form's order")
}

// TestCollapsedClaudeFields_WrappedBecauseOneLineDoesNotFit is the negative control
// on the shape of the collapsed block, and the reason #690's literal copy is spread
// over two rows rather than transcribed onto one.
//
// Without it, "at most two rows" would look like a stylistic choice and the block
// could be flattened back to one line — which fits a developer's wide terminal and
// is cut on the 80-col one the whole issue is about.
func TestCollapsedClaudeFields_WrappedBecauseOneLineDoesNotFit(t *testing.T) {
	rows := strings.Split(renderCollapsedClaudeFields(), "\n")
	require.Len(t, rows, 2, "the collapsed block is two rows")
	for _, row := range rows {
		assert.LessOrEqualf(t, lipgloss.Width(xansi.Strip(row)), claudeFieldInnerWidth,
			"each row must fit the 42 cells an 80-col terminal gives the form: %q", row)
	}

	oneLine := collapsedClaudeLabel + claudeFieldNA // #690's literal, unwrapped
	assert.Greater(t, lipgloss.Width(oneLine), claudeFieldInnerWidth,
		"the one-line form must NOT fit, or the wrap is decoration and one row would do")
}

// TestCollapsedClaudeFields_TabRingIsUnchanged holds the focus half of #797's
// acceptance bar. The three stops are skipped because they are disabled
// (stopEnabled), not because they are unrendered, so collapsing the rendering must
// not move the ring — in either profile case.
//
// It walks the ring with real Tab presses rather than reading focus.stops: nothing
// else here proves the collapsed form can still be traversed at all.
func TestCollapsedClaudeFields_TabRingIsUnchanged(t *testing.T) {
	ring := func(o *TextInputOverlay) []focusStop {
		var seen []focusStop
		start := o.currentStop()
		seen = append(seen, start)
		for range 20 {
			o.HandleKeyPress(keyMsg("tab"))
			if o.currentStop() == start {
				return seen
			}
			seen = append(seen, o.currentStop())
		}
		t.Fatal("the tab ring never came back round")
		return nil
	}

	nonClaude := ring(floorForm(t, selectOnlyNonClaude))
	assert.NotContains(t, nonClaude, stopModel, "an inert field takes no focus")
	assert.NotContains(t, nonClaude, stopEffort)
	assert.NotContains(t, nonClaude, stopMode)
	assert.Contains(t, nonClaude, stopEnter, "the form is still submittable from the ring")

	claude := ring(floorForm(t, nil))
	for _, want := range []focusStop{stopModel, stopEffort, stopMode} {
		assert.Contains(t, claude, want, "a claude form still stops on all three")
	}
}

// TestPromptPlaceholder_KeepsTheSkipInstructionAtTheFloor is #690's second defect.
// The placeholder is the only thing on screen that says how to leave the prompt
// field, and at 80 columns the textarea cut it to "Optional — sent to the agent
// once it" — losing exactly that clause, with no ellipsis to say anything had gone.
//
// It asserts on the textarea's placeholder after compose rather than on the
// composed row, and that is the whole point: the textarea pads the row back out to
// its full width, so the row is 42 cells whether the copy fit or was cut, and no
// width assertion — including this package's own composed-line sweep — can tell the
// two apart. That is why the defect shipped.
func TestPromptPlaceholder_KeepsTheSkipInstructionAtTheFloor(t *testing.T) {
	o := floorForm(t, selectOnlyNonClaude)
	_, innerWidth, _ := o.compose()
	require.Equal(t, claudeFieldInnerWidth, innerWidth)

	shown := o.textarea.Placeholder
	assert.Contains(t, shown, "(Enter or Tab to skip)",
		"the instruction the line exists to deliver must survive the floor")
	assert.LessOrEqual(t, lipgloss.Width(shown), innerWidth,
		"and it must survive whole — anything wider is cut by the textarea, silently")
	assert.NotContains(t, shown, "…",
		"at a supported width the ladder must fit, not ellipsize")
}

// TestPromptPlaceholder_WidestRungOnARoomyTerminal is the negative control on the
// ladder: without it every rung could be replaced by the narrowest and every
// assertion above would still pass, having traded a cut line for a needlessly
// terse one at every width.
//
// The width is the one an actual 120-col terminal yields (int(0.6*120) - 6), and
// the full copy fits it exactly, which is why that rung is the length it is.
func TestPromptPlaceholder_WidestRungOnARoomyTerminal(t *testing.T) {
	const roomyInnerWidth = 66
	require.Equal(t, roomyInnerWidth, lipgloss.Width(PromptPlaceholderOptional),
		"the widest rung is sized to the 120-col terminal exactly")

	o := floorForm(t, func(o *TextInputOverlay) { o.SetSize(roomyInnerWidth+6, 40) })
	_, innerWidth, _ := o.compose()
	require.Equal(t, roomyInnerWidth, innerWidth)
	assert.Equal(t, PromptPlaceholderOptional, o.textarea.Placeholder,
		"a terminal that can afford the full sentence must get it")
}

// TestPromptPlaceholder_EllipsizesBelowTheFloor is the backstop, and the clause of
// #690's "every truncated line in the form ellipsizes" that the ladder alone does
// not deliver: below the supported floor even the narrowest rung does not fit, and
// what the user gets must SAY it was cut rather than end mid-word.
func TestPromptPlaceholder_EllipsizesBelowTheFloor(t *testing.T) {
	o := floorForm(t, func(o *TextInputOverlay) { o.SetSize(26, floorFormHeight) })
	_, innerWidth, _ := o.compose()
	require.Less(t, innerWidth, lipgloss.Width(promptPlaceholderOptionalRungs[len(promptPlaceholderOptionalRungs)-1]),
		"the fixture must be narrower than the narrowest rung, or nothing is truncated")

	shown := o.textarea.Placeholder
	assert.True(t, strings.HasSuffix(shown, "…"), "a cut placeholder must say so: %q", shown)
	assert.LessOrEqual(t, lipgloss.Width(shown), innerWidth,
		"the tail must fit inside the budget, not past it")
}

// TestPromptPlaceholders_EveryExportedOneHasALadder is the drift guard on
// promptPlaceholderRungs' lookup: an exported placeholder that is not recognised
// falls back to a one-rung ladder and silently reacquires the truncation this
// change removed. The fork form is the live case (app/app_fork.go), and its copy
// is 56 cells against the floor's 42.
func TestPromptPlaceholders_EveryExportedOneHasALadder(t *testing.T) {
	for _, widest := range []string{PromptPlaceholderOptional, PromptPlaceholderFork} {
		rungs := promptPlaceholderRungs(widest)
		assert.Equalf(t, widest, rungs[0], "%q must be its ladder's widest rung", widest)
		assert.Greaterf(t, len(rungs), 1,
			"%q is %d cells and the floor is %d — it needs a narrow rung",
			widest, lipgloss.Width(widest), claudeFieldInnerWidth)
	}
}

// TestPromptPlaceholder_ForkKeepsItsIdentityAtEveryWidth pins that the accessor
// reports the placeholder's identity rather than the rung the terminal happens to
// afford. app/app_fork.go sets it and app's fork tests read it back to tell a fork
// form from a create form; if that answer moved with the width, an 80-col drive
// would report the wrong form.
func TestPromptPlaceholder_ForkKeepsItsIdentityAtEveryWidth(t *testing.T) {
	o := floorForm(t, func(o *TextInputOverlay) { o.SetPromptPlaceholder(PromptPlaceholderFork) })
	o.compose()

	assert.Equal(t, PromptPlaceholderFork, o.PromptPlaceholder(),
		"the accessor reports the full placeholder, not the fitted rung")
	assert.NotEqual(t, PromptPlaceholderFork, o.textarea.Placeholder,
		"while what the floor actually renders is the narrow rung")
	assert.Contains(t, o.textarea.Placeholder, "Required",
		"whose leading word is the fact the two placeholders exist to distinguish")
}

// TestCreateForm_FloorGoldens is the frame oracle #797 asks for: the whole
// overlay, as an 80-col terminal draws it, in both profile cases.
//
// Both cases, because the change has two ways to be wrong and a single golden sees
// only one of them. The non-claude frame is where the nine rows became two; the
// claude frame is the control that the three sections are still there, live, in
// order — "collapsed" and "deleted" produce identical non-claude frames.
//
// Rendered, not composed: this is the only assertion in the package that sees
// fitOverlay's output, and so the only one that can show what the user is actually
// looking at — including which rows the height budget sheds at the floor.
//
// Regenerate with:
//
//	CS_UPDATE_GOLDEN=1 go test ./ui/overlay/ -run TestCreateForm_FloorGoldens
func TestCreateForm_FloorGoldens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*TextInputOverlay)
	}{
		{"createform-nonclaude", selectOnlyNonClaude},
		{"createform-claude", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := xansi.Strip(floorForm(t, tc.prepare).Render())
			compareOverlayGolden(t, filepath.Join("testdata", tc.name+"-80x24.txt"), got+"\n")
		})
	}
}

// compareOverlayGolden is app's compareGolden, unexported there and small enough
// to have rather than to plumb: write on CS_UPDATE_GOLDEN, else compare and report
// the first differing line. The first divergence is nearly always the whole story,
// and a 24-row dump of two frames is not readable in test output.
func compareOverlayGolden(t *testing.T, path, got string) {
	t.Helper()

	if os.Getenv("CS_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		t.Logf("golden updated: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "missing golden %s — regenerate with CS_UPDATE_GOLDEN=1", path)
	if string(want) == got {
		return
	}

	wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var wline, gline string
		if i < len(wl) {
			wline = wl[i]
		}
		if i < len(gl) {
			gline = gl[i]
		}
		if wline != gline {
			t.Fatalf("%s differs at line %d\nwant: %q\n got: %q\n\nfull frame:\n%s",
				path, i+1, wline, gline, got)
		}
	}
	t.Fatalf("%s differs in length: want %d lines, got %d", path, len(wl), len(gl))
}

// renderedHeight is the form's height as PlaceOverlay sees it — after fitOverlay,
// because that is the number the app centres.
func renderedHeight(o *TextInputOverlay) int {
	return len(strings.Split(xansi.Strip(o.Render()), "\n"))
}

// formHeightAt builds a form of the given terminal height and returns what it
// renders to, with the claude variant selected and then deselected.
func formHeightAt(t *testing.T, height int) (claude, nonClaude int) {
	t.Helper()
	build := func(prepare func(*TextInputOverlay)) int {
		o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
		o.SetBranchResults([]string{"main", "develop"}, o.BranchFilterVersion())
		o.SetSize(createOverlayWidth, height)
		if prepare != nil {
			prepare(o)
		}
		return renderedHeight(o)
	}
	return build(nil), build(selectOnlyNonClaude)
}

// TestCollapsedClaudeFields_HeightHoldsAsTheVariantFlips is the guard for the
// cost the collapse would otherwise impose, which is not a width defect and not
// one Tab can reach.
//
// The collapse is driven by the variant control — a ↑/↓ on the very row the user
// is holding a key on — and the app centres this overlay with PlaceOverlay, which
// re-centres on every height change. So any row the collapse frees and does not
// hand back comes off the form's height and shifts that row out from under the
// cursor, then back on the next press. Un-refitted the shift is the full nine
// rows the three sections cost.
//
// fitRows therefore budgets the collapsed section instead of the three, and
// syncClaudeFieldsEnabled re-fits when the flip happens rather than only at
// SetSize. That converts the freed rows into picker and prompt rows — while there
// is room to convert them into. Past roughly a 46-row terminal both forms sit at
// maxPickerRows with the prompt at its preferred height, and there is nothing left
// to absorb with; the assertion below states that residual rather than pretending
// it is gone, and bounds it by the only thing that can move here.
func TestCollapsedClaudeFields_HeightHoldsAsTheVariantFlips(t *testing.T) {
	const absorbBand = 45 // above this both forms are pinned at their row caps

	for h := floorFormHeight; h <= 60; h++ {
		claude, nonClaude := formHeightAt(t, h)
		delta := claude - nonClaude
		if delta < 0 {
			delta = -delta
		}
		if h <= absorbBand {
			assert.LessOrEqualf(t, delta, 3,
				"at %d rows the freed rows must go to the pickers and the prompt, "+
					"not come off the height (claude=%d non-claude=%d)", h, claude, nonClaude)
			continue
		}
		assert.LessOrEqualf(t, delta, collapsedClaudeRowsSaved(),
			"above the absorb band the collapse may shorten the form, but by no more "+
				"than the rows it frees — anything larger is a second cause (h=%d "+
				"claude=%d non-claude=%d)", h, claude, nonClaude)
	}
}

// TestCollapsedClaudeFields_HeightReturnsOnTheRoundTrip is the same property as a
// round trip: whatever the flip costs in height, flipping back must pay it
// straight back. A refit that only ran in one direction would leave the form a
// different size than it started, which is the shift above made permanent.
func TestCollapsedClaudeFields_HeightReturnsOnTheRoundTrip(t *testing.T) {
	for _, h := range []int{floorFormHeight, 32, 40, 52} {
		o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
		o.SetBranchResults([]string{"main", "develop"}, o.BranchFilterVersion())
		o.SetSize(createOverlayWidth, h)

		before := renderedHeight(o)
		selectOnlyNonClaude(o)
		require.True(t, o.claudeFieldsCollapsed(), "h=%d: the flip must have happened", h)
		selectClaude(o)
		require.False(t, o.claudeFieldsCollapsed(), "h=%d: the flip must have reversed", h)

		assert.Equalf(t, before, renderedHeight(o),
			"at %d rows the form must be the size it started after a full round trip", h)
	}
}
