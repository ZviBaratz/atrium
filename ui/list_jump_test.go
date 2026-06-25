package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// NextUnread walks forward to each unread Ready session, skipping seen-Ready and
// non-Ready rows, and wraps back to the top.
func TestNextUnread_SkipsNonUnreadAndWraps(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoA", "/tmp/repoA")
	markUnread(t, l.items[0])
	l.items[0].MarkSeen() // seen Ready — must be skipped
	markUnread(t, l.items[1])
	l.items[2].SetStatus(session.Running) // working — must be skipped
	markUnread(t, l.items[3])

	l.SetSelectedInstance(0)
	require.True(t, l.NextUnread())
	require.Same(t, l.items[1], l.GetSelectedInstance())

	require.True(t, l.NextUnread())
	require.Same(t, l.items[3], l.GetSelectedInstance())

	require.True(t, l.NextUnread(), "wraps past the end")
	require.Same(t, l.items[1], l.GetSelectedInstance())
}

// With no unread session anywhere, NextUnread is a no-op and reports false (the
// dispatcher turns that into the hint).
func TestNextUnread_NoneReturnsFalse(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")
	l.items[0].SetStatus(session.Running)
	markUnread(t, l.items[1])
	l.items[1].MarkSeen()

	l.SetSelectedInstance(0)
	require.False(t, l.NextUnread())
	require.Same(t, l.items[0], l.GetSelectedInstance(), "selection unchanged")
}

// "next" is others-only: sitting on the sole unread session reports false rather
// than re-selecting itself.
func TestNextUnread_OnlyCurrentReturnsFalse(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")
	markUnread(t, l.items[0])
	l.items[1].SetStatus(session.Running)

	l.SetSelectedInstance(0)
	require.False(t, l.NextUnread())
	require.Same(t, l.items[0], l.GetSelectedInstance())
}

// NextNeedsInput matches only NeedsInput sessions, ignoring unread Ready ones, and
// wraps.
func TestNextNeedsInput_MatchesOnlyNeedsInput(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoA")
	markUnread(t, l.items[0]) // unread Ready — not a NeedsInput target
	l.items[1].SetStatus(session.NeedsInput)
	l.items[2].SetStatus(session.NeedsInput)

	l.SetSelectedInstance(0)
	require.True(t, l.NextNeedsInput())
	require.Same(t, l.items[1], l.GetSelectedInstance())

	require.True(t, l.NextNeedsInput())
	require.Same(t, l.items[2], l.GetSelectedInstance())

	require.True(t, l.NextNeedsInput(), "wraps")
	require.Same(t, l.items[1], l.GetSelectedInstance())
}

// NextUnread ignores NeedsInput sessions: a list with only NeedsInput rows has no
// unread target.
func TestNextUnread_IgnoresNeedsInput(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")
	l.items[0].SetStatus(session.NeedsInput)
	l.items[1].SetStatus(session.NeedsInput)

	l.SetSelectedInstance(0)
	require.False(t, l.NextUnread())
}

// An unread session folded inside a collapsed group is surfaced by landing on the
// group's (badged) anchor, and a further press still advances to a different
// group's unread — proving the scan never sticks on the collapsed anchor.
func TestNextUnread_CollapsedGroupLandsOnAnchorThenAdvances(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoB", "/tmp/repoB")
	l.SetSelectedInstance(0)
	require.True(t, l.Collapse()) // fold repoA; index 1 becomes hidden

	markUnread(t, l.items[1]) // unread hidden member of collapsed repoA
	markUnread(t, l.items[3]) // unread visible member of repoB

	l.SetSelectedInstance(3)
	require.True(t, l.NextUnread())
	require.Same(t, l.items[0], l.GetSelectedInstance(), "lands on repoA's collapsed anchor")

	require.True(t, l.NextUnread())
	require.Same(t, l.items[3], l.GetSelectedInstance(), "advances past the anchor to repoB's unread")
}

// A committed filter narrows the scan: an unread session hidden by the filter is
// skipped, and only a filter-matching unread is selected.
func TestNextUnread_RespectsCommittedFilter(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoA")
	l.items[0].SetDisplayName("redfish")
	l.items[1].SetDisplayName("bluegill")
	l.items[2].SetDisplayName("redtail")
	markUnread(t, l.items[0])
	markUnread(t, l.items[1]) // unread but will be filtered out
	markUnread(t, l.items[2])

	l.SetSelectedInstance(0)
	l.SetFilter("red") // hides bluegill

	require.True(t, l.NextUnread())
	require.Same(t, l.items[2], l.GetSelectedInstance(), "skips the filter-hidden unread, lands on redtail")
}
