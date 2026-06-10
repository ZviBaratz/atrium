package hints

import (
	"net/url"
	"regexp"
	"strings"
)

// linkSpan is one visible run of text covered by an OSC 8 hyperlink, located
// in stripped-text coordinates (Row = line, Col = first rune's index, Width
// = rune count). Target is the hyperlink's URI — authoritative even when the
// visible text shows something else (eza's bare filenames, shortened link
// labels).
type linkSpan struct {
	Target string
	Row    int
	Col    int
	Width  int
}

// osc8RE matches one OSC 8 hyperlink sequence: ESC ] 8 ; params ; target,
// terminated by BEL or ST (ESC \). Group 1 is the params field (id=…),
// group 2 the target; an empty target closes the hyperlink.
var osc8RE = regexp.MustCompile(`\x1b\]8;([^;\x07\x1b]*);([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// stripWithLinks strips raw exactly like StripANSI and additionally reports
// the visible spans OSC 8 hyperlinks cover. Splitting on the OSC 8 sequences
// first and StripANSI-ing each segment keeps text and span coordinates in
// lockstep without a hand-rolled VT parser. Spans split at newlines (hint
// geometry is row-local); an unterminated link closes implicitly at end of
// input; a close with no open link is inert.
func stripWithLinks(raw string) (string, []linkSpan) {
	locs := osc8RE.FindAllStringSubmatchIndex(raw, -1)
	if locs == nil {
		return StripANSI(raw), nil
	}
	var (
		b      strings.Builder
		spans  []linkSpan
		row    int
		col    int
		target string
		cur    *linkSpan
	)
	closeSpan := func() {
		if cur != nil && cur.Width > 0 {
			spans = append(spans, *cur)
		}
		cur = nil
	}
	openSpan := func() {
		if target != "" {
			cur = &linkSpan{Target: target, Row: row, Col: col}
		}
	}
	emit := func(segment string) {
		for _, r := range StripANSI(segment) {
			if r == '\n' {
				closeSpan()
				row++
				col = 0
				b.WriteRune('\n')
				openSpan()
				continue
			}
			if cur != nil {
				cur.Width++
			}
			col++
			b.WriteRune(r)
		}
	}
	last := 0
	for _, loc := range locs {
		emit(raw[last:loc[0]])
		closeSpan()
		target = raw[loc[4]:loc[5]]
		openSpan()
		last = loc[1]
	}
	emit(raw[last:])
	closeSpan()
	return b.String(), spans
}

// linkMatch converts a span into a match candidate. file:// targets become
// decoded filesystem paths — that is what the user wants on the clipboard,
// and browser-open is meaningless for them; everything else stays a URL
// whose copyable text is the target itself.
func linkMatch(sp linkSpan) Match {
	m := Match{Text: sp.Target, Kind: KindURL, Row: sp.Row, Col: sp.Col, Width: sp.Width}
	if rest, ok := strings.CutPrefix(sp.Target, "file://"); ok {
		if dec, err := url.PathUnescape(rest); err == nil {
			rest = dec
		}
		m.Text, m.Kind = rest, KindPath
	}
	return m
}
