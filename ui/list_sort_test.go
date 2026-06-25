package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// In status mode each repo group is ordered by action-priority: NeedsInput, then
// unread Ready, then seen Ready, then Running.
func TestSessionSort_StatusOrdersWithinGroup(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/a", "/r/a")
	running, needs, seen, unread := l.items[0], l.items[1], l.items[2], l.items[3]
	running.SetStatus(session.Running)
	needs.SetStatus(session.NeedsInput)
	// seen stays a fresh Ready (NewInstance starts Ready without a transition).
	markUnread(t, unread)

	l.SetSortMode("status")

	require.Equal(t, []*session.Instance{needs, unread, seen, running},
		l.items, "group ordered NeedsInput, unread Ready, seen Ready, Running")
}

// The sort never moves a session across repo-group boundaries: a NeedsInput in
// repoB must not jump ahead of repoA, and same-repo items stay contiguous.
func TestSessionSort_RespectsGroupBoundaries(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/b", "/r/b")
	l.items[3].SetStatus(session.NeedsInput) // most urgent, but in repoB

	l.SetSortMode("status")

	require.Equal(t, []string{"a", "a", "b", "b"}, repoKeys(l), "groups stay contiguous and in order")
	require.Equal(t, session.NeedsInput, l.items[2].GetStatus(), "NeedsInput rose to the top of repoB only")
}

// Same-tier sessions keep their canonical order (stable sort) — no churn among
// equals.
func TestSessionSort_StableTieBreak(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/a")
	a, b, c := l.items[0], l.items[1], l.items[2]
	for _, inst := range []*session.Instance{a, b, c} {
		inst.SetStatus(session.Running)
	}

	l.SetSortMode("status")

	require.Equal(t, []*session.Instance{a, b, c}, l.items, "equal-priority order unchanged")
}

// Re-sorting keeps the cursor on the same session by identity, even when that
// session moves position.
func TestSessionSort_PreservesSelection(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/a")
	running := l.items[0]
	running.SetStatus(session.Running) // will sink to the bottom
	l.SetSelectedInstance(0)

	l.SetSortMode("status")

	require.Same(t, running, l.GetSelectedInstance(), "selection follows the moved session")
}

// J/K manual reorder is disabled under a sort mode (the app shows a hint), while
// whole-group reorder stays available.
func TestSessionSort_DisablesManualReorderButKeepsGroupMove(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/b")
	l.SetSortMode("status")

	require.False(t, l.ManualReorderEnabled())
	require.False(t, l.MoveUp(), "J/K within-group reorder is inert in status mode")
	require.False(t, l.MoveDown())

	l.SetSelectedInstance(0) // inside repoA
	require.True(t, l.MoveGroupDown(), "group reorder still works in status mode")
	require.Equal(t, []string{"b", "a", "a"}, repoKeys(l))
}

// Switching to status and back restores the manual order captured on entry,
// including a manual J/K reordering made beforehand.
func TestSessionSort_RoundTripRestoresManualOrder(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/a")
	a, b, c := l.items[0], l.items[1], l.items[2]

	// Manually reorder in creation mode: move c up to the front.
	l.SetSelectedInstance(2)
	require.True(t, l.MoveUp())
	require.True(t, l.MoveUp())
	require.Equal(t, []*session.Instance{c, a, b}, l.items)

	a.SetStatus(session.NeedsInput) // make status order differ from manual
	l.SetSortMode("status")
	require.Equal(t, []*session.Instance{a, c, b}, l.items, "status order differs from manual")

	l.SetSortMode("creation")
	require.Equal(t, []*session.Instance{c, a, b}, l.items, "manual order restored")
}

// A kill while sorted is reflected in the canonical order, so switching back to
// creation shows the removal.
func TestSessionSort_KillReflectedInCanonicalOrder(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/a")
	a, b, c := l.items[0], l.items[1], l.items[2]
	_ = b
	l.SetSortMode("status")

	l.KillInstance(l.items[1])
	l.SetSortMode("creation")

	require.Equal(t, []*session.Instance{a, c}, l.items, "killed session gone from canonical order")
}

// A new session added while sorted lands at its priority position and persists in
// the canonical order.
func TestSessionSort_AddInstanceWhileSorted(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a")
	l.items[0].SetStatus(session.Running)
	l.items[1].SetStatus(session.Running)
	l.SetSortMode("status")

	newInst, err := session.NewInstance(session.InstanceOptions{Title: "x", Path: "/r/a", Program: "echo"})
	require.NoError(t, err)
	newInst.SetStatus(session.NeedsInput)
	l.AddInstance(newInst)()

	require.Same(t, newInst, l.items[0], "new urgent session floats to the top of its group")
	require.Len(t, l.InstancesForPersist(), 3)
}

// ApplySort reports no change (and does not churn) when the computed order matches
// the current order.
func TestSessionSort_ApplySortNoChurnWhenUnchanged(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a")
	l.SetSortMode("status")
	require.False(t, l.ApplySort(), "second sort with unchanged statuses is a no-op")
}

// ApplySort is inert in creation mode — the default never reorders.
func TestSessionSort_ApplySortNoopInCreation(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a")
	a, b := l.items[0], l.items[1]
	a.SetStatus(session.Running)
	b.SetStatus(session.NeedsInput)

	require.False(t, l.ApplySort())
	require.Equal(t, []*session.Instance{a, b}, l.items, "creation order untouched")
}

// regroupManualLike reorders manual's group blocks to match like's group order
// while preserving each group's internal manual order.
func TestRegroupManualLike_ReordersGroupsKeepsWithinGroupOrder(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/b")
	a1, a2, b1 := l.items[0], l.items[1], l.items[2]

	// like puts group b before group a; within-group order must be untouched.
	got := regroupManualLike([]*session.Instance{a1, a2, b1}, []*session.Instance{b1, a1, a2})
	require.Equal(t, []*session.Instance{b1, a1, a2}, got)
}

// Safety net: a group present in manual but never mentioned by like is kept
// (appended in manual order), not silently dropped from the canonical order.
func TestRegroupManualLike_KeepsGroupsAbsentFromLike(t *testing.T) {
	l := newGroupList(t, "/r/a", "/r/a", "/r/b")
	a1, a2, b1 := l.items[0], l.items[1], l.items[2]

	got := regroupManualLike([]*session.Instance{a1, a2, b1}, []*session.Instance{a1, a2})
	require.Equal(t, []*session.Instance{a1, a2, b1}, got, "missing group b is appended, not lost")
}
