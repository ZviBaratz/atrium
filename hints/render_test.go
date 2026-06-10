// hints/render_test.go
package hints

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainStyles render no escape codes, so position assertions read literally.
func plainStyles() Styles { return Styles{} }

// The label overlays the match's first cells: the hint replaces what it
// covers instead of shifting the line (tmux-thumbs' left position).
func TestRender_LabelOverlaysMatchStart(t *testing.T) {
	s := NewScreen("go to /tmp/file.go now", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())
	assert.Equal(t, "go to atmp/file.go now", out,
		"label 'a' must replace the first rune of the match")
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

// Lines with no matches are passed through verbatim (modulo styling).
func TestRender_PlainLinesUntouched(t *testing.T) {
	s := NewScreen("no matches here\n/tmp/x.go", 80, 10)
	out := strings.Split(s.Render("", plainStyles()), "\n")
	assert.Equal(t, "no matches here", out[0])
}
