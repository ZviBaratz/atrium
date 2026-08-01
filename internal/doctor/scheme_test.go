package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/require"
)

// doctor reports what detection can and cannot see, because a user whose theme did
// not adapt has no other way to find out WHY. The three answers are materially
// different: "your terminal said light", "your terminal did not answer, so COLORFGBG
// decided", and "nothing answered, so it stayed dark".
func TestCheckSchemeNamesTheRungThatAnswered(t *testing.T) {
	t.Run("COLORFGBG answers light", func(t *testing.T) {
		got := CheckScheme([]string{"COLORFGBG=0;15"})
		require.Equal(t, theme.SchemeLight, got.Scheme)
		require.Equal(t, "0;15", got.ColorFGBG)

		out := RenderScheme(got)
		require.Contains(t, out, "light")
		require.Contains(t, out, "COLORFGBG")
		require.Contains(t, out, "0;15", "the raw value is what a user would have to check themselves")
	})

	t.Run("COLORFGBG answers dark", func(t *testing.T) {
		out := RenderScheme(CheckScheme([]string{"COLORFGBG=15;0"}))
		require.Contains(t, out, "dark")
		require.Contains(t, out, "COLORFGBG")
	})

	t.Run("nothing answers", func(t *testing.T) {
		got := CheckScheme([]string{"TERM=xterm-256color"})
		require.Equal(t, theme.SchemeUnknown, got.Scheme)
		require.Empty(t, got.ColorFGBG)

		out := strings.ToLower(RenderScheme(got))
		require.Contains(t, out, "dark", "no answer resolves to the shipped dark default")
		require.Contains(t, out, "no")
		require.Contains(t, out, "unset", "an unset COLORFGBG must say so, not be silently omitted")
	})

	t.Run("a malformed COLORFGBG is no answer", func(t *testing.T) {
		require.Equal(t, theme.SchemeUnknown, CheckScheme([]string{"COLORFGBG=nonsense"}).Scheme)
	})
}

// environ is a parameter, so the check is a pure function of it — and that means
// matching os.Environ's semantics for a duplicated name, where the later entry wins.
// theme.NoColorRequested documents the same rule for the same reason.
func TestCheckSchemeTakesTheLastCOLORFGBG(t *testing.T) {
	got := CheckScheme([]string{"COLORFGBG=15;0", "COLORFGBG=0;15"})
	require.Equal(t, theme.SchemeLight, got.Scheme)
	require.Equal(t, "0;15", got.ColorFGBG)
}

// A variable whose name merely CONTAINS the key is not the key. Cut-based matching
// gets this right and prefix matching does not, which is the bug this pins.
func TestCheckSchemeIgnoresALookalikeVariable(t *testing.T) {
	require.Equal(t, theme.SchemeUnknown,
		CheckScheme([]string{"COLORFGBG_OLD=0;15", "MY_COLORFGBG=0;15"}).Scheme)
}

// doctor runs OUTSIDE the Bubble Tea loop, so it cannot send an OSC 11 query and
// wait for the reply. Saying so is better than implying it probed and found nothing
// — the distinction is the whole value of the section.
func TestRenderSchemeSaysItCannotProbeOSC11(t *testing.T) {
	out := RenderScheme(CheckScheme([]string{"TERM=xterm-256color"}))
	require.Contains(t, out, "OSC 11",
		"doctor must name the rung it cannot test, not silently omit it")
	require.Contains(t, out, "auto",
		"naming the rungs is only actionable beside the config value that consults them")
}

// The section has to sit beside the others without looking foreign: same header
// shape, same two-space rows, newline-terminated with no trailing blank (main.go
// owns the separators).
func TestRenderSchemeMatchesTheSectionConvention(t *testing.T) {
	out := RenderScheme(CheckScheme(nil))

	require.False(t, strings.HasPrefix(out, "\n"), "the blank separator is main.go's job")
	require.True(t, strings.HasSuffix(out, "\n"), "sections are newline-terminated")
	require.False(t, strings.HasSuffix(out, "\n\n"), "no trailing blank line")

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	require.Equal(t, "Terminal background detection:", lines[0],
		"header is a bare Title-case line ending in a colon, like Host capacity:")
	for _, l := range lines[1:] {
		require.True(t, strings.HasPrefix(l, "  "), "row %q must be indented two spaces", l)
	}
}
