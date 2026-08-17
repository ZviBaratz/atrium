package agent

import (
	"regexp"
	"strings"
	"unicode/utf8"
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

// isBoxWallLine reports whether the cleaned line is the INTERIOR of a box: a line carrying
// the box's own left and right side walls. It is what bounds bottomBoxBlock, and it is a
// deliberately different question from isHorizontalRule (a horizontal EDGE) and from
// isBoxBorderLine (either edge, tolerating an embedded label).
//
// A blank interior row still carries its walls in every GEMINI pane this repo has captured —
// see gemini_pane_test.go, where the padding rows inside the trust dialog render as "│      │"
// at every driven width — so requiring them costs nothing there. That scope is the claim; an
// earlier draft said "every pane this repo has captured", which claude's disprove: claude draws
// a BORDERLESS interior bracketed by "─" rules (inputBoxText's doc below, and claudeTrustPane
// in registry_test.go), so no row of it is walled, blank or otherwise. bottomBoxBlock therefore
// cannot anchor a claude-shaped box at all — this predicate is gemini-shaped, and an adapter
// author reading it as general would find nothing. An agent that pads a box with
// genuinely empty lines loses the rows above that padding, and when the padding sits directly
// above the border it loses the block entirely: bottomBoxBlock returns (nil, false) and the
// gate goes DOWN, not short. Both are the fail-safe direction — a missed gate is #713, a false
// one is #342 — but it is a total loss rather than a partial one, which an earlier draft of
// this paragraph understated as "would truncate the block at that row".
// TestBottomBoxBlockLosesTheBlockToAnUnwalledPaddingRow.
//
// Only the LIGHT wall is accepted. An earlier draft also took the heavy '┃', which was dead
// code the compiler cannot see: isHorizontalRule accepts no heavy glyph, so '┗━━┛' is not a
// rule, no heavy box can present a bottom border, and bottomBoxBlock could never reach a
// heavy wall to test it. Accepting it advertised support the anchor does not deliver. An
// adapter that draws heavy boxes needs both halves taught together, plus a driven capture.
// TestBottomBoxBlockDoesNotAnchorAHeavyBox.
func isBoxWallLine(line string) bool {
	line = strings.TrimSpace(line)
	first, size := utf8.DecodeRuneInString(line)
	if size == 0 || size == len(line) {
		return false // empty, or a single rune that would be both walls at once
	}
	last, _ := utf8.DecodeLastRuneInString(line)
	return first == '│' && last == '│'
}

// isBoxBottomBorder reports whether line is a box's BOTTOM edge: a horizontal rule that opens
// with a bottom-left corner. isHorizontalRule alone is too weak to anchor a claim that a box
// ENDS here — it accepts a bare "────" the agent printed as a markdown rule or a table edge,
// and it accepts a box's own interior divider "├────┤", which means the lower half of the box
// is missing and the frame is torn or mid-repaint. Both used to anchor bottomBoxBlock.
//
// BOTH corners are required, and an earlier draft required only the left one on the reasoning
// that everything narrow in these captures is cut from the RIGHT (the option rows at width 20),
// so a right-truncated frame is the more plausible unseen shape. That rationale was self-
// defeating: truncation cuts every row at the same column, so a frame missing its right corner
// is missing its right WALL too, and isBoxWallLine requires both walls. bottomBoxBlock would
// find such a border, walk up, match no wall, and return (nil, false) — the loose corner bought
// the anchor nothing on the one shape it was loosened for. Measured before the change:
// isBoxBottomBorder(" ╰──────") true, isBoxWallLine(" │ ● 1. Trust folder (repo)") false, block
// lost. Requiring both corners is therefore inert for bottomBoxBlock, its only caller, and it
// stops the pair documenting two different beliefs about what a truncated box looks like.
// Reaching a right-truncated frame needs isBoxWallLine loosened in the same commit, with a
// driven capture of one — no rung has produced one yet.
// TestBottomBoxBlockNeedsABottomCornerNotAnyRule, TestBoxPredicatesAgreeOnATruncatedFrame.
func isBoxBottomBorder(line string) bool {
	if !isHorizontalRule(line) {
		return false
	}
	trimmed := strings.TrimSpace(line)
	first, _ := utf8.DecodeRuneInString(trimmed)
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	return (first == '╰' || first == '└') && (last == '╯' || last == '┘')
}

// trailingBelowBoxCap is how many pane ROWS may sit BELOW a box's bottom border while that box
// still counts as the pane's live bottom-most element.
//
// ROWS, not rendered content: a blank row spends the budget exactly as a written one does,
// because bottomBoxBlock walks array positions up from the last non-empty line. That is
// deliberate and it is the half of this constant with teeth, so it is stated before the count.
// gemini's FolderTrustDialog renders three siblings below its box, and the blank row is what
// tells them apart. <ShowMoreLines> is wrapped in a Box carrying paddingX and marginBottom and
// NO marginTop, so the overflow hint lands on the row directly beneath the border. Its two
// siblings — the isRestarting and exiting notices — each carry marginTop: 1, so each renders
// with a blank row above it (all three read off the FolderTrustDialog render in
// interactiveCli-*.js). Counting non-empty rows instead would step over that blank and hold the
// gate UP on a dialog the user has just ANSWERED, which is the #342 direction on a real gemini
// screen. TestBottomBoxBlockSpendsTheAllowanceOnABlankRow.
//
// The count is 1 because a dialog that overflows its pane draws exactly one row under itself,
// and because 0 shipped #713 a second time. The blurb sits in a MaxSizedBox capped at
// max(4, terminalHeight-12), and <ShowMoreLines> renders when that overflows; the component
// returns null otherwise and is otherwise a single <Text wrap="truncate"> — one line, never
// wrapped, truncated instead ("Press Ctrl+O to s…" at width 24). So the overflow hint cannot be
// two rows, and a cap of 1 admits it exactly.
//
// The zero-line form was measured against 12 natively-driven gemini 0.55.1 panes and missed
// the gate on 7 — every geometry where the dialog overflows, including the 45x19 agent pane a
// plain 70x24 terminal produces at the default split, and the 28x19 an 80-column terminal
// produces with the list dragged wide. Miss → PaneIdle → Ready → the false completion ding
// #713 is about. geminiTrustGateOverflowLadder holds two of those panes verbatim.
//
// The cost is one line of transcript tolerance in the other direction: quoted box art followed
// by a single rendered line now gates where it previously did not. That exposure is the same
// one bottomBoxBlock already discloses below, one line wider, and it is asserted rather than
// described — TestGeminiTrustGateFiresOnQuotedBoxArtEndingThePane. Raising this cap widens it
// proportionally, so a future adapter needing more should carry its own driven capture.
const trailingBelowBoxCap = 1

// bottomBoxBlock returns the interior lines of the pane's bottom-most box, and true only when
// that box's bottom border is the last non-empty line on screen or sits trailingBelowBoxCap
// lines above it. It is the liveness anchor
// for a modal dialog the way footerBelowBox is for claude's footer, and the two halves do
// different jobs:
//
//   - "the border all but ends the pane" separates an OPEN dialog from a dismissed one. An
//     agent that answers a modal draws its composer below where the modal was, and a composer
//     is a box — more than trailingBelowBoxCap lines — so this returns false once it appears.
//     Without the half a dialog left in scrollback keeps matching forever, which for a startup
//     gate means the queued first prompt is never delivered (registry_test.go's TestAgyTrustGate
//     pins the same property for agy, which earns it by REPLACING the screen instead). The cap
//     is what keeps this from also rejecting a dialog that is still open and merely overflowing;
//     it costs the tolerance of one trailing line, which is why it is a named constant with its
//     evidence attached rather than a bare "last non-empty line".
//   - "walled above it" separates a live dialog from ordinary transcript. A quoted phrase in
//     the conversation body carries no side walls, so it cannot reach the returned block —
//     which a bottom-N window (liveChromeLines) cannot promise, because the transcript scrolls
//     through it.
//
// That second half is a narrowing, not a guarantee, and an earlier draft of this doc claimed
// the guarantee ("it cannot reach the returned block", full stop). Quoted BOX ART carries
// walls and a corner border like any other box, so a transcript that quotes a dialog — this
// repo's own fixtures, a PR body, a review comment — and happens to end at the quoted bottom
// border does yield its interior, and gemini's gate fires on it. Measured, not reasoned:
// TestGeminiTrustGateFiresOnQuotedBoxArtEndingThePane. What the anchor buys is the size of the
// target: not "the phrase appears in the last N lines" but "the phrase is inside a box whose
// bottom border is the last non-empty line on screen, or one line above it", which no line of
// prose satisfies and a
// quoted frame satisfies only while it is the final thing rendered. It is transient where the
// window form was persistent, but it is not zero, and a liveness anchor built out of pane text
// cannot make it zero.
//
// The block is the contiguous run of isBoxWallLine rows immediately above that border, and
// NOT everything back to a matching top border. Both alternatives were measured against the
// captured dialogs, and the top-border scan loses twice:
//
//   - HEIGHT. The agent's tmux pane is sized to the preview pane, not to the terminal
//     (session/instance.go SetPreviewSize ← ui/tabbed_window.go SetSize), so it is a few rows
//     shorter than the terminal and about half as wide. gemini's trust dialog BOX is 28 rows
//     at width 24 and 33 at width 20 — geminiDialogRows in gemini_pane_test.go owns those
//     numbers and computes them from the captures; this sentence used to give 37, which is the
//     width-24 CAPTURE's height, a different measurement off by nine rows in the unsafe
//     direction. A pane shorter than the box has scrolled its own top border off, leaving a
//     scan that demands one to find nothing and take the gate down. Measured on the committed
//     capture: the top-border form went false at pane height 25 for the width-24 rung and 15
//     for the width-40 rung. That is #713's exact symptom (missed gate → Ready → false
//     completion ding) reintroduced on the other axis, at ordinary terminal sizes.
//     TestGeminiTrustGateSurvivesADialogTallerThanThePane.
//   - BOUNDS. Two rules with anything at all between them made that span "box interior".
//     Measured: 60 transcript lines bracketed by a pair of plain "────" rules returned the
//     whole span, so a quoted row 60 lines up fired the gate. The wall run needs no
//     gateRegionCap/aboveBoxBlockCap equivalent because it terminates itself at the first
//     line that is not box interior. TestGeminiTrustGateIgnoresTranscriptBetweenTwoRules,
//     which closes the span with a real "╰───╯" rather than the plain rule the measurement
//     used: since isBoxBottomBorder a plain rule fails the anchor outright, and a guard that
//     died there would stop testing the wall run it exists for.
//
// Interior lines are returned as lines, deliberately unjoined and unflattened: a caller
// matching a literal gets no cross-line synthesis, so two adjacent wrapped lines cannot
// manufacture a phrase neither of them renders. A caller that needs wrap repair should join
// and flatten the result itself, and accept that cost knowingly.
//
// Input must already be cleaned for detection (ANSI stripped), like every other predicate
// here. The bottom border is matched with isBoxBottomBorder, which is isHorizontalRule plus a
// bottom-left corner — so neither a bare rule the agent printed nor a box's own interior
// divider anchors this, and a border carrying an embedded title ("──── name ──", which claude
// renders and aboveBoxBlock uses the loose isBoxBorderLine for) does not either: this returns
// false rather than reading past it. gemini's dialog draws an untitled, corner-terminated
// border at every captured width IN THE DEFAULT RENDER, which is the qualifier this sentence
// used to omit. 0.55.1 ships a second FolderTrustDialog layout behind the useAlternateBuffer
// setting (shouldEnterAlternateScreen(useAlternateBuffer, isScreenReader), defined in
// bundle/chunk-TBDX7VEE.js): there the title sits in its own bordered header, the body box
// carries borderTop/borderBottom false — walls only — and the bottom edge is a separate
// {height: 0, borderBottom: true, borderStyle: "round"} box. That shape still ends in a round
// bottom border above walled rows, so this anchor plausibly holds for it; plausibly is the
// whole claim, because all 12 driven captures came back in the default shape and the alternate
// one has never been driven. An adapter whose dialog is titled needs the loose predicate and
// its own guard. Returns (nil, false) when no bottom border sits within trailingBelowBoxCap
// lines of the last non-empty line, or when nothing walled sits above that border.
func bottomBoxBlock(content string) ([]string, bool) {
	lines := strings.Split(content, "\n")

	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
			break
		}
	}
	if last < 0 {
		return nil, false
	}

	border := -1
	for i := last; i >= 0 && last-i <= trailingBelowBoxCap; i-- {
		if isBoxBottomBorder(lines[i]) {
			border = i
			break
		}
	}
	if border < 0 {
		return nil, false
	}
	start := border
	for start > 0 && isBoxWallLine(lines[start-1]) {
		start--
	}
	if start == border {
		return nil, false
	}
	return lines[start:border], true
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
// A menu selector drawn with the same glyph is deliberately NOT excluded here. Codex draws
// both its composer and its menu selector with "›", and the shapes are not separable: a
// queued prompt that is a numbered list renders as "› 1. refactor the parser" / "  2. add a
// regression test", which is byte-identical in shape to the trust gate's "› 1. Yes,
// continue" / "  2. No, quit" (both captured live at 0.147.0, widths 120 and 20 — see
// codexNumberedComposerPane*). Any rule that rejects the menu therefore also rejects a real
// prompt, and rejecting a real prompt is worse than accepting the menu: the box check is not
// the guard that keeps a queued prompt off a menu — GateUp and DetectPrompt are (see
// Adapter.InputBoxVisible and Adapter.GateWindow).
type promptSet []string

// defaultPrompts is what an adapter that declares no set of its own resolves to: claude's
// "❯" and the plain ASCII ">" that aider and agy draw. See Adapter.InputBoxPrompts for
// why the zero value must resolve to this rather than to an empty set.
var defaultPrompts = promptSet{"❯", ">"}

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
	for _, g := range prompts {
		if strings.HasPrefix(s, g) {
			return true
		}
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
	for _, g := range prompts {
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
