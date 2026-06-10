// hints/screen_test.go
package hints

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bottom-most matches get the shortest labels: the match nearest the prompt
// is almost always the wanted one in an agent session.
func TestNewScreen_BottomUpAssignment(t *testing.T) {
	s := NewScreen("/top/one\n\n/bottom/two", 80, 10)
	require.Equal(t, 2, s.MatchCount())
	bottom, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, bottom)
	assert.Equal(t, "/bottom/two", bottom.Text)
}

// Identical text shares one label (tmux-fingers' dedup): one keystroke,
// regardless of how many times the same path is on screen.
func TestNewScreen_DedupSameText(t *testing.T) {
	s := NewScreen("/same/path\n/same/path\n/other/path", 80, 10)
	// 3 visual matches but only 2 distinct labels.
	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	labels := map[string]bool{}
	for _, mm := range s.matches {
		labels[mm.Label] = true
	}
	assert.Len(t, labels, 2)
}

// Geometry clipping: matches beyond the visible rows, or starting past the
// pane's width, must not get hints — a hint must label something visible.
func TestNewScreen_ClipsToGeometry(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		s := NewScreen("/visible/a\n/clipped/b", 80, 1)
		require.Equal(t, 1, s.MatchCount())
		m, _ := s.Resolve("a")
		assert.Equal(t, "/visible/a", m.Text)
	})
	t.Run("width", func(t *testing.T) {
		pad := strings.Repeat("x", 30)
		s := NewScreen(pad+" /far/away", 20, 10)
		assert.Equal(t, 0, s.MatchCount())
	})
	// Wide runes (CJK, emoji) occupy two terminal columns but one rune:
	// clipping must compare display columns, not rune indices, or hints
	// appear for matches the width truncation already cut off.
	t.Run("width with wide runes", func(t *testing.T) {
		wide := strings.Repeat("漢", 10) // 10 runes, 20 columns
		s := NewScreen(wide+" /far/x", 20, 10)
		assert.Equal(t, 0, s.MatchCount(),
			"match at rune 11 but display column 21 is off-pane")

		s = NewScreen(strings.Repeat("漢", 5)+" /near/x", 20, 10)
		assert.Equal(t, 1, s.MatchCount(),
			"display column 11 is on-pane")
	})
}

// Resolve narrows by typed prefix: full label -> the match; proper prefix ->
// nil but valid; a character no label starts with -> invalid.
func TestScreen_Resolve(t *testing.T) {
	// 27 distinct SHAs force two-character labels for the top rows.
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("abcdef%02x", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	require.Equal(t, 27, s.MatchCount())

	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	assert.Equal(t, "abcdef1a", m.Text, "bottom row gets label a")

	m, valid = s.Resolve("n") // prefix of na/ns, not a full label
	assert.True(t, valid)
	assert.Nil(t, m)

	m, valid = s.Resolve("nz") // no such label
	assert.False(t, valid)
	assert.Nil(t, m)
}
