package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// plainStyles is a no-op style set: renderInline/renderMarkdown structure can be
// asserted on the visible text without matching ANSI byte sequences.
func plainStyles() mdStyles {
	n := lipgloss.NewStyle()
	return mdStyles{Bold: n, Italic: n, Strike: n, Code: n, Link: n, Heading: n, Quote: n, Fence: n}
}

func TestRenderInlineStripsMarkers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "bold"},
		{"__bold__", "bold"},
		{"*italic*", "italic"},
		{"_italic_", "italic"},
		{"~~strike~~", "strike"},
		{"`code`", "code"},
		{"a **b** and `c` and [link](http://x)", "a b and c and link"},
		{"escaped \\*not italic\\*", "escaped *not italic*"},
		{"unterminated **bold", "unterminated **bold"}, // no close: literal
		{"nested **a `b` c**", "nested a b c"},
	}
	for _, tc := range cases {
		got := ansi.Strip(renderInline(tc.in, plainStyles()))
		if got != tc.want {
			t.Errorf("renderInline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderInlineStyling(t *testing.T) {
	// With the real theme style set the markers are stripped and the visible
	// text is intact. (Whether ANSI bytes are emitted depends on the terminal
	// color profile, which is absent under `go test`, so only structure is
	// asserted here.)
	out := renderInline("a **bold** word", mdStyleSet())
	if got := ansi.Strip(out); got != "a bold word" {
		t.Errorf("renderInline stripped = %q, want %q", got, "a bold word")
	}
}

func TestRenderMarkdownLists(t *testing.T) {
	src := "intro\n\n- first\n- second item\n\n1. one\n2. two"
	lines := renderMarkdown(src, plainStyles())
	var visible []string
	for _, ml := range lines {
		visible = append(visible, ansi.Strip(ml.Marker)+ansi.Strip(ml.Text))
	}
	joined := strings.Join(visible, "\n")
	for _, want := range []string{"- first", "- second item", "1. one", "2. two"} {
		if !strings.Contains(joined, want) {
			t.Errorf("list rendering missing %q\n---\n%s", want, joined)
		}
	}
}

func TestRenderProseHangingIndentAndBullet(t *testing.T) {
	const width = 30
	// A long assistant paragraph then a list, leads with "● ".
	src := "This is a reasonably long assistant paragraph that must wrap.\n\n- a list item that is also long enough to wrap onto another row"
	out := renderProse(src, "● ", width, plainStyles())
	rows := strings.Split(out, "\n")
	if !strings.HasPrefix(rows[0], "● ") {
		t.Errorf("first row must lead with bullet: %q", rows[0])
	}
	for i, row := range rows {
		if w := lipgloss.Width(row); w > width {
			t.Errorf("row %d exceeds width %d (%d): %q", i, width, w, row)
		}
		if i > 0 && row != "" && strings.HasPrefix(row, "●") {
			t.Errorf("only the first row may carry the bullet, row %d: %q", i, row)
		}
	}
	if !strings.Contains(ansi.Strip(out), "- a list item") {
		t.Errorf("list marker missing:\n%s", ansi.Strip(out))
	}
}

func TestRenderMarkdownFenceNoInline(t *testing.T) {
	src := "```\nx := **not bold** `kept`\n```"
	lines := renderMarkdown(src, plainStyles())
	var got string
	for _, ml := range lines {
		if ml.NoWrap {
			got = ansi.Strip(ml.Text)
		}
	}
	if got != "x := **not bold** `kept`" {
		t.Errorf("fence content should be verbatim, got %q", got)
	}
}
