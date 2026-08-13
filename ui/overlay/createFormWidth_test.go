package overlay

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
)

// createOverlayWidth is the width app_layout.go hands SetSize on the worst
// realistic terminal: 80 cols → int(0.6*80) = 48. SetSize takes the *overlay's*
// width, not the terminal's, which is the trap in the older tests that pass
// SetSize(80, …) — that is a 133-col terminal, three sections wider than the one
// the copy has to survive.
const createOverlayWidth = 48

// titleVerdictBudget is how many cells a title verdict may occupy. The Title row is
// label(5) + gap(2) + the input + " (" + verdict + ")", and renderCreateForm carves the
// verdict's columns out of the input so the message never lands past fitOverlay's edge —
// but the input has a floor of 10 (+1 for the end-of-line cursor cell), so past that
// point the row grows instead. 42 - 5 - 2 - 11 - 3 = 21 is where it stops fitting.
//
// The floor is what makes the overflow silent: a verdict of 22 cells is the first that
// cannot be paid for, and every message app's titleConflict produced was over it (#545).
// Pinned by TestTitleVerdict_FillsTheRow below; the copy is held to it end-to-end by
// app's TestTitleVerdicts_SurviveAn80ColRender.
const titleVerdictBudget = 21

// pinnedProfiles pin every claude override at its widest *nameable* value, so the
// no-op-chip hints render "program pins accept-edits" rather than the short
// unnamed form (see syncClaudeFieldsEnabled).
var pinnedProfiles = []config.Profile{
	{Name: "Claude", Program: "claude --model claude-opus-4-6 --effort xhigh --permission-mode acceptEdits"},
}

// TestCreateForm_ComposesWithinInnerWidth is the width guard for the *whole*
// create form, and it states the invariant the per-field guards only sample:
// fitOverlay is a safety net for content Atrium does not control — a deep project
// path, a profile's command — not the layout mechanism for Atrium's own copy. So
// it measures what the form *composes*, before fitOverlay's truncation pass, and
// every fixture below uses short paths and branch names for exactly that reason:
// a failure here is a copy or arithmetic defect, never unbounded user content.
//
// It exists because TestClaudeChipFields_FitInnerWidth renders standalone fields
// that are enabled, focused and on the chip row, with width 0 — one corner of a
// four-axis state space. Three overflows lived in the rest of it: the model
// field's 61-cell custom-mode hint (#464), the 47-cell claudeFieldNA placeholder
// every field shows while the program is not claude, and the custom-mode input
// row, which bubbles renders one column past its Width for the end-of-line cursor
// cell. All three truncated silently at 80 cols; none of them could fail a test.
//
// It then missed three more (#541) for the complementary reason: the sweep walks the
// form's *focus* stops and its chip/custom modes, but a state only some other component
// can push in was outside it — no fixture called SetVariantError, so the variant label
// line was never measured carrying a batch refusal. Enumerate the senders of a
// variable-length line, not only the states the form can reach on its own.
func TestCreateForm_ComposesWithinInnerWidth(t *testing.T) {
	// A 64-char value is the model input's CharLimit — the widest the field can
	// hold, so the input row is measured full rather than nearly empty.
	longModel := strings.Repeat("a", 64)

	for _, fixture := range []struct {
		name  string
		build func() *TextInputOverlay
		// after runs once the shared setup below has seeded branch results and sized
		// the form. It exists for states that setup would otherwise undo — SetResults
		// clears the errored flag, so a fixture that fails the branch search has to do
		// it last (#557).
		after func(*TextInputOverlay)
	}{
		{"bare claude form", func() *TextInputOverlay {
			return NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
		}, nil},
		{"link paths", func() *TextInputOverlay {
			return NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", []string{"node_modules"})
		}, nil},
		// The Dependencies row goes inert for a non-git target, and its placeholder is
		// the longest string that row ever holds — the case most likely to overflow.
		{"link paths, direct target", func() *TextInputOverlay {
			return NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", []string{"node_modules"})
		}, func(o *TextInputOverlay) { o.SetTargetValidity(true, true, "") }},
		{"profiles", func() *TextInputOverlay {
			return NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
		}, nil},
		{"profiles and accounts", func() *TextInputOverlay {
			return NewSessionCreateOverlay(mixedProfiles, twoAccounts, []string{"/repo/a"}, "", nil)
		}, nil},
		{"pinned overrides", func() *TextInputOverlay {
			return NewSessionCreateOverlay(pinnedProfiles, nil, []string{"/repo/a"}, "", nil)
		}, nil},
		{"non-claude selected", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
			selectOnlyNonClaude(o) // the claude fields go inert → claudeFieldNA
			return o
		}, nil},
		{"project hint", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			o.SetProjectHint("detecting project…") // the widest smart-dispatch note
			return o
		}, nil},
		{"clear armed", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			ctrlR(o) // arms the footer's "⌃R again" spelling
			return o
		}, nil},
		{"direct target", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			o.SetTargetValidity(true, true, "") // a directory that is not a git repo
			return o
		}, nil},
		{"invalid target", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			o.SetTargetValidity(false, false, "") // not a directory at all
			return o
		}, nil},
		{"branch search failed", func() *TextInputOverlay {
			// The last state that pushes copy into this form from outside (#557). The
			// shared setup calls SetBranchResults, which clears the errored flag, so the
			// failure is armed in `after` — a state is only covered if the fixture ends
			// in it.
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			o.SetTargetValidity(true, false, "develop") // an ordinary branch name, not "main"
			return o
		}, func(o *TextInputOverlay) {
			o.SetBranchSearchError(o.BranchFilterVersion())
		}},
		{"title verdict", func() *TextInputOverlay {
			o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
			o.SetTitleError(strings.Repeat("x", titleVerdictBudget))
			return o
		}, nil},
		{"variant error", func() *TextInputOverlay {
			// The batch-refusal state (#541), which no fixture entered until now — which
			// is exactly why this sweep missed three cut messages. The message is a
			// synthetic at the budget rather than a real refusal because overlay cannot
			// import app, where the copy lives; the copy is held to this number by app's
			// TestVariantRefusals_SurviveAn80ColRender, and the number itself by
			// TestVariantPicker_ErrorFillsTheLabelLine.
			o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
			o.SetVariantError(strings.Repeat("x", variantErrorBudget))
			return o
		}, nil},
	} {
		for _, mode := range []struct {
			name  string
			enter func(*TextInputOverlay)
		}{
			{"chips", func(*TextInputOverlay) {}},
			{"custom model", func(o *TextInputOverlay) {
				o.focusStop(stopModel)
				o.HandleKeyPress(textMsg(longModel))
			}},
		} {
			// Only the focused field renders its hint, so the sweep has to walk the
			// stops rather than render one arrangement.
			for _, stop := range []struct {
				name string
				kind focusStop
			}{
				// stopDirectory was missing from this list until #545, which is the
				// other half of why the project row's overflow was invisible here: the
				// focused header composes a different (wider) line than the blurred one.
				{"project", stopDirectory},
				{"title", stopTitle},
				{"variants", stopVariants},
				{"model", stopModel},
				{"effort", stopEffort},
				{"mode", stopMode},
				{"account", stopAccount},
				{"deps", stopDeps},
				{"prompt", stopTextarea},
				{"branch", stopBranch},
			} {
				t.Run(fixture.name+"/"+mode.name+"/"+stop.name, func(t *testing.T) {
					o := fixture.build()
					o.SetBranchResults([]string{"main", "develop"}, o.BranchFilterVersion())
					o.SetSize(createOverlayWidth, 24)
					if fixture.after != nil {
						fixture.after(o)
					}
					mode.enter(o)
					o.focusStop(stop.kind) // a no-op for a stop this form does not have

					content, innerWidth, _ := o.compose()
					assert.Equal(t, claudeFieldInnerWidth, innerWidth,
						"the 80-col terminal must yield the budget the field guards assume")
					assertComposesWithin(t, content, innerWidth)
				})
			}
		}
	}
}

// TestPromptOverlays_ComposeWithinInnerWidth is the same invariant for the plain
// prompt overlays, which share compose and fitOverlay with the create form and so
// share its failure mode. Their one hint fits today; this is what keeps the next
// clause added to it from silently losing its tail — the audit the create form's
// five overflows earned for every sibling of the same shape.
func TestPromptOverlays_ComposeWithinInnerWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    *TextInputOverlay
	}{
		{"quick send", NewQuickSendOverlay("Send to session")},
		{"smart dispatch", NewSmartDispatchOverlay("New session")},
		{"plain input", NewTextInputOverlay("Rename", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.o.SetSize(createOverlayWidth, 24)
			content, innerWidth, _ := tc.o.compose()
			assertComposesWithin(t, content, innerWidth)
		})
	}
}

// A verdict of exactly titleVerdictBudget cells fills the Title row to the inner width
// an 80-col terminal gives the form — no more, no less.
//
// The equality is the point, for the same reason as TestVariantPicker_ErrorFillsTheLabelLine:
// "fits" alone would hold for any budget below the real one, leaving the number free to
// drift low and the copy needlessly cramped. Asserting the row is *exactly* 42 pins the
// budget together with the arithmetic around it — the label, the two-space gap, the input's
// floor of 10, the cursor cell bubbles renders past its Width, and the " (…)" wrapper. Move
// any of those and this fails.
func TestTitleVerdict_FillsTheRow(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.SetSize(createOverlayWidth, 24)
	o.SetTitleError(strings.Repeat("x", titleVerdictBudget))

	assert.Equal(t, claudeFieldInnerWidth, lipgloss.Width(rowContaining(t, o, "Title")),
		"a %d-cell verdict must fill the row exactly", titleVerdictBudget)
}

// A verdict one cell over the budget must NOT fit — the negative control that keeps the
// number honest. Without it, titleVerdictBudget could drift down to any smaller value and
// every assertion above would still pass while the copy got needlessly terse.
func TestTitleVerdict_OneCellOverOverflows(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.SetSize(createOverlayWidth, 24)
	o.SetTitleError(strings.Repeat("x", titleVerdictBudget+1))

	assert.Greater(t, lipgloss.Width(rowContaining(t, o, "Title")), claudeFieldInnerWidth,
		"a verdict past the budget must overflow, or the budget is understated")
}

// rowContaining returns the composed (pre-truncation) row carrying marker.
func rowContaining(t *testing.T, o *TextInputOverlay, marker string) string {
	t.Helper()
	content, _, _ := o.compose()
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("no composed row contains %q:\n%s", marker, content)
	return ""
}

// assertComposesWithin fails on any composed line wider than the overlay's inner
// width — i.e. any line fitOverlay would have to cut. It reports the line itself,
// because the defect is always a specific string rather than a number.
func assertComposesWithin(t *testing.T, content string, innerWidth int) {
	t.Helper()
	for i, line := range strings.Split(content, "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(line), innerWidth,
			"line %d needs fitOverlay to fit and so loses its tail: %q", i, line)
	}
}
