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
	require.True(t, l.Collapse())

	out := l.String()
	require.NotContains(t, out, "alpha", "collapsed group members are hidden")
	require.NotContains(t, out, "beta", "collapsed group members are hidden")
	require.Contains(t, out, "(2)", "collapsed header shows its member count")
	require.Contains(t, out, "▸", "collapsed header shows the folded marker")
	require.Contains(t, out, "gamma", "other groups stay visible")

	require.True(t, l.Expand())
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

	require.True(t, l.Collapse())
	require.Equal(t, 0, l.selectedIdx)
}

// Navigation skips the hidden members of a collapsed group.
func TestCollapse_NavigationSkipsHiddenMembers(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse())

	l.Down() // from repoA anchor, skip hidden member, land on repoB
	require.Equal(t, 2, l.selectedIdx)

	l.Up() // back to repoA anchor
	require.Equal(t, 0, l.selectedIdx)
}

// Folding is meaningless with a single repo (no headers render), so both directions are no-ops.
func TestCollapse_SingleRepoIsNoOp(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.False(t, l.Collapse())
	require.False(t, l.Expand())
}

// Within-group reorder (J/K) is blocked while the group is collapsed — there are no visible
// siblings to swap with.
func TestMoveWithinGroup_BlockedWhenCollapsed(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse())

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
	require.True(t, l.Collapse())
	l.SetSelectedInstance(1)
	require.True(t, l.Collapse())

	// Kill repoB, leaving only repoA.
	l.SetSelectedInstance(1)
	_ = l.KillInstance(l.GetSelectedInstance())

	out := l.String()
	require.Contains(t, out, "alpha", "the lone surviving group must be visible")
	require.NotContains(t, strings.ToUpper(out), "(1)", "no collapsed header for a single repo")
}

// Creating a session into a collapsed group must expand it, so the new session is never hidden.
func TestAddInstance_AutoExpandsCollapsedTargetGroup(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse()) // collapse repoA
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
	require.True(t, l.Collapse())
	l.SetSelectedInstance(1)
	require.True(t, l.Collapse())
	require.ElementsMatch(t, []string{"repoA", "repoB"}, l.CollapsedRepos())

	l.SetSelectedInstance(1) // repoB
	_ = l.KillInstance(l.GetSelectedInstance())
	require.Equal(t, []string{"repoA"}, l.CollapsedRepos(), "repoB's stale key is pruned")
}

// Killing the anchor of a collapsed group leaves the selection on a visible item.
func TestKill_AnchorOfCollapsedGroupKeepsSelectionVisible(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse()) // collapse repoA (2 members)

	l.SetSelectedInstance(0) // anchor
	_ = l.KillInstance(l.GetSelectedInstance())
	require.False(t, l.isHidden(l.selectedIdx), "selection must rest on a visible item after kill")
}

// A folded group stays folded after it is moved as a whole.
func TestMoveGroup_PreservesCollapsedFlag(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoC")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1)
	require.True(t, l.Collapse()) // collapse repoB

	require.True(t, l.MoveGroupDown()) // repoB moves below repoC
	require.True(t, l.effectiveCollapsed("repoB"), "the fold travels with the group")
}

// Navigating up off the top wraps to the bottom and skips a collapsed group's hidden members,
// landing on its anchor.
func TestCollapse_UpWrapSkipsHiddenMembers(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoB", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1)
	require.True(t, l.Collapse()) // collapse repoB (anchor 1, hidden member 2)

	l.SetSelectedInstance(0) // repoA
	l.Up()                   // wraps past hidden index 2 to repoB anchor
	require.Equal(t, 1, l.selectedIdx)
}

// Collapse is directional, not a toggle: folding an already-folded group is a no-op
// (returns false), so the caller skips the persistence write and re-render.
func TestCollapse_NoOpWhenAlreadyCollapsed(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)

	require.True(t, l.Collapse())
	require.False(t, l.Collapse(), "folding a folded group must not report a change")
	require.Equal(t, []string{"repoA"}, l.CollapsedRepos(), "the fold itself is untouched")
}

// Expand is directional too: unfolding an already-expanded group is a no-op.
func TestExpand_NoOpWhenExpanded(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(0)

	require.False(t, l.Expand(), "unfolding an unfolded group must not report a change")
	require.Empty(t, l.CollapsedRepos())
}

// Expanding from the collapsed header unfolds the group and leaves the selection on the
// anchor, so the cursor stays where the user's eye already is.
func TestExpand_FromHeaderKeepsSelectionOnAnchor(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(1)
	require.True(t, l.Collapse()) // selection snaps to the anchor (index 0)
	require.Equal(t, 0, l.selectedIdx)

	require.True(t, l.Expand())
	require.Equal(t, 0, l.selectedIdx, "selection stays on the anchor after expanding")
	require.False(t, l.isHidden(1), "the group's members are visible again")
}

// A fold is inert below two groups (effectiveCollapsed), so the row that reactivates
// one is the *first* row of a NEW repo — a group AddInstanceKeepingFolds never touches.
// The cursor is what wins there: rather than let a stale fold hide the row a human left
// the cursor on, the call drops that one fold.
//
// Both alternatives are the same hazard wearing different clothes. Leaving the fold and
// clamping moves the selection to a DIFFERENT *session.Instance, which the app then
// repoints preview, diff and menu at; leaving the fold and not clamping parks the cursor
// on a row nobody can see. Every destructive key targets the selection, so either one
// means a background create can arrange for the next keypress to land somewhere the user
// did not put it. Hence require.Same on the instance, not an index: an index that
// happens to match after a shift would prove nothing about which session is selected.
func TestAddInstanceKeepingFolds_NewRepoKeepsTheCursorRatherThanTheFold(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)

	// Fold repoA while two groups exist — Collapse refuses below that.
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse())

	// Kill repoB's only session. RemoveInstance never prunes l.collapsed, so the fold
	// key survives while effectiveCollapsed makes it inert at one group.
	l.RemoveInstance(l.items[2])
	require.True(t, l.collapsed["repoA"], "the fold key survives the removal")
	require.False(t, l.effectiveCollapsed("repoA"), "and is inert with one group left")

	// Every repoA row is drawn, so the cursor can rest on a non-anchor member.
	l.SetSelectedInstance(1)
	require.False(t, l.isHidden(1), "precondition: the row is visible")
	parked := l.GetSelectedInstance()
	require.NotSame(t, l.items[0], parked, "precondition: not already on the anchor")

	// A background create lands the first row of a third repo, taking the list back
	// over two groups — which would reactivate repoA's fold underneath the cursor.
	inst, err := session.NewInstance(session.InstanceOptions{Title: "bg", Path: "/x/repoC", Program: "echo"})
	require.NoError(t, err)
	l.AddInstanceKeepingFolds(inst)

	require.Same(t, parked, l.GetSelectedInstance(),
		"a background create must not move the cursor to another session")
	require.False(t, l.isHidden(l.selectedIdx), "nor leave it on a row nobody can see")
	require.False(t, l.effectiveCollapsed("repoA"),
		"the one fold that would have hidden the cursor is the one that yields")
}

// The fold that yields above is exactly one: a fold on a group the cursor is NOT in
// survives being reactivated, because nothing about it hides the selection. Without
// this, "drop the fold that hides the cursor" and "drop every fold" pass the same
// assertions, and the second is just AddInstance.
func TestAddInstanceKeepingFolds_NewRepoKeepsAFoldTheCursorIsNotIn(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)

	l.SetSelectedInstance(0)
	require.True(t, l.Collapse()) // fold repoA
	l.RemoveInstance(l.items[2])  // drop repoB; repoA's fold goes inert

	// The cursor sits on repoA's ANCHOR this time, which a live fold leaves visible.
	l.SetSelectedInstance(0)
	anchored := l.GetSelectedInstance()

	inst, err := session.NewInstance(session.InstanceOptions{Title: "bg", Path: "/x/repoC", Program: "echo"})
	require.NoError(t, err)
	l.AddInstanceKeepingFolds(inst)

	require.Same(t, anchored, l.GetSelectedInstance(), "the cursor still did not move")
	require.True(t, l.effectiveCollapsed("repoA"),
		"and repoA stays folded, because folding it hides no row the cursor is on")
	require.True(t, l.isHidden(1), "its non-anchor member is folded away as asked")
}

// The plainest case, and the one the two above are variations on: a list with no fold to
// reactivate keeps its cursor exactly where it was. Fold SURVIVAL is not what this pins —
// nothing here is folded — that is
// TestAddInstanceKeepingFolds_NewRepoKeepsAFoldTheCursorIsNotIn's job.
func TestAddInstanceKeepingFolds_LeavesASettledCursorAlone(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)
	l.SetSelectedInstance(2) // repoB's row, visible throughout
	settled := l.GetSelectedInstance()

	inst, err := session.NewInstance(session.InstanceOptions{Title: "bg", Path: "/x/repoA", Program: "echo"})
	require.NoError(t, err)
	l.AddInstanceKeepingFolds(inst)

	// By identity, not by index: the insert lands above repoB, so AddInstance shifts
	// the index to keep pointing at the same row. What must not happen is the clamp
	// moving the cursor to a *different* session.
	require.Same(t, settled, l.GetSelectedInstance(), "the clamp does not move a visible selection")
	require.False(t, l.isHidden(l.selectedIdx))
}

// While a filter is active, a hidden selection is hidden because it does not MATCH:
// isHidden gates on the filter ahead of folds and returns there, so no fold change can
// reveal the row. Dropping the fold anyway would destroy persisted state the user set and
// buy nothing — an easy over-correction, since the "is the selection hidden?" test reads
// true in both cases and only the reason differs.
func TestAddInstanceKeepingFolds_FilterHiddenSelectionKeepsTheFold(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSize(80, 40)

	l.SetSelectedInstance(0)
	require.True(t, l.Collapse()) // fold repoA
	l.RemoveInstance(l.items[2])  // drop repoB; repoA's fold goes inert
	l.SetSelectedInstance(1)

	// A filter nothing matches (every fixture instance is titled "x").
	l.SetFilter("zzz-matches-nothing")
	require.True(t, l.Filtering())
	require.True(t, l.isHidden(l.selectedIdx), "precondition: hidden, but by the filter")

	inst, err := session.NewInstance(session.InstanceOptions{Title: "bg", Path: "/x/repoC", Program: "echo"})
	require.NoError(t, err)
	l.AddInstanceKeepingFolds(inst)

	require.True(t, l.collapsed["repoA"],
		"a fold must not be spent on a selection the filter is hiding")
}
