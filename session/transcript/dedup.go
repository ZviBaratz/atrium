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

	// Both sides reduce to a flat stream of prose *words*, not whole lines. The
	// transcript wraps to the preview width with our own "● "/hanging indent
	// while the pane carries Claude's own margin, so the same paragraph breaks at
	// different word boundaries — line-for-line matching misses it and the seam
	// renders the block twice. Words carry no line breaks, so the overlap is
	// wrap-independent. Each transcript word remembers its source line and
	// whether it opened that line, so the cut can land on a clean line boundary.
	tw, tLine, tFirst := wordStream(tLines)
	pw, _, _ := wordStream(strings.Split(pane, "\n"))
	if len(tw) == 0 || len(pw) == 0 {
		return transcript, false
	}

	// The overlap is anchored at the pane's top: history flows straight into the
	// live screen, so the transcript's tail equals the pane's *leading* prose
	// words. Take the longest transcript word-suffix that is a prefix of the
	// pane's words. Matching anywhere inside the pane instead would let a
	// re-quoted tail splice mid-screen and reorder/duplicate history — the
	// wrong-splice case the divider fallback exists to avoid.
	best := 0
	for k := min(len(tw), len(pw)); k >= 1; k-- {
		if equalWords(tw[len(tw)-k:], pw[:k]) {
			best = k
			break
		}
	}
	if best == 0 {
		return transcript, false
	}
	start := len(tw) - best
	// Distinctiveness: the shared run must be wide enough to trust. A one- or
	// two-word coincidental tail ("Done.", "ok thanks") is too generic; a real
	// shared paragraph clears this easily.
	if lipgloss.Width(strings.Join(pw[:best], " ")) < minDistinctiveWidth {
		return transcript, false
	}
	// Clean cut: the overlap must begin at a transcript line boundary. When the
	// pane's top row is a mid-paragraph cut (the overlap starts mid-line), cutting
	// the whole line would drop head-of-line history that scrolled above the
	// screen, and keeping it would duplicate the line's tail — so fall back to the
	// divider, which is honest and lossless. Real seams start at a message or
	// blank boundary, so this rarely rejects.
	if !tFirst[start] {
		return transcript, false
	}

	cut := tLine[start]
	return strings.TrimRight(strings.Join(tLines[:cut], "\n"), "\n"), true
}

// wordStream flattens prose lines into their normalized words, dropping blank
// and chrome lines (the same filter both sides share). For each word it records
// the source line index and whether the word opened its line, so a word-level
// overlap can be cut back to a whole-line boundary.
func wordStream(lines []string) (words []string, lineIdx []int, firstOnLine []bool) {
	for i, l := range lines {
		n := normLine(l)
		if n == "" || isChrome(n) {
			continue
		}
		for w, field := range strings.Fields(n) {
			words = append(words, field)
			lineIdx = append(lineIdx, i)
			firstOnLine = append(firstOnLine, w == 0)
		}
	}
	return words, lineIdx, firstOnLine
}

// normLine reduces a rendered line to a comparable form: ANSI escapes stripped
// (never via a CSI regex — ansi.Strip preserves the visible text of OSC 8 links)
// and internal whitespace collapsed to single spaces.
func normLine(l string) string {
	return strings.Join(strings.Fields(ansi.Strip(l)), " ")
}

// equalWords reports whether two word slices are elementwise equal.
func equalWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
	if n == "" {
		return false
	}
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
