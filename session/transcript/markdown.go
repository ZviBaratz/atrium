package transcript

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// mdStyles carries the lipgloss styles the markdown renderer applies. It is
// built once per render from the active theme (renderEntries) and injected so
// tests can pass no-op styles and assert on structure rather than ANSI bytes.
type mdStyles struct {
	Bold, Italic, Strike, Code, Link, Heading, Quote, Fence lipgloss.Style
}

// mdLine is one logical (pre-wrap) line of rendered markdown. A prose paragraph
// is a single mdLine the wrapper reflows; a list item or code-block row is its
// own mdLine. Text is already ANSI-styled content with no indentation; Marker
// is the visible lead (list bullet, quote bar) that sits at the block's base
// indent on the first visual row and hangs as spaces on wrapped rows. A blank
// mdLine (empty Text and Marker) is a paragraph separator.
type mdLine struct {
	Text   string // styled content, no leading indent
	Marker string // visible lead within the base indent ("- ", "1. ", "▌ ", "")
	NoWrap bool   // code-block row: clip, never reflow, no inline pass
}

// renderMarkdown converts a raw-markdown block (assistant or user prose) into
// styled, unwrapped logical lines. The subset matches what Claude Code's own
// renderer surfaces: inline emphasis/code/links, ATX headings, bullet and
// numbered lists (one nesting level), fenced code, blockquotes, and rules.
// Anything unrecognized passes through as literal prose — never panics, never
// hangs on malformed markup.
func renderMarkdown(src string, st mdStyles) []mdLine {
	var out []mdLine
	lines := strings.Split(src, "\n")
	inFence := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)

		// Fenced code blocks: ``` or ~~~ toggles; rows inside render verbatim
		// (no inline pass) and never wrap. A missing close fence is closed by
		// the end of the block.
		if isFence(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, mdLine{Text: st.Fence.Render(raw), NoWrap: true})
			continue
		}

		// Blank line: preserve paragraph separation.
		if trimmed == "" {
			out = append(out, mdLine{})
			continue
		}

		// Horizontal rule on its own line.
		if isRule(trimmed) {
			out = append(out, mdLine{Text: st.Quote.Render("───")})
			continue
		}

		// ATX heading: strip leading #'s, render bold, no box/margin.
		if h := headingText(trimmed); h != "" {
			out = append(out, mdLine{Text: st.Heading.Render(renderInline(h, st))})
			continue
		}

		// Blockquote.
		if q, ok := strings.CutPrefix(trimmed, ">"); ok {
			out = append(out, mdLine{
				Text:   renderInline(strings.TrimSpace(q), st),
				Marker: st.Quote.Render("▌ "),
			})
			continue
		}

		// List items (bullet or numbered). One nesting level is detected from
		// the leading-space count; the marker rides the block's base indent.
		if marker, rest, ok := listItem(raw); ok {
			if leadingSpaces(raw) >= 2 {
				marker = "  " + marker // one nesting level
			}
			out = append(out, mdLine{Text: renderInline(rest, st), Marker: marker})
			continue
		}

		// Plain paragraph.
		out = append(out, mdLine{Text: renderInline(trimmed, st)})
	}
	return out
}

// renderInline applies inline markdown to a single line with a left-to-right
// scanner (no regex: bounds cost on a 512KB tail and never backtracks). Markers
// with no close emit literally so malformed markup can't drop text.
func renderInline(s string, st mdStyles) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == '\\' && i+1 < len(runes):
			// Escape: emit the next char literally.
			b.WriteRune(runes[i+1])
			i += 2
		case r == '`':
			if j := indexRune(runes, '`', i+1); j > i {
				b.WriteString(st.Code.Render(string(runes[i+1 : j])))
				i = j + 1
			} else {
				b.WriteRune(r)
				i++
			}
		case hasMarkerAt(runes, i, "**"), hasMarkerAt(runes, i, "__"):
			m := string(runes[i : i+2])
			if j := indexMarker(runes, m, i+2); j > i {
				b.WriteString(st.Bold.Render(renderInline(string(runes[i+2:j]), st)))
				i = j + 2
			} else {
				b.WriteString(m)
				i += 2
			}
		case r == '~' && hasMarkerAt(runes, i, "~~"):
			if j := indexMarker(runes, "~~", i+2); j > i {
				b.WriteString(st.Strike.Render(renderInline(string(runes[i+2:j]), st)))
				i = j + 2
			} else {
				b.WriteString("~~")
				i += 2
			}
		case r == '*' || r == '_':
			if j := indexRune(runes, r, i+1); j > i {
				b.WriteString(st.Italic.Render(renderInline(string(runes[i+1:j]), st)))
				i = j + 1
			} else {
				b.WriteRune(r)
				i++
			}
		case r == '[':
			if text, _, end, ok := parseLink(runes, i); ok {
				b.WriteString(st.Link.Render(text))
				i = end
			} else {
				b.WriteRune(r)
				i++
			}
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

// parseLink parses a [text](url) link starting at runes[i] == '['. It returns
// the link text, the url, the index just past the closing ')', and ok. Nested
// brackets are not supported (rare in assistant prose).
func parseLink(runes []rune, i int) (text, url string, end int, ok bool) {
	close := indexRune(runes, ']', i+1)
	if close < 0 || close+1 >= len(runes) || runes[close+1] != '(' {
		return "", "", 0, false
	}
	paren := indexRune(runes, ')', close+2)
	if paren < 0 {
		return "", "", 0, false
	}
	return string(runes[i+1 : close]), string(runes[close+2 : paren]), paren + 1, true
}

// hasMarkerAt reports whether the two-rune marker m sits at runes[i].
func hasMarkerAt(runes []rune, i int, m string) bool {
	mr := []rune(m)
	if i+len(mr) > len(runes) {
		return false
	}
	for k, r := range mr {
		if runes[i+k] != r {
			return false
		}
	}
	return true
}

// indexMarker finds the next occurrence of two-rune marker m at or after start.
func indexMarker(runes []rune, m string, start int) int {
	for i := start; i+1 < len(runes); i++ {
		if hasMarkerAt(runes, i, m) {
			return i
		}
	}
	return -1
}

// indexRune finds the next r at or after start, or -1.
func indexRune(runes []rune, r rune, start int) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == r {
			return i
		}
	}
	return -1
}

// isFence reports whether a trimmed line opens or closes a code fence.
func isFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// isRule reports whether a trimmed line is a thematic break (---, ***, ___).
func isRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	c := trimmed[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != c {
			return false
		}
	}
	return true
}

// headingText returns the heading body of an ATX heading line (1–6 leading #'s
// followed by a space), or "" when the line is not a heading.
func headingText(trimmed string) string {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(trimmed) || trimmed[n] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[n+1:])
}

// listItem splits a raw line into its list marker (kept, with trailing space)
// and the item body, or ok=false when the line is not a list item. Bullets
// ("- ", "* ", "+ ") normalize to "- "; numbered markers ("1. ") are kept
// verbatim.
func listItem(raw string) (marker, rest string, ok bool) {
	s := strings.TrimLeft(raw, " ")
	for _, b := range []string{"- ", "* ", "+ "} {
		if r, found := strings.CutPrefix(s, b); found {
			return "- ", r, true
		}
	}
	// Numbered: one-or-more digits, then '.' or ')', then a space.
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n > 0 && n+1 < len(s) && (s[n] == '.' || s[n] == ')') && s[n+1] == ' ' {
		return s[:n+2], s[n+2:], true
	}
	return "", "", false
}

// leadingSpaces counts the leading ASCII spaces of raw (tabs count as one).
func leadingSpaces(raw string) int {
	n := 0
	for n < len(raw) && (raw[n] == ' ' || raw[n] == '\t') {
		n++
	}
	return n
}
