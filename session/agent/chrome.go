package agent

import (
	"regexp"
	"strings"
)

// Pure-text windowing over a captured pane. These moved here from session/tmux
// together with the heuristics that depend on them: a matcher's window size and
// the windowing semantics must evolve in lockstep, so they live in one package.
// The input is expected to be cleaned for detection already (ANSI stripped,
// trailing whitespace trimmed — see tmux's cleanForDetection).

var whiteSpaceRegex = regexp.MustCompile(`\s+`)

// workChromeLines is footerRegion's fallback window when the pane shows no
// input-box border: the last few non-empty lines, where a minimal footer or a
// degenerate capture keeps its live status.
const workChromeLines = 3

// liveChromeLines returns the last n non-empty lines of the pane — the region where
// an agent renders its live status bar, prompt, and input box. Marker detection must
// be confined here: capture-pane returns the whole visible pane including the
// scrolled-back transcript, so the same strings ("esc to interrupt", a prompt footer)
// can appear in the conversation body, and only their presence in the bottom chrome
// reflects the live state.
func liveChromeLines(content string, n int) string {
	lines := strings.Split(content, "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append(kept, lines[i])
		}
	}
	// kept is collected bottom-up; reverse to natural top-to-bottom reading order so callers
	// that reconstruct wrapped multi-line text (flattenChrome) join the lines in the order
	// they were rendered. Substring callers (busy markers) are order-independent.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	return strings.Join(kept, "\n")
}

// flattenChrome collapses the last n non-empty lines into one whitespace-normalized line.
// A prompt's key-hint footer ("Enter to select · … · Esc to cancel") and the permission
// dialog's decline option wrap across physical lines at a narrow pane width; flattening
// (whiteSpaceRegex already spans newlines) reconstructs them so the substring/token matches
// survive the wrap instead of silently leaving a waiting session classified as idle.
func flattenChrome(content string, n int) string {
	return whiteSpaceRegex.ReplaceAllString(liveChromeLines(content, n), " ")
}

// isHorizontalRule reports whether line is a box-drawing horizontal border — the top or
// bottom edge of claude's input box. Such a line is made only of horizontal dashes, box
// corners/sides, and padding, and contains a real run of dashes (so a prose line with a
// stray "│" doesn't qualify). It anchors the live footer in footerRegion.
func isHorizontalRule(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	dashes := 0
	for _, r := range line {
		switch r {
		case '─':
			dashes++
		case '╭', '╮', '╰', '╯', '│', '┌', '┐', '└', '┘', '├', '┤', ' ':
			// box corners/sides and interior padding are allowed
		default:
			return false
		}
	}
	return dashes >= 3
}

// footerRegion returns the live footer of the pane: the lines below the input box's bottom
// border. Claude renders its status hints and the variable-height agent-team selector (one
// line per teammate) there, and the busy marker sits among them — so anchoring to the box
// border, rather than a fixed bottom-N window, keeps the marker detectable no matter how many
// teammates the selector lists. Everything below the last box border is pure live chrome, so
// this still excludes the scrolled-back transcript above the box. When the pane has no border
// — a minimal footer, a non-claude agent, or a degenerate capture — it falls back to the last
// workChromeLines non-empty lines, preserving the previous behavior.
func footerRegion(content string) string {
	lines := strings.Split(content, "\n")
	lastRule := -1
	for i, line := range lines {
		if isHorizontalRule(line) {
			lastRule = i
		}
	}
	if lastRule < 0 {
		return liveChromeLines(content, workChromeLines)
	}
	return strings.Join(lines[lastRule+1:], "\n")
}
