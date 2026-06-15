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
		// Intraword underscores are literal (CommonMark): identifiers in prose
		// must survive intact, not be eaten as italic delimiters.
		{"set the file_path_prefix value", "set the file_path_prefix value"},
		{"call foo_bar_baz() now", "call foo_bar_baz() now"},
		// An emphasis run must be left-flanking: a '*' followed by a space is a
		// literal asterisk, not an opener that mis-pairs with a later "**".
		{"a * b **c** d", "a * b c d"},
		// Underscore emphasis still works at word boundaries.
		{"an _emphasized_ word", "an emphasized word"},
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

// markerOf returns the stripped marker of the first non-blank rendered line.
func firstMarker(lines []mdLine) string {
	for _, ml := range lines {
		if ml.Text != "" || ml.Marker != "" {
			return ansi.Strip(ml.Marker)
		}
	}
	return ""
}

// TestRenderMarkdownBlockquoteRequiresSpace: a real blockquote ("> quoted")
// renders with the "▌ " bar, but a comparison or redirection line that merely
// starts with '>' ("- >= 3", "> out.txt" without the markdown space) is NOT a
// blockquote — otherwise prose math and shell snippets lose their leading '>'.
func TestRenderMarkdownBlockquoteRequiresSpace(t *testing.T) {
	if m := firstMarker(renderMarkdown("> quoted text", plainStyles())); m != "▌ " {
		t.Errorf("`> quoted` should be a blockquote, marker=%q", m)
	}
	if m := firstMarker(renderMarkdown(">= 3 required", plainStyles())); m == "▌ " {
		t.Errorf("`>= 3 required` must NOT be a blockquote")
	}
	// The '>' must survive in the rendered text of the non-quote line.
	lines := renderMarkdown(">= 3 required", plainStyles())
	if got := ansi.Strip(lines[0].Text); got != ">= 3 required" {
		t.Errorf("comparison line mangled: %q", got)
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
