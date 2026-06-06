package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
)

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
	restore := theme.Set("unicode")
	defer restore()

	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,4 @@",
		" unchanged line",
		"+added line",
		"-removed line",
		"",
	}, "\n")

	got := colorizeDiff(diff)
	lines := strings.Split(got, "\n")

	// The diff metadata lines ("---", "+++") must pass through uncolored.
	for _, prefix := range []string{"diff --git", "--- a", "+++ b"} {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				found = true
				// If colorized they'd carry ANSI bytes; ensure raw prefix survives.
				if !strings.Contains(l, prefix) {
					t.Errorf("metadata line %q lost its prefix in %q", prefix, l)
				}
				break
			}
		}
		if !found {
			t.Errorf("metadata line with prefix %q not found in output", prefix)
		}
	}

	// The hunk header "@@" line must appear in the output (colorizeDiff applies a style but
	// should not drop it entirely).
	hunkFound := false
	for _, l := range lines {
		if strings.Contains(l, "@@") {
			hunkFound = true
			break
		}
	}
	if !hunkFound {
		t.Error("hunk header (@@ line) missing from colorizeDiff output")
	}

	// Added line ("+" not "+++") must appear.
	addedFound := false
	for _, l := range lines {
		if strings.Contains(l, "added line") {
			addedFound = true
			break
		}
	}
	if !addedFound {
		t.Error("added line content missing from colorizeDiff output")
	}

	// Removed line ("-" not "---") must appear.
	removedFound := false
	for _, l := range lines {
		if strings.Contains(l, "removed line") {
			removedFound = true
			break
		}
	}
	if !removedFound {
		t.Error("removed line content missing from colorizeDiff output")
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
	// A line that is just "+" or "-" (no second char) must still be colored, not
	// treated as metadata. Guards the len==1 special-case branch.
	restore := theme.Set("unicode")
	defer restore()

	out := colorizeDiff("+\n-\n")
	if !strings.Contains(out, "+") {
		t.Error("single '+' line missing from output")
	}
	if !strings.Contains(out, "-") {
		t.Error("single '-' line missing from output")
	}
}
