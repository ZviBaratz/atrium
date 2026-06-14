package agent

import (
	"strings"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
)

// Ghost-suggestion detection for claude. Claude Code (prompt suggestions,
// present since ~2.1.13x) renders a predicted next prompt as dim text after
// the "❯" inside the idle input box. There is no hint string, no hook event,
// and no other machine-readable signal in interactive mode (verified against
// the 2.1.175 binary and docs, 2026-06-12): the SGR dim attribute is the ONLY
// thing distinguishing a ghost suggestion from draft text the user typed —
// which is exactly the text a follow-up Enter must never submit. So unlike
// every other heuristic in this package, this one reads the RAW capture, ANSI
// intact, and the gate fails closed: when in doubt nothing is dim, nothing is
// accepted, and the worst outcome of a stale heuristic is "a does nothing on
// an idle claude" — never a stray keystroke.
//
// Live fixture provenance (suggestion_test.go pins these bytes): claude
// 2.1.17x via `tmux capture-pane -p -e -J`, 2026-06-12 —
//
//	\x1b[39m❯ \x1b[2mGo ahead and resolve the threads, then merge if unblocked\x1b[0m
//
// with the box delimited above and below by ─── rule lines and no side
// borders (older claude drew "│" sides; the cell walk tolerates both).

// claudeSuggestionVisible backs the claude adapter's SuggestionVisible: it
// locates the input box structurally and reports whether its interior is
// non-empty and entirely dim. raw is the uncleaned capture.
//
// The box is found by scanning adjacent rule pairs bottom-up and requiring
// the interior's first non-empty line to be the box's "❯" line — the same
// structural trick as footerVisibleInSegments, and for the same reason: a
// custom statusLine below the box may draw its own ─── divider and become the
// last rule on screen, so "between the last two rules" is not robust.
func claudeSuggestionVisible(raw string) bool {
	lines := strings.Split(raw, "\n")
	cleaned := make([]string, len(lines))
	for i, l := range lines {
		cleaned[i] = strings.TrimRight(xansi.Strip(l), " \t")
	}

	var rules []int
	for i, l := range cleaned {
		if isBoxBorderLine(l) {
			rules = append(rules, i)
		}
	}

	for k := len(rules) - 1; k >= 1; k-- {
		for i := rules[k-1] + 1; i < rules[k]; i++ {
			if cleaned[i] == "" {
				continue
			}
			if !isInputBoxLine(cleaned[i]) {
				// This rule pair brackets something else (a statusLine, a
				// dialog); the box, if any, is in a pair further up.
				break
			}
			return boxInteriorAllDim(lines[i], cleaned[i])
		}
	}
	return false
}

// isBoxBorderLine reports whether the cleaned line is an input-box border for
// the purposes of this detector: after an optional corner/side prefix it
// begins with a run of at least 3 horizontal dashes. Deliberately looser than
// chrome.go's isHorizontalRule (whose other callers depend on "pure rule
// only"): a session with a named agent context renders the name INSIDE the
// top border ("──── context-name ──", observed live 2026-06-12), which the
// strict predicate rejects — and that would make every such session read as
// suggestion-less. The loosening is safe here because the border only locates
// the box; the dim gate still decides.
func isBoxBorderLine(line string) bool {
	line = strings.TrimSpace(line)
	dashes := 0
	for _, r := range line {
		switch r {
		case '─':
			dashes++
			continue
		case '╭', '╮', '╰', '╯', '│', '┌', '┐', '└', '┘', '├', '┤':
			if dashes == 0 {
				continue // corner/side prefix before the dash run
			}
		}
		break
	}
	return dashes >= 3
}

// boxInteriorAllDim reports whether the input-box line's content after the
// prompt char is non-empty and rendered entirely with the SGR dim attribute.
// rawLine carries the escapes; cleanedLine is its stripped counterpart, used
// only to recognize the older bordered style (trailing "│").
func boxInteriorAllDim(rawLine, cleanedLine string) bool {
	cells := sgrCells(rawLine)

	// Older bordered box style: drop the trailing "│" (and padding) so the
	// border glyph doesn't count as a non-dim visible char.
	if strings.HasSuffix(cleanedLine, "│") {
		for len(cells) > 0 {
			last := cells[len(cells)-1]
			cells = cells[:len(cells)-1]
			if last.r == '│' {
				break
			}
		}
	}

	// Find the prompt char: "❯" wherever it is, or ">" as the first visible
	// non-border char (matching isInputBoxLine's two accepted prompts).
	start := -1
	for i, c := range cells {
		if c.r == '❯' {
			start = i + 1
			break
		}
		if unicode.IsSpace(c.r) || c.r == '│' {
			continue
		}
		if c.r == '>' {
			start = i + 1
		}
		break
	}
	if start < 0 {
		return false
	}

	seen := false
	for _, c := range cells[start:] {
		// unicode.IsSpace, not == ' ': claude pads the prompt char with a
		// NO-BREAK SPACE (U+00A0), which renders before the dim sequence
		// starts and must read as padding, not as a non-dim visible char.
		if unicode.IsSpace(c.r) {
			continue
		}
		if !c.dim {
			return false
		}
		seen = true
	}
	return seen
}

// sgrCell is one visible rune of a pane line with the dim state it was
// rendered under.
type sgrCell struct {
	r   rune
	dim bool
}

// sgrCells walks a raw pane line, tracking the SGR dim attribute (CSI ... m:
// parameter 2 sets dim; 0, the empty parameter, and 22 clear it — including
// inside combined sequences like ESC[2;39m) and skipping every other escape
// sequence. capture-pane -e also re-emits OSC 8 hyperlinks (ESC ] 8 ; ; URL
// ... ST), so the OSC payload is skipped wholesale — otherwise the URL bytes
// would leak in as non-dim visible cells and could turn a genuine all-dim
// suggestion into a false negative.
func sgrCells(line string) []sgrCell {
	var cells []sgrCell
	dim := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '\x1b' {
			cells = append(cells, sgrCell{r: runes[i], dim: dim})
			continue
		}
		// OSC (ESC ]): skip the whole control string up to its terminator —
		// BEL, or ST (ESC \) — so a hyperlink's URL never reads as content.
		if i+1 < len(runes) && runes[i+1] == ']' {
			j := i + 2
			for j < len(runes) {
				if runes[j] == '\x07' {
					break // BEL terminator
				}
				if runes[j] == '\x1b' && j+1 < len(runes) && runes[j+1] == '\\' {
					j++ // ST terminator: consume the ESC, the '\' is consumed by i++
					break
				}
				j++
			}
			i = j
			continue
		}
		if i+1 >= len(runes) || runes[i+1] != '[' {
			continue // bare ESC (or other introducer): drop it, keep walking
		}
		j := i + 2
		for j < len(runes) && (runes[j] == ';' || runes[j] == '?' || (runes[j] >= '0' && runes[j] <= '9')) {
			j++
		}
		if j < len(runes) && runes[j] == 'm' {
			for _, p := range strings.Split(string(runes[i+2:j]), ";") {
				switch p {
				case "2":
					dim = true
				case "", "0", "22":
					dim = false
				}
			}
		}
		i = j // skip the sequence (final byte consumed by the loop increment)
	}
	return cells
}
