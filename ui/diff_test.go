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

	// colorizeDiff maps input lines to output lines 1:1, so assertions can be
	// positional: each output line must preserve its input's content, and be
	// styled (or not) according to its classification.
	in := []string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,4 @@",
		" unchanged line",
		"+added line",
		"-removed line",
	}
	got := strings.Split(colorizeDiff(strings.Join(in, "\n")), "\n")
	if len(got) != len(in)+1 || got[len(in)] != "" {
		t.Fatalf("expected %d lines plus trailing newline, got %d: %q", len(in), len(got), got)
	}

	// Metadata ("diff --git", "---", "+++") and context lines pass through
	// byte-identical — no styling.
	for _, i := range []int{0, 1, 2, 4} {
		if got[i] != in[i] {
			t.Errorf("line %d: want unstyled %q, got %q", i, in[i], got[i])
		}
	}

	// Hunk header, added, and removed lines are styled with content preserved.
	for _, i := range []int{3, 5, 6} {
		if !styled(got[i]) {
			t.Errorf("line %d: %q should be styled, got %q", i, in[i], got[i])
		}
		if !strings.Contains(got[i], in[i]) {
			t.Errorf("line %d: styled output %q lost content %q", i, got[i], in[i])
		}
	}
}

func TestColorizeDiff_EmptyInput(t *testing.T) {
	restore := theme.Set("unicode")
	defer restore()

	// strings.Split("", "\n") yields one empty element, so the loop emits one
	// empty line — a trailing newline is the only output.
	got := colorizeDiff("")
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

	out := strings.Split(colorizeDiff("+\n-"), "\n")
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
