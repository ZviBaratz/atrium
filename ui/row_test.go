package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// withAsciiProfile strips ANSI so assertions compare visible text, and pins the
// unicode theme for stable glyphs. Cleanups restore both.
func withAsciiProfile(t *testing.T) {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })
}

func TestComposeLine_FlexFillsAndRightAligns(t *testing.T) {
	withAsciiProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.seg("L", th.Palette.Fg), p.flexSeg("name", th.Palette.Fg, false)}
	right := []rowSeg{p.seg("R", th.Palette.Fg)}
	out := p.composeLine(20, left, right)
	require.Equal(t, 20, runewidth.StringWidth(out), "line must total exactly the width")
	require.True(t, strings.HasPrefix(out, "Lname"), "fixed + flex lead the line: %q", out)
	require.True(t, strings.HasSuffix(out, "R"), "right group is flush right: %q", out)
}

func TestComposeLine_FlexTruncatesWithEllipsis(t *testing.T) {
	withAsciiProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.flexSeg("a-very-long-name-indeed", th.Palette.Fg, false)}
	right := []rowSeg{p.seg("RIGHT", th.Palette.Fg)}
	out := p.composeLine(12, left, right)
	require.Equal(t, 12, runewidth.StringWidth(out))
	require.Contains(t, out, "…", "an over-long flex segment is truncated with an ellipsis")
}

func TestComposeLine_EmptiedFlexCollapsesAdjacentSeparator(t *testing.T) {
	withAsciiProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	// indent + flex(branch) + sep + chip; too narrow for any branch.
	left := []rowSeg{
		p.seg("    ", th.Palette.FgDim),
		p.flexSeg("zzzzzzzzzzzzzzzz", th.Palette.FgDim, false),
		p.sepSeg(),
		p.seg("#42", th.Palette.FgDim),
	}
	out := p.composeLine(10, left, nil)
	require.Equal(t, 10, runewidth.StringWidth(out))
	require.NotContains(t, out, "·", "the separator orphaned by the emptied flex must collapse")
	require.Contains(t, out, "#42", "the trailing chip still renders")
}

func TestComposeLine_NoFlexKeepsFixedSegments(t *testing.T) {
	withAsciiProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.seg("AB", th.Palette.Fg)}
	right := []rowSeg{p.seg("CD", th.Palette.Fg)}
	out := p.composeLine(10, left, right)
	require.Equal(t, 10, runewidth.StringWidth(out))
	require.True(t, strings.HasPrefix(out, "AB"))
	require.True(t, strings.HasSuffix(out, "CD"))
}

func TestComposeLine_SelectedBakesBackgroundIntoGap(t *testing.T) {
	t.Cleanup(theme.Set("tokyo-night")) // a theme with a real BgElevated color
	// Force a color-capable profile: the test binary has no TTY, so lipgloss
	// otherwise defaults to Ascii and strips every background.
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })

	th := theme.Current()
	p := newRowPaint(th, true) // selected → non-NoColor bg
	left := []rowSeg{p.flexSeg("x", th.Palette.Fg, false)}
	out := p.composeLine(20, left, nil)
	// The gap is rendered through p.pad, which sets a background; with color on,
	// the output must contain SGR sequences (no bare-space tail).
	require.Contains(t, out, "\x1b[", "selected-row gap must carry background styling")
}
