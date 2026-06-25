package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// markInst builds a minimal git session with the given title for multi-select
// tests (mkInst, shared with the other ui tests, needs an explicit path).
func markInst(t *testing.T, title string) *session.Instance {
	t.Helper()
	return mkInst(t, title, ".")
}

// ToggleMark flips membership; MarkedCount tracks it; ClearMarks empties it.
func TestList_ToggleMarkAndClear(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	a := markInst(t, "a")
	b := markInst(t, "b")
	l.AddInstance(a)
	l.AddInstance(b)

	require.Equal(t, 0, l.MarkedCount(), "nothing marked initially")
	require.False(t, l.IsMarked(a))

	l.ToggleMark(a)
	require.True(t, l.IsMarked(a))
	require.Equal(t, 1, l.MarkedCount())

	l.ToggleMark(b)
	require.Equal(t, 2, l.MarkedCount())

	l.ToggleMark(a) // unmark a
	require.False(t, l.IsMarked(a))
	require.Equal(t, 1, l.MarkedCount())

	l.ClearMarks()
	require.Equal(t, 0, l.MarkedCount())
	require.False(t, l.IsMarked(b))
}

// ToggleMark(nil) is a no-op (the selected instance can be nil on an empty list).
func TestList_ToggleMarkNilIsNoop(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	l.ToggleMark(nil)
	require.Equal(t, 0, l.MarkedCount())
}

// MarkedInstancesInView returns marked rows in list order and respects the active
// filter, so a marked row hidden by the filter is out of scope for batch actions.
func TestList_MarkedInstancesInViewRespectsFilterAndOrder(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	alpha := markInst(t, "alpha")
	beta := markInst(t, "beta")
	gamma := markInst(t, "gamma")
	l.AddInstance(alpha)
	l.AddInstance(beta)
	l.AddInstance(gamma)

	l.ToggleMark(gamma)
	l.ToggleMark(alpha)

	got := l.MarkedInstancesInView()
	require.Equal(t, []*session.Instance{alpha, gamma}, got, "marked instances come back in list order")

	// A filter that hides alpha drops it from the marked-in-view scope.
	l.SetFilter("beta")
	require.Empty(t, l.MarkedInstancesInView(), "no marked row passes the 'beta' filter")
	require.Equal(t, 0, l.MarkedCount())

	l.SetFilter("")
	require.Len(t, l.MarkedInstancesInView(), 2, "clearing the filter restores the marked scope")
}

// A marked instance removed from the list since marking simply drops out of the
// marked-in-view scope (the stale pointer is harmless).
func TestList_MarkedInstancesInViewDropsRemoved(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	a := markInst(t, "a")
	b := markInst(t, "b")
	l.AddInstance(a)
	l.AddInstance(b)
	l.ToggleMark(a)
	l.ToggleMark(b)

	l.KillInstance(a) // a leaves the list while still in the marked set

	got := l.MarkedInstancesInView()
	require.Equal(t, []*session.Instance{b}, got, "a removed instance drops from the marked scope")
	require.Equal(t, 1, l.MarkedCount())
}

// A marked row renders the mark glyph in its left gutter; an unmarked row does not.
func TestRender_MarkGlyph(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	mark := theme.Current().Glyphs.MarkChecked

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst := markInst(t, "t")

	marked := ansi.Strip(r.Render(inst, 1, false, true))
	require.Contains(t, marked, mark, "a marked row must show the mark glyph")

	unmarked := ansi.Strip(r.Render(inst, 1, false, false))
	require.NotContains(t, unmarked, mark, "an unmarked row must not show the mark glyph")
}
