package hints

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Match is one actionable string found on the screen.
type Match struct {
	// Text is the copyable content (the `match` group when the pattern has one).
	Text string
	// Kind decides the open variant's behavior.
	Kind Kind
	// Row and Col locate the first rune of Text on the stripped screen:
	// Row is the 0-based visible line, Col the 0-based rune index within it.
	Row, Col int
	// Label is the assigned hint sequence (set by NewScreen, empty from Scan).
	Label string
}

// ansiRE matches the CSI escape sequences tmux capture-pane -e emits.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;:?]*[A-Za-z]`)

// StripANSI removes ANSI escape sequences so matching and rendering operate
// on plain text. Hint mode re-renders the screen itself with a dim backdrop,
// so original colors are deliberately dropped while the mode is active —
// the contrast effect tmux-fingers applies on purpose.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// Scan finds all matches in stripped multi-line text, top to bottom.
func Scan(text string) []Match {
	var out []Match
	for row, line := range strings.Split(text, "\n") {
		out = append(out, scanLine(line, row)...)
	}
	return out
}

// scanLine finds matches in one stripped line, left to right, non-overlapping.
// All patterns run at each position; the earliest match wins, ties broken by
// pattern priority order. The scanner then advances past the full match (not
// just the capture), so a pattern's consumed prefix ("modified: ") is skipped.
func scanLine(line string, row int) []Match {
	var out []Match
	offset := 0 // byte offset into line
	for offset < len(line) {
		best := -1
		var bestLoc []int
		for i, p := range builtinPatterns {
			loc := p.re.FindStringSubmatchIndex(line[offset:])
			if loc == nil {
				continue
			}
			if best == -1 || loc[0] < bestLoc[0] {
				best, bestLoc = i, loc
			}
		}
		if best == -1 {
			break
		}
		p := builtinPatterns[best]
		text := line[offset+bestLoc[0] : offset+bestLoc[1]]
		textStart := offset + bestLoc[0]
		if gi := p.re.SubexpIndex("match"); gi >= 0 && bestLoc[2*gi] >= 0 {
			text = line[offset+bestLoc[2*gi] : offset+bestLoc[2*gi+1]]
			textStart = offset + bestLoc[2*gi]
		}
		if p.kind == KindURL {
			// Sentence-final URLs in logs: the trailing punctuation is prose,
			// not address.
			text = strings.TrimRight(text, ".,;:")
		}
		if text != "" {
			out = append(out, Match{
				Text: text,
				Kind: p.kind,
				Row:  row,
				Col:  utf8.RuneCountInString(line[:textStart]),
			})
		}
		offset += bestLoc[1]
	}
	return out
}
