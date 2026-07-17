package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// withColorProfile pins the "unicode" theme and a truecolor profile so styled
// renders emit their SGR sequences. The default test environment strips color
// (collapsing the dim and danger styles to identical plain bytes — which is why
// row-level tests inspect the style object instead), so a rendered-string
// desaturation assertion needs a color-emitting profile to have any teeth.
func withColorProfile(t *testing.T) {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })
}

// TestDiffStatLine_DimsZeroSide pins the #378 header rule: the diff-tab stat line
// always renders both sides, but a zero addition or deletion side recedes to the
// dim/meta style rather than its semantic green/red — one −0 rule shared with the
// row's +adds/−dels chip. Both text presence and color (the desaturation property)
// are pinned here.
func TestDiffStatLine_DimsZeroSide(t *testing.T) {
	withColorProfile(t)

	// +12 −0: additions render; the zero deletions side is present but dim (its
	// text still shows), never omitted and never in the danger color.
	line := diffStatLine(&git.DiffStats{Added: 12, Removed: 0})
	require.Contains(t, ansi.Strip(line), "12 additions(+)")
	require.Contains(t, ansi.Strip(line), "0 deletions(-)", "a zero side is dimmed, not omitted")
	require.Contains(t, line, metaStyle().Render("0 deletions(-)"),
		"a zero −0 side must render in the dim/meta style")
	require.NotContains(t, line, deletionStyle().Render("0 deletions(-)"),
		"a zero −0 side must never render in the danger color")

	// +0 −5: the symmetric case — zero additions go dim, never Success-green.
	line = diffStatLine(&git.DiffStats{Added: 0, Removed: 5})
	require.Contains(t, ansi.Strip(line), "5 deletions(-)")
	require.Contains(t, ansi.Strip(line), "0 additions(+)", "a zero side is dimmed, not omitted")
	require.Contains(t, line, metaStyle().Render("0 additions(+)"),
		"a zero +0 side must render in the dim/meta style")
	require.NotContains(t, line, additionStyle().Render("0 additions(+)"),
		"a zero +0 side must never render in the success color")

	// Both nonzero: both render in their semantic colors.
	line = diffStatLine(&git.DiffStats{Added: 3, Removed: 2})
	require.Contains(t, line, additionStyle().Render("3 additions(+)"))
	require.Contains(t, line, deletionStyle().Render("2 deletions(-)"))

	// A content-only diff (a rename netting to zero lines) still shows a dim
	// "0 additions(+) 0 deletions(-)" rather than vanishing — the summary stays
	// visible so the change is not read as "no change".
	line = diffStatLine(&git.DiffStats{Content: "x"})
	require.Contains(t, ansi.Strip(line), "0 additions(+)")
	require.Contains(t, ansi.Strip(line), "0 deletions(-)")
}
