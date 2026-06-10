package hints

import (
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Screen is one frozen, hinted capture of a preview pane: the stripped
// visible lines plus the labeled matches found on them. Immutable after
// NewScreen; Render and Resolve are read-only.
type Screen struct {
	lines   []string
	width   int
	matches []Match
}

// NewScreen strips raw pane content, clips it to the pane's visible geometry
// (rows lines of width columns — the same slice the live preview renders),
// then scans, dedups, and labels the matches. Bottom-most matches get the
// shortest labels; identical text shares one label. A non-positive width or
// negative rows disables that axis of clipping (used by tests).
func NewScreen(raw string, width, rows int) *Screen {
	plain, links := stripWithLinks(raw)
	lines := strings.Split(plain, "\n")
	if rows >= 0 && len(lines) > rows {
		lines = lines[:rows]
	}

	matches := Scan(strings.Join(lines, "\n"))
	// Hyperlink targets are authoritative: a scanner match overlapping a
	// link's visible span only re-detects the link from its display text
	// (often the URL printed verbatim), so it yields to the link match.
	var linkMs []Match
	for _, sp := range links {
		if sp.Row < len(lines) {
			linkMs = append(linkMs, linkMatch(sp))
		}
	}
	if len(linkMs) > 0 {
		unlinked := matches[:0]
		for _, m := range matches {
			if !overlapsLink(m, linkMs) {
				unlinked = append(unlinked, m)
			}
		}
		matches = append(unlinked, linkMs...)
	}
	// A hint must label something the user can see: drop matches whose first
	// rune is already clipped by the pane's width truncation. Compare display
	// columns, not rune indices — wide runes (CJK, emoji) occupy two columns
	// each, so rune index alone undercounts how far right a match sits.
	visible := matches[:0]
	for _, m := range matches {
		if width <= 0 || displayCol(lines[m.Row], m.Col) < width {
			visible = append(visible, m)
		}
	}
	// Bottom-up: the match nearest the prompt gets the shortest label.
	sort.SliceStable(visible, func(i, j int) bool {
		if visible[i].Row != visible[j].Row {
			return visible[i].Row > visible[j].Row
		}
		return visible[i].Col < visible[j].Col
	})
	labels := assignLabels(countDistinct(visible))
	byText := make(map[string]string)
	next := 0
	for i := range visible {
		if l, ok := byText[visible[i].Text]; ok {
			visible[i].Label = l
			continue
		}
		visible[i].Label = labels[next]
		byText[visible[i].Text] = labels[next]
		next++
	}
	// A label longer than its visible span would overhang the match
	// (fingers' guard). Dropping after assignment keeps the remaining labels
	// prefix-free.
	kept := visible[:0]
	for _, m := range visible {
		if m.Width >= len(m.Label) {
			kept = append(kept, m)
		}
	}
	return &Screen{lines: lines, width: width, matches: kept}
}

// displayCol converts a rune index within line to its terminal column.
func displayCol(line string, runeIdx int) int {
	runes := []rune(line)
	if runeIdx > len(runes) {
		runeIdx = len(runes)
	}
	return ansi.StringWidth(string(runes[:runeIdx]))
}

// overlapsLink reports whether m's visible range intersects any link span
// match on the same row.
func overlapsLink(m Match, links []Match) bool {
	for _, l := range links {
		if m.Row == l.Row && m.Col < l.Col+l.Width && l.Col < m.Col+m.Width {
			return true
		}
	}
	return false
}

func countDistinct(ms []Match) int {
	seen := make(map[string]struct{}, len(ms))
	for _, m := range ms {
		seen[m.Text] = struct{}{}
	}
	return len(seen)
}

// MatchCount reports how many labeled matches the screen holds.
func (s *Screen) MatchCount() int { return len(s.matches) }

// Resolve narrows the matches by a typed (lowercased) prefix. It returns the
// selected match when typed equals a full label; match=nil with valid=true
// when typed is a proper prefix of at least one label; valid=false when no
// label starts with typed.
func (s *Screen) Resolve(typed string) (match *Match, valid bool) {
	for i := range s.matches {
		if s.matches[i].Label == typed {
			return &s.matches[i], true
		}
		if strings.HasPrefix(s.matches[i].Label, typed) {
			valid = true
		}
	}
	return nil, valid
}
