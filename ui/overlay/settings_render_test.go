package overlay

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComposeRowLineIsExactlyThePaneWidth pins the invariant every later width guard
// rests on: whatever the inputs, the composed line fills the pane exactly. Short of it
// and the badge stops being right-aligned; over it and the lipgloss box soft-wraps the
// line, grows a row, and pushes the pinned hint off the bottom.
func TestComposeRowLineIsExactlyThePaneWidth(t *testing.T) {
	cases := []struct {
		name                string
		width, labelW       int
		label, value, badge string
	}{
		{"roomy", 52, 26, "Session limit", "auto (8)", "live"},
		{"exact fit", 40, 20, "Notify command", "(built-in)", "live"},
		{"badge must go", 34, 26, "Smart dispatch auto-create", "[ ] off", "new sessions"},
		{"value must shrink", 36, 26, "Release notes after update", "a-very-long-value-indeed", "restart"},
		{"no badge at all", 44, 12, "Theme", "‹ atrium ›", ""},
		{"wide value, no badge", 30, 11, "Carry files", ".claude/settings.local.json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := composeRowLine(tc.width, tc.labelW, "▎", "•", tc.label, tc.value, tc.badge)
			assert.Equal(t, tc.width, ansi.StringWidth(p.plain()),
				"composed line must be exactly the pane width: %q", p.plain())
		})
	}
}

// TestComposeRowLineDropsTheBadgeBeforeTruncatingTheValue pins spec §10's priority order.
// Both signals compete for the same slack; dropping the badge costs the user a timing
// note they can still read in the help pane, while truncating first would hide part of
// the value for no reason.
func TestComposeRowLineDropsTheBadgeBeforeTruncatingTheValue(t *testing.T) {
	const (
		width  = 50
		labelW = 20
		value  = "‹off› bell desktop osc"
		badge  = "new sessions"
	)
	// TWO preconditions, because the case only exercises the priority order when both
	// hold: the badge must not fit, AND the value must fit once it is gone. With only the
	// first, the value is truncated too and the second assertion below is impossible.
	avail := width - (rowMarkerCells + labelW + rowLabelGap)
	require.Greater(t, ansi.StringWidth(value)+ansi.StringWidth(badge)+1, avail,
		"the badge must not fit in the %d-cell slack, or nothing is dropped", avail)
	require.LessOrEqual(t, ansi.StringWidth(value), avail,
		"the value must fit once the badge is gone, or this tests truncation instead")

	p := composeRowLine(width, labelW, "▎", " ", "Notifications", value, badge)
	assert.Empty(t, p.badge, "the badge is dropped first")
	assert.Equal(t, value, p.value, "the value survives intact once the badge is gone")
}

// TestComposeRowLineTruncatesTheValueWithATailEllipsis pins the second rung: when even a
// badgeless line cannot hold the value, it loses its tail rather than its head — the
// start of a value is what identifies it.
func TestComposeRowLineTruncatesTheValueWithATailEllipsis(t *testing.T) {
	p := composeRowLine(30, 11, "▎", " ", "Carry files", ".claude/settings.local.json", "live")
	assert.Empty(t, p.badge)
	assert.True(t, strings.HasSuffix(p.value, "…"), "value must be tail-ellipsized: %q", p.value)
	assert.True(t, strings.HasPrefix(p.value, ".claude"), "the head of the value must survive: %q", p.value)
	assert.Equal(t, 30, ansi.StringWidth(p.plain()))
}

// TestComposeRowLineNeverTruncatesTheLabelThatFits pins the rule spec §10 states most
// emphatically: a half-written label makes the row unidentifiable, so the label is the last
// column sacrificed. The value goes first, then the line goes short — but as long as the
// label column fits the pane, it is rendered whole.
//
// The labelW values are deliberately SMALLER than the label in half the cases, which is
// where padRight cannot save the test: passing labelW = len(label) throughout would prove
// only that padRight does not truncate, which it never does.
func TestComposeRowLineNeverTruncatesTheLabelThatFits(t *testing.T) {
	const label = "Smart dispatch auto-create" // 26 cells
	for _, tc := range []struct{ width, labelW int }{
		{52, 26}, {40, 26}, {34, 26}, {31, 26}, // labelW == the label
		{52, 12}, {40, 10}, {34, 8}, // labelW UNDER it: padRight must not clip either
	} {
		p := composeRowLine(tc.width, tc.labelW, "▎", "•", label, "[ ] off", "live")
		assert.Containsf(t, p.head, label,
			"label truncated at width %d / labelW %d: %q", tc.width, tc.labelW, p.head)
		assert.NotContainsf(t, p.head, "…",
			"no ellipsis may appear in the label column (width %d)", tc.width)
	}
}

// TestComposeRowLineNeverExceedsThePaneEvenWhenTheLabelCannotFit pins the floor below which
// the label rule yields. Below rowMarkerCells + label + rowLabelGap there is no line that
// both shows the label whole and fits the pane, and an over-wide line is the worse failure:
// lipgloss soft-wraps it, the box grows a row, and the pinned hint gets clipped off the
// bottom.
//
// Hard-clipping there is parity with the pre-PR-B renderer, which truncated every body line
// to the inner width — not a new regression.
func TestComposeRowLineNeverExceedsThePaneEvenWhenTheLabelCannotFit(t *testing.T) {
	for width := 4; width <= 40; width++ {
		p := composeRowLine(width, 26, "▎", "•", "Smart dispatch auto-create", "[ ] off", "live")
		assert.LessOrEqualf(t, ansi.StringWidth(p.plain()), width,
			"composed line must never exceed the pane, even at width %d: %q", width, p.plain())
	}
}

// TestComposeRowLineRightAlignsTheBadge pins that the badge ends flush with the pane's
// right edge — the whole point of a badge column is that it lines up across rows so a
// user can scan "which of these apply on restart?" without reading each line.
func TestComposeRowLineRightAlignsTheBadge(t *testing.T) {
	for _, label := range []string{"Theme", "Glyph set", "Hint bar"} {
		p := composeRowLine(52, 12, " ", " ", label, "‹ on ›", "live")
		require.NotEmptyf(t, p.badge, "the badge must fit at this width (label %q)", label)
		assert.Truef(t, strings.HasSuffix(p.plain(), "live"),
			"the badge must be flush right for label %q: %q", label, p.plain())
	}
	// Alignment ACROSS rows follows from every line being exactly the pane width, which
	// TestComposeRowLineIsExactlyThePaneWidth pins. Re-asserting it here by comparing three
	// widths that are all `width` by construction would be a tautology.
}

// TestComposeRowLineKeepsTheMarkerColumnsSeparate pins spec §10's explicit requirement,
// and the trap it exists to prevent: a row that is both selected and modified must show
// BOTH marks. Reusing the SelectionMark cell for the modified marker would make "changed
// from default" invisible on exactly the row the user is looking at.
func TestComposeRowLineKeepsTheMarkerColumnsSeparate(t *testing.T) {
	p := composeRowLine(52, 12, "▎", "•", "Theme", "‹ atrium ›", "live")
	assert.True(t, strings.HasPrefix(p.head, "▎• "),
		"selection and modified marks occupy separate adjacent cells: %q", p.head)

	// And the columns hold their positions when only one mark is present, so labels stay
	// aligned down the pane.
	//
	// The offset is measured in CELLS, not bytes: strings.Index returns 5 for the glyph
	// cases, since "▎" and "•" are three bytes each, and comparing that to rowMarkerCells
	// (3) would fail two of the four cases for the wrong reason.
	for _, marks := range [][2]string{{"▎", " "}, {" ", "•"}, {" ", " "}, {"▎", "•"}} {
		p := composeRowLine(52, 12, marks[0], marks[1], "Theme", "‹ atrium ›", "live")
		at := strings.Index(p.head, "Theme")
		require.GreaterOrEqualf(t, at, 0, "the label must be present: %q", p.head)
		assert.Equalf(t, rowMarkerCells, ansi.StringWidth(p.head[:at]),
			"the label must start at cell %d whatever the marks %v: %q", rowMarkerCells, marks, p.head)
	}
}

// TestEnumValueCandidatesLeadWithTheAlternatives pins the fix for D8: `‹ desktop ›` alone
// never revealed that three other modes existed, so discovering them meant cycling — and
// every ←/→ press persists to disk and live-applies, so discovering four options wrote
// four of them. The rich rendering shows all options with the current one bracketed; the
// compact one is the fallback for a pane that cannot hold them.
func TestEnumValueCandidatesLeadWithTheAlternatives(t *testing.T) {
	got := enumValueCandidates("off", []string{"off", "bell", "desktop", "osc"})
	require.Len(t, got, 2, "a rich rendering and a compact fallback")
	assert.Equal(t, "‹off› bell desktop osc", got[0])
	assert.Equal(t, "‹ off ›", got[1])
	assert.Greater(t, ansi.StringWidth(got[0]), ansi.StringWidth(got[1]),
		"the candidates must be ordered widest first, or the caller picks wrong")
}

// TestEnumValueCandidatesForASingleOptionHasNoAlternatives pins that a one-option enum —
// default_program with a single profile — does not render a bracketed value beside an
// empty alternatives list.
func TestEnumValueCandidatesForASingleOptionHasNoAlternatives(t *testing.T) {
	got := enumValueCandidates("bash", []string{"bash"})
	assert.Equal(t, []string{"‹ bash ›"}, got)
}

// TestEveryEnumRowsRichValueIsOfferedFirst pins the ladder against the real schema rather
// than a synthetic case: for every enum row, the rich candidate must contain every option,
// so no option can go missing from the inline alternatives.
func TestEveryEnumRowsRichValueIsOfferedFirst(t *testing.T) {
	cfg := config.DefaultConfig()
	enums := 0
	for _, r := range newSettingRows(cfg) {
		if r.kind != kindEnum {
			continue
		}
		opts := r.options(cfg)
		if len(opts) < 2 {
			continue
		}
		enums++
		rich := enumValueCandidates(r.get(cfg), opts)[0]
		for _, o := range opts {
			assert.Containsf(t, rich, o, "row %q's inline alternatives omit option %q", r.key, o)
		}
	}
	require.Positive(t, enums, "the schema must declare at least one multi-option enum")
}

// dynamicValueRows are the enum rows whose option strings are not a fixed vocabulary, so
// their rendered width is not something the panel's geometry can be budgeted against. The
// same three rows are exempt from the gloss guard for the same underlying reason
// (glossExemptRows) — their vocabulary grows outside this package.
var dynamicValueRows = map[string]string{
	"theme":           "theme names grow with the registry; tokyo-night alone is 15 cells",
	"splash":          "variant names grow with every splash added",
	"default_program": "options are the user's own profile names",
}

// TestRowMinValueCellsHoldsEveryFixedVocabularyValue ties rowMinValueCells to the real
// schema. It is the constant minRowsPaneWidth — and so the single-pane threshold — is derived
// from, so a value too small would keep offering two panes at a width where the value column
// cannot show a row's compact rendering.
//
// Only bounded values are asserted: int and text values are arbitrary-length by nature (a
// tmux config path can be anything) and are exactly what the truncation ladder exists for,
// and the three dynamic-vocabulary enums above can grow without this package changing. A
// dynamic row therefore truncates by a cell or two at the very narrowest two-pane width, and
// the help pane shows it in full — which is the designed degradation, not a defect.
func TestRowMinValueCellsHoldsEveryFixedVocabularyValue(t *testing.T) {
	cfg := config.DefaultConfig()
	widest, key, checked := 0, "", 0
	for _, r := range newSettingRows(cfg) {
		if _, dynamic := dynamicValueRows[r.key]; dynamic {
			continue
		}
		var v string
		switch r.kind {
		case kindEnum:
			// The compact form is always LAST, and a single-option enum yields only that
			// one — indexing [1] panics there.
			cands := enumValueCandidates(r.get(cfg), r.options(cfg))
			v = cands[len(cands)-1]
		case kindBool:
			v = "[ ] off"
		default:
			continue
		}
		checked++
		if n := ansi.StringWidth(v); n > widest {
			widest, key = n, r.key
		}
	}
	require.Positive(t, checked, "the schema must declare fixed-vocabulary enum or bool rows")
	assert.LessOrEqualf(t, widest, rowMinValueCells,
		"row %q's compact value is %d cells, over the %d-cell minimum the threshold budgets for",
		key, widest, rowMinValueCells)

	// The exemption list must not rot into a way of hiding a row that could be bounded.
	for k, reason := range dynamicValueRows {
		assert.NotEmptyf(t, reason, "exemption %q must document why it is unbounded", k)
		r := rowByKey(t, cfg, k)
		assert.Equalf(t, kindEnum, r.kind, "only an enum row can have a dynamic option list: %q", k)
	}
}

// TestSelectingTheLongestHelpRowKeepsTheRowCount is spec §13's guard 5 and the direct
// regression test for D5, the defect that motivated this redesign: at 80x24, selecting
// Account clustering collapsed the list to 8 visible rows while its help took 8 lines,
// because the footer's height fed the body's budget.
//
// The assertion is deliberately over EVERY row rather than the worst one: the property is
// that the visible row count does not depend on the cursor at all, and picking a single
// "longest help" row would silently stop testing that the day the copy changes.
func TestSelectingTheLongestHelpRowKeepsTheRowCount(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {100, 32}, {80, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)

			// All settings, so every row is in one pane and the count is comparable across
			// rows from different categories.
			o.railCursor = 0
			want := len(o.rowsPaneLines())
			require.Greater(t, want, 3, "the pane must show real rows for this to mean anything")

			for _, r := range newSettingRows(config.DefaultConfig()) {
				require.True(t, o.SelectRow(r.key))
				o.railCursor = 0 // stay in the flat view; SelectRow moved the rail
				assert.Equalf(t, want, len(o.rowsPaneLines()),
					"selecting %q changed the visible row count from %d to %d — help height "+
						"must not depend on the cursor (D5)", r.key, want, len(o.rowsPaneLines()))
				assert.Equalf(t, o.helpHeight(), len(o.helpLines()),
					"the help pane must be exactly helpHeight() lines on row %q", r.key)
			}
		})
	}
}

// TestHelpHeightIgnoresTheCursor pins the same property one level down, on the function
// rather than on the render. If this passes and guard 5 fails, the leak is in the pane
// budget; if both fail, it is here.
func TestHelpHeightIgnoresTheCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	want := o.helpHeight()
	require.Equal(t, helpPaneLines, want, "80x24 must afford the full help pane")

	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.True(t, o.SelectRow(r.key))
		assert.Equalf(t, want, o.helpHeight(), "row %q changed the help pane's height", r.key)
	}
}

// TestRailFitsUnscrolledAtTheFloor is spec §13's guard 4 and §4's rail-height invariant:
// the whole rail must be visible at the project's 80x24 degradation floor, because the
// rail is the panel's only orientation — a rail that scrolls means the user cannot see
// what categories exist.
//
// Thirteen entries fit thirteen pane rows exactly. That is not a coincidence to be
// preserved by luck: a fourteenth entry has to displace another, and this test is what
// forces that decision to be deliberate.
func TestRailFitsUnscrolledAtTheFloor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)

	assert.GreaterOrEqual(t, o.paneHeight(), len(railEntries()),
		"the rail must fit unscrolled at 80x24 (spec §4); a 14th entry must displace another")

	lines := o.railLines()
	require.Len(t, lines, o.paneHeight(), "the rail pane is padded to the shared pane height")
	rail := stripANSI(strings.Join(lines, "\n"))
	for _, e := range railEntries() {
		assert.Containsf(t, rail, e.label, "rail entry %q is not visible at 80x24", e.label)
	}
}

// TestRailRendersEveryLabelWhole pins that no rail label is ever clipped. The rail is the
// panel's only orientation, so a half-written category name is worse than a scrolled rail.
func TestRailRendersEveryLabelWhole(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24) // the floor, where all thirteen fit unscrolled
	rail := stripANSI(strings.Join(o.railLines(), "\n"))
	assert.NotContains(t, rail, "…", "no rail label may be truncated")
	for _, e := range railEntries() {
		assert.Containsf(t, rail, e.label, "rail label %q is clipped", e.label)
	}
}

// TestSelectedRowIsAlwaysVisible pins the invariant the panel exists to serve: whatever the
// terminal size, whatever the category, the row the cursor is on is rendered.
//
// This is the guard the plan's first draft lacked, and it caught a real defect in review:
// with the cursor pinned to the LAST visible line, the "↓ n more" overflow marker lands on
// top of it, so the selected row vanished for every cursor position except the very last —
// in All settings at 80x24 past row 13, and in every category on a terminal of 14 rows or
// fewer. Guard 5 counts lines and cannot see it; only asserting the label can.
func TestSelectedRowIsAlwaysVisible(t *testing.T) {
	sizes := []struct{ w, h int }{{100, 32}, {80, 24}, {80, 14}, {80, 12}, {72, 24}, {60, 20}, {50, 16}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for _, r := range newSettingRows(config.DefaultConfig()) {
				for _, entry := range []int{railIndexForCategory(r.category), 0} {
					require.True(t, o.SelectRow(r.key))
					o.railCursor = entry // 0 exercises the flat view, where windowing bites
					o.syncCursorToRail()
					pane := stripANSI(strings.Join(o.rowsPaneLines(), "\n"))
					assert.Containsf(t, pane, r.label,
						"selected row %q is not rendered in entry %q at %dx%d",
						r.key, railEntries()[entry].label, size.w, size.h)
				}
			}
		})
	}
}

// TestCurrentRailEntryIsAlwaysVisible is the rail's half of the same invariant. Below 24 rows
// the rail cannot show all thirteen entries, and a draft that simply dropped the tail left
// navigating to Accounts on an 80x20 terminal with no visible selection mark anywhere.
func TestCurrentRailEntryIsAlwaysVisible(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {80, 20}, {80, 14}, {80, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for i, e := range railEntries() {
				o.railCursor = i
				o.syncCursorToRail()
				rail := stripANSI(strings.Join(o.railLines(), "\n"))
				assert.Containsf(t, rail, e.label,
					"current rail entry %q is not rendered at %dx%d", e.label, size.w, size.h)
			}
		})
	}
}

// TestBoxHeightDependsOnlyOnTheTerminal pins that the box neither jumps as the rail moves
// nor changes when the row cursor does. A centered overlay whose height changes gets
// re-centered under the user's cursor mid-navigation — the jump
// ui/overlay/textInput_size.go:3-8 warns about.
func TestBoxHeightDependsOnlyOnTheTerminal(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	want := lipgloss.Height(o.Render())

	for i := range railEntries() {
		o.railCursor = i
		o.syncCursorToRail()
		assert.Equalf(t, want, lipgloss.Height(o.Render()),
			"rail entry %q changed the box height", railEntries()[i].label)
	}
	for _, key := range []string{"max_sessions", "group_mode", "agent_oom_margin", "config_file"} {
		require.True(t, o.SelectRow(key))
		assert.Equalf(t, want, lipgloss.Height(o.Render()), "row %q changed the box height", key)
	}
}

// TestNoPaneLineOverflowsItsWidth is spec §13's guard 6, measured where it can actually
// fail: on the plain composed lines, before the bordered box pads them all to the same
// width. A post-render width assert is a tautology — an over-wide line makes lipgloss
// soft-wrap and grow the box, never exceed its width.
func TestNoPaneLineOverflowsItsWidth(t *testing.T) {
	cfg := config.DefaultConfig()
	// A pathologically long value, so the truncation paths are exercised rather than merely
	// available.
	cfg.TmuxConfigOverride = strings.Repeat("/very/long/path", 20)
	cfg.ProjectSearchRoots = []string{strings.Repeat("~/deeply/nested", 12)}

	sizes := []struct{ w, h int }{{80, 24}, {100, 32}, {74, 24}, {73, 24}, {60, 20}, {40, 14}, {30, 14}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(cfg)
			o.SetSize(size.w, size.h)
			for i := range railEntries() {
				o.railCursor = i
				o.syncCursorToRail()
				for _, line := range o.rowsPaneLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), o.rowsPaneWidth(),
						"a rows-pane line overflows %d cells on entry %q: %q",
						o.rowsPaneWidth(), railEntries()[i].label, stripANSI(line))
				}
				for _, line := range o.railLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), railWidth(),
						"a rail line overflows %d cells: %q", railWidth(), stripANSI(line))
				}
				for _, line := range o.helpLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), o.innerWidth(),
						"a help line overflows the inner width %d: %q", o.innerWidth(), stripANSI(line))
				}
			}
		})
	}
}

// TestMaxPaneLinesMatchesTheFlatView pins that the pane-height cap is the real size of the
// tallest view rather than a formula that can drift from it. maxPaneLines caps paneHeight, so
// an over-estimate leaves permanent blank rows at the bottom of a tall terminal and an
// under-estimate makes the flat view scroll when it need not.
func TestMaxPaneLinesMatchesTheFlatView(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 200) // tall enough that nothing is windowed
	o.railCursor = 0    // All settings
	assert.Equal(t, len(o.rowsPaneContent(o.rowsPaneWidth())), o.maxPaneLines(),
		"maxPaneLines must equal the flat view's actual line count")
}

// TestNoBodyLineOverflowsTheInnerWidth is the guard TestNoPaneLineOverflowsItsWidth cannot be.
// That one measures each pane against its own width, so both can be internally consistent
// while the JOINED line is not — which is exactly the bug this caught: rowsPaneWidth() returns
// the whole inner width in single-pane mode, so joining it beside the rail anyway produced
// 56-cell lines in a 34-cell box, lipgloss soft-wrapped every one of them, and the box grew
// from 24 rows to 28.
//
// The width sweep crosses the two-pane threshold in single steps, because that boundary is
// where the two layouts disagree about who owns the width.
func TestNoBodyLineOverflowsTheInnerWidth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TmuxConfigOverride = strings.Repeat("/very/long/path", 20)
	o := NewSettingsOverlay(cfg)

	for w := 30; w <= 110; w++ {
		for _, h := range []int{12, 24, 32} {
			o.SetSize(w, h)
			for _, focus := range []settingsFocus{focusRail, focusRows} {
				o.focus = focus
				for i := range railEntries() {
					o.railCursor = i
					o.syncCursorToRail()
					for _, line := range o.bodyLines() {
						assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), o.innerWidth(),
							"body line overflows inner width %d at %dx%d (entry %q, focus %d): %q",
							o.innerWidth(), w, h, railEntries()[i].label, focus, stripANSI(line))
					}
				}
			}
		}
	}
}

// TestBoxNeverOutgrowsTheTerminal sweeps both dimensions. The height half is the lockstep net
// between paneHeight and helpHeight — two separate formulas that must stay in numeric
// agreement — and the width half catches a layout that soft-wraps rather than degrades.
func TestBoxNeverOutgrowsTheTerminal(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, widestFooterRow(t))

	for h := 10; h <= 40; h++ {
		o.SetSize(80, h)
		assert.LessOrEqualf(t, lipgloss.Height(o.Render()), h, "box must fit terminal height %d", h)
		assert.Containsf(t, stripANSI(o.Render()), "esc", "a hint must survive at height %d", h)
	}
	for w := 30; w <= 120; w++ {
		o.SetSize(w, 24)
		assert.LessOrEqualf(t, lipgloss.Height(o.Render()), 24, "box must fit 24 rows at width %d", w)
		assert.Containsf(t, stripANSI(o.Render()), "esc", "a hint must survive at width %d", w)
	}
}
