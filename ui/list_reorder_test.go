package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// MoveGroupDown moves the selected session's entire repo group below the next group,
// as a unit, and keeps the same session selected.
func TestMoveGroupDown_MovesWholeGroup(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB", "/x/repoC")
	require.Equal(t, []string{"repoA", "repoA", "repoB", "repoC"}, repoKeys(l))

	l.SetSelectedInstance(0) // inside repoA
	sel := l.items[0]

	require.True(t, l.MoveGroupDown())
	require.Equal(t, []string{"repoB", "repoA", "repoA", "repoC"}, repoKeys(l))
	// Selection follows the moved session.
	require.Same(t, sel, l.GetSelectedInstance())
}

// MoveGroupUp is the mirror of MoveGroupDown.
func TestMoveGroupUp_MovesWholeGroup(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoB", "/x/repoC")
	require.Equal(t, []string{"repoA", "repoB", "repoB", "repoC"}, repoKeys(l))

	l.SetSelectedInstance(2) // inside repoB (its second item)
	sel := l.items[2]

	require.True(t, l.MoveGroupUp())
	require.Equal(t, []string{"repoB", "repoB", "repoA", "repoC"}, repoKeys(l))
	require.Same(t, sel, l.GetSelectedInstance())
}

// Moving the first group up, or the last group down, is a no-op.
func TestMoveGroup_AtEdges(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB")

	l.SetSelectedInstance(0) // first group
	require.False(t, l.MoveGroupUp())
	require.Equal(t, []string{"repoA", "repoB"}, repoKeys(l))

	l.SetSelectedInstance(1) // last group
	require.False(t, l.MoveGroupDown())
	require.Equal(t, []string{"repoA", "repoB"}, repoKeys(l))
}

// With a single group there is nothing to move past.
func TestMoveGroup_SingleGroup(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA")
	l.SetSelectedInstance(0)
	require.False(t, l.MoveGroupUp())
	require.False(t, l.MoveGroupDown())
}
