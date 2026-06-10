package hints

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Match is one actionable string found on the screen.
type Match struct {
	// Text is the copyable content (the `match` group when the pattern has one).
	Text string
	// Kind decides the open variant's behavior.
	Kind Kind
	// Row and Col locate the match's first rune on the stripped screen:
	// Row is the 0-based visible line, Col the 0-based rune index within it.
	Row, Col int
	// Width is the visible extent in runes. For scanner matches it equals
	// len([]rune(Text)); for hyperlink matches Text is the target, which can
	// be longer (or shorter) than the visible span it decorates.
	Width int
	// Label is the assigned hint sequence (set by NewScreen, empty from Scan).
	Label string
}

// StripANSI removes ALL escape sequences — CSI colors, OSC (including the
// OSC 8 hyperlinks Claude Code wraps URLs in, which tmux capture-pane -e
// re-emits), DCS, and friends — so matching and rendering operate on plain
// text. Hint mode re-renders the screen itself with a dim backdrop, so
// original colors are deliberately dropped while the mode is active — the
// contrast effect tmux-fingers applies on purpose. Delegates to x/ansi's
// parser; a homegrown CSI-only regex let OSC 8 targets leak into match text.
func StripANSI(s string) string { return ansi.Strip(s) }

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
		// Greedy .+ captures (git-status, diff-path) pick up tmux's
		// width-padding; padding is never part of the copyable text.
		text = strings.TrimRight(text, " \t")
		if p.kind == KindURL || p.kind == KindPath {
			// Sentence-final URLs/paths in prose: the trailing punctuation is
			// prose, not address. Trimmed before validate so a sentence dot
			// ("copied/opened.") can't pass as a filesystem signal.
			text = strings.TrimRight(text, ".,;:")
		}
		kind := p.kind
		if kind == KindURL && !hasURLScheme(text) {
			// A markdown link to a relative target must not be browser-opened;
			// degrade to a copyable path.
			kind = KindPath
		}
		if text != "" && (p.validate == nil || p.validate(text)) {
			out = append(out, Match{
				Text:  text,
				Kind:  kind,
				Row:   row,
				Col:   utf8.RuneCountInString(line[:textStart]),
				Width: utf8.RuneCountInString(text),
			})
		}
		offset += bestLoc[1]
	}
	return out
}
