package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/lipgloss"
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
	// rowMinValueCells is the narrowest value column worth offering: enough for every row's
	// compact value rendering, the widest of which is "‹ creation ›" at 12 cells. It is what
	// makes minRowsPaneWidth — and so the single-pane threshold — a derived number, and
	// TestRowMinValueCellsHoldsTheWidestCompactValue ties it to the real schema.
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
// a header per category and a spacer between them. Capping paneHeight at it means the box
// grows with the terminal but never past what it can fill. Pinned against the flat view's
// real line count by TestMaxPaneLinesMatchesTheFlatView.
func (s *SettingsOverlay) maxPaneLines() int {
	cats := len(allCategories())
	return max(len(railEntries()), len(s.rows)+cats+(cats-1))
}

// paneHeight is the shared height of the rail and rows panes.
//
// It is a function of the terminal size alone — not of the rail cursor, not of the row
// cursor — so the centered box never changes height as you navigate. At 80x24 it is 13,
// which is exactly the thirteen rail entries (spec §4's invariant).
func (s *SettingsOverlay) paneHeight() int {
	return clamp(s.height-settingsVChrome-s.helpBlockHeight(), settingsMinBody, s.maxPaneLines())
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
	start, end := s.rowRange(s.selectedEntry())
	w := 0
	for _, r := range s.rows[start:end] {
		if n := ansi.StringWidth(r.label); n > w {
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

	rendered := make([]string, 0, len(entries))
	for i, e := range entries {
		mark := " "
		if i == s.railCursor {
			mark = t.Glyphs.SelectionMark
		}
		trail := " "
		if e.kind == railHandoff {
			trail = t.Glyphs.Handoff
		}
		line := mark + " " + padRight(e.label, labelW) + " " + trail

		style := t.DimStyle()
		switch {
		case i == s.railCursor && s.focus == focusRail:
			style = t.AccentStyle()
		case i == s.railCursor:
			// Current but unfocused: still legible, but the accent belongs to whichever pane
			// is taking keys, so exactly one bright marker is on screen at a time.
			style = t.FgStyle()
		case e.kind == railHandoff:
			// Dimmer than an ordinary entry: PR B cannot open these yet.
			style = t.FaintStyle()
		}
		rendered = append(rendered, style.Render(line))
	}
	return windowPane(rendered, s.railCursor, s.paneHeight())
}

// paneLine is one rows-pane line together with the row it belongs to, so the windowing can
// locate the cursor without re-deriving it from the text. rowIdx is -1 for a category header,
// a spacer, an overflow marker, or a handoff note.
type paneLine struct {
	text   string
	rowIdx int
}

// rowsPaneContent builds every line the current rail entry could show, unwindowed.
//
// The All settings view adds a header per category and a blank spacer between them — the
// shape of the pre-redesign list, which is what makes it usable for auditing a config. A
// single category shows its rows bare: the rail entry already names it, so a header would
// only repeat itself.
func (s *SettingsOverlay) rowsPaneContent(width int) []paneLine {
	e := s.selectedEntry()
	if e.kind == railHandoff {
		return s.handoffPaneContent(e, width)
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

// handoffPaneContent is the rows pane for an entry that owns no rows: its note, wrapped.
// Naming the surface that does own the config is the honest thing to render until PR C wires
// Accounts to the @ overlay and PR D builds the Profiles editor — an empty pane would read as
// a bug.
func (s *SettingsOverlay) handoffPaneContent(e railEntry, width int) []paneLine {
	style := theme.Current().DimStyle()
	var lines []paneLine
	for _, l := range strings.Split(ansi.Wrap(e.note, width, ""), "\n") {
		lines = append(lines, paneLine{text: style.Render(l), rowIdx: -1})
	}
	return lines
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
		if l.rowIdx == s.cursor {
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

	// The badge column carries the apply timing — unless the row is inert, in which case the
	// reason takes it: a row that does nothing right now has more urgent news than when it
	// would take effect. Either way spec §10 drops this column first when the pane is narrow,
	// which is why the help pane repeats both in prose.
	// An inert row's chip takes the badge column: a row that does nothing right now has more
	// urgent news than when it would take effect. Unlike a timing badge it degrades rather than
	// dropping — see inertBadgeShort.
	inert := s.inertReason(i)
	candidates := []string{row.timing.badge()}
	if inert != "" {
		candidates = inertBadgeCandidates(inert)
	}

	// The value is sized against the SHORTEST badge candidate, not the widest. This refines
	// spec §10, which ordered badge-before-value but never contemplated an enum's inline
	// alternatives competing with the badge — at the 80-column floor they do.
	//
	// Reserving against the widest was the first attempt and it fails in a way worth recording:
	// at 73 columns the rich value found no room beside the 19-cell reason, fell back to the
	// full slack, took 15 cells for itself, and left nothing for the chip the ladder was
	// supposed to guarantee. Reserving against the form that always survives is what makes the
	// guarantee real.
	value := s.valueCell(i, width, labelW, candidates[len(candidates)-1])
	badge := fitBadge(candidates, width, labelW, value)
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

// fitBadge returns the widest candidate that fits beside the value, or "" when none does.
// composeRowLine would drop an over-wide badge silently; this is what lets a caller offer a
// shorter rendering instead of losing the column.
func fitBadge(candidates []string, width, labelW int, value string) string {
	avail := width - rowMarkerCells - labelW - rowLabelGap - ansi.StringWidth(value) - 1
	for _, c := range candidates {
		if ansi.StringWidth(c) <= avail {
			return c
		}
	}
	return ""
}

// valueWasTruncated reports whether the selected row's value cell had to be shortened to fit
// its pane, which is what obliges the help pane to show it in full (spec §10).
func (s *SettingsOverlay) valueWasTruncated() bool {
	labelW := s.visibleLabelWidth()
	width := s.rowsPaneWidth()
	p := composeRowLine(width, labelW, " ", " ", s.selectedRow().label,
		s.valueCell(s.cursor, width, labelW, ""), "")
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
	prose := s.selectedRow().footerText()
	if s.selectedEntry().kind == railHandoff {
		// A handoff entry's note is already the whole content of the rows pane, so echoing it
		// here would print the same sentence twice in one frame. The pane stays blank.
		prose = ""
	}
	if s.lastErr != "" {
		prose, style = s.lastErr, t.DangerStyle()
	}

	// The context line carries the position readout, so it must never be the thing that gets
	// evicted when the prose is long: capping the prose at h-1 is what makes contextLine's
	// "always" true of the rendered panel rather than only of the function.
	ctx := ""
	if s.lastErr == "" && s.selectedEntry().kind != railHandoff {
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
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		return ""
	}
	pos := fmt.Sprintf("%d/%d", s.cursor-start+1, end-start)

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
// It deliberately does not advertise `/` or `r`. Search and reset are PR C, and a hint for a
// key that does nothing is worse than no hint at all.
func (s *SettingsOverlay) hintLine() string {
	var ladder []string
	switch {
	case s.editing:
		ladder = []string{"↵ save · esc cancel", "esc cancel"}
	case s.helpOpen:
		ladder = []string{"↑/↓ scroll · ? or esc back", "esc back"}
	case s.focus == focusRows:
		ladder = []string{
			"↑/↓ move · ←/→ change · ↵ edit · ? more · ⇥ pane · esc back",
			"↑/↓ · ←/→ · ↵ edit · ? more · esc back",
			"↵ edit · ? more · esc back",
			"esc back",
		}
	default:
		ladder = []string{
			"↑/↓ category · → rows · ⇥ pane · esc close",
			"↑/↓ · → rows · esc close",
			"esc close",
		}
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
