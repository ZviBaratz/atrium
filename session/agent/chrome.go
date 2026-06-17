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

// footerBelowBox returns the lines below the input box's bottom border and true
// when such a border is on screen. The border proves everything below it is live
// chrome, never scrolled-back transcript — so a caller that must not false-match
// a phrase quoted in the conversation (permission-mode detection) gates on the
// ok result. When the pane shows no border — a minimal footer, a non-claude
// agent, a pre-box startup frame, or a degenerate capture — there is no anchor
// to make that guarantee, so it returns ("", false).
func footerBelowBox(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	lastRule := -1
	for i, line := range lines {
		if isHorizontalRule(line) {
			lastRule = i
		}
	}
	if lastRule < 0 {
		return "", false
	}
	return strings.Join(lines[lastRule+1:], "\n"), true
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
	if footer, ok := footerBelowBox(content); ok {
		return footer
	}
	return liveChromeLines(content, workChromeLines)
}

// isInputBoxLine reports whether line is the interior of an agent's input box: the "❯" or
// ">" prompt, optionally inside the box's "│" side borders, possibly followed by typed
// text. The box is drawn only while no overlay is up, so reaching it while scanning upward
// proves everything above is scrolled-back transcript.
func isInputBoxLine(line string) bool {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, "│"))
	return strings.HasPrefix(s, "❯") || strings.HasPrefix(s, ">")
}

// footerVisibleInSegments reports whether a live key-hint footer — recognized by the
// tokens predicate — is on screen. It exists for footers a custom multi-line statusLine
// can render *below*, pushing them out of any fixed bottom-N window — and a statusLine may
// draw its own ─── dividers, which defeats any single "below the last rule" anchor by
// becoming the last rule itself. So instead of one anchor, the pane is scanned as
// rule-delimited segments, bottom-up:
//
//   - The footer tokens must co-occur within a single segment (flattened, so a footer
//     hard-wrapped at a narrow pane width is reconstructed), which also keeps unrelated hint
//     text in neighboring segments from combining into a false footer.
//   - The scan stops at the input box interior (isInputBoxLine as a segment's first
//     non-empty line): the box and an overlay are mutually exclusive, and the live footer
//     always sits below any "❯" option pointer, so a segment opening with the prompt char
//     means everything above is transcript — where a quoted footer must not count. A
//     statusLine segment that itself opens with "❯"/">" stops the scan early and hides a
//     footer above it; that residual miss needs a statusLine with both a divider and a
//     prompt-char-initial line.
//   - The scan is confined to the bottom WindowPrompt non-empty lines — the same budget
//     the dialog matchers use — which caps how far a rule-bearing transcript can be
//     searched on degenerate panes that show neither a box nor an overlay, at the cost of
//     missing footers displaced by statusLines taller than that budget.
//   - With no rule on screen there is no structure to segment by; fall back to the tight
//     workChromeLines window, preserving the fixed-window behavior for minimal footers.
func footerVisibleInSegments(content string, tokens func(string) bool) bool {
	lines := strings.Split(content, "\n")
	nonEmpty := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			nonEmpty++
			if nonEmpty == WindowPrompt {
				lines = lines[i:]
				break
			}
		}
	}

	var rules []int
	for i, line := range lines {
		if isHorizontalRule(line) {
			rules = append(rules, i)
		}
	}
	if len(rules) == 0 {
		return tokens(flattenChrome(content, workChromeLines))
	}

	end := len(lines)
	for k := len(rules) - 1; k >= -1; k-- {
		start := 0
		if k >= 0 {
			start = rules[k] + 1
		}
		segment := lines[start:end]
		if tokens(whiteSpaceRegex.ReplaceAllString(strings.Join(segment, " "), " ")) {
			return true
		}
		for _, line := range segment {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if isInputBoxLine(line) {
				return false
			}
			break
		}
		if k >= 0 {
			end = rules[k]
		}
	}
	return false
}
