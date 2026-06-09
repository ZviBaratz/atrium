package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceColorProfile pins lipgloss to ANSI256 so styled lines carry escape
// sequences even without a TTY — go test detects Ascii and strips all styling,
// which would make style assertions vacuous. Restored via t.Cleanup, same
// pattern as overhaul_test.go.
func forceColorProfile(t *testing.T) {
	t.Helper()
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })
}

// styled reports whether s carries ANSI escape bytes.
func styled(s string) bool { return strings.Contains(s, "\x1b[") }

func TestPluralize(t *testing.T) {
	cases := []struct {
		n    int
		noun string
		want string
	}{
		{0, "file", "0 files"},
		{1, "file", "1 file"},
		{2, "file", "2 files"},
		{1, "commit", "1 commit"},
		{5, "commit", "5 commits"},
	}
	for _, tc := range cases {
		if got := pluralize(tc.n, tc.noun); got != tc.want {
			t.Errorf("pluralize(%d, %q) = %q, want %q", tc.n, tc.noun, got, tc.want)
		}
	}
}

func TestColorizeDiff_LineClassification(t *testing.T) {
	// Pin the unicode theme so rendering is deterministic.
	defer theme.Set("unicode")()
	forceColorProfile(t)

	in := []string{
		"diff --git a/foo.go b/foo.go",
		"index 123..456 100644",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,4 @@",
		" unchanged line",
		"+added line",
		"-removed line",
	}
	out := colorizeDiff(strings.Join(in, "\n"), 80)
	got := strings.Split(out, "\n")

	// The "diff --git" line becomes a file boundary: a rule line plus the bold
	// b-side path, so a multi-file diff reads as sections, not one stream.
	if !strings.Contains(got[0], "─") {
		t.Errorf("line 0: expected a boundary rule, got %q", got[0])
	}
	if !strings.Contains(got[1], "foo.go") || !styled(got[1]) {
		t.Errorf("line 1: expected styled file path, got %q", got[1])
	}

	// Remaining metadata (index/---/+++ at output lines 2-4) is dimmed, not raw.
	for i := 2; i <= 4; i++ {
		if !styled(got[i]) {
			t.Errorf("line %d: metadata should be dimmed (styled), got %q", i, got[i])
		}
	}

	// Context passes through unstyled; hunk/added/removed are styled with
	// content preserved (output index = input index + 1 after the boundary).
	if got[6] != " unchanged line" {
		t.Errorf("context line: want unstyled %q, got %q", " unchanged line", got[6])
	}
	for outIdx, inIdx := range map[int]int{5: 4, 7: 6, 8: 7} {
		if !styled(got[outIdx]) {
			t.Errorf("line %d: %q should be styled, got %q", outIdx, in[inIdx], got[outIdx])
		}
		if !strings.Contains(got[outIdx], in[inIdx]) {
			t.Errorf("line %d: styled output %q lost content %q", outIdx, got[outIdx], in[inIdx])
		}
	}
}

// Long lines truncate to the pane width instead of soft-wrapping in the
// viewport — wrapped rows make scroll position and file boundaries jump.
func TestColorizeDiff_TruncatesToWidth(t *testing.T) {
	defer theme.Set("unicode")()
	forceColorProfile(t)

	long := "+" + strings.Repeat("x", 200)
	out := colorizeDiff(long, 40)
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 40 {
			t.Errorf("line %d width %d exceeds pane width 40", i, w)
		}
	}
	if !strings.Contains(out, "…") {
		t.Error("truncated line should carry an ellipsis")
	}
}

// Tabs expand to spaces before width math, so indentation renders predictably
// instead of depending on the terminal's tab stops.
func TestColorizeDiff_ExpandsTabs(t *testing.T) {
	defer theme.Set("unicode")()

	out := colorizeDiff("+\tindented", 80)
	if strings.Contains(out, "\t") {
		t.Errorf("output must not contain raw tabs: %q", out)
	}
	if !strings.Contains(out, "    indented") {
		t.Errorf("tab should expand to spaces: %q", out)
	}
}

// A zero/unset width (startup, tests) must not truncate or panic.
func TestColorizeDiff_ZeroWidthLeavesLinesAlone(t *testing.T) {
	defer theme.Set("unicode")()

	long := "+" + strings.Repeat("x", 120)
	out := colorizeDiff(long, 0)
	if !strings.Contains(out, strings.Repeat("x", 120)) {
		t.Errorf("zero width must not truncate: %q", out)
	}
}

func TestColorizeDiff_EmptyInput(t *testing.T) {
	restore := theme.Set("unicode")
	defer restore()

	// strings.Split("", "\n") yields one empty element, so the loop emits one
	// empty line — a trailing newline is the only output.
	got := colorizeDiff("", 80)
	if strings.TrimSpace(got) != "" {
		t.Errorf("colorizeDiff(\"\") = %q, expected only whitespace", got)
	}
}

func TestColorizeDiff_SinglePlusAndMinus(t *testing.T) {
	// A line that is just "+" or "-" (no second char) exercises the len==1
	// branch: it must not panic on the line[1] lookahead, and must be classified
	// as added/removed (styled), not metadata.
	defer theme.Set("unicode")()
	forceColorProfile(t)

	out := strings.Split(colorizeDiff("+\n-", 80), "\n")
	if len(out) < 2 {
		t.Fatalf("expected at least 2 output lines, got %q", out)
	}
	for i, want := range []string{"+", "-"} {
		if !styled(out[i]) {
			t.Errorf("line %d: single %q should be styled, got %q", i, want, out[i])
		}
		if !strings.Contains(out[i], want) {
			t.Errorf("line %d: %q missing from output %q", i, want, out[i])
		}
	}
}
