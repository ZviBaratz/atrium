package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestDiffStatLine_OmitsZeroSide pins the #378 header rule: the diff-tab stat line
// omits a zero addition or deletion side entirely (the omit-zero convention
// gitContextHeader follows just below it), so a red "0 deletions(-)" never flags
// attention at nothing.
func TestDiffStatLine_OmitsZeroSide(t *testing.T) {
	defer theme.Set("unicode")()

	// +12 −0: the deletions side is gone; only the additions render.
	line := ansi.Strip(diffStatLine(&git.DiffStats{Added: 12, Removed: 0}))
	require.Contains(t, line, "12 additions(+)")
	require.NotContains(t, line, "deletions", "a zero deletions side is omitted, not shown as 0")

	// +0 −5: the symmetric case.
	line = ansi.Strip(diffStatLine(&git.DiffStats{Added: 0, Removed: 5}))
	require.Contains(t, line, "5 deletions(-)")
	require.NotContains(t, line, "additions", "a zero additions side is omitted")

	// Both nonzero: both render.
	line = ansi.Strip(diffStatLine(&git.DiffStats{Added: 3, Removed: 2}))
	require.Contains(t, line, "3 additions(+)")
	require.Contains(t, line, "2 deletions(-)")

	// A content-only diff (a rename netting to zero lines) yields an empty stat
	// line; the git-context header carries the summary instead.
	require.Empty(t, diffStatLine(&git.DiffStats{Content: "x"}))
}
