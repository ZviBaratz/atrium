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
		{
			// Timestamps, PR numbers, line counts: all-decimal runs would
			// otherwise flood the overlay with bogus "sha" hints.
			name:     "all-decimal run is not a sha",
			line:     "deployed at 20260610 done",
			expected: nil,
			kinds:    nil,
		},
		{
			// git-describe suffixes must keep matching (digits + hex letters).
			name:     "git describe hash is a sha",
			line:     "version 0.3.0-27-g5441edb",
			expected: []string{"5441edb"},
			kinds:    []Kind{KindText},
		},
		{
			// English words spelled entirely in hex letters are not hashes.
			name:     "pure-letter hex word is not a sha",
			line:     "it effaced the data",
			expected: nil,
			kinds:    nil,
		},
		{
			name:     "date fraction is not a path",
			line:     "passed 10/25 tests",
			expected: nil,
			kinds:    nil,
		},
		{
			name:     "absolute numeric path is kept",
			line:     "saved to /2024/06",
			expected: []string{"/2024/06"},
			kinds:    []Kind{KindPath},
		},
		{
			// A relative markdown target must not be browser-opened: demote
			// to path so the open variant degrades to copy.
			name:     "relative markdown link is a path",
			line:     "see [readme](./README.md)",
			expected: []string{"./README.md"},
			kinds:    []Kind{KindPath},
		},
		{
			// tmux pads captured lines to the pane width; the greedy .+
			// captures must not copy that padding.
			name:     "trailing padding spaces are trimmed",
			line:     "        modified:   session/instance.go      ",
			expected: []string{"session/instance.go"},
			kinds:    []Kind{KindPath},
		},
		{
			// Prose word-pairs ("copied/opened", "ssh/git", "CJK/emoji",
			// "timestamps/PR") are slashes in English, not filesystem
			// locations — they flooded the first smoke test with hints.
			name:     "prose word-pair is not a path",
			line:     "fed into the copied/opened text via ssh/git pairs",
			expected: nil,
			kinds:    nil,
		},
		{
			name:     "bare module-ish pair without a signal is not a path",
			line:     "delegates to x/ansi internals",
			expected: nil,
			kinds:    nil,
		},
		{
			// Sentence-final punctuation must be trimmed BEFORE validation,
			// or the trailing dot would count as a filesystem signal.
			name:     "sentence-final word-pair stays rejected",
			line:     "raw bytes reached the copied/opened. Next",
			expected: nil,
			kinds:    nil,
		},
		{
			name:     "branch name with hyphen is a path",
			line:     "pushed to zvi/copy-pasting now",
			expected: []string{"zvi/copy-pasting"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "two slashes of depth are a path",
			line:     "built src/app/main today",
			expected: []string{"src/app/main"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "line-number suffix is a path signal",
			line:     "failed at pkg/util:42 today",
			expected: []string{"pkg/util:42"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "tilde prefix is a path",
			line:     "installed to ~/bin/atrium ok",
			expected: []string{"~/bin/atrium"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "multi-slash date is still not a path",
			line:     "dated 2024/06/15 ok",
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
	assert.Equal(t, Match{Text: "/tmp/x.go", Kind: KindPath, Row: 1, Col: 4, Width: 9}, ms[0])
	assert.Equal(t, Match{Text: "foo/bar.go", Kind: KindPath, Row: 2, Col: 12, Width: 10}, ms[1])
}

// Matching always operates on stripped text; StripANSI removes the SGR
// sequences tmux capture-pane -e embeds.
func TestStripANSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m /tmp/a"
	assert.Equal(t, "red /tmp/a", StripANSI(in))
}

// Defense-in-depth: even if unstripped input ever reaches the scanner (a
// future stripping gap), the permissive url/markdown char classes must not
// swallow ESC bytes into the copyable text.
func TestScan_ControlBytesNeverInMatchText(t *testing.T) {
	raw := "PR \x1b]8;;https://e.com/a\x1b\\https://e.com/a\x1b]8;;\x1b\\ " +
		"[x](\x1b]8;;https://e.com/b\x1b\\b\x1b]8;;\x1b\\)"
	for _, m := range Scan(raw) {
		assert.NotContains(t, m.Text, "\x1b", "match %q", m.Text)
		assert.NotContains(t, m.Text, "\x07", "match %q", m.Text)
	}
}

// tmux >= 3.4 re-emits OSC 8 hyperlinks in capture-pane -e, and Claude Code
// wraps every URL it prints in one. The whole sequence — params, target, and
// both terminators — must vanish, leaving only the visible text; the leaked
// target was the source of duplicated URLs on screen and ESC bytes reaching
// the clipboard/opener (PR #97 smoke test).
func TestStripANSI_OSC8Hyperlink(t *testing.T) {
	in := "PR: \x1b]8;;https://github.com/x/pull/97\x1b\\" +
		"https://github.com/x/pull/97\x1b]8;;\x1b\\ done"
	got := StripANSI(in)
	assert.Equal(t, "PR: https://github.com/x/pull/97 done", got)
	assert.NotContains(t, got, "\x1b")
}

// OSC sequences may terminate with BEL instead of ST (window titles do).
func TestStripANSI_OSCBelTerminated(t *testing.T) {
	assert.Equal(t, "after", StripANSI("\x1b]0;some title\x07after"))
}

// A torture mix of CSI (truecolor), OSC 8, and DCS must strip completely:
// any surviving ESC byte desyncs the terminal when re-emitted in the hint
// frame.
func TestStripANSI_NoEscapeSurvives(t *testing.T) {
	in := "\x1b[38;2;10;20;30mcolor\x1b[0m " +
		"\x1b]8;id=x;https://e.com\x07link\x1b]8;;\x07 " +
		"\x1bPsome dcs payload\x1b\\ tail"
	got := StripANSI(in)
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\x07")
	assert.Contains(t, got, "color")
	assert.Contains(t, got, "link")
	assert.Contains(t, got, "tail")
}
