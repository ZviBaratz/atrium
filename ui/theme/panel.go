package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Panel renders content inside a rounded-border box whose top edge carries an
// inset title, e.g.:
//
//	╭─ Sessions ─────────────╮
//	│ content                │
//	╰────────────────────────╯
//
// width and height are the OUTER dimensions; the returned string is exactly
// width columns wide on every line and height lines tall. active selects the
// accent border color, otherwise the faint color is used. content is expected
// to fit within (width-2) x (height-2); it is left/top aligned and padded.
//
// lipgloss v1 has no inset-title border API, so the top edge is composed by
// hand (the body's own top border is disabled and replaced by this row).
func (t *Theme) Panel(title, content string, width, height int, active bool) string {
	return t.PanelWithBadge(title, "", content, width, height, active)
}

// PanelWithBadge is Panel plus a single optional right-aligned, attention-colored
// badge inset in the top border:
//
//	╭─ Sessions ──────── ⇡ v0.7.1 ─╮
//
// See PanelWithBadges for the degradation contract.
func (t *Theme) PanelWithBadge(title, badge, content string, width, height int, active bool) string {
	return t.PanelWithBadges(title, []string{badge}, content, width, height, active)
}

// PanelWithBadges is Panel plus one or more right-aligned, attention-colored
// badges inset in the top border:
//
//	╭─ Sessions ──── ⇡ v0.7.1 ⚠ stale ─╮
//
// Each badge is plain text (no ANSI) shaped as "glyph text"; empty entries are
// skipped, so a caller may pass an absent badge as "". The title always wins the
// width contest, and the badges degrade together through a fixed ladder — the
// widest form that fits beside the title is rendered, else none:
//
//   - one badge:  full → text after the glyph → glyph alone → nothing
//     (a version string is never cut mid-word, which would mislead)
//   - many badges: all full → every glyph alone → nothing
//     (a narrow panel keeps every signal as its glyph rather than orphaning or
//     dropping a whole badge — the drift "⚠" survives beside the update "⇡")
func (t *Theme) PanelWithBadges(title string, badges []string, content string, width, height int, active bool) string {
	if width < 2 {
		width = 2
	}
	if height < 2 {
		height = 2
	}

	b := t.Borders.Style
	color := t.Palette.FgFaint
	if active {
		color = t.Palette.Accent
	}

	// Inset title segment, e.g. " Sessions ". Budget for two corners, one
	// leading horizontal, and at least one trailing horizontal.
	var titleSeg string
	if title != "" {
		maxTitle := width - 5
		if maxTitle < 0 {
			maxTitle = 0
		}
		titleSeg = " " + runewidth.Truncate(title, maxTitle, "…") + " "
	}
	titleSegW := runewidth.StringWidth(titleSeg)

	// Pick the widest badge variant that fits beside the (untouched) title.
	// Budget: two corners, one leading and one trailing horizontal around the
	// segments.
	var badge, badgeSeg string
	for _, c := range badgeCandidates(badges) {
		if seg := " " + c + " "; width-4-titleSegW-runewidth.StringWidth(seg) >= 0 {
			badge, badgeSeg = c, seg
			break
		}
	}

	border := lipgloss.NewStyle().Foreground(color)
	var topRow string
	if badgeSeg == "" {
		fill := width - 3 - titleSegW // 2 corners + 1 leading horiz
		if fill < 0 {
			fill = 0
		}
		topRow = border.Render(b.TopLeft + b.Top + titleSeg + strings.Repeat(b.Top, fill) + b.TopRight)
	} else {
		// The badge needs its own (attention) style, so the row is composed
		// from separately rendered segments; all width math stays on the
		// plain strings (ANSI adds zero columns).
		gap := width - 4 - titleSegW - runewidth.StringWidth(badgeSeg)
		topRow = border.Render(b.TopLeft+b.Top+titleSeg+strings.Repeat(b.Top, gap)) +
			" " + t.AttentionStyle().Render(badge) + " " +
			border.Render(b.Top+b.TopRight)
	}

	// Clip content to the inner box. lipgloss .Height/.Width pad but never
	// truncate, so oversized content would overflow the panel (and applying
	// MaxHeight to the bordered block could eat the bottom border). Truncate the
	// content ourselves, then let the fixed-size style pad the remainder.
	inner := clipContent(content, width-2, height-2)

	// Body: left/right/bottom borders only (top is the inset row above).
	body := lipgloss.NewStyle().
		Border(b, false, true, true, true).
		BorderForeground(color).
		Width(width - 2).
		Height(height - 2).
		Render(inner)

	return lipgloss.JoinVertical(lipgloss.Left, topRow, body)
}

// badgeCandidates returns the badge degradation ladder (widest first) for the
// non-empty entries of badges. PanelWithBadges renders the first candidate that
// fits beside the title, or none. See PanelWithBadges for the contract: one badge
// keeps its text as it narrows; many badges collapse to their glyphs together.
func badgeCandidates(badges []string) []string {
	present := make([]string, 0, len(badges))
	for _, b := range badges {
		if b != "" {
			present = append(present, b)
		}
	}
	switch len(present) {
	case 0:
		return nil
	case 1:
		b := present[0]
		ladder := []string{b}
		if i := strings.IndexByte(b, ' '); i >= 0 {
			ladder = append(ladder, b[i+1:], b[:i])
		}
		return ladder
	default:
		glyphs := make([]string, len(present))
		for i, b := range present {
			glyphs[i] = badgeGlyph(b)
		}
		return []string{strings.Join(present, " "), strings.Join(glyphs, " ")}
	}
}

// badgeGlyph returns the leading glyph token of a "glyph text" badge.
func badgeGlyph(b string) string {
	if i := strings.IndexByte(b, ' '); i >= 0 {
		return b[:i]
	}
	return b
}

// SanitizeWidth makes untrusted captured content (tmux pane output, diffs) safe to
// lay out, by removing the codepoints that let a terminal's *rendered* width diverge
// from what width libraries (lipgloss / x-ansi / go-runewidth) *measure*.
//
// Emoji combine via a zero-width joiner (U+200D), an emoji/text presentation selector
// (U+FE0F / U+FE0E), or a skin-tone modifier (U+1F3FB–U+1F3FF) into a single grapheme
// cluster that those libraries count as one 2-cell glyph. A terminal whose font lacks
// the combined glyph instead renders each component separately and far wider — e.g.
// the family "👨‍👩‍👧" measures 2 but renders as three 2-cell people (6 cells). The line
// then overflows its pane, wraps onto an extra physical row, and desyncs bubbletea's
// incremental alt-screen renderer, which never erases lines — leaving the duplicated,
// accumulating rows seen when navigating between repo groups (only a full repaint, e.g.
// attaching and detaching, recovers it).
//
// Stripping the joiners/modifiers decomposes such clusters into standalone emoji, which
// every renderer AND the terminal measure identically, so the laid-out width matches the
// rendered width and nothing wraps. (Regional-indicator flag pairs combine without a
// joiner and are not handled here; the manual redraw key is the backstop for those.)
func SanitizeWidth(s string) string {
	risky := func(r rune) bool {
		return r == 0x200D || // ZERO WIDTH JOINER
			r == 0xFE0F || r == 0xFE0E || // variation selectors (emoji / text presentation)
			(r >= 0x1F3FB && r <= 0x1F3FF) // skin-tone modifiers
	}
	if strings.IndexFunc(s, risky) < 0 {
		return s // common case: nothing to strip, no allocation
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !risky(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// clipContent truncates content to at most h lines, each at most w columns
// (measured by display width, ANSI-aware). Shorter content is left as-is for
// the caller's fixed-size style to pad.
func clipContent(content string, w, h int) string {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "")
	}
	return strings.Join(lines, "\n")
}
