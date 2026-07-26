package overlay

import (
	"strings"

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
