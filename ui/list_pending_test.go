package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Pending (a background sub-agent still in flight after the main turn ended, #290) must be
// visually distinct from BOTH Running (foreground work) and Ready (done): a still cyan
// Pending glyph, never the moving spinner and never the green done-dot. Shape and color
// both differ so the signal survives colorblindness.
func TestStateGlyph_PendingDistinct(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}

	inst, err := session.NewInstance(session.InstanceOptions{Title: "p", Path: ".", Program: "echo"})
	require.NoError(t, err)

	inst.SetStatus(session.Pending)
	glyph, color := r.stateGlyph(inst, th)
	require.Equal(t, th.Glyphs.Pending, glyph, "pending uses the still Pending glyph")
	require.Equal(t, th.Palette.Pending, color, "pending uses the calm Pending tint")

	// Distinct from Running: different color (Running recedes to Working) and a still glyph
	// rather than an animated spinner frame.
	inst.SetStatus(session.Running)
	runGlyph, runColor := r.stateGlyph(inst, th)
	require.NotEqual(t, color, runColor, "pending tint must differ from the working tint")
	require.NotEqual(t, glyph, runGlyph, "pending glyph must differ from the running spinner frame")

	// Distinct from Ready ("done"): different color and glyph.
	require.NotEqual(t, th.Palette.Success, color, "pending must not reuse the done color")
	require.NotEqual(t, th.Glyphs.Ready, glyph, "pending must not reuse the done glyph")
}

// A Pending row carries the still Pending glyph and a faint elapsed cue ("· Ns"), so the
// row reads as "busy with autonomous work" and hints how long it has been churning.
func TestRender_PendingElapsedSuffix(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(48)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "migrate", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Pending)

	// Strip styling: the separator and the value are separately-styled segments, so the
	// "· 0s" cue is only contiguous once the interposed ANSI codes are removed.
	out := ansi.Strip(r.Render(inst, 0, false, false))
	require.Contains(t, out, theme.Current().Glyphs.Pending, "pending row shows the still Pending glyph")
	require.Contains(t, out, "· 0s", "pending row shows the elapsed suffix (0s just after entering pending)")
}

// At a width too narrow to keep any of the name, the elapsed cue's separator is a
// collapsible sepSeg, so it drops out instead of dangling: the line-1 gutter degrades to
// "◐ 0s", never "◐  · 0s" with an orphaned separator and no name.
func TestRender_PendingElapsed_SeparatorCollapsesWhenNameStarved(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(10) // too narrow to keep the name → the name flex empties

	inst, err := session.NewInstance(session.InstanceOptions{Title: "a-very-long-session-name", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Pending)

	line1 := ansi.Strip(strings.SplitN(r.Render(inst, 0, false, false), "\n", 2)[0])
	require.Contains(t, line1, "0s", "the elapsed value survives even when the name is starved")
	require.NotContains(t, line1, " · ", "the orphaned separator collapses instead of dangling")
}

func TestFmtPendingElapsed(t *testing.T) {
	require.Equal(t, "", fmtPendingElapsed(time.Time{}), "zero time stays blank")

	now := time.Now()
	require.Equal(t, "0s", fmtPendingElapsed(now.Add(-500*time.Millisecond)), "sub-second rounds to 0s (still shown, unlike fmtAge)")
	require.Equal(t, "14s", fmtPendingElapsed(now.Add(-14*time.Second)), "seconds bucket")
	require.Equal(t, "2m", fmtPendingElapsed(now.Add(-2*time.Minute)), "minutes bucket")
	require.Equal(t, "3h", fmtPendingElapsed(now.Add(-3*time.Hour)), "hours bucket")
	require.Equal(t, "5d", fmtPendingElapsed(now.Add(-5*24*time.Hour)), "days bucket")
}

// The in-session header (barState) mirrors the list: Pending gets the still Pending glyph in
// the Pending tint, never the working marker, so the list and the header agree on state.
func TestBarState_Pending(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()

	glyph, color := barState(session.Pending, th)
	require.Equal(t, th.Glyphs.Pending, glyph)
	require.Equal(t, string(th.Palette.Pending), color)

	// Distinct from Running's working marker.
	runGlyph, runColor := barState(session.Running, th)
	require.False(t, glyph == runGlyph && color == runColor, "pending header must differ from running")
	require.NotEqual(t, string(th.Palette.Success), color, "pending header must not reuse the done color")
}
