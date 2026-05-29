package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// addRepo appends a session for the given repo path to the list, mirroring newGroupList.
func addRepo(t *testing.T, l *List, path string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: "x", Path: path, Program: "echo"})
	require.NoError(t, err)
	l.AddInstance(inst)
	return inst
}

// Collapsing a group hides all its members (the header stands in for them, with a count)
// while leaving other groups fully visible. Expanding restores them.
func TestCollapse_HidesMembersAndShowsCount(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.items[0].SetDisplayName("alpha")
	l.items[1].SetDisplayName("beta")
	l.items[2].SetDisplayName("gamma")

	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse())

	out := l.String()
	require.NotContains(t, out, "alpha", "collapsed group members are hidden")
	require.NotContains(t, out, "beta", "collapsed group members are hidden")
	require.Contains(t, out, "(2)", "collapsed header shows its member count")
	require.Contains(t, out, "▸", "collapsed header shows the folded marker")
	require.Contains(t, out, "gamma", "other groups stay visible")

	require.True(t, l.ToggleCollapse())
	out = l.String()
	require.Contains(t, out, "alpha", "expanding restores members")
	require.Contains(t, out, "beta")
	require.Contains(t, out, "▾", "expanded header shows the unfolded marker")
}

// Collapsing from a non-anchor member snaps the selection to the group anchor so the cursor
// never rests on a hidden item.
func TestCollapse_SnapsSelectionToAnchor(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1) // non-anchor member of repoA

	require.True(t, l.ToggleCollapse())
	require.Equal(t, 0, l.selectedIdx)
}

// Navigation skips the hidden members of a collapsed group.
func TestCollapse_NavigationSkipsHiddenMembers(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse())

	l.Down() // from repoA anchor, skip hidden member, land on repoB
	require.Equal(t, 2, l.selectedIdx)

	l.Up() // back to repoA anchor
	require.Equal(t, 0, l.selectedIdx)
}

// Collapse is meaningless with a single repo (no headers render), so it is a no-op.
func TestCollapse_SingleRepoIsNoOp(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.False(t, l.ToggleCollapse())
}

// Within-group reorder (J/K) is blocked while the group is collapsed — there are no visible
// siblings to swap with.
func TestMoveWithinGroup_BlockedWhenCollapsed(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse())

	require.False(t, l.MoveDown())
}

// ToggleCollapseAll collapses every group when any is expanded, then expands every group.
func TestCollapseAll_TogglesEverything(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoC")
	l.SetSize(80, 40)
	l.items[0].SetDisplayName("alpha")
	l.items[1].SetDisplayName("beta")
	l.items[2].SetDisplayName("gamma")

	require.True(t, l.ToggleCollapseAll())
	out := l.String()
	require.NotContains(t, out, "alpha")
	require.NotContains(t, out, "beta")
	require.NotContains(t, out, "gamma")

	require.True(t, l.ToggleCollapseAll())
	out = l.String()
	require.Contains(t, out, "alpha")
	require.Contains(t, out, "beta")
	require.Contains(t, out, "gamma")
}

// Regression: collapsing groups then killing one down to a single remaining repo must not
// hide the survivor. Headers stop rendering at distinctRepoCount<=1, so collapse must be
// inert there or the list soft-locks with everything hidden.
func TestCollapse_IgnoredWhenDownToSingleRepo(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.items[0].SetDisplayName("alpha")
	l.items[1].SetDisplayName("beta")

	// Collapse both groups.
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse())
	l.SetSelectedInstance(1)
	require.True(t, l.ToggleCollapse())

	// Kill repoB, leaving only repoA.
	l.SetSelectedInstance(1)
	l.Kill()

	out := l.String()
	require.Contains(t, out, "alpha", "the lone surviving group must be visible")
	require.NotContains(t, strings.ToUpper(out), "(1)", "no collapsed header for a single repo")
}

// Creating a session into a collapsed group must expand it, so the new session is never hidden.
func TestAddInstance_AutoExpandsCollapsedTargetGroup(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse()) // collapse repoA
	require.True(t, l.effectiveCollapsed("repoA"))

	added := addRepo(t, l, "/x/repoA")
	require.False(t, l.effectiveCollapsed("repoA"), "adding into a folded group expands it")

	l.SelectInstance(added)
	require.False(t, l.isHidden(l.selectedIdx), "the new session is visible")
}

// CollapsedRepos drops keys for repos no longer in the list (so the persisted set can't grow
// unbounded), while keeping keys for repos still present.
func TestCollapsedRepos_PrunesVanishedRepos(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse())
	l.SetSelectedInstance(1)
	require.True(t, l.ToggleCollapse())
	require.ElementsMatch(t, []string{"repoA", "repoB"}, l.CollapsedRepos())

	l.SetSelectedInstance(1) // repoB
	l.Kill()
	require.Equal(t, []string{"repoA"}, l.CollapsedRepos(), "repoB's stale key is pruned")
}

// Killing the anchor of a collapsed group leaves the selection on a visible item.
func TestKill_AnchorOfCollapsedGroupKeepsSelectionVisible(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.ToggleCollapse()) // collapse repoA (2 members)

	l.SetSelectedInstance(0) // anchor
	l.Kill()
	require.False(t, l.isHidden(l.selectedIdx), "selection must rest on a visible item after kill")
}

// A folded group stays folded after it is moved as a whole.
func TestMoveGroup_PreservesCollapsedFlag(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoC")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1)
	require.True(t, l.ToggleCollapse()) // collapse repoB

	require.True(t, l.MoveGroupDown()) // repoB moves below repoC
	require.True(t, l.effectiveCollapsed("repoB"), "the fold travels with the group")
}

// Navigating up off the top wraps to the bottom and skips a collapsed group's hidden members,
// landing on its anchor.
func TestCollapse_UpWrapSkipsHiddenMembers(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1)
	require.True(t, l.ToggleCollapse()) // collapse repoB (anchor 1, hidden member 2)

	l.SetSelectedInstance(0) // repoA
	l.Up()                   // wraps past hidden index 2 to repoB anchor
	require.Equal(t, 1, l.selectedIdx)
}
