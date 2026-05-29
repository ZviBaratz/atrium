package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
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

func newTestList(titles ...string) *List {
	s := spinner.New()
	l := NewList(&s, false)
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
