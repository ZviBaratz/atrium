package hints

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Styles carries the three roles hint rendering needs. The caller builds them
// from the active theme so this package stays theme-agnostic (and tests can
// pass zero values for plain-text output).
type Styles struct {
	// Backdrop dims all non-match text.
	Backdrop lipgloss.Style
	// Match highlights matched text after its label.
	Match lipgloss.Style
	// MatchURL highlights KindURL matches instead of Match — the app
	// underlines them so openable hints are visible before any key is
	// pressed. The zero value renders plain, same as Match's.
	MatchURL lipgloss.Style
	// Label renders the hint characters themselves.
	Label lipgloss.Style
}

// Render draws the frozen screen with hint decorations: every line dimmed,
// matches highlighted, each match's label in the blank gutter cell(s) just
// left of it — keeping the full match text readable — falling back to
// overlaying the match's first cells when no gutter exists (column 0, a
// non-space predecessor, or an adjacent earlier match). typed is the
// already-entered (lowercased) prefix: labels that no longer match it lose
// their decoration; matching labels show only their remaining suffix,
// keeping the next keys to type in front of the user.
//
// Splicing happens at rune indices into the same rune slice the line came
// from, so alignment is self-consistent: the output is exactly the original
// runes with some replaced by ASCII label characters. (All pattern matches
// are ASCII, so the replaced cells are single-width.)
func (s *Screen) Render(typed string, st Styles) string {
	byRow := make(map[int][]Match)
	for _, m := range s.matches {
		byRow[m.Row] = append(byRow[m.Row], m)
	}
	out := make([]string, len(s.lines))
	for row, line := range s.lines {
		ms := byRow[row]
		sort.Slice(ms, func(i, j int) bool { return ms[i].Col < ms[j].Col })
		out[row] = renderLine(line, ms, typed, st)
	}
	return strings.Join(out, "\n")
}

func renderLine(line string, ms []Match, typed string, st Styles) string {
	runes := []rune(line)
	var b strings.Builder
	pos := 0
	for _, m := range ms {
		if m.Col < pos || m.Col > len(runes) {
			continue // overlap or out of range: keep the earlier match
		}
		// Width, not len(Text): a hyperlink match's copyable target can be
		// longer than the visible span it decorates.
		end := m.Col + m.Width
		if end > len(runes) {
			end = len(runes)
		}
		if !strings.HasPrefix(m.Label, typed) {
			// Filtered out by the typed prefix: back to plain backdrop.
			b.WriteString(st.Backdrop.Render(string(runes[pos:end])))
			pos = end
			continue
		}
		suffix := m.Label[len(typed):]
		matchStyle := st.Match
		if m.Kind == KindURL {
			matchStyle = st.MatchURL
		}
		// link wraps a rendered match span in an OSC 8 hyperlink to the match's
		// copyable target, but only for URL kinds — so an openable hint is
		// clickable on supporting terminals. The escapes are zero-width, so the
		// gutter/overlay column math above (rune indices into the frozen line) is
		// untouched. Non-URL kinds (SHAs, paths) render exactly as before.
		link := func(styled string) string {
			if m.Kind != KindURL {
				return styled
			}
			return ansi.SetHyperlink(m.Text) + styled + ansi.ResetHyperlink()
		}
		// Gutter placement is decided on the FULL label so narrowing never
		// flips a hint between gutter and overlay mid-flight; the suffix
		// right-aligns against the match start.
		if gutter := m.Col - len(m.Label); gutter >= pos && isBlank(runes[gutter:m.Col]) {
			b.WriteString(st.Backdrop.Render(string(runes[pos : m.Col-len(suffix)])))
			b.WriteString(st.Label.Render(suffix))
			b.WriteString(link(matchStyle.Render(string(runes[m.Col:end]))))
			pos = end
			continue
		}
		b.WriteString(st.Backdrop.Render(string(runes[pos:m.Col])))
		if n := end - m.Col; len(suffix) > n {
			suffix = suffix[:n]
		}
		b.WriteString(st.Label.Render(suffix))
		b.WriteString(link(matchStyle.Render(string(runes[m.Col+len(suffix) : end]))))
		pos = end
	}
	if pos < len(runes) {
		b.WriteString(st.Backdrop.Render(string(runes[pos:])))
	}
	return b.String()
}

// isBlank reports whether every rune is a plain space — the only cells the
// gutter may claim without eating a visible glyph.
func isBlank(rs []rune) bool {
	for _, r := range rs {
		if r != ' ' {
			return false
		}
	}
	return true
}
