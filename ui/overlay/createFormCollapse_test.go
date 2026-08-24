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
	// The block's text rows, not its total: it pads out to the height the three
	// sections had (see collapsedClaudeSectionLines), and the padding is blank.
	var rows []string
	for _, row := range strings.Split(renderCollapsedClaudeFields(), "\n") {
		if strings.TrimSpace(xansi.Strip(row)) != "" {
			rows = append(rows, row)
		}
	}
	require.Len(t, rows, 2, "the collapsed block says its piece in two rows")
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

// claudeAndNonClaudeForms builds the same form twice at one terminal height: once
// with the claude variant selected, once driven to a non-claude-only batch through
// the variant control the user would use.
func claudeAndNonClaudeForms(t *testing.T, height int) (claude, nonClaude *TextInputOverlay) {
	t.Helper()
	build := func(prepare func(*TextInputOverlay)) *TextInputOverlay {
		o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
		o.SetBranchResults([]string{"main", "develop"}, o.BranchFilterVersion())
		o.SetSize(createOverlayWidth, height)
		if prepare != nil {
			prepare(o)
		}
		return o
	}
	return build(nil), build(selectOnlyNonClaude)
}

// variantRowIndex is the rendered row the variant control sits on — the row the
// user is holding a key on when the collapse fires. Rendered, not composed:
// fitOverlay's shedding is part of what decides where it lands, and the screen
// position is the whole subject here.
func variantRowIndex(t *testing.T, o *TextInputOverlay) int {
	t.Helper()
	for i, r := range strings.Split(xansi.Strip(o.Render()), "\n") {
		if strings.Contains(r, "Variants") {
			return i
		}
	}
	t.Fatalf("no variant row in the rendered form")
	return -1
}

// shedsRows reports whether fitOverlay had to drop anything to fit this form —
// i.e. whether the composed form was taller than the terminal allows. Derived
// rather than expressed as a terminal height, because the height where shedding
// starts is a function of every section constant in this form and would go stale
// the first time one of them was tuned.
func shedsRows(t *testing.T, o *TextInputOverlay) bool {
	t.Helper()
	content, _, _ := o.compose()
	const boxChrome = 4 // border top/bottom + vertical padding
	return len(strings.Split(content, "\n"))+boxChrome > renderedHeight(o)
}

// TestCollapsedClaudeFields_HoldsTheFormStill is the guard for the cost the
// collapse would otherwise impose, and it measures the thing that actually moves.
//
// The collapse flips under a ↑/↓ on the variant control, and the app centres this
// overlay with PlaceOverlay, which re-centres on every height change. So a section
// that got shorter here would walk the form up the screen under the very keypress
// that triggered it, and walk it back on the next press. Holding the section's
// height is what stops that, and the first assertion is the whole mechanism: the
// form is the same size either way, at every terminal height.
//
// Note what the second assertion measures: the row's position, not the form's
// height. Those come apart, and the difference is not academic. An earlier attempt
// at this fix handed the freed rows to the pickers and the prompt so the form's
// HEIGHT barely moved — and made the defect worse, because those sections render
// above the variant row, so the row was pushed down by the reflow while the
// re-centre lifted the form, and the two added. Height moved by one row; the row
// under the cursor moved eight. A guard on height passed the whole time.
//
// Where fitOverlay is shedding, the row can still shift a little: the two forms
// offer it different blank rows to drop, so it drops different ones. The form does
// not move — only the row's place inside it — and the bound is asserted rather
// than described.
func TestCollapsedClaudeFields_HoldsTheFormStill(t *testing.T) {
	// The most a shed can shift the row. Not a tuning knob: raising it would be
	// accepting a bigger jump, which is the defect this test exists for.
	const shedSlack = 3

	for h := floorFormHeight; h <= 60; h++ {
		claude, nonClaude := claudeAndNonClaudeForms(t, h)

		require.Equalf(t, renderedHeight(claude), renderedHeight(nonClaude),
			"at %d rows the collapse must not change the form's height, or PlaceOverlay "+
				"re-centres it under the keypress that caused the collapse", h)

		delta := variantRowIndex(t, nonClaude) - variantRowIndex(t, claude)
		if delta < 0 {
			delta = -delta
		}
		if shedsRows(t, claude) || shedsRows(t, nonClaude) {
			assert.LessOrEqualf(t, delta, shedSlack,
				"at %d rows fitOverlay is shedding, so the variant row may settle "+
					"differently — but not by more than %d rows", h, shedSlack)
			continue
		}
		assert.Zerof(t, delta,
			"at %d rows nothing is shed, so the variant row — the row the user is "+
				"holding a key on when the collapse fires — must not move at all", h)
	}
}

// TestCollapsedClaudeFields_PaddingIsShedAtTheFloor is the other half of the
// bargain the padding strikes. Holding the height costs rows, and the terminal
// where rows are scarce is the one #690 measured the defect on — so the padding
// must not be what a 80×24 form spends its budget on.
//
// It is not, and the mechanism is fitOverlay's shedding order: blank lines go
// before dividers, which go before anything with text on it. This asserts the
// consequence rather than the order — at the floor the form carries no more blank
// rows than the claude form it must stay level with.
func TestCollapsedClaudeFields_PaddingIsShedAtTheFloor(t *testing.T) {
	claude, nonClaude := claudeAndNonClaudeForms(t, floorFormHeight)

	blanks := func(o *TextInputOverlay) int {
		n := 0
		for _, r := range strings.Split(xansi.Strip(o.Render()), "\n") {
			if strings.TrimSpace(r) == "" {
				n++
			}
		}
		return n
	}
	assert.LessOrEqual(t, blanks(nonClaude), blanks(claude),
		"the padding must be shed at the floor, not spent there")
}

// TestCollapsedClaudeFields_BlockOccupiesWhatItClaims ties the rendered block to
// the constant fitRows budgets for it. Without this the constant is only ever
// compared against itself: collapsedClaudeSectionLines feeds both the budget and
// every test computed from it, so understating it moves the budget and the
// expectation together and nothing notices — while the form composes a row taller
// than budgeted and fitOverlay silently sheds one.
func TestCollapsedClaudeFields_BlockOccupiesWhatItClaims(t *testing.T) {
	rows := strings.Split(renderCollapsedClaudeFields(), "\n")

	// -1 for the divider section() adds, which the constant counts.
	assert.Equal(t, collapsedClaudeSectionLines-1, len(rows),
		"the block must occupy exactly the rows fitRows budgets for it")

	said := 0
	for _, r := range rows {
		if strings.TrimSpace(xansi.Strip(r)) != "" {
			said++
		}
	}
	assert.Equal(t, collapsedClaudeContentRows, said,
		"only the label and the n/a sentence may carry text; the rest is padding")
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
