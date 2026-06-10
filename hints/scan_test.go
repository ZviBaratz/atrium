// hints/scan_test.go
package hints

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func textsOf(ms []Match) []string {
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// The curated patterns against realistic agent-session output. Each case is
// one stripped line; expected is the matched texts in left-to-right order.
func TestScan_Patterns(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		expected []string
		kinds    []Kind
	}{
		{
			name:     "url in prose, trailing period trimmed",
			line:     "PR opened at https://github.com/x/y/pull/9.",
			expected: []string{"https://github.com/x/y/pull/9"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "markdown link captures the url only",
			line:     "see [the docs](https://example.com/docs) for details",
			expected: []string{"https://example.com/docs"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "path with line and column",
			line:     "error in app/app_update.go:412:7",
			expected: []string{"app/app_update.go:412:7"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "git status captures the filename only",
			line:     "        modified:   session/instance.go",
			expected: []string{"session/instance.go"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "diff header captures the path only",
			line:     "+++ b/ui/preview.go",
			expected: []string{"ui/preview.go"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "uuid wins over sha on overlap",
			line:     "id 123e4567-e89b-42d3-a456-426614174000 ok",
			expected: []string{"123e4567-e89b-42d3-a456-426614174000"},
			kinds:    []Kind{KindText},
		},
		{
			name:     "url wins over path (contains slashes)",
			line:     "git@github.com:x/y.git cloned",
			expected: []string{"git@github.com:x/y.git"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "sha in a commit line",
			line:     "commit 6912021ab3 (HEAD)",
			expected: []string{"6912021ab3"},
			kinds:    []Kind{KindText},
		},
		{
			name:     "no matches",
			line:     "Thinking about the problem",
			expected: nil,
			kinds:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := scanLine(tc.line, 0)
			require.Equal(t, tc.expected, textsOf(ms))
			for i, m := range ms {
				assert.Equal(t, tc.kinds[i], m.Kind, "kind of %q", m.Text)
			}
		})
	}
}

// Rows and rune columns must locate the copyable text exactly — Col points at
// the capture group's first rune, not the full pattern's.
func TestScan_RowsAndCols(t *testing.T) {
	text := "line one\nsee /tmp/x.go here\nmodified:   foo/bar.go"
	ms := Scan(text)
	require.Len(t, ms, 2)
	assert.Equal(t, Match{Text: "/tmp/x.go", Kind: KindPath, Row: 1, Col: 4}, ms[0])
	assert.Equal(t, Match{Text: "foo/bar.go", Kind: KindPath, Row: 2, Col: 12}, ms[1])
}

// Matching always operates on stripped text; StripANSI removes the SGR
// sequences tmux capture-pane -e embeds.
func TestStripANSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m /tmp/a"
	assert.Equal(t, "red /tmp/a", StripANSI(in))
}
