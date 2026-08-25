package overlay

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
)

// Rail line geometry: [selection 1][space 1][label ...][space 1][handoff 1].
const (
	railMarkerCells = 2 // the selection mark and the space after it
	railTrailCells  = 2 // the space before the handoff cell, and the cell itself
)

// Rows-pane line geometry:
//
//	[selection 1][modified 1][space 1][label ...][gap 2][value ...][slack][badge]
//
// The selection mark and the modified marker are SEPARATE single-cell columns. A row that
// is both selected and modified shows both, so the modified marker must not reuse the
// SelectionMark cell (spec §10, pinned by TestComposeRowLineKeepsTheMarkerColumnsSeparate).
const (
	rowMarkerCells = 3 // selection + modified + the space after them
	rowLabelGap    = 2
	// rowMinValueCells is the narrowest value column worth offering: enough for every
	// bounded-vocabulary row's compact rendering, the widest of which is "‹ creation ›" at 12
	// cells. It is what makes minRowsPaneWidth — and so the single-pane threshold — a derived
	// number, and TestRowMinValueCellsHoldsEveryFixedVocabularyValue ties it to the real schema.
	//
	// Three enum rows are excluded from that bound (theme, splash, default_program), along with
	// every int and text row: their widths are not bounded by anything this package controls — a
	// theme name grows with the registry, a path can be any length. Excluding them keeps the
	// threshold from widening to chase them; when one does outgrow its pane the truncation ladder
	// shortens it and the help pane shows the value in full (see contextLine).
	rowMinValueCells = 14
)

// paneDividerCells is what the vertical rail/rows divider costs: a space, the theme's own
// left-border rune, and a space.
const paneDividerCells = 3

// railWidth is the rail's fixed width: the widest rail label plus its marker and
// handoff cells. Derived from railEntries() rather than a literal, so renaming a
// category moves the rail — and, through twoPaneMinInner, the degradation threshold with
// it (spec §10: the threshold "must be computed from the parts, not hardcoded").
func railWidth() int {
	w := 0
	for _, e := range railEntries() {
		if n := ansi.StringWidth(e.label); n > w {
			w = n
		}
	}
	return railMarkerCells + w + railTrailCells
}

// helpHeight is the help pane's line count: helpPaneLines whenever the terminal can afford
// them, fewer only when it cannot.
//
// It reads the terminal height and NOTHING else — in particular not the cursor. That
// independence is the fix for D5 and is what TestSelectingTheLongestHelpRowKeepsTheRowCount
// and TestHelpHeightIgnoresTheCursor pin. The -1 reserves the separator, which is drawn only
// alongside a help pane.
func (s *SettingsOverlay) helpHeight() int {
	return clamp(s.height-settingsVChrome-settingsMinBody-1, 0, helpPaneLines)
}

// helpBlockHeight is the help pane plus its separator, or 0 when there is no help pane.
func (s *SettingsOverlay) helpBlockHeight() int {
	if h := s.helpHeight(); h > 0 {
		return h + 1
	}
	return 0
}

// maxPaneLines is the tallest content any rail entry could need: the All settings view, with
// a header per category and a spacer between them. It caps the flat view's pane
// (neededPaneLines), so the box grows with the terminal but never past what that view can
// fill. Pinned against the flat view's real line count by TestMaxPaneLinesMatchesTheFlatView.
func (s *SettingsOverlay) maxPaneLines() int {
	cats := len(allCategories())
	return max(len(railEntries()), len(s.rows)+cats+(cats-1))
}

// neededPaneLines is the tallest pane the SELECTED VIEW can fill: the rail's own length
// for every view but the flat one — no category, the profiles editor's default list, nor
// a handoff note outgrows the thirteen rail entries beside them, and a view that someday
// does will window with its "n more" markers rather than grow the box — and the flat
// view's full grouped length there. Deliberately a function of the rail entry's KIND
// alone: not the row set (an availability gate flipping a row must not resize the box),
// not the filter (the search view rides the selected entry's height), not a cursor. The
// box is therefore height-stable everywhere except crossing the All-settings boundary,
// which is the one deliberate re-centre (#693 traded the always-terminal-tall box, three
// quarters blank on every category, for this).
func (s *SettingsOverlay) neededPaneLines() int {
	if s.selectedEntry().kind == railAll {
		return s.maxPaneLines()
	}
	return len(railEntries())
}

// paneHeight is the shared height of the rail and rows panes: the terminal's budget,
// capped at what the selected view can fill (neededPaneLines) and floored at
// settingsMinBody. At 80x24 it is 13 in every view, which is exactly the thirteen rail
// entries (spec §4's invariant); on taller terminals a category view stays at 13 —
// sizing to content is the point (#693) — while the flat view keeps growing.
func (s *SettingsOverlay) paneHeight() int {
	return clamp(s.height-settingsVChrome-s.helpBlockHeight(), settingsMinBody, s.neededPaneLines())
}

// rowLineParts is one rows-pane line decomposed into the plain-text segments the renderer
// styles independently — the head dim, the value bright, the badge faint, exactly as the
// single-column renderer coloured its two halves.
type rowLineParts struct {
	head  string // selection + modified + space + padded label + gap
	value string
	gap   string // the slack that right-aligns the badge
	badge string // "" when dropped for width
}

// plain returns the whole line as unstyled text.
//
// Tests measure THIS, not Render()'s output. The bordered lipgloss box pads every line to
// the same width, so asserting on a rendered line's width is a tautology that can never
// fail — an over-wide line soft-wraps and grows the box instead of exceeding it.
func (p rowLineParts) plain() string { return p.head + p.value + p.gap + p.badge }

// composeRowLine lays out one rows-pane line to exactly width cells.
//
// Truncation priority is spec §10's, and the order is the whole point: drop the badge
// first, then tail-ellipsize the value, and never touch the label. A half-written label
// makes the row unidentifiable, while a truncated value is recoverable — the help pane
// renders it in full (see contextLine).
//
// sel and modified are single-cell strings (a glyph or a space); passing an empty string
// would collapse the columns and misalign every label below.
func composeRowLine(width, labelW int, sel, modified, label, value, badge string) rowLineParts {
	p := rowLineParts{
		head: sel + modified + " " + padRight(label, labelW) + strings.Repeat(" ", rowLabelGap),
	}
	// The floor where the label rule yields. Below rowMarkerCells + label + rowLabelGap no
	// line both shows the label whole and fits the pane, and an over-wide line is the worse
	// failure: lipgloss soft-wraps it, the box grows a row, and the pinned hint gets clipped
	// off the bottom. The pre-PR-B renderer clipped every body line to the inner width, so
	// this is parity rather than a new regression.
	//
	// This branch is the whole floor. An earlier draft also clamped labelW to
	// width-rowMarkerCells-rowLabelGap before building the head; a mutation removing that
	// clamp changed no test, because padRight only ever pads — so it could never shorten an
	// over-long label, and this truncate already covered every case it appeared to.
	if ansi.StringWidth(p.head) > width {
		p.head = ansi.Truncate(p.head, width, "")
		return p
	}
	avail := width - ansi.StringWidth(p.head)
	if avail < 1 {
		return p
	}
	// Keep the badge if the value, the badge and at least one separating space all fit.
	if badge != "" && ansi.StringWidth(value)+ansi.StringWidth(badge)+1 <= avail {
		p.value, p.badge = value, badge
		p.gap = strings.Repeat(" ", avail-ansi.StringWidth(value)-ansi.StringWidth(badge))
		return p
	}
	if ansi.StringWidth(value) > avail {
		value = ansi.Truncate(value, avail, "…")
	}
	p.value = value
	p.gap = strings.Repeat(" ", avail-ansi.StringWidth(value))
	return p
}

// enumValueCandidates returns an enum's value renderings from widest to plainest, so the
// caller can take the widest that fits — the degradation ladder theme.badgeCandidates uses
// for panel badges.
//
// The rich form is the fix for D8. "‹ desktop ›" alone never revealed that three other modes
// existed, so the only way to discover them was to cycle — and every left/right press
// persists to disk and live-applies, so discovering four options wrote four of them.
func enumValueCandidates(cur string, opts []string) []string {
	compact := "‹ " + cur + " ›"
	if len(opts) < 2 {
		return []string{compact}
	}
	parts := make([]string, 0, len(opts))
	for _, o := range opts {
		if o == cur {
			parts = append(parts, "‹"+o+"›")
			continue
		}
		parts = append(parts, o)
	}
	return []string{strings.Join(parts, " "), compact}
}

// visibleLabelWidth is the label column width for the rows currently on screen: the widest
// label among them.
//
// It is per-entry rather than global on purpose. Padding every pane to the schema's widest
// label (26 cells, "Smart dispatch auto-create") would spend that width in Input, whose
// widest is 21, for no gain. The degradation threshold still budgets for the global worst
// case (minRowsPaneWidth), so a narrow pane is never a surprise.
func (s *SettingsOverlay) visibleLabelWidth() int {
	w := 0
	for _, i := range s.visibleRowIndices() {
		if n := ansi.StringWidth(s.rows[i].label); n > w {
			w = n
		}
	}
	return w
}

// longestRowLabel is the widest label in the whole schema — the worst case the rows pane
// must hold without truncating one, and so the basis of the single-pane threshold.
func (s *SettingsOverlay) longestRowLabel() int {
	w := 0
	for _, r := range s.rows {
		if n := ansi.StringWidth(r.label); n > w {
			w = n
		}
	}
	return w
}

// minRowsPaneWidth is the narrowest rows pane worth rendering: the widest label untruncated —
// spec §10 never truncates a label — plus a legible value column.
func (s *SettingsOverlay) minRowsPaneWidth() int {
	return rowMarkerCells + s.longestRowLabel() + rowLabelGap + rowMinValueCells
}

// twoPaneMinInner is the inner width below which the panel falls back to single-pane
// drill-in.
//
// It is computed from the parts — rail, divider, minimum rows pane — because spec §10
// requires exactly that. A hardcoded threshold would silently stop tracking a renamed
// category or a longer label, offering two panes at a width where the rows pane can no
// longer show one. Pinned by TestThresholdIsDerivedFromTheParts.
func (s *SettingsOverlay) twoPaneMinInner() int {
	return railWidth() + paneDividerCells + s.minRowsPaneWidth()
}

// twoPane reports whether the terminal is wide enough for the rail and rows side by side.
func (s *SettingsOverlay) twoPane() bool { return s.innerWidth() >= s.twoPaneMinInner() }

// rowsPaneWidth is the rows pane's width: the inner width less the rail and divider in
// two-pane mode, or the whole inner width when the rail is a separate screen.
func (s *SettingsOverlay) rowsPaneWidth() int {
	if s.twoPane() {
		return s.innerWidth() - railWidth() - paneDividerCells
	}
	return s.innerWidth()
}

// editorWidth is the inline editor's width: the rows pane less the marker columns, the
// visible label column and its gap.
func (s *SettingsOverlay) editorWidth() int {
	return max(10, s.rowsPaneWidth()-rowMarkerCells-s.visibleLabelWidth()-rowLabelGap)
}

// windowPane windows lines around a cursor index, padding to exactly budget lines and
// replacing the edge lines with "n more" markers when content is hidden.
//
// The cursor is kept one line INSIDE the window whenever the budget allows, so a marker
// overwriting an edge line can never hide the line the user is pointing at. Getting this
// wrong is not cosmetic: with the cursor pinned to the last visible line, the "↓ n more"
// marker lands on top of it and the selected row disappears for every cursor position except
// the very last. Pinned by TestSelectedRowIsAlwaysVisible and
// TestCurrentRailEntryIsAlwaysVisible.
func windowPane(lines []string, cursor, budget int) []string {
	if budget < 1 {
		return nil
	}
	out := make([]string, 0, budget)
	if len(lines) <= budget {
		out = append(out, lines...)
		for len(out) < budget {
			out = append(out, "")
		}
		return out
	}

	// Leave one line of margin below the cursor when there is room for it. settingsMinBody is
	// 3, so the budget never drops below the width this needs.
	margin := 0
	if budget >= 3 {
		margin = 1
	}
	start := 0
	if cursor >= budget-margin {
		start = cursor - budget + 1 + margin
	}
	if maxStart := len(lines) - budget; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}

	out = append(out, lines[start:start+budget]...)
	faint := theme.Current().FaintStyle()
	if start > 0 {
		out[0] = faint.Render(fmt.Sprintf("  ↑ %d more", start))
	}
	if hidden := len(lines) - start - budget; hidden > 0 {
		out[budget-1] = faint.Render(fmt.Sprintf("  ↓ %d more", hidden))
	}
	return out
}

// railMatchCounts is the per-entry match count the rail shows while a filter is active
// (spec §8), or nil when there is none.
//
// It is a read-out of searchResults rather than a second query — a rail that counted matches
// its own way could disagree with the pane about what matched. Only real categories are
// counted: All settings is a view whose count is the total (which the pane already shows),
// and a handoff owns no rows.
func (s *SettingsOverlay) railMatchCounts() []int {
	if !s.searching() {
		return nil
	}
	byCategory := make(map[settingCategory]int, len(allCategories()))
	for _, i := range s.searchResults() {
		byCategory[s.rows[i].category]++
	}
	counts := make([]int, len(railEntries()))
	for i, e := range railEntries() {
		if e.kind == railCategory {
			counts[i] = byCategory[e.category]
		}
	}
	return counts
}

// railLines renders the left rail, padded to the shared pane height so the divider column
// runs its full length.
//
// At the 80x24 floor all thirteen entries fit unscrolled — spec §4's invariant, pinned by
// TestRailFitsUnscrolledAtTheFloor. Below 24 rows they cannot, so the rail windows around its
// cursor exactly as the rows pane does. Spec §4 anticipates this ("the rail windows like
// today's body does"); what it must never do is silently drop the entries past the budget,
// leaving the current entry off-screen with no indication of where you are.
func (s *SettingsOverlay) railLines() []string {
	t := theme.Current()
	labelW := railWidth() - railMarkerCells - railTrailCells
	entries := railEntries()

	counts := s.railMatchCounts()
	rendered := make([]string, 0, len(entries))
	for i, e := range entries {
		mark := " "
		if i == s.railCursor {
			mark = t.Glyphs.SelectionMark
		}
		trail := " "
		switch {
		case counts != nil && counts[i] > 0:
			// One digit always: the largest category has six rows
			// (TestEveryCategoryMatchCountFitsTheRailsOneCell), so the count fits the cell the
			// handoff arrow otherwise occupies and railWidth() does not move when / is pressed.
			trail = strconv.Itoa(counts[i])
		case e.kind == railHandoff:
			trail = t.Glyphs.Handoff
		}
		line := mark + " " + padRight(e.label, labelW) + " " + trail

		style := t.DimStyle()
		switch {
		case i == s.railCursor && s.focus == focusRail:
			// searching() cannot hold here: startSearch always sets focusRows, and every exit
			// from a filter clears it first.
			style = t.AccentStyle()
		case i == s.railCursor:
			// Current but not taking keys — including throughout a search, where the marker
			// tracks the highlighted result's category rather than a cursor the user can move.
			style = t.FgStyle()
		case s.searching(), e.kind == railHandoff:
			// The rail is inert under a filter (spec §8: "the rail dims"), and a handoff entry
			// is dimmer than an ordinary one at all times.
			style = t.FaintStyle()
		}
		rendered = append(rendered, style.Render(line))
	}
	return windowPane(rendered, s.railCursor, s.paneHeight())
}

// paneLine is one rows-pane line together with the record it belongs to, so the windowing can
// locate the cursor without re-deriving it from the text.
//
// rowIdx indexes whatever list the pane is over — s.rows for a category or the flat view,
// cfg.Profiles for the Profiles editor — and is -1 for a category header, a spacer, an overflow
// marker, or wrapped prose. See paneCursor for why the distinction matters.
type paneLine struct {
	text   string
	rowIdx int
}

// wrappedPaneLines wraps prose to the pane width as styled, rowIdx -1 pane lines — the shape
// every non-row pane content uses: a handoff note, a no-match line, an empty editor.
func wrappedPaneLines(text string, width int, style lipgloss.Style) []paneLine {
	var lines []paneLine
	for _, l := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
		lines = append(lines, paneLine{text: style.Render(l), rowIdx: -1})
	}
	return lines
}

// paneCursor is the index rowsPaneLines matches paneLine.rowIdx against: the row cursor for a
// settingRow pane, the profile cursor for the Profiles editor.
//
// The two index DIFFERENT lists. Before the editor there was one index space and rowsPaneLines
// could read s.cursor directly; a profile line carries a profile index, and s.cursor is a small
// int too, so matching against the wrong one silently windows around an unrelated line and the
// selected profile scrolls off screen.
func (s *SettingsOverlay) paneCursor() int {
	if s.profilesPaneActive() {
		return s.profileCursor
	}
	return s.cursor
}

// rowsPaneContent builds every line the current rail entry could show, unwindowed.
//
// The All settings view adds a header per category and a blank spacer between them — the
// shape of the pre-redesign list, which is what makes it usable for auditing a config. A
// single category shows its rows bare: the rail entry already names it, so a header would
// only repeat itself.
func (s *SettingsOverlay) rowsPaneContent(width int) []paneLine {
	if s.searching() {
		return s.searchPaneContent(width)
	}
	e := s.selectedEntry()
	if e.kind == railHandoff {
		return s.handoffPaneContent(e, width)
	}
	if e.kind == railProfiles {
		return s.profilesPaneContent(width)
	}
	t := theme.Current()
	start, end := s.rowRange(e)
	labelW := s.visibleLabelWidth()

	var lines []paneLine
	// Single-pane drill-in hides the rail, so the pane has to name the category itself —
	// otherwise the user is looking at an unlabelled list of rows, which is D2 (no
	// orientation) reintroduced at narrow widths. In two-pane mode the rail already names it
	// and a header would only repeat it.
	if !s.twoPane() && e.kind == railCategory {
		lines = append(lines, paneLine{
			text:   t.DimStyle().Bold(true).Render(e.label),
			rowIdx: -1,
		})
	}
	// The explicit `first` flag is required because catSessions is 0: an uninitialized
	// lastCategory would equal the first row's category and swallow its header.
	first := true
	lastCategory := allCategories()[0]
	for i := start; i < end; i++ {
		if e.kind == railAll && (first || s.rows[i].category != lastCategory) {
			if !first {
				lines = append(lines, paneLine{text: "", rowIdx: -1})
			}
			lines = append(lines, paneLine{
				text:   t.DimStyle().Bold(true).Render(s.rows[i].category.label()),
				rowIdx: -1,
			})
			lastCategory = s.rows[i].category
		}
		first = false
		lines = append(lines, paneLine{text: s.renderRowLine(i, width, labelW), rowIdx: i})
	}
	return lines
}

// searchPaneContent is the rows pane under a filter: a flat list of every match, in rank
// order, each carrying its category (spec §8). It ignores the rail entry entirely — a filter
// that only searched the current category would be a category filter, not a search.
func (s *SettingsOverlay) searchPaneContent(width int) []paneLine {
	results := s.searchResults()
	if len(results) == 0 {
		// An empty pane reads as a broken panel. Naming the query and the way out of it is the
		// same obligation a handoff entry's note carries.
		text := "No setting matches " + strconv.Quote(s.search.filter) + "."
		return wrappedPaneLines(text, width, theme.Current().FaintStyle())
	}
	labelW := s.visibleLabelWidth()
	lines := make([]paneLine, 0, len(results))
	for _, i := range results {
		lines = append(lines, paneLine{text: s.renderRowLine(i, width, labelW), rowIdx: i})
	}
	return lines
}

// handoffPaneContent is the rows pane for a handoff entry: its note, wrapped. Naming the
// surface that does own the config is the honest thing to render for config this panel
// cannot edit — an empty pane would read as a bug. Accounts is the only entry that needs
// it; Profiles has an editor of its own (profilesPaneContent).
func (s *SettingsOverlay) handoffPaneContent(e railEntry, width int) []paneLine {
	return wrappedPaneLines(e.note, width, theme.Current().DimStyle())
}

// rowsPaneLines renders the highlighted entry's rows, windowed around the cursor and padded
// to the shared pane height. When the entry outgrows the pane — All settings always does — an
// edge line becomes an "n more" marker, so the panel says that more exists rather than just
// ending (D2: no orientation).
func (s *SettingsOverlay) rowsPaneLines() []string {
	content := s.rowsPaneContent(s.rowsPaneWidth())
	lines := make([]string, len(content))
	cursorLine := 0
	for i, l := range content {
		lines[i] = l.text
		if l.rowIdx == s.paneCursor() {
			cursorLine = i
		}
	}
	return windowPane(lines, cursorLine, s.paneHeight())
}

// renderRowLine composes and styles one row's line. Task 7 fills in the modified marker, the
// badge and the inert dimming; here the marker columns are reserved but empty, so the layout
// is final before the signals land on it.
func (s *SettingsOverlay) renderRowLine(i, width, labelW int) string {
	t := theme.Current()
	row := s.rows[i]
	selected := i == s.cursor

	// Both panes always show their cursor — hiding the rows pane's while the rail has focus
	// would leave the user unable to see where → would land. Only the STYLE differs, so
	// exactly one accent-bright marker is on screen at a time.
	sel := " "
	rowStyle := t.FgStyle()
	if selected {
		sel = t.Glyphs.SelectionMark
		if s.focus == focusRows {
			rowStyle = t.AccentStyle()
		}
	}

	modified := " "
	if s.isModified(i) {
		modified = t.Glyphs.Modified
	}

	if s.editing && selected {
		// The live text input carries its own cursor styling, so it is appended rather than
		// composed — an editor is not a value cell and must not be truncated.
		//
		// The head must spend the SAME three marker cells composeRowLine does (selection,
		// modified, space), or every label jumps sideways the instant Enter opens the editor.
		head := sel + modified + " " + padRight(row.label, labelW) + strings.Repeat(" ", rowLabelGap)
		return t.AccentStyle().Render(head) + s.input.View()
	}

	inert := s.inertReason(i)
	value, badge := s.rowValueAndBadge(i, width, labelW, inert)
	p := composeRowLine(width, labelW, sel, modified, row.label, value, badge)
	switch {
	case selected:
		// Accent wins over dimming: the row under the cursor must stay legible. The chip still
		// says the row is inert, and the help pane spells it out.
		return rowStyle.Render(p.plain())
	case inert != "":
		// Dimmed, not hidden. Inert means "changing this has no effect right now", never "you
		// may not touch this" — a user may configure ahead of enabling the parent (spec §5).
		return t.FaintStyle().Render(p.plain())
	default:
		return t.DimStyle().Render(p.head) +
			t.FgStyle().Render(p.value+p.gap) +
			t.FaintStyle().Render(p.badge)
	}
}

// rowValueAndBadge sizes a row's value cell and picks its right-aligned badge. There are
// three claims on that column, in priority order:
//
//   - an inert reason chip, which degrades to one word but never drops: a dimmed row with no
//     marker reads as broken, and the help pane only describes the SELECTED row;
//   - a search result's category, which degrades by truncation for the same reason — a flat
//     list drawn from ten categories needs it — and is why the timing badge yields while a
//     filter is active;
//   - the apply timing, which is reference information and is dropped outright (spec §10).
//
// For the first two the value is sized against the SHORTEST form the badge can take, never
// the widest: reserving against the widest lets a rich enum value find no room beside a long
// chip, fall back to the whole slack, and evict the very chip the ladder was supposed to
// guarantee. fitValue then covers the kinds valueCell's own reservation does not reach.
func (s *SettingsOverlay) rowValueAndBadge(i, width, labelW int, inert string) (value, badge string) {
	avail := valueAvail(width, labelW)
	switch {
	case inert != "":
		candidates := inertBadgeCandidates(inert)
		shortest := candidates[len(candidates)-1]
		value = fitValue(s.valueCell(i, width, labelW, shortest), avail, ansi.StringWidth(shortest))
		return value, fitBadge(candidates, width, labelW, value)
	case s.searching():
		// The stand-in's WIDTH is the reservation; valueCell reads nothing else from it.
		value = fitValue(
			s.valueCell(i, width, labelW, strings.Repeat("x", searchBadgeMinCells)),
			avail, searchBadgeMinCells)
		return value, searchBadge(s.rows[i].category.label(), badgeAvail(width, labelW, value))
	default:
		// No fitValue here, deliberately: spec §10 drops a timing badge before touching the
		// value, and a timing badge is reference information that loses nothing by going.
		candidates := []string{s.rows[i].timing.badge()}
		value = s.valueCell(i, width, labelW, candidates[0])
		return value, fitBadge(candidates, width, labelW, value)
	}
}

// valueCell formats a row's value by kind.
//
// For an enum it takes the widest rendering that fits — and `badge` is what the caller intends
// to put in the right-aligned column, so the ladder can step down to the compact form rather
// than squeezing the badge out. That ordering matters: the badge carries the inert reason, and
// a rich value that evicted it would dim a row with no explanation. See Task 7, where a real
// badge is passed; here it is always "".
func (s *SettingsOverlay) valueCell(i, width, labelW int, badge string) string {
	row := s.rows[i]
	v := row.get(s.cfg)
	switch row.kind {
	case kindBool:
		if v == "on" {
			return "[x] on"
		}
		return "[ ] off"
	case kindEnum:
		avail := width - rowMarkerCells - labelW - rowLabelGap
		// Try to fit beside the badge first, then to fit the pane at all. The inline
		// alternatives are an enrichment, so they are the first thing to go; the compact
		// "‹ v ›" loses nothing about the current value (it is the pre-PR-B rendering).
		budgets := []int{avail}
		if badge != "" {
			budgets = []int{avail - ansi.StringWidth(badge) - 1, avail}
		}
		for _, budget := range budgets {
			for _, c := range enumValueCandidates(v, row.options(s.cfg)) {
				if ansi.StringWidth(c) <= budget {
					return c
				}
			}
		}
		return "‹ " + v + " ›"
	default:
		// kindInt, kindText and kindReadOnly all show the value bare; a read-only row gets no
		// editor affordance.
		return v
	}
}

// badgeAvail is the room left for the right-aligned badge column once the head and the value
// have taken theirs, including the one space that separates them. One definition, so fitBadge
// and searchBadge cannot disagree about how much room there is.
func badgeAvail(width, labelW int, value string) int {
	return width - rowMarkerCells - labelW - rowLabelGap - ansi.StringWidth(value) - 1
}

// valueAvail is the room the value column has before any badge is considered.
func valueAvail(width, labelW int) int { return width - rowMarkerCells - labelW - rowLabelGap }

// searchBadgeMinCells is the floor a category chip degrades to before it is dropped: three
// cells of name plus the ellipsis. Below that the chip carries no information a user could
// act on, and the column is better spent on the value.
const searchBadgeMinCells = 4

// searchBadge is the category chip on a search result row.
//
// Unlike a timing badge — reference information, dropped outright by spec §10 — the category
// is what tells two similarly-named results apart in a list flattened across ten categories,
// so it degrades the way an inert chip does: "Worktr…" still does the job a blank column
// cannot. fitValue is what makes that promise keepable; see its comment.
func searchBadge(category string, avail int) string {
	if avail < searchBadgeMinCells {
		return ""
	}
	if ansi.StringWidth(category) <= avail {
		return category
	}
	return ansi.Truncate(category, avail, "…")
}

// squeezedValueCells is the narrowest a value may be squeezed to keep a chip beside it: seven
// cells and the ellipsis. Below it the value says nothing at all and the chip is dropped
// instead.
//
// It is deliberately smaller than rowMinValueCells, which is a different number for a
// different job: that one sizes the degradation *threshold* — the value column the panel
// promises before it gives up on two panes — while this is the last-resort squeeze once the
// pane is already that narrow. Measured, the tightest two-pane geometry (width 73, the flat
// 26-cell label column) leaves a 14-cell value column, so reserving 4 for the chip leaves 9 —
// above this floor across the whole two-pane range. TestSearchRowLinesFillThePaneExactlyAndKeepTheirChip
// is what fails if that geometry moves.
const squeezedValueCells = 8

// fitValue shortens a value so a badge of `reserve` cells survives beside it — unless doing
// so would squeeze the value below squeezedValueCells, at which point the value is the more
// useful of the two and the badge is dropped instead.
//
// It exists because valueCell's own reservation bites ONLY on kindEnum: kindBool, kindInt,
// kindText and kindReadOnly return their value bare and ignore the badge argument entirely,
// so a long path or a long command evicts the chip beside it. Measured on the real schema,
// that drops "Advanced" from the Config file row at EVERY terminal width, and the category
// from Carry files up to 90 columns.
//
// That eviction is correct for a timing badge — spec §10 drops it first — and wrong for the
// two badges that must not vanish: an inert reason chip (a dimmed row with no marker reads as
// broken) and a search result's category. So only those two callers reserve. Nothing is lost
// either way: contextLine renders a squeezed value in full (spec §10).
func fitValue(v string, avail, reserve int) string {
	budget := avail - reserve - 1
	if budget < squeezedValueCells || ansi.StringWidth(v) <= budget {
		return v
	}
	return ansi.Truncate(v, budget, "…")
}

// fitBadge returns the widest candidate that fits beside the value, or "" when none does.
// composeRowLine would drop an over-wide badge silently; this is what lets a caller offer a
// shorter rendering instead of losing the column.
func fitBadge(candidates []string, width, labelW int, value string) string {
	avail := badgeAvail(width, labelW, value)
	for _, c := range candidates {
		if ansi.StringWidth(c) <= avail {
			return c
		}
	}
	return ""
}

// valueWasTruncated reports whether the selected row's value cell had to be shortened to fit
// its pane, which is what obliges the help pane to show it in full (spec §10).
// It asks rowValueAndBadge for the value the renderer actually draws, not a second
// composition of its own: the value can be shortened twice over — once by fitValue, to keep a
// chip beside it, and again by composeRowLine — and a helper that re-derived only the second
// would report a squeezed value as whole and quietly drop the obligation.
func (s *SettingsOverlay) valueWasTruncated() bool {
	labelW := s.visibleLabelWidth()
	width := s.rowsPaneWidth()
	value, badge := s.rowValueAndBadge(s.cursor, width, labelW, s.inertReason(s.cursor))
	if strings.HasSuffix(value, "…") {
		return true
	}
	p := composeRowLine(width, labelW, " ", " ", s.selectedRow().label, value, badge)
	return strings.HasSuffix(p.value, "…")
}

// paneDivider is the vertical rule between the rail and the rows pane, taken from the theme's
// own border rune so it matches the box's rounded/square style.
func paneDivider() string {
	left := theme.Current().Borders.Style.Left
	if lipgloss.Width(left) != 1 {
		// A border style with no single-cell vertical would break every column below it.
		return "|"
	}
	return left
}

// bodyLines is the pane region: the rail beside the rows on a wide terminal, or one of them
// on a narrow one.
//
// Below twoPaneMinInner the panel becomes single-pane drill-in (spec §10) — the rail, then
// Enter opens a category's rows and Esc returns. It is the same mental model and the same
// focus state; only the drawing changes, which is why the navigation tests do not care which
// layout is active.
//
// The fallback is load-bearing, not cosmetic: rowsPaneWidth() returns the WHOLE inner width
// when !twoPane(), so joining it beside the rail anyway would emit lines of
// railWidth+divider+inner cells — 56 in a 34-cell box at 40 columns — which lipgloss
// soft-wraps, doubling every body line and growing the box past the terminal.
func (s *SettingsOverlay) bodyLines() []string {
	if !s.twoPane() {
		if s.focus == focusRows {
			return s.rowsPaneLines()
		}
		return s.railLines()
	}

	rail := s.railLines()
	rows := s.rowsPaneLines()
	div := paneDivider()
	out := make([]string, 0, len(rail))
	for i := range rail {
		// Pad each side explicitly rather than using JoinHorizontal: the panes are already
		// exactly railWidth() and rowsPaneWidth() cells, and JoinHorizontal would pad to its
		// own idea of the widest line — which for a styled line is not the same number.
		out = append(out, padRight(rail[i], railWidth())+" "+div+" "+rows[i])
	}
	return out
}

// helpLines renders the fixed-height help pane: the selected row's summary with its caution
// and timing note, wrapped, then one context line — capped and padded to exactly helpHeight()
// lines.
//
// The prose is settingRow.footerText() unchanged from PR A, which appends the caution and the
// timing note after the summary. The timing therefore appears twice on a wide pane — once as
// the row's badge, once here. That is deliberate: the badge is a scannable column for
// comparing rows, the prose is the sentence for the selected one, and because spec §10 drops
// the badge first on a narrow pane, the prose is its fallback rather than pure duplication.
// Reusing footerText also keeps TestEveryCautionReachesTheFooter and TestFooterTextFitsTwoLines
// live with no schema change.
func (s *SettingsOverlay) helpLines() []string {
	h := s.helpHeight()
	if h == 0 {
		return nil
	}
	t := theme.Current()
	inner := s.innerWidth()

	style := t.DimStyle()
	var prose string
	switch {
	case s.selectedEntry().kind == railHandoff:
		// A handoff entry's note is already the whole content of the rows pane, so echoing it
		// here would print the same sentence twice in one frame. The pane stays blank.
	case s.profilesPaneActive():
		// s.cursor still points at whatever settingRow it last sat on, so footerText() would
		// describe an unrelated setting under a list of profiles.
		var danger bool
		prose, danger = s.profilesHelp()
		if danger {
			style = t.DangerStyle()
		}
	default:
		prose = s.selectedRow().footerText()
	}
	if s.searching() && len(s.searchResults()) == 0 {
		// With nothing matching, describing s.cursor's row would describe a row the list is not
		// showing. Say what happened and name the way out instead.
		prose = "Nothing matches — backspace to widen the search, esc to clear it."
	}
	if s.lastErr != "" {
		prose, style = s.lastErr, t.DangerStyle()
	}

	// The context line carries the position readout, so it must never be the thing that gets
	// evicted when the prose is long: capping the prose at h-1 is what makes contextLine's
	// "always" true of the rendered panel rather than only of the function.
	ctx := ""
	// Written as (!A || B) rather than !(A && B): staticcheck's QF1001 flags the negated
	// conjunction, and .golangci.yml excludes only QF1003 — so the negated form fails lint
	// while build, vet, fmt and the whole suite stay green.
	if s.lastErr == "" && s.selectedEntry().kind != railHandoff &&
		(!s.searching() || len(s.searchResults()) > 0) {
		ctx = s.contextLine(inner)
	}
	proseBudget := h
	if ctx != "" {
		proseBudget = h - 1
	}

	var lines []string
	if prose != "" {
		lines = strings.Split(ansi.Wrap(prose, inner, ""), "\n")
	}
	if len(lines) > proseBudget {
		lines = lines[:max(0, proseBudget)]
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			// Appending the ellipsis to an already-full line would push it past inner, and the
			// lipgloss box would soft-wrap it, add a row, and clip the pinned hint.
			if ansi.StringWidth(last) > inner-1 {
				last = ansi.Truncate(last, inner-1, "")
			}
			lines[len(lines)-1] = last + "…"
		}
	}
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	if ctx != "" {
		// Pad first, so the context line — and with it the position readout — is always the
		// pane's LAST line rather than floating up under a short summary.
		for len(lines) < h-1 {
			lines = append(lines, "")
		}
		lines = append(lines, ctx)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// contextLine is the help pane's last line: whatever the row line could not say. Task 7 fills
// in the inert reason and the enum gloss; here it carries the full value when the row line
// truncated it, plus the position readout.
//
// The position counter always rides this line, right-aligned, so the pane says where you are
// in the category even when there is nothing else to add (D2: no orientation, no position).
// Content is truncated to make room for it, never the other way round — the counter is five
// cells and the content is recoverable from `?`.
func (s *SettingsOverlay) contextLine(width int) string {
	if s.profilesPaneActive() {
		return s.profilesContextLine(width)
	}
	var pos string
	if s.searching() {
		results := s.searchResults()
		if len(results) == 0 {
			return ""
		}
		// Under a filter the counter counts RESULTS: "3/5" must mean the third of five hits, not
		// the third row of whatever category the rail happens to mark.
		pos = fmt.Sprintf("%d/%d", s.search.cursor+1, len(results))
	} else {
		start, end := s.rowRange(s.selectedEntry())
		if end <= start {
			return ""
		}
		pos = fmt.Sprintf("%d/%d", s.cursor-start+1, end-start)
	}

	var body string
	switch chip := s.inertReason(s.cursor); {
	case s.valueWasTruncated():
		// Spec §10: a truncated value must be shown in full here, or the truncation loses
		// information rather than deferring it.
		body = s.selectedRow().get(s.cfg)
	case chip != "":
		// The chip is three words in a column and is easy to misread as a prohibition; this is
		// the sentence that says what it actually means.
		body = "No effect right now — " + chip + "."
	default:
		// The current option's gloss, which is what makes cycling an enum teach rather than
		// guess (D8) — and failing that, the first sentence of detail.
		//
		// The detail fallback is what makes spec §3's mockup literal: its second help line for
		// Notifications ("The selected, attached and muted sessions stay silent.") is that
		// row's detail, not its summary. Without it a row whose long-form help is only detail
		// shows one prose line and a blank, and the prose PR A moved into detail stays
		// invisible until `?`.
		row := s.selectedRow()
		body = row.gloss[row.get(s.cfg)]
		if body == "" {
			body = firstSentence(row.detail)
		}
	}

	if s.searching() {
		// The badge column carries the category only when the row is live and the pane is wide
		// enough for it — and below the two-pane threshold there is no rail and no match counts
		// at all. Naming it here means the highlighted result ALWAYS says where it lives, which
		// is what keeps a flat cross-category list navigable.
		category := s.selectedRow().category.label()
		if body == "" {
			body = category
		} else {
			body = category + " · " + body
		}
	}

	return rightAligned(body, pos, width)
}

// rightAligned lays a body string and a right-aligned position readout into exactly width
// cells, truncating the body to make room. The counter is five cells and the body is
// recoverable from `?`, so content yields to it and never the other way round.
func rightAligned(body, pos string, width int) string {
	budget := width - ansi.StringWidth(pos) - 1
	if budget < 1 {
		return theme.Current().FaintStyle().Render(pos)
	}
	if ansi.StringWidth(body) > budget {
		body = ansi.Truncate(body, budget, "…")
	}
	gap := width - ansi.StringWidth(body) - ansi.StringWidth(pos)
	return theme.Current().FaintStyle().Render(body + strings.Repeat(" ", gap) + pos)
}

// separatorLine is the horizontal rule between the panes and the help pane, with a tee where
// the vertical divider meets it.
//
// It sits inside the box padding rather than spliced into the border: lipgloss v1 has no API
// for a mid-border row (theme.PanelWithBadges hand-composes its top border for exactly this
// reason), and a rule two cells short of the border reads the same. It is omitted entirely
// when the terminal is too short for a help pane, since there is then nothing to separate.
func (s *SettingsOverlay) separatorLine() string {
	if s.helpHeight() == 0 {
		return ""
	}
	b := theme.Current().Borders.Style
	h := b.Bottom
	if lipgloss.Width(h) != 1 {
		h = "─"
	}
	inner := s.innerWidth()
	rule := strings.Repeat(h, inner)
	if s.twoPane() {
		tee := b.MiddleBottom
		if lipgloss.Width(tee) != 1 {
			tee = h
		}
		at := railWidth() + 1 // the divider sits one cell into the three-cell gap
		rule = strings.Repeat(h, at) + tee + strings.Repeat(h, inner-at-1)
	}
	return theme.Current().FaintStyle().Render(rule)
}

// hintLine is the key hints for the current focus, at the widest wording that fits.
//
// The hints differ per focus rather than being one static string, because Esc closes from the
// rail but only backs out of the rows pane — advertising the wrong one is how a layered Esc
// becomes surprising instead of discoverable (spec §15). The ladder guarantees that "esc back"
// / "esc close" survives at any width: it is the hint a user stuck in the panel needs most.
//
// Both `/` and `r` are advertised now that PR C makes them live; PR B deliberately shipped
// neither, because a hint for a key that does nothing is worse than no hint at all.
func (s *SettingsOverlay) hintLine() string {
	var ladder []string
	switch {
	case s.editing:
		ladder = []string{"↵ save · esc cancel", "esc cancel"}
	case s.profileForm != nil:
		ladder = []string{"⇥ field · ↵ save · esc cancel", "↵ save · esc cancel", "esc cancel"}
	case s.helpOpen:
		ladder = []string{"↑/↓ scroll · ? or esc back", "esc back"}
	case s.searching():
		// The filter's own esc level, the third of three (spec §8: clear, back, close). "type to
		// filter" is the affordance, since nothing else on screen says the runes are going
		// somewhere.
		ladder = []string{
			"type to filter · ↑/↓ move · ↵ edit · ? more · esc clear",
			"↑/↓ move · ↵ edit · ? more · esc clear",
			"↑/↓ · ↵ edit · esc clear",
			"esc clear",
		}
	case s.profilesPaneActive() && s.focus == focusRows:
		ladder = s.profilesHintLadder()
	case s.focus == focusRows:
		ladder = []string{
			"↑/↓ move · ←/→ change · ↵ edit · r reset · / search · ? more · ⇥ pane · esc back",
			"↑/↓ · ←/→ · ↵ edit · r reset · / search · ? more · esc back",
			"↵ edit · r reset · / search · esc back",
			"/ search · esc back",
			"esc back",
		}
	default:
		ladder = railHintLadder(s.selectedEntry())
	}
	inner := s.innerWidth()
	hint := ladder[len(ladder)-1]
	for _, h := range ladder {
		if ansi.StringWidth(h) <= inner {
			hint = h
			break
		}
	}
	return ansi.Truncate(theme.Current().OverlayHintStyle().Render(hint), inner, "…")
}

// railHintLadder is the rail's key hints for one entry, widest wording first.
//
// The forward key does three different things on the rail — focus the rows, open another
// overlay, or nothing at all — so the hint names the one that applies. A static "→ rows" on
// an entry with no rows is the same class of lie a static esc hint would be (spec §15).
//
// "⇥ pane" is held to that same standard, which is why it is not a constant rung. handleRailKey
// routes tab together with right and enter, so tab is not a pane toggle on the rail at all: it
// is the forward key. On an entry that owns rows that amounts to a pane swap and the hint is
// honest; on a handoff it hands off or does nothing, and focus never enters the empty pane
// either way. So the two hints stand or fall together, and both leave a handoff entry's ladder
// (TestRailHintNeverPromisesAPaneSwapWithoutRows).
func railHintLadder(e railEntry) []string {
	if e.kind == railProfiles {
		// The editor owns a pane but no rows, so the forward key names the editor rather than
		// "rows" — and "⇥ pane" is honest here, because tab really does focus it.
		return []string{
			"↑/↓ category · → profiles · / search · ⇥ pane · esc close",
			"↑/↓ · → profiles · / search · esc close",
			"/ search · esc close",
			"esc close",
		}
	}
	if e.kind == railHandoff {
		// Every handoff is wired — TestEveryHandoffEntryNamesItsSurface is what keeps that true,
		// and an unwired one would render a ladder naming no forward key at all, the lie this
		// function exists to prevent.
		forward := handoffHint(e.opens)
		return []string{
			"↑/↓ category · " + forward + " · / search · esc close",
			"↑/↓ · " + forward + " · / search · esc close",
			"/ search · esc close",
			"esc close",
		}
	}
	return []string{
		"↑/↓ category · → rows · / search · ⇥ pane · esc close",
		"↑/↓ · → rows · / search · esc close",
		"/ search · esc close",
		"esc close",
	}
}

// handoffHint names the forward key's effect for an entry that hands off, in the rail's hint
// voice: the key the note tells the user to press, then the surface it opens.
//
// A handoff with no wording here would silently render a ladder that names no forward key at
// all — the same lie railHintLadder exists to prevent — so
// TestEveryWiredHandoffNamesItsForwardKey fails rather than letting a new handoff arrive
// without one.
func handoffHint(h SettingsHandoff) string {
	if h == HandoffAccounts {
		return "↵ accounts"
	}
	return ""
}

// inertReasons is the right-aligned chip for each row whose change currently has no effect
// (spec §5's table). Reason strings live here rather than in the schema because they are
// rendered text: PR A deliberately gave settingRow only the predicate, leaving the wording to
// the renderer that positions it.
//
// Every predicated row must appear here — a row dimmed with no explanation is worse than a row
// not dimmed at all, because the user can see something is off and has nothing to act on.
// TestEveryInertPredicateHasAReason enforces both directions.
var inertReasons = map[string]string{
	"notifications_finished":  "needs Notifications",
	"notify_when_focused":     "needs Notifications",
	"notify_command":          "needs desktop mode",
	"notify_throttle_seconds": "needs Notifications",
	"fast_forward_local_base": "needs Update base on create",
	"daemon_poll_interval":    "needs Auto-yes",
	"agent_oom_margin":        "Linux only",
	// group_mode is here even though it declares no activeWhen: its gate is session-derived and
	// injected by home, not expressible as a config predicate. Keeping the string in this map
	// rather than inline in inertReason is what keeps every reason chip in one place, and it is
	// the one key TestEveryInertPredicateHasAReason must exempt from its "no stale entries"
	// direction.
	"group_mode": "nothing to cluster",
}

// inertReasonsWithoutPredicate names the rows whose reason chip is driven by something other
// than settingRow.activeWhen, so the completeness guard can tell a deliberate exception from a
// stale entry.
var inertReasonsWithoutPredicate = map[string]string{
	"group_mode": "gate is session-derived; home injects it via SetAccountClusteringVisible",
}

// inertBadgeShort is the one-word fallback an inert row's chip degrades to when the full reason
// cannot fit the pane.
//
// It exists because dropping the chip entirely is the one degradation this column must not
// make: a dimmed row with no marker at all reads as broken, and the help pane only describes
// the SELECTED row, so an unexplained dim two rows down has nothing to explain it. Degrading to
// a word keeps the signal; the full reason is one keypress away. Timing badges, by contrast, are
// reference information and are dropped outright, which is spec §10's stated priority.
const inertBadgeShort = "inactive"

// inertBadgeCandidates returns an inert row's chip renderings widest first, for the same
// take-the-widest-that-fits ladder the enum value and the hint line use.
func inertBadgeCandidates(reason string) []string {
	if reason == "" {
		return nil
	}
	return []string{reason, inertBadgeShort}
}

// inertReason returns row i's reason chip when changing it currently has no effect, or "" when
// the row is live.
func (s *SettingsOverlay) inertReason(i int) string {
	row := s.rows[i]
	if row.key == "group_mode" {
		// The one row whose gate is not a config predicate: ui.List clusters only when
		// accountGrouped AND more than one distinct cluster key is present across the LIVE
		// sessions, and that key is a session's rotation pool when it has one. It is inert when
		// clustering is switched ON but the list has nothing to cluster — "off" is not inert,
		// it is off, and a chip there would be noise. clusteringVisible is nil until home
		// injects the list's own answer.
		if groupModeOnOff(s.cfg) == "on" && s.clusteringVisible != nil && !*s.clusteringVisible {
			return inertReasons["group_mode"]
		}
		return ""
	}
	if row.activeWhen == nil || row.activeWhen(s.cfg) {
		return ""
	}
	return inertReasons[row.key]
}

// firstSentence returns s up to and including its first sentence-ending period, or "" when
// there is none. It surfaces one line of a row's detail in the help pane without the pane
// trying to render a paragraph it has no room for.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	if strings.HasSuffix(s, ".") {
		return s
	}
	return ""
}

// expandedHelpContent assembles everything the panel knows about row i, in reading order: what
// it does, the caution, when a change takes effect, the long-form detail, one line per enum
// option from gloss, and the current value in full.
//
// This is the surface settingRow.detail and settingRow.gloss were written for. PR A stored both
// and rendered neither, so up to 443 characters per row existed only in the source until now —
// see TestEveryDetailAndGlossReachExpandedHelp.
func (s *SettingsOverlay) expandedHelpContent(i int) string {
	row := s.rows[i]
	var b strings.Builder

	b.WriteString(row.label + "\n\n" + row.summary + "\n")
	if row.caution != "" {
		b.WriteString("\nCaution: " + row.caution + ".\n")
	}
	if note := row.timing.footerNote(); note != "" {
		b.WriteString("\nA change " + note + ".\n")
	} else {
		// timingLive has no footer note by design — saying "live" on 25 of 38 rows would be
		// noise in the help pane. Here there is room to say it.
		b.WriteString("\nA change applies immediately.\n")
	}
	if chip := s.inertReason(i); chip != "" {
		b.WriteString("\nNo effect right now — " + chip + ". You can still change it now and it " +
			"will apply once the parent setting is on.\n")
	}
	if row.detail != "" {
		b.WriteString("\n" + row.detail + "\n")
	}
	if row.kind == kindEnum {
		if opts := row.options(s.cfg); len(opts) > 0 {
			b.WriteString("\nOptions:\n")
			for _, o := range opts {
				if g := row.gloss[o]; g != "" {
					b.WriteString("  " + o + " — " + g + "\n")
					continue
				}
				b.WriteString("  " + o + "\n")
			}
		}
	}
	b.WriteString("\nCurrent value: " + row.get(s.cfg) + "\n")
	return b.String()
}

// expandedHelpHeight is the number of lines `?` may fill: the panes plus the help block, so the
// box's height is identical open or closed. A centered overlay that resized on `?` would jump
// under the user's cursor.
func (s *SettingsOverlay) expandedHelpHeight() int {
	return s.paneHeight() + s.helpBlockHeight()
}

// expandedHelpWrapped is the content wrapped to the inner width, unwindowed.
func (s *SettingsOverlay) expandedHelpWrapped() []string {
	return strings.Split(ansi.Wrap(s.expandedHelpContent(s.cursor), s.innerWidth(), ""), "\n")
}

// maxHelpScroll is the furthest the expanded help can scroll, so both the key handler and the
// renderer clamp to the same number.
func (s *SettingsOverlay) maxHelpScroll() int {
	return max(0, len(s.expandedHelpWrapped())-s.expandedHelpHeight())
}

// expandedHelpLines renders the `?` view: the wrapped content, scrolled and padded to exactly
// expandedHelpHeight() lines, with a position readout when it overflows.
func (s *SettingsOverlay) expandedHelpLines() []string {
	t := theme.Current()
	lines := s.expandedHelpWrapped()
	budget := s.expandedHelpHeight()

	s.helpScroll = clamp(s.helpScroll, 0, s.maxHelpScroll())
	end := min(len(lines), s.helpScroll+budget)

	out := make([]string, 0, budget)
	for _, l := range lines[s.helpScroll:end] {
		out = append(out, t.DimStyle().Render(ansi.Truncate(l, s.innerWidth(), "")))
	}
	for len(out) < budget {
		out = append(out, "")
	}
	if s.maxHelpScroll() > 0 {
		// Overwrite the last line rather than adding one, so the height holds.
		out[budget-1] = t.FaintStyle().Render(fmt.Sprintf("  %d/%d", end, len(lines)))
	}
	return out
}
