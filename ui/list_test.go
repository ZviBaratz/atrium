package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// renderRow renders a single instance row with the given diff stats at a width
// wide enough that the git-context cluster is never dropped for space. It pins
// the unicode theme so glyph assertions (⇣ ⇡ *) are stable across themes.
func renderRow(t *testing.T, branch string, stats *git.DiffStats) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = branch
	inst.SetDiffStats(stats)
	return r.Render(inst, 1, false)
}

func TestRender_GitContextCluster(t *testing.T) {
	// Behind, ahead, and dirty all present → all three glyphs render.
	out := renderRow(t, "feat", &git.DiffStats{Added: 5, Removed: 2, Commits: 3, Behind: 2, Dirty: true})
	require.Contains(t, out, "⇣2", "behind count should render")
	require.Contains(t, out, "⇡3", "commit count should render")
	require.Contains(t, out, "*", "dirty marker should render")

	// Clean, all committed, base unchanged → no extra glyphs, just the diff.
	out = renderRow(t, "feat", &git.DiffStats{Added: 5, Removed: 2, Commits: 2})
	require.NotContains(t, out, "⇣", "behind glyph must be absent when not behind")
	require.NotContains(t, out, "*", "dirty marker must be absent when clean")
	require.Contains(t, out, "⇡2", "commit count should still render")
}

// A direct (non-git) session has no branch, so rendering the git line would leave a
// dangling branch glyph with no name. The row must instead show a dim "direct" marker —
// consistent with the diff pane, menu, and picker hint.
func TestRender_DirectSessionShowsMarkerNotBranchGlyph(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	g := theme.Current().Glyphs

	gitInst, err := session.NewInstance(session.InstanceOptions{Title: "g", Path: ".", Program: "echo"})
	require.NoError(t, err)
	require.Contains(t, r.Render(gitInst, 1, false), g.Branch,
		"a git session row carries the branch glyph")

	directInst, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	row := r.Render(directInst, 1, false)
	require.Contains(t, row, "direct", "a direct session row shows the direct marker")
	require.NotContains(t, row, g.Branch, "a direct session row must not render a dangling branch glyph")
}

func newTestList(titles ...string) *List {
	s := spinner.New()
	l := NewList(&s)
	for _, t := range titles {
		inst, _ := session.NewInstance(session.InstanceOptions{
			Title:   t,
			Path:    ".",
			Program: "echo",
		})
		l.AddInstance(inst)
	}
	return l
}

// An empty, unfiltered list is the primary first-run surface once the always-on
// hint bar is gone, so it must surface the two essential onboarding keys.
func TestList_EmptyStateHint(t *testing.T) {
	l := newTestList()
	l.SetSize(40, 20)
	out := l.String()
	require.Contains(t, out, "new", "empty list shows the new-session hint")
	require.Contains(t, out, "keys", "empty list shows the help hint")
}

// The onboarding hint is for the genuinely empty list only: it must not appear
// once sessions exist, nor clobber the filter affordances during an active filter.
func TestList_EmptyStateHint_SuppressedWhenNotEmptyOrFiltering(t *testing.T) {
	// Non-empty list: no onboarding hint. ("keys" is the hint's distinctive marker —
	// it appears in neither session rows nor the "no matches" line.)
	l := newTestList("alpha")
	l.SetSize(40, 20)
	require.NotContains(t, l.String(), "keys", "a non-empty list must not show the onboarding hint")

	// Empty but mid-filter (filter bar active, empty query): the filter bar owns the
	// view; the onboarding hint must not overwrite it.
	lf := newTestList()
	lf.SetSize(40, 20)
	lf.SetFilterActive(true)
	require.NotContains(t, lf.String(), "keys", "an active filter must suppress the onboarding hint")

	// Empty result from a non-matching query keeps the existing "no matches" hint,
	// not the onboarding hint.
	lq := newTestList("alpha")
	lq.SetSize(40, 20)
	lq.SetFilterActive(true)
	lq.SetFilter("zzz")
	out := lq.String()
	require.Contains(t, out, "no matches", "a non-matching filter shows the no-matches hint")
	require.NotContains(t, out, "keys", "a filtered list must not show the onboarding hint")
}

func TestMoveUp(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveUp()
	require.True(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "b", l.items[0].Title)
	require.Equal(t, "a", l.items[1].Title)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveUp_AtTop(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(0)

	moved := l.MoveUp()
	require.False(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
}

func TestMoveDown(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveDown()
	require.True(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
	require.Equal(t, "c", l.items[1].Title)
	require.Equal(t, "b", l.items[2].Title)
}

func TestMoveDown_AtBottom(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(2)

	moved := l.MoveDown()
	require.False(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveWithSingleItem(t *testing.T) {
	l := newTestList("only")
	l.SetSelectedInstance(0)

	require.False(t, l.MoveUp())
	require.False(t, l.MoveDown())
}

func TestList_RendersDisplayLabel(t *testing.T) {
	l := newTestList("original")
	l.SetSize(80, 20)

	// With no label set, the list shows the Title.
	require.Contains(t, l.String(), "original", "shows Title when no label is set")

	// Once a cosmetic label is set, the list shows it in place of the Title.
	l.items[0].SetDisplayName("renamed")
	require.Contains(t, l.String(), "renamed", "shows the custom label when set")
}

func TestFmtAge(t *testing.T) {
	require.Equal(t, "", fmtAge(time.Time{}), "zero time returns empty string")
	require.Equal(t, "", fmtAge(time.Now().Add(-30*time.Second)), "sub-minute returns empty string")
	require.Equal(t, "5m", fmtAge(time.Now().Add(-5*time.Minute)), "minutes label")
	require.Equal(t, "2h", fmtAge(time.Now().Add(-2*time.Hour)), "hours label")
	require.Equal(t, "3d", fmtAge(time.Now().Add(-72*time.Hour)), "days label")
}

func TestRender_SessionAge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "feat/x"

	// Match an age token (digits followed by m/h/d at a word boundary) so the
	// assertion targets the label specifically, not any incidental "m"/"h"/"d"
	// in a branch name or status word.
	ageToken := regexp.MustCompile(`\d+[mhd]\b`)

	// Brand-new session (CreatedAt = time.Now()) → no age label.
	out := r.Render(inst, 0, false)
	require.NotRegexp(t, ageToken, out, "fresh session should not show age")

	// Simulate a 3-hour-old session.
	inst.CreatedAt = time.Now().Add(-3 * time.Hour)
	out = r.Render(inst, 0, false)
	require.Contains(t, out, "3h", "3-hour-old session should show '3h'")

	// Simulate a 2-day-old session.
	inst.CreatedAt = time.Now().Add(-48 * time.Hour)
	out = r.Render(inst, 0, false)
	require.Contains(t, out, "2d", "2-day-old session should show '2d'")
}

// A direct (non-git) session carries the same right-aligned age label as a git
// session: CreatedAt is just as meaningful without a worktree. Fresh (< 1 min)
// direct sessions stay unlabeled, like their git counterparts.
func TestRender_SessionAge_Direct(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)

	ageToken := regexp.MustCompile(`\d+[mhd]\b`)

	// Brand-new direct session → no age label.
	out := r.Render(inst, 0, false)
	require.NotRegexp(t, ageToken, out, "fresh direct session should not show age")

	// Simulate a 3-hour-old direct session: the label renders alongside the
	// direct marker.
	inst.CreatedAt = time.Now().Add(-3 * time.Hour)
	out = r.Render(inst, 0, false)
	require.Contains(t, out, "3h", "3-hour-old direct session should show '3h'")
	require.Contains(t, out, "direct", "the direct marker must survive the age label")
}

// TestRender_SessionAgeBudget locks in the headline width property: the age
// label steals horizontal budget from the branch (the only variable-length
// part) so the rendered line still fits the width exactly. A regression here is
// what reintroduces width-wrap, which desyncs bubbletea's incremental renderer.
func TestRender_SessionAgeBudget(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	const width = 28
	r.setWidth(width)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "feature/a-very-long-branch-name-that-overflows"

	lineWidth := func(out string) int {
		// The row is two lines (identity + version-control); measure the wider.
		// ansi.StringWidth ignores escape sequences, giving true display width.
		widest := 0
		for _, ln := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(ln); w > widest {
				widest = w
			}
		}
		return widest
	}

	withoutAge := r.Render(inst, 0, false)
	require.LessOrEqual(t, lineWidth(withoutAge), width, "row must fit width with no age label")

	inst.CreatedAt = time.Now().Add(-3 * time.Hour)
	withAge := r.Render(inst, 0, false)
	require.Contains(t, withAge, "3h", "age label should render")
	require.LessOrEqual(t, lineWidth(withAge), width, "row must still fit width once the age label is added")

	// Direct (non-git) mode: the fixed marker is the only left-hand content, so
	// the age steals budget from it instead of a branch. The row must still fit
	// exactly — including at a width narrower than the marker itself.
	direct, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	direct.CreatedAt = time.Now().Add(-3 * time.Hour)
	directRow := r.Render(direct, 0, false)
	require.Contains(t, directRow, "3h", "direct-mode age label should render")
	require.LessOrEqual(t, lineWidth(directRow), width, "direct row must fit width with the age label")

	const narrow = 12 // narrower than the "direct · no git isolation" marker
	r.setWidth(narrow)
	require.LessOrEqual(t, lineWidth(r.Render(direct, 0, false)), narrow,
		"direct row must fit even when the marker itself must truncate")
}
