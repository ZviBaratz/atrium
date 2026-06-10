package hints

import (
	"sort"
	"strings"
	"unicode/utf8"
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
	lines := strings.Split(StripANSI(raw), "\n")
	if rows >= 0 && len(lines) > rows {
		lines = lines[:rows]
	}

	matches := Scan(strings.Join(lines, "\n"))
	// A hint must label something the user can see: drop matches whose first
	// rune is already clipped by the pane's width truncation.
	visible := matches[:0]
	for _, m := range matches {
		if width <= 0 || m.Col < width {
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
	// A label longer than its text would overhang the match (fingers' guard).
	// Dropping after assignment keeps the remaining labels prefix-free.
	kept := visible[:0]
	for _, m := range visible {
		if utf8.RuneCountInString(m.Text) >= len(m.Label) {
			kept = append(kept, m)
		}
	}
	return &Screen{lines: lines, width: width, matches: kept}
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
