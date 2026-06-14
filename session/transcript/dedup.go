package transcript

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// minDistinctiveWidth is the visible-cell threshold above which a single matched
// line is distinctive enough to anchor an overlap on its own. Below it, a lone
// match (e.g. "Done." or "ok") is too generic to trust.
const minDistinctiveWidth = 24

// TrimOverlap removes the trailing lines of transcript that duplicate content
// already visible in pane — a frozen capture of the current screen appended
// below the scrollback — and reports ok=true when a confident overlap is found.
// On ok the caller drops the "── current screen" divider and concatenates the
// trimmed transcript directly above the pane, so history flows continuously
// into the live view; otherwise it keeps the divider as a safe fallback.
//
// Matching is on normalized prose lines: ANSI stripped, whitespace collapsed,
// and chrome removed (blank lines, our own aggregate/error/image lines, and the
// live view's spinner, input box, status bar and turn footers). The same chrome
// filter runs on both sides, so a line that only one side has never breaks the
// contiguity of a real prose overlap. Differently-wrapped long paragraphs may
// not line up; that simply lowers the match length and, below the confidence
// bar, falls back to the divider — never a wrong splice.
func TrimOverlap(transcript, pane string) (string, bool) {
	tLines := strings.Split(transcript, "\n")

	type mline struct {
		norm string
		idx  int
	}
	var tm []mline
	for i, l := range tLines {
		if n := normLine(l); n != "" && !isChrome(n) {
			tm = append(tm, mline{n, i})
		}
	}
	var pm []string
	for _, l := range strings.Split(pane, "\n") {
		if n := normLine(l); n != "" && !isChrome(n) {
			pm = append(pm, n)
		}
	}
	if len(tm) == 0 || len(pm) == 0 {
		return transcript, false
	}

	// Longest suffix of the transcript's prose lines that occurs as a contiguous
	// block somewhere in the pane's prose lines.
	suffix := make([]string, len(tm))
	for i, m := range tm {
		suffix[i] = m.norm
	}
	best, ambiguous := 0, false
	for k := min(len(tm), len(pm)); k >= 1; k-- {
		if n := countContiguous(pm, suffix[len(suffix)-k:]); n >= 1 {
			best, ambiguous = k, n >= 2
			break
		}
	}
	if best == 0 {
		return transcript, false
	}
	// A single matched line must be long and unique; two or more lines stand on
	// their own.
	if best == 1 && (ambiguous || lipgloss.Width(tm[len(tm)-1].norm) < minDistinctiveWidth) {
		return transcript, false
	}

	cut := tm[len(tm)-best].idx
	return strings.TrimRight(strings.Join(tLines[:cut], "\n"), "\n"), true
}

// normLine reduces a rendered line to a comparable form: ANSI escapes stripped
// (never via a CSI regex — ansi.Strip preserves the visible text of OSC 8 links)
// and internal whitespace collapsed to single spaces.
func normLine(l string) string {
	return strings.Join(strings.Fields(ansi.Strip(l)), " ")
}

// countContiguous returns how many start positions in hay match needle as a
// contiguous in-order block.
func countContiguous(hay, needle []string) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return 0
	}
	count := 0
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			count++
		}
	}
	return count
}

// isChrome reports whether a normalized line is non-prose and must be excluded
// from overlap matching. It covers both sides: transcript-only lines (aggregate
// tool summaries, errored-result markers, image placeholders, the truncation
// header) and live-only lines (the input box, status bar, spinner, and turn
// footers). Filtering symmetrically keeps a real prose overlap contiguous even
// when one side interleaves chrome the other lacks.
func isChrome(n string) bool {
	switch {
	case strings.HasPrefix(n, "⎿"),
		strings.HasPrefix(n, "[Image"),
		strings.HasPrefix(n, "[image"),
		strings.HasPrefix(n, "— transcript truncated"),
		strings.HasPrefix(n, "── current screen"),
		strings.HasPrefix(n, "current screen"),
		n == "❯":
		return true
	}
	if isAggregateLine(n) || isStatusChrome(n) {
		return true
	}
	return false
}

// isAggregateLine matches a collapsed tool-aggregate line by its leading verb.
// Real prose that happens to open with one of these verbs is filtered too, but
// since the filter is symmetric that only drops the line from both match
// sequences — never a one-sided desync.
func isAggregateLine(n string) bool {
	for _, v := range []string{"Ran ", "Read ", "Made ", "Updated ", "Recalled ", "Wrote ", "Called ", "Used "} {
		if strings.HasPrefix(n, v) {
			return true
		}
	}
	return false
}

// isStatusChrome matches the live view's framing: box-drawing rows (input frame,
// rules), spinner / turn-footer glyphs, and status-bar fragments.
func isStatusChrome(n string) bool {
	r0 := []rune(n)[0]
	if strings.ContainsRune("╭╮╰╯│─", r0) || strings.ContainsRune("✻✶✳✽✢∗*", r0) {
		return true
	}
	for _, frag := range []string{
		"esc to interrupt", "⏵⏵", "? for shortcuts", "auto mode",
		"to cycle", "tokens", "to save", "new task?", "for agents",
	} {
		if strings.Contains(n, frag) {
			return true
		}
	}
	return false
}
