// hints/render_test.go
package hints

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainStyles render no escape codes, so position assertions read literally.
func plainStyles() Styles { return Styles{} }

// The label renders in the gutter — the blank cell(s) immediately left of
// the match — so the full match text stays readable (overlaying the first
// rune reads as corruption: "sf1f06a").
func TestRender_LabelInGutterBeforeMatch(t *testing.T) {
	s := NewScreen("go to /tmp/file.go now", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())
	assert.Equal(t, "go toa/tmp/file.go now", out,
		"label 'a' must occupy the space before the match")
}

// No gutter at column 0: fall back to overlaying the match's first rune.
func TestRender_ColumnZeroFallsBackToOverlay(t *testing.T) {
	s := NewScreen("/tmp/x.go", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	assert.Equal(t, "atmp/x.go", s.Render("", plainStyles()))
}

// A non-space predecessor (the sha inside a git-describe token) also falls
// back to overlay — the gutter never eats a visible glyph.
func TestRender_NonSpacePredecessorFallsBackToOverlay(t *testing.T) {
	s := NewScreen("tag g5441edb", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	assert.Equal(t, "tag ga441edb", s.Render("", plainStyles()))
}

// Placement is decided on the FULL label so it never flips between gutter
// and overlay while typing; the remaining suffix right-aligns against the
// match start, freed cells reverting to backdrop spaces.
func TestRender_TwoCharLabelGutterAndNarrowing(t *testing.T) {
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("  /dir/file%02d", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	require.Equal(t, 27, s.MatchCount())

	rows := strings.Split(s.Render("", plainStyles()), "\n")
	assert.Equal(t, "ns/dir/file00", rows[0], "two-char label fills the two-space gutter")
	assert.Equal(t, " a/dir/file26", rows[26], "one-char label right-aligns in it")

	rows = strings.Split(s.Render("n", plainStyles()), "\n")
	assert.Equal(t, " s/dir/file00", rows[0], "suffix right-aligns; freed cell is a space again")
	assert.Equal(t, "  /dir/file02", rows[2], "filtered label leaves plain text")
}

// A two-char label with only one blank cell cannot use the gutter: overlay.
func TestRender_GutterTooSmallFallsBackToOverlay(t *testing.T) {
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf(" /dir/file%02d", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	rows := strings.Split(s.Render("", plainStyles()), "\n")
	assert.Equal(t, " nsir/file00", rows[0], "two-char label overlays the match start")
	assert.Equal(t, "a/dir/file26", rows[26], "one-char label still fits the one-space gutter")
}

// Typing a valid prefix consumes it: matching labels show only their
// remaining suffix over the match start, and matches whose labels no longer
// fit the prefix lose their decoration entirely.
func TestRender_TypedPrefixNarrows(t *testing.T) {
	// 27 distinct paths force two-char labels. Bottom-up assignment over
	// Alphabet ("asdf…ybn") gives: row 26 -> "a", …, row 1 -> "na", row 0 -> "ns"
	// ('n' is the popped expansion char; its group follows alphabet order).
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("/dir/file%02d", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	require.Equal(t, 27, s.MatchCount())

	rows := strings.Split(s.Render("n", plainStyles()), "\n")
	// Rows 0 and 1 keep their hints, narrowed to the remaining suffix
	// rendered over the match's first rune.
	assert.Equal(t, "sdir/file00", rows[0], `row 0's label "ns" narrows to "s"`)
	assert.Equal(t, "adir/file01", rows[1], `row 1's label "na" narrows to "a"`)
	// Every other row's label no longer matches the prefix: plain text again.
	assert.Equal(t, "/dir/file02", rows[2])
	assert.Equal(t, "/dir/file26", rows[26])
}

// URL matches render through MatchURL (underlined by the app) so the user
// can see which hints the open variant works on before pressing uppercase.
// Transform-based styles make the routing observable without depending on
// the terminal color profile.
func TestRender_URLMatchesUseURLStyle(t *testing.T) {
	st := Styles{MatchURL: lipgloss.NewStyle().Transform(strings.ToUpper)}
	s := NewScreen("x https://e.com/a y\n/tmp/path.go", 80, 10)
	out := s.Render("", st)
	assert.Contains(t, out, "TTPS://E.COM/A",
		"URL text (after its label rune) must go through MatchURL")
	assert.Contains(t, out, "tmp/path.go",
		"non-URL matches keep the plain Match style")
}

// Lines with no matches are passed through verbatim (modulo styling).
func TestRender_PlainLinesUntouched(t *testing.T) {
	s := NewScreen("no matches here\n/tmp/x.go", 80, 10)
	out := strings.Split(s.Render("", plainStyles()), "\n")
	assert.Equal(t, "no matches here", out[0])
}

// A URL match is wrapped in an OSC 8 hyperlink to its copyable target, so it is
// clickable on supporting terminals — and the escapes add zero display columns,
// so the frozen screen's alignment is unchanged (the no-drift guard). With
// plainStyles the only escapes present are the OSC 8 wrapper, so equal display
// and rune widths prove the wrapper is weightless.
func TestRender_URLMatchHyperlinkNoWidthDrift(t *testing.T) {
	s := NewScreen("see https://example.com/p now", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())

	require.Contains(t, out, ansi.SetHyperlink("https://example.com/p"), "URL match opens an OSC 8 link")
	require.Contains(t, out, ansi.ResetHyperlink(), "and closes it")
	for _, ln := range strings.Split(out, "\n") {
		require.Equal(t, len([]rune(ansi.Strip(ln))), ansi.StringWidth(ln),
			"OSC 8 escapes must not add display columns: %q", ln)
	}
}

// Non-URL matches (SHAs, paths) are never hyperlinked — clicking them opens
// nothing useful, and the visible text carries the copyable value already. This
// also pins the KindURL guard: were it dropped, a path match would gain an OSC 8.
func TestRender_NonURLMatchNotHyperlinked(t *testing.T) {
	s := NewScreen("edit /tmp/notes.go here", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())
	require.NotContains(t, out, "\x1b]8;", "a path match must not be hyperlinked")
}
