package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

const prURL = "https://github.com/ZviBaratz/atrium/pull/372"

// A PR chip carrying a URL becomes an OSC 8 hyperlink to it, while the visible
// text stays the "#<number>" chip — never the raw URL.
func TestPRSeg_HyperlinkWhenURLPresent(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	seg, ok := prSeg(p, &git.PRStatus{HasPR: true, Number: 372, URL: prURL})
	require.True(t, ok)

	rendered := seg.render()
	require.Contains(t, rendered, ansi.SetHyperlink(prURL), "the chip opens an OSC 8 link to the PR URL")
	require.Contains(t, rendered, ansi.ResetHyperlink(), "and closes it")
	require.Equal(t, th.Glyphs.PR+"#372", ansi.Strip(rendered), "visible text is the chip, not the URL")
}

// Without a URL the chip renders exactly as before — no OSC 8 at all.
func TestPRSeg_NoHyperlinkWithoutURL(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	seg, ok := prSeg(p, &git.PRStatus{HasPR: true, Number: 372})
	require.True(t, ok)
	require.NotContains(t, seg.render(), "\x1b]8;", "no URL means no hyperlink escape")
}

// The no-drift guard: a hyperlinked chip measures identically to a bare one, in
// both the width() the layout engine uses and the rendered display width. If the
// OSC 8 wrapper ever leaked into width math, these would diverge and rows would
// misalign.
func TestPRSeg_WidthInvariantWithHyperlink(t *testing.T) {
	withASCIIProfile(t)
	th := theme.Current()
	p := newRowPaint(th, false)

	bare, ok := prSeg(p, &git.PRStatus{HasPR: true, Number: 372})
	require.True(t, ok)
	linked, ok := prSeg(p, &git.PRStatus{HasPR: true, Number: 372, URL: prURL})
	require.True(t, ok)

	require.Equal(t, bare.width(), linked.width(),
		"layout width() is measured on the visible text, so the link adds no columns")
	require.Equal(t, ansi.StringWidth(bare.render()), ansi.StringWidth(linked.render()),
		"rendered display width is identical with and without the hyperlink")
	require.Equal(t, ansi.Strip(bare.render()), ansi.Strip(linked.render()),
		"visible text is identical with and without the hyperlink")
}

// The diff-header PR segment becomes a hyperlink when a URL is known, and the
// header's laid-out width is unchanged (OSC 8 stays out of lipgloss width math).
func TestGitContextHeader_PRSegmentHyperlinkNoWidthDrift(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	forceColorProfile(t)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "feat"
	stats := &git.DiffStats{Added: 3, Removed: 1, FilesChanged: 1}

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 5, State: "OPEN", URL: "https://github.com/x/y/pull/5"})
	linked := gitContextHeader(inst, stats)
	require.Contains(t, linked, ansi.SetHyperlink("https://github.com/x/y/pull/5"))
	require.Contains(t, ansi.Strip(linked), "PR #5 open", "visible PR text is preserved")

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 5, State: "OPEN"}) // no URL
	bare := gitContextHeader(inst, stats)
	require.NotContains(t, bare, "\x1b]8;", "no URL means no hyperlink")

	require.Equal(t, lipgloss.Width(bare), lipgloss.Width(linked),
		"the OSC 8 hyperlink adds no width to the diff header")
	require.Equal(t, ansi.Strip(bare), ansi.Strip(linked),
		"visible header text is identical with and without the hyperlink")
}
