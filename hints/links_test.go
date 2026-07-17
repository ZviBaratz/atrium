package hints

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Claude Code shape: visible text equals the target. Stripping must
// match StripANSI exactly, and the span must locate the visible URL.
func TestStripWithLinks_VisibleEqualsTarget(t *testing.T) {
	raw := "PR: \x1b]8;;https://github.com/x/pull/97\x1b\\" +
		"https://github.com/x/pull/97\x1b]8;;\x1b\\ done"
	plain, spans := stripWithLinks(raw)
	assert.Equal(t, "PR: https://github.com/x/pull/97 done", plain)
	require.Len(t, spans, 1)
	assert.Equal(t, linkSpan{
		Target: "https://github.com/x/pull/97",
		Row:    0, Col: 4, Width: 28,
	}, spans[0])
}

// id= params and BEL terminators are part of the OSC 8 grammar; the params
// must not bleed into the target.
func TestStripWithLinks_IdParamsAndBel(t *testing.T) {
	raw := "see \x1b]8;id=42;https://e.com/docs\x07the docs\x1b]8;;\x07 ok"
	plain, spans := stripWithLinks(raw)
	assert.Equal(t, "see the docs ok", plain)
	require.Len(t, spans, 1)
	assert.Equal(t, linkSpan{Target: "https://e.com/docs", Row: 0, Col: 4, Width: 8}, spans[0])
}

// An unterminated link closes implicitly at end of input.
func TestStripWithLinks_UnterminatedLink(t *testing.T) {
	raw := "x \x1b]8;;https://e.com\x1b\\tail"
	plain, spans := stripWithLinks(raw)
	assert.Equal(t, "x tail", plain)
	require.Len(t, spans, 1)
	assert.Equal(t, linkSpan{Target: "https://e.com", Row: 0, Col: 2, Width: 4}, spans[0])
}

// A close marker with no open link is inert.
func TestStripWithLinks_CloseWithoutOpen(t *testing.T) {
	plain, spans := stripWithLinks("a\x1b]8;;\x1b\\b")
	assert.Equal(t, "ab", plain)
	assert.Empty(t, spans)
}

// A link whose visible text crosses a newline yields one span per line, so
// row-local hint geometry stays valid.
func TestStripWithLinks_SpanSplitsAtNewline(t *testing.T) {
	raw := "\x1b]8;;https://e.com\x1b\\ab\ncd\x1b]8;;\x1b\\!"
	plain, spans := stripWithLinks(raw)
	assert.Equal(t, "ab\ncd!", plain)
	require.Len(t, spans, 2)
	assert.Equal(t, linkSpan{Target: "https://e.com", Row: 0, Col: 0, Width: 2}, spans[0])
	assert.Equal(t, linkSpan{Target: "https://e.com", Row: 1, Col: 0, Width: 2}, spans[1])
}

// Mixed CSI styling inside the link text must not shift span coordinates.
func TestStripWithLinks_CSIInsideLink(t *testing.T) {
	raw := "> \x1b]8;;https://e.com\x1b\\\x1b[1mbold link\x1b[0m\x1b]8;;\x1b\\"
	plain, spans := stripWithLinks(raw)
	assert.Equal(t, "> bold link", plain)
	require.Len(t, spans, 1)
	assert.Equal(t, linkSpan{Target: "https://e.com", Row: 0, Col: 2, Width: 9}, spans[0])
}

// The hyperlink target is authoritative: when visible text shows something
// else entirely, the hint copies the target, not the label text.
func TestNewScreen_HyperlinkTargetAuthoritative(t *testing.T) {
	raw := "docs: \x1b]8;;https://example.com/deep/page\x1b\\click here\x1b]8;;\x1b\\"
	s := NewScreen(raw, 80, 10)
	require.Equal(t, 1, s.MatchCount())
	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	assert.Equal(t, "https://example.com/deep/page", m.Text)
	assert.Equal(t, KindURL, m.Kind)
}

// When the visible text IS the URL (Claude Code), the regex scanner would
// re-find it inside the link span; the merge must yield exactly one match,
// not a duplicate pair.
func TestNewScreen_HyperlinkDedupsVisibleURL(t *testing.T) {
	raw := "PR: \x1b]8;;https://github.com/x/pull/97\x1b\\" +
		"https://github.com/x/pull/97\x1b]8;;\x1b\\"
	s := NewScreen(raw, 80, 10)
	require.Equal(t, 1, s.MatchCount())
	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	assert.Equal(t, "https://github.com/x/pull/97", m.Text)
}

// file:// targets (eza-style listings) surface as paths — the user wants
// the decoded filesystem path on the clipboard, and "open in browser" makes
// no sense for them.
func TestNewScreen_FileTargetBecomesPath(t *testing.T) {
	raw := "\x1b]8;;file:///home/u/my%20doc.pdf\x1b\\doc.pdf\x1b]8;;\x1b\\"
	s := NewScreen(raw, 80, 10)
	require.Equal(t, 1, s.MatchCount())
	m, _ := s.Resolve("a")
	require.NotNil(t, m)
	assert.Equal(t, "/home/u/my doc.pdf", m.Text)
	assert.Equal(t, KindPath, m.Kind)
}

// The label and highlight must stay within the link's visible span even
// though the copyable target is longer than the text on screen.
func TestRender_HyperlinkSpliceStaysInSpan(t *testing.T) {
	raw := "go \x1b]8;;https://example.com/deep/page\x1b\\here\x1b]8;;\x1b\\ now"
	s := NewScreen(raw, 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())
	// A URL match now carries a zero-width OSC 8 hyperlink to its target, so the
	// raw bytes gained the escapes — but the visible layout is byte-for-byte
	// unchanged once stripped: label 'a' in the gutter, the "here" span intact.
	assert.Equal(t, "goahere now", ansi.Strip(out),
		"label sits in the gutter; the visible span stays intact")
	assert.Equal(t, "go"+"a"+ansi.SetHyperlink("https://example.com/deep/page")+"here"+ansi.ResetHyperlink()+" now", out,
		"the span is wrapped in an OSC 8 link to the copyable target")
}
