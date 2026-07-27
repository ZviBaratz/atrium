package overlay

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	tea "github.com/charmbracelet/bubbletea"
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
				require.True(t, o.OpenAt(r.key))
				o.railCursor = 0 // stay in the flat view; OpenAt moved the rail
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
		require.True(t, o.OpenAt(r.key))
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
					require.True(t, o.OpenAt(r.key))
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
		require.True(t, o.OpenAt(key))
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

// TestSinglePaneFallbackBelowTheThreshold is spec §13's guard 10: below the derived width the
// panel shows one pane at a time, and both are reachable. The two widths are taken from
// twoPaneMinInner() rather than written as literals, so the test follows the threshold instead
// of pinning today's value of it.
func TestSinglePaneFallbackBelowTheThreshold(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24) // wide, so twoPaneMinInner() is measured against a stable rail
	minInner := o.twoPaneMinInner()

	// boxWidth = min(96, width-2) and innerWidth = boxWidth-4, so inner == width-6 below the
	// 96 cap. One cell either side of the threshold.
	wide, narrow := minInner+6, minInner+5

	o.SetSize(wide, 24)
	require.True(t, o.twoPane(), "inner %d must afford two panes", o.innerWidth())
	body := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, body, "Sessions", "the rail is visible")
	assert.Contains(t, body, "Session limit", "and so are the rows, side by side")

	o.SetSize(narrow, 24)
	require.False(t, o.twoPane(), "inner %d must fall back to one pane", o.innerWidth())

	// Focused on the rail: the rail is the whole body.
	require.Equal(t, focusRail, o.focus)
	railOnly := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, railOnly, "Worktrees & git", "the rail is the whole pane")
	assert.NotContains(t, railOnly, "Session limit", "the rows are a separate screen now")

	// Enter drills in; the rows become the whole body and the rail steps aside.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, focusRows, o.focus)
	rowsOnly := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, rowsOnly, "Session limit", "the rows are reachable")
	assert.NotContains(t, rowsOnly, "Worktrees & git", "the rail is not drawn beside them")

	// Esc returns, so nothing is a one-way door.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, focusRail, o.focus)
	assert.Contains(t, stripANSI(strings.Join(o.bodyLines(), "\n")), "Worktrees & git")
}

// TestThresholdIsDerivedFromTheParts pins spec §10's requirement that the threshold be
// "computed from the parts, not hardcoded as a magic number".
//
// Be precise about what this catches, because it is less than it looks: replacing the sum with
// the literal 67 passes today, since 67 *is* the sum. What it catches is the literal going
// STALE — rename a category and the expected value moves to 84 while the literal stays at 67,
// which is exactly when a hardcoded threshold starts offering two panes at a width where the
// rail has eaten the rows pane. That is the real failure mode, and it is verified by mutation:
// hardcode the threshold, widen the longest rail label, and this fails alongside
// TestRailWidthTracksItsLongestLabel.
func TestThresholdIsDerivedFromTheParts(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24)

	assert.Equal(t, railWidth()+paneDividerCells+o.minRowsPaneWidth(), o.twoPaneMinInner(),
		"the threshold is the sum of its parts, not a literal")
	assert.Equal(t, rowMarkerCells+o.longestRowLabel()+rowLabelGap+rowMinValueCells,
		o.minRowsPaneWidth(),
		"the minimum rows pane holds the widest label untruncated plus a legible value")

	// At the threshold the rows pane can still show the widest label without truncating it —
	// which is what the threshold is FOR.
	o.SetSize(o.twoPaneMinInner()+6, 24)
	require.True(t, o.twoPane())
	assert.GreaterOrEqual(t, o.rowsPaneWidth(), rowMarkerCells+o.longestRowLabel(),
		"at the threshold the widest label must still fit whole")
}

// TestSinglePaneFallbackShowsTheCategoryName pins that drilling in does not lose the
// orientation the rail was providing. Below the threshold the rail is not drawn, so without a
// header the user is looking at an unlabelled list of rows — D2, the defect this redesign
// exists to fix, reintroduced at narrow widths.
func TestSinglePaneFallbackShowsTheCategoryName(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24)
	o.SetSize(o.twoPaneMinInner()+5, 24) // one cell under the threshold
	require.False(t, o.twoPane())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // drill in
	require.Equal(t, focusRows, o.focus)
	assert.Contains(t, stripANSI(strings.Join(o.bodyLines(), "\n")), o.selectedEntry().label,
		"the drilled-in pane must name the category the rail is no longer showing")

	// And the header must NOT appear in two-pane mode, where the rail already names it.
	o.SetSize(100, 32)
	require.True(t, o.twoPane())
	assert.NotContains(t, stripANSI(strings.Join(o.rowsPaneLines(), "\n")), o.selectedEntry().label,
		"a header beside the rail would only repeat it")
}

// TestModifiedMarkerAndSelectionMarkAreSeparateColumns pins spec §10's requirement, and the
// trap it exists to prevent: a row that is both selected and modified must show BOTH marks.
// Reusing the SelectionMark cell for the modified marker would make "changed from default"
// invisible on exactly the row the user is looking at.
func TestModifiedMarkerAndSelectionMarkAreSeparateColumns(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "mouse")
	i := o.cursor
	require.False(t, o.isModified(i), "mouse starts at its default")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	require.Equal(t, "mouse", changed)
	require.True(t, o.isModified(i))

	g := theme.Current().Glyphs
	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Containsf(t, line, g.SelectionMark+g.Modified,
		"a selected+modified row shows both marks in adjacent cells: %q", line)
}

// TestUnmodifiedRowShowsNoMarker pins the negative direction — the render twin of guard 2.
// Without it the marker could be hardwired on and every assertion above would still pass.
func TestUnmodifiedRowShowsNoMarker(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "theme")
	marked := 0
	for i := range o.rows {
		if strings.Contains(stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth())),
			theme.Current().Glyphs.Modified) {
			marked++
		}
	}
	assert.Zero(t, marked, "no row may show a modified marker on a fresh DefaultConfig")
}

// TestEditingRowKeepsTheLabelColumn pins that opening the inline editor does not shift the
// label. The editing branch builds its own head rather than going through composeRowLine, so it
// has to spend the same three marker cells — otherwise every label jumps sideways the instant
// Enter is pressed, which reads as the panel glitching.
func TestEditingRowKeepsTheLabelColumn(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "branch_prefix")
	i := o.cursor
	labelW, width := o.visibleLabelWidth(), o.rowsPaneWidth()

	at := func(line string) int {
		return ansi.StringWidth(line[:strings.Index(line, "Branch prefix")])
	}
	before := at(stripANSI(o.renderRowLine(i, width, labelW)))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, o.editing)
	after := at(stripANSI(o.renderRowLine(i, width, labelW)))

	assert.Equal(t, before, after, "the label column must not move when editing opens")
	assert.Equal(t, rowMarkerCells, after)
}

// TestTimingBadgeRendersForEveryNonLiveRow pins that applyTiming.badge() reaches the screen.
// PR A declared badge() and nothing called it; a projection no renderer reads is the same bug
// class TestEveryCautionReachesTheFooter caught.
func TestTimingBadgeRendersForEveryNonLiveRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32) // wide, so no badge is dropped for width

	badged := 0
	for i, r := range o.rows {
		if r.timing == timingLive || o.inertReason(i) != "" {
			continue // an inert row's chip replaces its badge, tested separately below
		}
		badged++
		o.railCursor = railIndexForCategory(r.category)
		o.syncCursorToRail()
		line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
		assert.Containsf(t, line, r.timing.badge(),
			"row %q must render its %q badge: %q", r.key, r.timing.badge(), line)
	}
	require.Positive(t, badged, "the schema must declare rows that are not timingLive")
}

// TestEveryInertPredicateHasAReason pins the drift guard that matters most here: a row dimmed
// with no explanation is worse than a row not dimmed at all, because the user sees something is
// off and has nothing to act on. Adding a seventh activeWhen predicate without a reason chip
// fails here.
func TestEveryInertPredicateHasAReason(t *testing.T) {
	predicated := map[string]bool{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.activeWhen == nil {
			continue
		}
		predicated[r.key] = true
		assert.NotEmptyf(t, inertReasons[r.key],
			"row %q declares activeWhen but has no reason chip — it would dim with no explanation", r.key)
	}
	require.NotEmpty(t, predicated, "the schema must declare at least one activeWhen")

	for key := range inertReasons {
		if reason, ok := inertReasonsWithoutPredicate[key]; ok {
			assert.NotEmptyf(t, reason, "exception %q must document why it has no predicate", key)
			continue
		}
		assert.Truef(t, predicated[key],
			"inertReasons names %q, which declares no activeWhen — a stale entry that can never render", key)
	}

	// The exception list itself must not rot: an entry naming a row that has since gained a
	// predicate would silently disable the guard for it.
	for key := range inertReasonsWithoutPredicate {
		assert.NotEmptyf(t, inertReasons[key], "exception %q names no reason chip", key)
		assert.Falsef(t, predicated[key],
			"row %q now declares activeWhen, so it no longer needs an exception", key)
	}
}

// TestInertRowIsDimmedAndChippedAndStillEditable is spec §13's guard 7, all three clauses. The
// transitions are driven through the real config so the predicate is exercised rather than the
// map: Notifications off makes Finished turns inert, and switching to desktop makes Notify
// command active.
func TestInertRowIsDimmedAndChippedAndStillEditable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	// Park the cursor elsewhere: a selected row is accented, so dimming can only be observed
	// on an unselected one.
	settingsAt(t, o, "notifications")
	finished := indexOfRowKey(t, o, "notifications_finished")
	require.NotEqual(t, o.cursor, finished)

	raw := o.renderRowLine(finished, o.rowsPaneWidth(), o.visibleLabelWidth())
	assert.Equal(t, "needs Notifications", o.inertReason(finished))
	assert.Contains(t, stripANSI(raw), "needs Notifications", "the reason chip must be on the row")
	assert.Equal(t, theme.Current().FaintStyle().Render(stripANSI(raw)), raw,
		"an inert row is rendered in the faint style")

	// Still fully editable: inert means "changing this has no effect right now", never "you may
	// not touch this" (spec §5).
	settingsAt(t, o, "notifications_finished")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications_finished", changed, "an inert row still cycles")

	// And the transition works the other way: desktop mode activates Notify command.
	cfg.Notifications = config.NotificationsDesktop
	cmd := indexOfRowKey(t, o, "notify_command")
	assert.Empty(t, o.inertReason(cmd), "desktop mode activates notify_command")
	assert.NotContains(t, stripANSI(o.renderRowLine(cmd, o.rowsPaneWidth(), o.visibleLabelWidth())),
		"needs desktop", "an active row carries no chip")
}

// TestInertChipReplacesTheTimingBadge pins that the two do not both try to occupy the
// right-aligned column. A row that currently does nothing has more urgent news than when it
// would take effect.
func TestInertChipReplacesTheTimingBadge(t *testing.T) {
	cfg := config.DefaultConfig()
	f := false
	cfg.UpdateBaseOnCreate = &f // makes fast_forward_local_base inert
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	i := indexOfRowKey(t, o, "fast_forward_local_base")
	require.Equal(t, timingNewSessions, o.rows[i].timing)
	o.railCursor = railIndexForCategory(o.rows[i].category)
	o.syncCursorToRail()
	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Contains(t, line, "needs Update base on create")
	assert.NotContains(t, line, timingNewSessions.badge(), "the chip takes the badge's column")
}

// TestVisibilitySignalsSurviveTheDegradationFloor is the guard the first draft was missing
// entirely: every other test in this task runs at 100x32, where nothing competes for width.
//
// At 80x24 — the project's floor, and the size the whole design is budgeted against — the rows
// pane is 52 cells, and an inline enum rendering that spends all of it leaves no room for the
// badge. The badge is what carries the inert reason, so the row would dim with no explanation:
// exactly what inertReasons' own doc comment calls worse than not dimming at all. Two rows in
// Notifications would disagree about it, one explained and one not.
func TestVisibilitySignalsSurviveTheDegradationFloor(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {76, 24}, {73, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Notifications = config.NotificationsOff // inerts two Notifications rows
			f := false
			cfg.UpdateBaseOnCreate = &f // inerts fast_forward_local_base
			o := NewSettingsOverlay(cfg)
			o.SetSize(size.w, size.h)

			checked, degraded := 0, 0
			for i, r := range o.rows {
				chip := o.inertReason(i)
				if chip == "" {
					continue
				}
				checked++
				o.railCursor = railIndexForCategory(r.category)
				o.syncCursorToRail()
				line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))

				// The full reason where it fits, the one-word fallback where it does not — but
				// never nothing. A dimmed row with no marker at all reads as broken, and the
				// help pane only describes the SELECTED row.
				switch {
				case strings.Contains(line, chip):
				case strings.Contains(line, inertBadgeShort):
					degraded++
				default:
					assert.Failf(t, "inert row lost its chip",
						"row %q is dimmed with no marker at %dx%d (reason %q): %q",
						r.key, size.w, size.h, chip, line)
				}
			}
			require.Positive(t, checked, "the fixture must make at least one row inert")
			if size.w >= 100 {
				assert.Zero(t, degraded, "a 100-column pane has room for every full reason")
			}
		})
	}
}

// TestContextLineExplainsAnInertRowInProse pins the help pane's half of the signal. The chip is
// three words in a column and is easy to misread as a prohibition; the sentence says what it
// actually means.
func TestContextLineExplainsAnInertRowInProse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AutoYes = false
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "daemon_poll_interval")

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "No effect right now")
	assert.Contains(t, ctx, "needs Auto-yes")
}

// TestContextLineShowsAGlossForTheCurrentEnumOption pins that gloss reaches the help pane on an
// ordinary row, which is what makes cycling an enum teach rather than guess.
func TestContextLineShowsAGlossForTheCurrentEnumOption(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOSC
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "notifications")

	row := o.selectedRow()
	require.NotEmpty(t, row.gloss[config.NotificationsOSC], "the schema glosses osc")
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), row.gloss[config.NotificationsOSC])
}

// TestContextLineFallsBackToDetail pins the fallback that makes spec §3's mockup literal: its
// second help line for Notifications is that row's DETAIL, not its summary. Without it, a row
// whose long-form help is only detail shows one prose line and a blank.
func TestContextLineFallsBackToDetail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "mouse") // has detail, no gloss, no chip, an untruncated value
	row := o.selectedRow()
	require.NotEmpty(t, row.detail)
	require.Empty(t, row.gloss)

	want := firstSentence(row.detail)
	require.NotEmpty(t, want, "the row's detail must have a first sentence to show")
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), want)
}

// TestContextLineShowsATruncatedValueInFull pins spec §10's obligation: the value column may be
// shortened, but the full value must appear in the help pane, or the truncation loses
// information outright.
func TestContextLineShowsATruncatedValueInFull(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	settingsAt(t, o, "carry_files")

	full := o.selectedRow().get(cfg)
	require.True(t, o.valueWasTruncated(),
		"carry_files' default (%d cells) must not fit an 80-col rows pane (%d cells), or this "+
			"test proves nothing", ansi.StringWidth(full), o.rowsPaneWidth())
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), full,
		"a truncated value must be shown in full in the help pane")
}

// TestPositionReadoutSurvivesInTheRenderedPane pins the orientation readout (D2: scrolled to
// the bottom of the old list you could not tell where you were).
//
// It asserts through helpLines(), NOT through contextLine() directly. That distinction is the
// whole test: a draft called contextLine and would have passed while the rendered pane threw
// the counter away — helpLines appended it and *then* capped the list, so any row whose prose
// filled the pane evicted it silently.
func TestPositionReadoutSurvivesInTheRenderedPane(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {56, 24}, {40, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for _, key := range []string{
				"default_program", "carry_files", "notifications", "config_file",
				"fast_forward_local_base", // the widest footer: the eviction case
			} {
				settingsAt(t, o, key)
				start, end := o.rowRange(o.selectedEntry())
				want := fmt.Sprintf("%d/%d", o.cursor-start+1, end-start)
				pane := stripANSI(strings.Join(o.helpLines(), "\n"))
				assert.Containsf(t, pane, want,
					"row %q's help pane must carry the position %q at %dx%d:\n%s",
					key, want, size.w, size.h, pane)
			}
		})
	}
}

// indexOfRowKey returns the row index for a key without moving the cursor, so a test can render
// a row it is not sitting on.
func indexOfRowKey(t *testing.T, o *SettingsOverlay, key string) int {
	t.Helper()
	for i, r := range o.rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("no settings row with key %q", key)
	return -1
}

// TestEveryDetailAndGlossReachExpandedHelp is the render-level twin of
// TestDetailRetainsTheMovedProse, and it guards the same bug class as
// TestEveryCautionReachesTheFooter: help copy living in a field the renderer never reads is
// invisible, and a test that only pins the field's contents still passes.
//
// PR A moved as much as 443 characters per row out of `description` into `detail` and rendered
// none of it. This is the test that says it is now visible.
func TestEveryDetailAndGlossReachExpandedHelp(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	details, glosses := 0, 0
	for i, r := range o.rows {
		content := o.expandedHelpContent(i)
		assert.Containsf(t, content, r.label, "row %q's expanded help must name the row", r.key)
		assert.Containsf(t, content, r.summary, "row %q's summary must reach expanded help", r.key)
		if r.detail != "" {
			details++
			assert.Containsf(t, content, r.detail, "row %q's detail must reach expanded help", r.key)
		}
		if r.caution != "" {
			assert.Containsf(t, content, r.caution, "row %q's caution must reach expanded help", r.key)
		}
		if r.kind != kindEnum {
			continue
		}
		for _, opt := range r.options(cfg) {
			assert.Containsf(t, content, opt, "row %q's option %q must be listed", r.key, opt)
			if g := r.gloss[opt]; g != "" {
				glosses++
				assert.Containsf(t, content, g, "row %q's gloss for %q must reach expanded help", r.key, g)
			}
		}
	}
	// Without these the loops could stop running and the test would still pass.
	require.Positive(t, details, "the schema must declare rows with detail")
	require.Positive(t, glosses, "the schema must declare glossed enum options")
}

// TestExpandedHelpShowsTheCurrentValueInFull pins that `?` is the escape hatch for a value the
// row line and even the help pane's context line had to shorten.
func TestExpandedHelpShowsTheCurrentValueInFull(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectSearchRoots = []string{strings.Repeat("~/deeply/nested/path", 8)}
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	i := indexOfRowKey(t, o, "project_search_roots")
	assert.Contains(t, o.expandedHelpContent(i), cfg.ProjectSearchRoots[0])
}

// TestExpandedHelpNamesTheApplyTimingForEveryRow pins that the timing is stated in words rather
// than only as a badge, since `?` is where a user goes when the badge was not enough.
func TestExpandedHelpNamesTheApplyTimingForEveryRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	for i, r := range o.rows {
		want := r.timing.footerNote()
		if want == "" {
			want = "applies immediately" // timingLive has no footer note by design
		}
		assert.Containsf(t, o.expandedHelpContent(i), want,
			"row %q's expanded help must state its apply timing", r.key)
	}
}

// TestQuestionMarkOpensAndClosesExpandedHelp pins the key grammar of spec §8: `?` opens from the
// rows pane, esc or a second `?` returns to whatever was focused, and an unrecognized key does
// NOT dismiss — the panel is a working surface, and closing on a stray keystroke would lose the
// user's place in the rail.
func TestQuestionMarkOpensAndClosesExpandedHelp(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "group_mode")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen)
	// Assert on the unwrapped content, not the render: ansi.Wrap breaks group_mode's detail
	// mid-phrase at inner 92, so a Contains against Render() would fail for a reason that has
	// nothing to do with whether the detail arrived.
	assert.Contains(t, o.expandedHelpContent(o.cursor), "an account boundary is refused",
		"the expanded view must carry the row's detail")
	assert.Contains(t, stripANSI(o.Render()), "Account clustering",
		"the expanded view is titled with the row's label")
	assert.NotContains(t, stripANSI(o.Render()), "Worktrees & git",
		"and it takes over the box, so the rail is not drawn beside it")

	o.HandleKeyPress(keyRunes("x"))
	assert.True(t, o.helpOpen, "an unrecognized key must not dismiss the help view")

	o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen, "a second ? closes it")
	assert.Equal(t, focusRows, o.focus, "and returns to the pane that was focused")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, o.helpOpen, "esc closes it too")
	assert.Equal(t, focusRows, o.focus, "esc must not also back out of the rows pane")
}

// TestExpandedHelpScrolls pins that long detail is reachable rather than clipped — the content
// that does not fit is exactly the content `?` exists to show.
//
// The SIZE is chosen to guarantee overflow rather than to be representative. At 80x24 the budget
// is paneHeight(13) + helpBlock(4) = 17 lines against inner 74, and no row in the schema wraps
// past 17 there. 60x20 gives a smaller budget against inner 54, where max_sessions' 343-character
// detail alone needs several lines.
func TestExpandedHelpScrolls(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(60, 20)
	settingsAt(t, o, "max_sessions") // the longest detail literal in the schema
	o.HandleKeyPress(keyRunes("?"))
	require.Positive(t, o.maxHelpScroll(),
		"this row's help must overflow a %d-line budget at inner %d, or scrolling is untested",
		o.expandedHelpHeight(), o.innerWidth())

	top := stripANSI(strings.Join(o.expandedHelpLines(), "\n"))
	for range 40 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, o.maxHelpScroll(), o.helpScroll, "↓ must clamp at the end, not run past it")
	bottom := stripANSI(strings.Join(o.expandedHelpLines(), "\n"))
	assert.NotEqual(t, top, bottom, "scrolling must change what is shown")
	assert.Contains(t, bottom, "Current value",
		"scrolling to the end must reach the last section, which no unscrolled view showed")
	assert.NotContains(t, top, "Current value",
		"or the assertion above proves nothing about scrolling")

	for range 40 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Zero(t, o.helpScroll, "↑ must clamp at the top")
}

// TestExpandedHelpDoesNotChangeTheBoxHeight pins that opening `?` cannot resize the panel.
// PlaceOverlay centers the box, so a height change would re-center it under the user's cursor
// the instant they press `?`.
func TestExpandedHelpDoesNotChangeTheBoxHeight(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {60, 20}, {80, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			settingsAt(t, o, "agent_oom_margin")
			closed := lipgloss.Height(o.Render())
			o.HandleKeyPress(keyRunes("?"))
			require.True(t, o.helpOpen)
			assert.Equal(t, closed, lipgloss.Height(o.Render()), "opening ? must not resize the box")
		})
	}
}

// TestGroupModeChipIsSilentUntilTheGateIsInjected pins the tri-state. A panel that cannot see the
// session list must not guess: the honest gate is session-derived, and a default of "inert" would
// put "nothing to cluster" on every row on every open, while a default of "active" would be a
// silent wrong answer. nil means no chip.
func TestGroupModeChipIsSilentUntilTheGateIsInjected(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.GroupMode = config.GroupModeAccount
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	assert.Empty(t, o.inertReason(indexOfRowKey(t, o, "group_mode")),
		"with no injected gate the panel says nothing")
}

// TestGroupModeChipTracksTheInjectedGate pins all four combinations of (setting, gate). The chip
// appears only when the setting is ON and the list says clustering is invisible — "off" is not
// inert, it is simply off, and a chip there would be noise on a row doing exactly what it says.
func TestGroupModeChipTracksTheInjectedGate(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		visible bool
		want    string
	}{
		{"on and clustering", config.GroupModeAccount, true, ""},
		{"on but nothing to cluster", config.GroupModeAccount, false, "nothing to cluster"},
		{"off and clustering", config.GroupModeRepo, true, ""},
		{"off and not clustering", config.GroupModeRepo, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.GroupMode = tc.mode
			o := NewSettingsOverlay(cfg)
			o.SetSize(100, 32)
			o.SetAccountClusteringVisible(tc.visible)
			assert.Equal(t, tc.want, o.inertReason(indexOfRowKey(t, o, "group_mode")))
		})
	}
}

// TestRailIndexRoundTrips pins the accessor pair home uses to remember the category across opens
// within a run (spec §7). Persisting it to state.json is a deliberate non-goal — a fresh launch
// starting at the top is fine — so an in-memory int on home is the whole mechanism.
func TestRailIndexRoundTrips(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for i := range railEntries() {
		o.SetRailIndex(i)
		assert.Equal(t, i, o.RailIndex())
		start, end := o.rowRange(o.selectedEntry())
		if end > start {
			assert.GreaterOrEqualf(t, o.cursor, start, "SetRailIndex(%d) left the cursor outside the pane", i)
			assert.Less(t, o.cursor, end)
		}
	}
	// Out-of-range values are clamped rather than panicking: home's remembered index could
	// outlive a rail that shrank.
	o.SetRailIndex(999)
	assert.Equal(t, len(railEntries())-1, o.RailIndex())
	o.SetRailIndex(-5)
	assert.Equal(t, 0, o.RailIndex())
}

// searchedPane renders the rows pane under a filter and returns its unstyled lines, which is
// the surface every assertion below is about.
func searchedPane(t *testing.T, o *SettingsOverlay, query string) []string {
	t.Helper()
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, query)
	out := make([]string, 0, len(o.rowsPaneLines()))
	for _, l := range o.rowsPaneLines() {
		out = append(out, stripANSI(l))
	}
	return out
}

// TestSearchResultsShowTheirCategory is spec §8's "each hit's category shown on the row". A
// flat list drawn from ten categories is unreadable without it.
func TestSearchResultsShowTheirCategory(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	lines := searchedPane(t, o, "glyph")

	require.NotEmpty(t, lines)
	assert.Contains(t, strings.Join(lines, "\n"), "Appearance",
		"the Glyph set hit names the category it lives in")
}

// TestSearchResultCategoryDegradesRatherThanVanishing sweeps every two-pane width. The
// category is what tells two similarly-named results apart, so — like the inert chip, and
// unlike the timing badge — it shortens rather than dropping.
//
// Three things this gets right that an obvious version does not:
//
//   - It asserts on the BADGE rowValueAndBadge returns, not on a substring of the rendered
//     line. strings.Contains(line, "…") also matches a truncated VALUE, so a line-level check
//     passes while the category is gone.
//   - It checks each row against ITS OWN category, not a literal. "base" is a subsequence
//     query and matches 13 rows across six categories.
//   - It sweeps three queries. "base"'s non-inert hits are all bool rows with short values, so
//     the badge column is never squeezed and that query proves nothing on its own; "config"
//     and "session" each bring an enum and a long text row, which is where the eviction
//     fitValue exists to prevent actually happens.
//
// update_base_on_create is switched off so fast_forward_local_base is genuinely inert: under
// DefaultConfig it is active, the skip never fires, and the branch this claims to cover is
// dead. require.NotZero keeps that honest.
func TestSearchResultCategoryDegradesRatherThanVanishing(t *testing.T) {
	skipped := 0
	for _, q := range []string{"base", "config", "session"} {
		for w := 73; w <= 120; w++ {
			cfg := config.DefaultConfig()
			off := false
			cfg.UpdateBaseOnCreate = &off
			o := NewSettingsOverlay(cfg)
			o.SetSize(w, 32)
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, q)
			results := o.searchResults()
			require.NotEmptyf(t, results, "query %q, width %d: must match", q, w)

			labelW, paneW := o.visibleLabelWidth(), o.rowsPaneWidth()
			for _, i := range results {
				if o.inertReason(i) != "" {
					skipped++ // the inert chip legitimately takes the column instead
					continue
				}
				_, badge := o.rowValueAndBadge(i, paneW, labelW, "")
				cat := o.rows[i].category.label()
				require.NotEmptyf(t, badge, "query %q, width %d, row %q lost its category %q",
					q, w, o.rows[i].key, cat)
				assert.Truef(t, strings.HasPrefix(cat, strings.TrimSuffix(badge, "…")),
					"query %q, width %d, row %q: badge %q is not a shortening of %q",
					q, w, o.rows[i].key, badge, cat)
			}
		}
	}
	require.NotZero(t, skipped, "the inert branch must actually be exercised")
}

// TestInertChipBeatsTheCategoryOnASearchRow: a row that does nothing right now has more
// urgent news than which category it lives in. The category is not lost — contextLine names
// it for the highlighted row.
func TestInertChipBeatsTheCategoryOnASearchRow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "finished")

	i := o.searchResults()[0]
	require.Equal(t, "notifications_finished", o.rows[i].key)
	require.NotEmpty(t, o.inertReason(i), "precondition: the row is inert with notifications off")

	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Contains(t, line, "needs Notifications", "the inert chip keeps the badge column")
}

// TestSearchRowLinesFillThePaneExactlyAndKeepTheirChip sweeps widths AND queries. A search row
// carries the widest content the panel ever composes — the label column is the widest MATCHING
// label, and the badge is a category name rather than a five-cell timing word.
//
// It asserts EQUALITY, not "<=". composeRowLine bounds its own output by construction (both
// branches set gap = avail − …, and an over-wide badge is dropped rather than overflowing), so
// a "<= paneW" assertion is a tautology no bug in searchBadge or valueCell can trip. What can
// actually break is the gap arithmetic, and the chip being evicted — the presence half below.
func TestSearchRowLinesFillThePaneExactlyAndKeepTheirChip(t *testing.T) {
	for _, q := range []string{"", "e", "s", "session", "base", "notif", "zzz"} {
		for w := 40; w <= 120; w += 7 {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(w, 32)
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, q)
			labelW, paneW := o.visibleLabelWidth(), o.rowsPaneWidth()
			for _, i := range o.searchResults() {
				line := stripANSI(o.renderRowLine(i, paneW, labelW))
				if rowMarkerCells+labelW+rowLabelGap+1 > paneW {
					// Below the floor the label rule yields and the head is truncated; parity with
					// the pre-PR-B renderer, and composeRowLine's documented branch.
					assert.LessOrEqualf(t, ansi.StringWidth(line), paneW,
						"query %q, width %d, row %q overflows even truncated", q, w, o.rows[i].key)
					continue
				}
				assert.Equalf(t, paneW, ansi.StringWidth(line),
					"query %q, width %d, row %q does not fill its pane: %q", q, w, o.rows[i].key, line)
				if w >= 73 && o.inertReason(i) == "" {
					_, badge := o.rowValueAndBadge(i, paneW, labelW, "")
					assert.NotEmptyf(t, badge,
						"query %q, width %d, row %q lost its category chip", q, w, o.rows[i].key)
				}
			}
		}
	}
}

// TestSearchCarriesTheCategoryBelowTheThreshold sweeps the widths the other search guards
// cannot reach. Below 73 columns the panel is single-pane: `/` focuses the rows, so the rail —
// and with it every match count — is not drawn at all, and the badge column is squeezed hard
// enough that the category chip genuinely does get dropped. contextLine's category prefix is
// the whole orientation down there, so it is what this asserts.
func TestSearchCarriesTheCategoryBelowTheThreshold(t *testing.T) {
	for w := 40; w < 73; w++ {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(w, 32)
		o.HandleKeyPress(keyRunes("/"))
		typeFilter(o, "config")
		require.NotEmptyf(t, o.searchResults(), "width %d: the query must match", w)
		require.Falsef(t, o.twoPane(), "width %d must be below the two-pane threshold", w)

		ctx := stripANSI(o.contextLine(o.innerWidth()))
		cat := o.selectedRow().category.label()
		assert.Truef(t, strings.Contains(ctx, cat) || strings.Contains(ctx, "…"),
			"width %d: the context line must still name %q: %q", w, cat, ctx)
		assert.LessOrEqualf(t, ansi.StringWidth(ctx), o.innerWidth(), "width %d: %q", w, ctx)
	}
}

// TestRailShowsPerCategoryMatchCounts is spec §8's rail behavior. The counts are the only
// orientation left once the pane goes flat: they say where the hits are without the user
// clearing the filter to find out.
func TestRailShowsPerCategoryMatchCounts(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "notif")

	counts := o.railMatchCounts()
	require.Len(t, counts, len(railEntries()))
	for i, e := range railEntries() {
		if e.kind == railCategory && e.category == catNotifications {
			assert.Greaterf(t, counts[i], 0, "the Notifications category must report its hits")
		}
		if e.kind != railCategory {
			assert.Equalf(t, 0, counts[i], "%q is not a category and reports no count", e.label)
		}
	}

	// The rendered line must CARRY the count. Asserting the rail merely contains
	// "Notifications" would hold with or without the feature — the rail renders all thirteen
	// labels unconditionally, filtered or not.
	var line string
	for _, l := range o.railLines() {
		if strings.Contains(stripANSI(l), "Notifications") {
			line = strings.TrimRight(stripANSI(l), " ")
		}
	}
	require.NotEmpty(t, line, "the Notifications entry must be on screen")
	want := strconv.Itoa(counts[railIndexForCategory(catNotifications)])
	require.NotEqual(t, "0", want, "precondition: the query must hit this category")
	assert.True(t, strings.HasSuffix(line, want),
		"the rail entry must end in its match count %s, got %q", want, line)
}

// TestRailCountsSumToTheResultCount pins that the rail is a read-out of the result list rather
// than a second query — the failure mode where the pane and the rail disagree about what
// matched.
func TestRailCountsSumToTheResultCount(t *testing.T) {
	for _, q := range []string{"", "e", "session", "base", "zzz"} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(keyRunes("/"))
		typeFilter(o, q)
		total := 0
		for _, n := range o.railMatchCounts() {
			total += n
		}
		assert.Equalf(t, len(o.searchResults()), total,
			"query %q: the rail counts must account for every result", q)
	}
}

// TestEveryCategoryMatchCountFitsTheRailsOneCell is the premise the single-cell count rests
// on: the largest category has six rows, so a count is always one digit and fits the trail
// cell the handoff arrow otherwise occupies — which is why railWidth() does not move when / is
// pressed. An eleventh category of ten rows would break the rail silently; this fails first
// and forces the decision.
//
// It is a premise guard, not a render assertion: TestRailShowsPerCategoryMatchCounts is what
// checks a rendered line carries its count.
func TestEveryCategoryMatchCountFitsTheRailsOneCell(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, 1, railTrailCells-1, "the trail is one glyph after its leading space")
	for _, c := range allCategories() {
		start, end := o.rowRange(railEntries()[railIndexForCategory(c)])
		assert.LessOrEqualf(t, end-start, 9,
			"category %q has %d rows; a match count no longer fits the rail's one cell",
			c.label(), end-start)
	}
}

// TestRailIsInertWhileFiltering: the rail takes no keys under a filter, so nothing on it may
// look like the focused pane. Its only bright marker is the highlighted result's category.
func TestRailIsInertWhileFiltering(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	before := o.railLines()

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "glyph")
	after := o.railLines()

	require.Len(t, after, len(before))
	assert.NotEqual(t, before, after, "the rail must visibly change when a filter is active")
	for i, l := range after {
		assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(l)), railWidth(),
			"rail line %d overflows while filtering", i)
	}
}

// TestNoMatchesSaysSoAndSaysHowToRecover: an empty pane reads as a broken panel. The prose
// names the two keys out of it, which is the same obligation the handoff note carries.
func TestNoMatchesSaysSoAndSaysHowToRecover(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	lines := searchedPane(t, o, "zzzz")
	require.Empty(t, o.searchResults())

	assert.Contains(t, strings.Join(lines, "\n"), "No setting matches")

	help := stripANSI(strings.Join(o.helpLines(), "\n"))
	assert.Contains(t, help, "backspace")
	assert.Contains(t, help, "esc")
	assert.NotContains(t, help, o.selectedRow().summary,
		"with nothing matching, the help must not describe a row the list is not showing")
}

// TestContextLineNamesTheCategoryWhileSearching closes the gap the badge column leaves: the
// highlighted result always says where it lives, even when the badge went to an inert chip or
// degraded to an ellipsis.
func TestContextLineNamesTheCategoryWhileSearching(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "finished")
	require.Equal(t, "notifications_finished", o.selectedRow().key)

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "Notifications", "the category of the highlighted result")
	assert.Contains(t, ctx, "1/", "the position readout counts results, not category rows")
}

// TestPositionReadoutCountsResultsWhileSearching: the counter is the pane's orientation, and
// under a filter "3/5" must mean the third of five hits, not the third of a category.
func TestPositionReadoutCountsResultsWhileSearching(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "session")
	n := len(o.searchResults())
	require.Greater(t, n, 1)

	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), fmt.Sprintf("1/%d", n))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), fmt.Sprintf("2/%d", n))
}

// TestFilterRidesTheTitleRow: the box's height depends on the terminal alone (PR B), so the
// filter must not claim a line of its own — pressing / would otherwise re-centre the whole
// panel under the user.
func TestFilterRidesTheTitleRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	before := lipgloss.Height(o.Render())

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	assert.Equal(t, before, lipgloss.Height(o.Render()), "opening the filter must not resize the box")

	title := stripANSI(o.titleLine())
	assert.Contains(t, title, "Settings")
	assert.Contains(t, title, "/theme")
	assert.LessOrEqual(t, ansi.StringWidth(title), o.innerWidth())
}

// TestTitleRowSurvivesAnOverlongFilter: the filter is user input with no length bound, and a
// title line wider than the box soft-wraps, grows the box a row and clips the pinned hint.
// This is the discriminating case; the sibling above cannot wrap at 100 columns.
func TestTitleRowSurvivesAnOverlongFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	before := lipgloss.Height(o.Render())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, strings.Repeat("x", 200))

	assert.LessOrEqual(t, ansi.StringWidth(stripANSI(o.titleLine())), o.innerWidth())
	assert.Equal(t, before, lipgloss.Height(o.Render()))
}

// TestHintAdvertisesSearchAndReset: PR B deliberately shipped neither, because a hint for a
// dead key is worse than no hint. Both are live now, so both must be advertised — and the
// filtered hint must name the esc level that applies (spec §15).
func TestHintAdvertisesSearchAndReset(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	assert.Contains(t, stripANSI(o.hintLine()), "/ search", "the rail advertises search")

	settingsAt(t, o, "theme")
	rows := stripANSI(o.hintLine())
	assert.Contains(t, rows, "r reset")
	assert.Contains(t, rows, "/ search")
	assert.Contains(t, rows, "esc back")

	o.HandleKeyPress(keyRunes("/"))
	filtered := stripANSI(o.hintLine())
	assert.Contains(t, filtered, "esc clear", "the filter's own esc level")
	assert.NotContains(t, filtered, "esc back")
}

// TestHintFitsEveryWidth sweeps the ladder rather than sampling it. It asserts the absence of
// an ellipsis, not a width bound: hintLine ends in ansi.Truncate(…, inner, "…"), so a width
// assertion can never fail — what actually breaks is a shortest rung that does not fit and
// gets clipped to something that says nothing.
func TestHintFitsEveryWidth(t *testing.T) {
	for w := 40; w <= 120; w++ {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(w, 32)
		// One overlay walked through three states in order: rail → rows → search.
		for _, state := range []struct {
			name string
			key  tea.KeyMsg
		}{
			{"rail", tea.KeyMsg{}},
			{"rows", tea.KeyMsg{Type: tea.KeyRight}},
			{"search", keyRunes("/")},
		} {
			if state.name != "rail" {
				o.HandleKeyPress(state.key)
			}
			hint := stripANSI(o.hintLine())
			assert.NotContainsf(t, hint, "…", "width %d, %s: the ladder has no rung that fits: %q",
				w, state.name, hint)
			assert.Containsf(t, hint, "esc", "width %d, %s: the esc level must survive: %q",
				w, state.name, hint)
		}
	}
}

// TestALongValueCannotEvictAnInertChip is a PR B bug this PR fixes, kept as its own guard
// because it has nothing to do with search.
//
// PR B's rule is that an inert chip degrades to one word but never vanishes — a dimmed row
// with no marker reads as broken, and the help pane only describes the SELECTED row. But
// valueCell's badge reservation bites only on kindEnum, so notify_command (kindText, inert
// whenever Notifications is not desktop) evicted its own chip as soon as the command was long:
// measured at 73, 80, 100 and 120 columns, the badge came back empty at every one.
func TestALongValueCannotEvictAnInertChip(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsBell // not desktop, so notify_command is inert
	cfg.NotifyCommand = strings.Repeat("n", 60)
	o := NewSettingsOverlay(cfg)

	var idx int
	for i, r := range o.rows {
		if r.key == "notify_command" {
			idx = i
		}
	}
	require.NotEmpty(t, o.inertReason(idx), "precondition: the row must be inert")

	for w := 73; w <= 120; w++ {
		o.SetSize(w, 32)
		o.SetRailIndex(railIndexForCategory(catNotifications))
		labelW, paneW := o.visibleLabelWidth(), o.rowsPaneWidth()
		_, badge := o.rowValueAndBadge(idx, paneW, labelW, o.inertReason(idx))
		assert.NotEmptyf(t, badge, "width %d: a long command evicted the inert chip", w)
	}
}
