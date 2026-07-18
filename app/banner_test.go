package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// TestAutoYesArmedAndBannerHeight pins the armed predicate and its reserved row: the
// safety banner claims exactly one top row iff app-wide auto-accept is on, which is
// what the two layout budgets and the divider Y-bound subtract.
func TestAutoYesArmedAndBannerHeight(t *testing.T) {
	require.False(t, (&home{}).autoYesArmed())
	require.Equal(t, 0, (&home{}).topBannerHeight())
	require.True(t, (&home{autoYes: true}).autoYesArmed())
	require.Equal(t, 1, (&home{autoYes: true}).topBannerHeight())
}

// bgParams renders a background-only style and returns just its SGR parameter body
// (e.g. "48;2;224;175;104"), so a test can assert a combined style embeds that exact
// background without hard-coding the theme's RGB.
func bgParams(c lipgloss.TerminalColor) string {
	r := lipgloss.NewStyle().Background(c).Render("X")
	open := r[:strings.IndexByte(r, 'X')]
	return strings.TrimSuffix(strings.TrimPrefix(open, "\x1b["), "m")
}

// TestAutoYesBanner_ExactWidthAndColor pins the safety banner's invariants: at every
// width it is a single line of exactly `width` printable cells (so it can never break
// the frame), it names the armed state, and it renders on the amber attention
// background — never the error red the palette reserves for failures. Width ≤ 0
// yields nothing, so an unarmed/zero-width frame reserves no row.
func TestAutoYesBanner_ExactWidthAndColor(t *testing.T) {
	defer theme.Set("unicode")()
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prof)

	m := &home{autoYes: true}

	require.Empty(t, m.autoYesBanner(0))
	require.Empty(t, m.autoYesBanner(-5))

	for _, w := range []int{10, 20, 40, 57, 80, 120} {
		out := m.autoYesBanner(w)
		require.Equalf(t, 1, strings.Count(out, "\n")+1, "banner must be a single line at width %d", w)
		require.Equalf(t, w, lipgloss.Width(out), "banner must be exactly %d cells wide", w)
	}

	// Names the armed state when there is room for the whole phrase.
	require.Contains(t, xansi.Strip(m.autoYesBanner(80)), "AUTO-ACCEPT ARMED")

	// Even truncated hard at a narrow width, the leading, most important words survive.
	require.Contains(t, xansi.Strip(m.autoYesBanner(24)), "AUTO-ACCEPT")

	// Color: amber attention background present, danger red absent.
	pal := theme.Current().Palette
	bar := m.autoYesBanner(80)
	require.Contains(t, bar, bgParams(pal.Attention), "banner must use the attention (amber) background")
	require.NotContains(t, bar, bgParams(pal.Danger), "banner must never use the danger (red) background")
}
