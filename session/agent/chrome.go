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

// pasteChipRegex matches claude's collapsed-paste placeholder in an input-box readback, e.g.
// "[Pasted text #1 +29 lines]" — the readback of a ≥4-line bracketed paste (claude renders it
// as a chip rather than the literal text). Deliberately tolerant: the "#N" index is optional
// and "line"/"lines" both match. Verified live against claude 2.1.207 (2026-07-13) and
// re-confirmed at 2.1.210 ("[Pasted text #1 +6 lines]", 2026-07-15, #332). See
// claudePasteCollapsed and prompt delivery (session/prompt.go boxHoldsPrompt).
var pasteChipRegex = regexp.MustCompile(`\[Pasted text[^\]]*\+\d+ lines?\]`)

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

// aboveBoxBlockCap bounds how far aboveBoxBlock scans upward on a degenerate pane
// that lacks the usual blank line delimiting the live status block from the transcript.
// It sits well above any real status block (a spinner line plus a task/tip list), so a
// normal pane is blank-delimited long before the cap is reached; it only stops a runaway
// scan into scrollback when the delimiter is missing.
const aboveBoxBlockCap = 40

// aboveBoxBlock returns the live status block rendered just above the input box's TOP
// border — the band where claude paints its spinner status line (and any task/tip lines
// below it), which lies outside footerRegion's below-the-box window. It is the upward
// mirror of footerBelowBox: anchor on the bottom-most input-box line, find the box-top
// border above it, skip the blank separator, and return the contiguous non-blank block
// above that, delimited by the blank line that separates the block from the scrolled-back
// transcript — so a spinner string quoted in the transcript never counts. Returns
// ("", false) when there is no box on screen (a pre-box startup frame, a non-boxed agent,
// or a degenerate capture), so callers treat "no anchor" as no signal rather than
// scanning transcript. Input is expected cleaned for detection (ANSI stripped, trailing
// whitespace trimmed) so blank lines are truly "".
//
// It anchors on isBoxBorderLine, not the stricter isHorizontalRule, because claude renders
// the session's agent-context / branch name INSIDE the top border ("──── name ──", seen
// live), which isHorizontalRule rejects — the same reason suggestion.go locates the box
// with the loose predicate. A spinner/task line never starts with a dash run, so the loose
// predicate cannot mistake block content for a border.
//
// It anchors with defaultPrompts rather than an adapter's own set because its only caller
// is claudeSpinnerWorking (spinner.go) — claude's own chrome. This is not a generic entry
// point; an adapter-aware caller would have to pass its own set.
func aboveBoxBlock(content string) (string, bool) {
	lines := strings.Split(content, "\n")

	box := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i], defaultPrompts) {
			box = i
			break
		}
	}
	if box < 0 {
		return "", false
	}

	top := -1
	for i := box - 1; i >= 0; i-- {
		if isBoxBorderLine(lines[i]) {
			top = i
			break
		}
	}
	if top < 0 {
		return "", false
	}

	// Skip the blank separator(s) between the box-top border and the status block.
	end := top - 1
	for end >= 0 && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	if end < 0 {
		return "", false
	}

	// Walk up the contiguous non-blank block, stopping at the blank line above it (the
	// transcript delimiter), a border, or the degenerate-pane cap.
	start := end
	for start > 0 && end-start < aboveBoxBlockCap {
		prev := start - 1
		if strings.TrimSpace(lines[prev]) == "" || isBoxBorderLine(lines[prev]) {
			break
		}
		start = prev
	}
	return strings.Join(lines[start:end+1], "\n"), true
}

// promptSet describes what one agent's composer interior line opens with. It is a type
// rather than a package constant because the glyph is a per-agent fact, and getting it
// wrong fails SILENTLY: an agent whose glyph is missing has no composer as far as
// InputBoxVisible is concerned, so AwaitingInput is permanently false and its queued
// prompts are neither delivered nor expired (#510 — codex draws "›" U+203A, which the
// two-glyph predicate never accepted; the glyph sat in this package's own fixtures the
// whole time and nothing ever fed it to the predicate).
type promptSet struct {
	// chars are the accepted leading glyphs, tried in order.
	chars []string
	// skipSelectorRows rejects a prompt-glyph line whose remaining text opens with a
	// numbered option ("1. …"), for an agent that draws its menu selector with the SAME
	// glyph as its composer. It is opt-in rather than global because claude's
	// "❯ 1. Yes" trust screen is deliberately allowed to read as a box — GateUp, not the
	// box check, is what excludes it (see TestTrustGateExcludedByGateNotBox), and
	// footerVisibleInSegments' scan-stop is documented against that behaviour.
	skipSelectorRows bool
}

// defaultPrompts is what an adapter that declares no set of its own resolves to: claude's
// "❯" and the plain ASCII ">" that aider and agy draw. See Adapter.InputBoxPrompts for
// why the zero value must resolve to this rather than to an empty set.
var defaultPrompts = promptSet{chars: []string{"❯", ">"}}

// selectorRowRegex matches the text of a menu selector's numbered option ("1. Yes,
// proceed"), the shape codex draws with its composer glyph on both its approval overlay
// and its trust gate.
var selectorRowRegex = regexp.MustCompile(`^\d+[.)]\s`)

// isInputBoxLine reports whether line is the interior of an agent's input box: one of
// that agent's prompt glyphs (prompts — "❯" for claude, ">" for aider and agy, "›" for
// codex), optionally inside the box's "│" side borders, possibly followed by typed text.
// The box is drawn only while no overlay is up, so reaching it while scanning upward
// proves everything above is scrolled-back transcript.
//
// The glyph set is a parameter, not a package constant, so that an agent whose menu
// chrome collides with another's composer cannot inherit it. Callers that already know
// the agent — the claude-only helpers in this package — pass defaultPrompts; the adapter
// path passes a.inputBoxPrompts().
func isInputBoxLine(line string, prompts promptSet) bool {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, "│"))
	for _, g := range prompts.chars {
		rest, ok := strings.CutPrefix(s, g)
		if !ok {
			continue
		}
		if prompts.skipSelectorRows && selectorRowRegex.MatchString(strings.TrimSpace(rest)) {
			return false
		}
		return true
	}
	return false
}

// stripBoxInterior removes an input-box interior line's side borders, its leading prompt
// glyph (one of prompts), and surrounding whitespace, leaving just the typed text. Used
// to read back what the user (or a queued-prompt send) has entered into the composer.
// The glyph must come off: the readback is compared against the prompt's signature
// (session/prompt.go boxHoldsPrompt), which does not carry it. At most one glyph is
// removed — a composer line opens with exactly one, and stripping a second would eat
// real text on an agent whose glyph is a legal first character of user input.
func stripBoxInterior(line string, prompts promptSet) string {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, "│")) // left border
	s = strings.TrimSpace(strings.TrimSuffix(s, "│")) // right border
	for _, g := range prompts.chars {
		if rest, ok := strings.CutPrefix(s, g); ok {
			return strings.TrimSpace(rest)
		}
	}
	return strings.TrimSpace(s)
}

// inputBoxText returns the text currently entered in the agent's live input box and
// whether a box is on screen at all. The box is the composer at the bottom of the pane: a
// line opening with one of the agent's prompt glyphs, optionally inside "│" side borders. Builds
// differ — claude draws a borderless interior wrapped by "─" horizontal rules; others use
// full "│"-bordered rows — so a long entry that wraps across several rows is read by joining
// every interior line below the prompt char up to the box's bottom rule (or the next box
// line), stripped of any borders and squashed to single spaces, making the readback
// width- and border-style-independent. Detection is confined to the bottom WindowPrompt
// non-empty lines (the same budget the prompt matchers use) so a prompt glyph quoted in
// the scrolled-back transcript never counts as the box.
//
// found=false means no composer is on screen. found=true with empty text means the box is
// genuinely blank; note that an otherwise-empty composer showing a placeholder/ghost
// suggestion (claude's `Try "…"` hint, codex's `Summarize recent commits`) reads that hint
// back as the text, so callers must not treat the readback as the user's input verbatim —
// they compare it against the prompt signature with a substring check (see boxHoldsPrompt)
// precisely so ghost text and the wrap point never cause a false match.
//
// The bottom-most anchor is a heuristic, not a proof of liveness: an agent that echoes the
// user's own messages into the transcript with its composer glyph (codex does) still reads
// as a box on a frame whose composer an overlay has replaced. That is why AwaitingInput
// pairs this with GateUp and DetectPrompt rather than trusting it alone.
func inputBoxText(content string, prompts promptSet) (string, bool) {
	lines := strings.Split(content, "\n")

	// Restrict to the bottom WindowPrompt non-empty lines.
	start := 0
	nonEmpty := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			nonEmpty++
			if nonEmpty == WindowPrompt {
				start = i
				break
			}
		}
	}
	window := lines[start:]

	// Anchor on the bottom-most prompt-char line; the box always sits below the
	// transcript, so the lowest prompt-glyph line is the live composer.
	anchor := -1
	for i := len(window) - 1; i >= 0; i-- {
		if isInputBoxLine(window[i], prompts) {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return "", false
	}

	// Join the wrapped interior rows below the prompt char. A "│"-bordered build and a
	// borderless one both terminate the box with a horizontal rule (the bottom border), so
	// reading until that rule — or a blank line, or a second prompt-char line (a new box) —
	// captures the whole entry without swallowing the footer that lives below the box.
	parts := []string{stripBoxInterior(window[anchor], prompts)}
	for i := anchor + 1; i < len(window); i++ {
		line := window[i]
		if strings.TrimSpace(line) == "" || isHorizontalRule(line) || isInputBoxLine(line, prompts) {
			break
		}
		parts = append(parts, stripBoxInterior(line, prompts))
	}
	text := whiteSpaceRegex.ReplaceAllString(strings.Join(parts, " "), " ")
	return strings.TrimSpace(text), true
}

// footerVisibleInSegments reports whether a live key-hint footer — recognized by the
// tokens predicate — is on screen. It exists for footers a custom multi-line statusLine
// can render *below*, pushing them out of any fixed bottom-N window — and a statusLine may
// draw its own ─── dividers, which defeats any single "below the last rule" anchor by
// becoming the last rule itself. So instead of one anchor, the pane is scanned as
// border-delimited segments, bottom-up:
//
// Segments are delimited with the loose isBoxBorderLine, not the strict isHorizontalRule.
// That matters because claude renders the session's agent-context / branch name INSIDE the
// input box's top border ("──── name ──"), which the strict predicate rejects (#332). With
// no delimiter there, the box stops opening a segment of its own: the bottom segment spans
// transcript AND box together, its first non-empty line is transcript, and the input-box
// stop below never fires — so a footer merely quoted above the box reads as live. The loose
// predicate still rejects the ╌ dialog rules and prose, so segmentation is otherwise
// unchanged, and every extra boundary it introduces only makes the stop fire sooner.
//
//   - The footer tokens must co-occur within a single segment (flattened, so a footer
//     hard-wrapped at a narrow pane width is reconstructed), which also keeps unrelated hint
//     text in neighboring segments from combining into a false footer.
//   - The scan stops at the input box interior (isInputBoxLine as a segment's first
//     non-empty line): the box and an overlay are mutually exclusive, and the live footer
//     always sits below any "❯" option pointer, so a segment opening with the prompt glyph
//     means everything above is transcript — where a quoted footer must not count. A
//     statusLine segment that itself opens with one of them stops the scan early and hides a
//     footer above it; that residual miss needs a statusLine with both a divider and a
//     prompt-char-initial line.
//   - The scan is confined to the bottom WindowPrompt non-empty lines — the same budget
//     the dialog matchers use — which caps how far a rule-bearing transcript can be
//     searched on degenerate panes that show neither a box nor an overlay, at the cost of
//     missing footers displaced by statusLines taller than that budget.
//   - With no rule on screen there is no structure to segment by; fall back to the tight
//     workChromeLines window, preserving the fixed-window behavior for minimal footers.
//   - The scan-stop uses defaultPrompts, not a per-adapter set: both callers
//     (claudeSelectionFooterVisible, claudeLocalPermissionVisible) are claude's.
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
		if isBoxBorderLine(line) {
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
			if isInputBoxLine(line, defaultPrompts) {
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
