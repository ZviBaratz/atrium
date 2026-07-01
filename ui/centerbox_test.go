package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// These mirror TestPreviewFallbackClampedToPaneBox for the three other panes
// that center a fallback in their box. centerInBox clamps its content to the
// box (lipgloss.Place alone does not clip, so an oversize fallback would
// silently inflate the whole frame and throw every centered overlay off-center
// — issue #251). ErrBox and Menu route their fallbacks straight through
// centerInBox; DiffPane feeds it into a viewport, whose View() re-clamps to the
// box. These lock both guarantees so a refactor can't quietly drop either.

func TestDiffFallbackClampedToPaneBox(t *testing.T) {
	pane := NewDiffPane()
	// Narrower than the "No changes" fallback (10 cols). The diff pane stores its
	// fallback in a viewport and String() == viewport.View(); the box clamp is
	// applied both by centerInBox and by the viewport, and this asserts the end
	// result stays inside the pane rather than any single layer's clamp.
	pane.SetSize(6, 8)
	pane.SetDiff(nil)

	lines := strings.Split(pane.String(), "\n")
	require.LessOrEqual(t, len(lines), 8, "fallback must not exceed the pane height")
	for i, l := range lines {
		require.LessOrEqualf(t, ansi.PrintableRuneWidth(l), 6,
			"fallback line %d wider than the pane", i)
	}
}

func TestErrFallbackClampedToPaneBox(t *testing.T) {
	e := NewErrBox()
	// Below width 3, ErrBox skips its own width-3 truncation guard, so only the
	// box clamp in centerInBox keeps a long error inside the frame.
	e.SetSize(2, 1)
	e.SetError(errors.New("a very long error message that overflows a tiny box"))

	lines := strings.Split(e.String(), "\n")
	require.LessOrEqual(t, len(lines), 1, "error banner must stay one row")
	for i, l := range lines {
		require.LessOrEqualf(t, ansi.PrintableRuneWidth(l), 2,
			"error line %d wider than the box", i)
	}
}

func TestMenuFallbackClampedToPaneBox(t *testing.T) {
	m := NewMenu()
	// At width 1 the notice's own width-2 truncation guard goes negative and is
	// skipped; the box clamp in centerInBox is what stops the row from overflowing.
	m.SetSize(1, 1)
	m.SetState(StateEmpty)
	m.SetNotice("a notice far wider than one column", NoticeInfo)

	lines := strings.Split(m.String(), "\n")
	require.LessOrEqual(t, len(lines), 1, "menu must stay one row")
	for i, l := range lines {
		require.LessOrEqualf(t, ansi.PrintableRuneWidth(l), 1,
			"menu line %d wider than the box", i)
	}
}
