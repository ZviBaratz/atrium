package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// withASCIIProfile strips ANSI so assertions compare visible text, and pins the
// unicode theme for stable glyphs. Cleanups restore both.
func withASCIIProfile(t *testing.T) {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })
}

func TestComposeLine_FlexFillsAndRightAligns(t *testing.T) {
	withASCIIProfile(t)
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
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.flexSeg("a-very-long-name-indeed", th.Palette.Fg, false)}
	right := []rowSeg{p.seg("RIGHT", th.Palette.Fg)}
	out := p.composeLine(12, left, right)
	require.Equal(t, 12, runewidth.StringWidth(out))
	require.Contains(t, out, "…", "an over-long flex segment is truncated with an ellipsis")
}

func TestComposeLine_EmptiedFlexCollapsesAdjacentSeparator(t *testing.T) {
	withASCIIProfile(t)
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
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.seg("AB", th.Palette.Fg)}
	right := []rowSeg{p.seg("CD", th.Palette.Fg)}
	out := p.composeLine(10, left, right)
	require.Equal(t, 10, runewidth.StringWidth(out))
	require.True(t, strings.HasPrefix(out, "AB"))
	require.True(t, strings.HasSuffix(out, "CD"))
}

// composeLine truncates the flex segment to fit, but must do so on its own copy
// — callers build the segment list fresh each render, yet a layout engine that
// mutated its input would be a latent footgun for any future reuse.
func TestComposeLine_DoesNotMutateInput(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	left := []rowSeg{p.flexSeg("a-very-long-name-indeed", th.Palette.Fg, false)}
	p.composeLine(8, left, nil) // width far too small → flex would be truncated
	require.Equal(t, "a-very-long-name-indeed", left[0].plain,
		"composeLine must not mutate the caller's flex segment")
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

func TestGutterSeg_PerState(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	p := newRowPaint(th, false)

	waiting := instWithStatus(t, "w", session.NeedsInput)
	seg := r.gutterSeg(p, waiting)
	require.Equal(t, th.Glyphs.Waiting, seg.plain, "needs-input gutter is the waiting glyph")
	require.Equal(t, 1, seg.width(), "the gutter is a single column")
}

// TestRender_QueuedPromptChip pins the pending-prompt indicator: a session with a queued
// prompt shows the Queued glyph, one without does not, and the glyph is additive — it never
// displaces the status gutter glyph.
func TestRender_QueuedPromptChip(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	queued := instWithStatus(t, "q", session.NeedsInput)
	queued.QueueFollowupPrompt("ship it")
	bare := instWithStatus(t, "b", session.NeedsInput)

	withGlyph := ansi.Strip(r.Render(queued, 0, false, false))
	require.Contains(t, withGlyph, th.Glyphs.Queued, "a queued prompt must show the pending-prompt glyph")
	require.Contains(t, withGlyph, th.Glyphs.Waiting,
		"the status gutter glyph is additive-only: NeedsInput still shows its waiting glyph")

	require.NotContains(t, ansi.Strip(r.Render(bare, 0, false, false)), th.Glyphs.Queued,
		"a session with no queued prompt shows no pending-prompt glyph")

	// Depth > 1 surfaces the count so the user knows there's a queue worth opening.
	queued.QueueFollowupPrompt("and again")
	deep := ansi.Strip(r.Render(queued, 0, false, false))
	require.Contains(t, deep, th.Glyphs.Queued+"2", "two queued prompts show the count")
}

func TestGitChips_PresentAndAbsent(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	chips := gitChips(p, &git.DiffStats{Behind: 2, Commits: 3, Dirty: true})
	joined := ""
	for _, s := range chips {
		joined += s.plain
	}
	require.Contains(t, joined, "⇣2")
	require.Contains(t, joined, "⇡3")
	require.NotContains(t, joined, "*", "dirty rides with the diff counts, not the position cluster")

	require.Empty(t, gitChips(p, &git.DiffStats{Commits: 0}), "no behind/ahead → no chips (dirty alone is not a chip)")
}

func TestChangeSegs_DirtyFrontsDiffCounts(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	// Dirty + a non-empty diff: the pencil precedes the "+adds −dels" pair.
	segs := changeSegs(p, &git.DiffStats{Added: 20, Removed: 0, Dirty: true})
	joined := ""
	for _, s := range segs {
		joined += s.plain
	}
	require.Contains(t, joined, "*")
	require.Contains(t, joined, "+20")
	require.Contains(t, joined, "-0")
	require.Less(t, strings.Index(joined, "*"), strings.Index(joined, "+20"),
		"the dirty glyph leads the diff counts")
}

func TestChangeSegs_DirtyWithoutDiff(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	// A dirty worktree whose diff against base is empty (e.g. an edit that nets
	// to zero) still shows the pencil — it is the only signal there is work in
	// flight.
	segs := changeSegs(p, &git.DiffStats{Dirty: true})
	joined := ""
	for _, s := range segs {
		joined += s.plain
	}
	require.Contains(t, joined, "*")
	require.NotContains(t, joined, "+", "no diff counts when the diff is empty")
}

func TestChangeSegs_CleanWithDiff(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	// Everything committed (clean tree) with a delta vs base: counts only, no pencil.
	segs := changeSegs(p, &git.DiffStats{Added: 9, Removed: 2})
	joined := ""
	for _, s := range segs {
		joined += s.plain
	}
	require.NotContains(t, joined, "*", "a clean tree shows no dirty glyph")
	require.Contains(t, joined, "+9")
	require.Contains(t, joined, "-2")
}

func TestChangeSegs_CleanAndEmpty(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	require.Empty(t, changeSegs(p, &git.DiffStats{}),
		"a clean, unchanged session contributes no change segments")
}

func TestDiffSegs_EmptyWhenNoChanges(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	require.Empty(t, diffSegs(p, &git.DiffStats{}), "an empty diff produces no segments")
	segs := diffSegs(p, &git.DiffStats{Added: 9, Removed: 2})
	joined := ""
	for _, s := range segs {
		joined += s.plain
	}
	require.Contains(t, joined, "+9")
	require.Contains(t, joined, "-2")
}

func TestDiffSegs_HumanizesLargeCounts(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	segs := diffSegs(p, &git.DiffStats{Added: 18918, Removed: 3239})
	joined := ""
	for _, s := range segs {
		joined += s.plain
	}
	require.Contains(t, joined, "+18.9k", "a large addition count collapses to a k-suffix")
	require.Contains(t, joined, "-3.2k", "a large deletion count collapses to a k-suffix")
}

func TestHumanizeCount(t *testing.T) {
	table := map[int]string{
		0:     "0",
		31:    "31",
		142:   "142",
		999:   "999",
		1000:  "1k",   // exact thousand drops the ".0"
		3239:  "3.2k", // rounds down
		6600:  "6.6k",
		9999:  "10k", // rounds up across the thousand
		18918: "18.9k",
	}
	for in, want := range table {
		require.Equalf(t, want, humanizeCount(in), "humanizeCount(%d)", in)
	}
}

func TestPRSeg_EmptyWhenNoPR(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)
	seg, ok := prSeg(p, nil)
	require.False(t, ok, "no PR status → no segment")
	_ = seg
	seg, ok = prSeg(p, &git.PRStatus{HasPR: true, Number: 86})
	require.True(t, ok)
	require.Contains(t, seg.plain, "#86")
}
