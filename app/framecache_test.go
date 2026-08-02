package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/require"

	tea "charm.land/bubbletea/v2"
)

// countingScan wraps scanFrame for the duration of a test and reports how many
// times viewContent actually paid for the bubblezone walk.
func countingScan(t *testing.T) *int {
	t.Helper()
	n := 0
	restore := scanFrame
	scanFrame = func(s string) string {
		n++
		return restore(s)
	}
	t.Cleanup(func() { scanFrame = restore })
	return &n
}

// An unchanged frame is stacked and scanned once, not once per render.
//
// This is the whole point: Bubble Tea calls View() after every message, and an idle
// Atrium produces ~12 of those a second (~32 before the spinner loop and the
// capture chain learned to stop), so without the memo the scan ran on every one of
// them over a frame that never changed.
//
// The scan and the join are counted separately — scanFrame through the package var,
// the join through the memo's own run count — because they are memoized together
// (#565) and a single number could not tell a skipped scan from a skipped stack.
func TestFrameCached_IdenticalFrameScansOnce(t *testing.T) {
	h := newBenchHome(t, 3)
	n := countingScan(t)

	first := h.viewContent()
	for range 9 {
		require.Equal(t, first, h.viewContent(), "an unchanged model must render an identical frame")
	}

	require.Equal(t, 1, *n, "10 renders of an unchanged frame must cost exactly one scan")
	require.Equal(t, 1, h.frameMemo.Runs(), "and exactly one frame stack")
}

// A changed frame is rescanned — the negative control. Without it, the test above
// passes on a memo that never invalidates, which would freeze every click target
// at its first-frame position.
func TestFrameCached_ChangedFrameRescans(t *testing.T) {
	h := newBenchHome(t, 3)
	n := countingScan(t)

	h.viewContent()
	require.Equal(t, 1, *n)

	// Move the selection: a different row is highlighted, so the frame differs.
	h.list.SetSelectedInstance(1)
	h.viewContent()

	require.Equal(t, 2, *n, "a changed frame must be scanned again")
	require.Equal(t, 2, h.frameMemo.Runs(), "and stacked again")
}

// A click still lands on the right row after a memoized render.
//
// This is the claim the memo rests on, asserted through the behaviour that would
// break rather than through the bounds themselves. A zone's ID lives INSIDE the
// pre-scan string as the ANSI marker zone.Mark wrote, so two frames equal at that
// point carry the same markers at the same offsets and the previous scan's
// registration still describes them. If that reasoning were wrong, the symptom
// would be exactly this: a click routed to the wrong session — the failure mode
// bubblezone bounds have produced here before (#434).
func TestFrameCached_ClickStillRoutesToTheRightRowAfterAMemoizedRender(t *testing.T) {
	h := newBenchHome(t, 4)
	want := h.list.GetInstances()[2]

	// Through waitAppZone, not a bare zone.Get: zone.Scan hands its registrations to
	// a background worker across a channel, and zone.DefaultManager is shared by
	// every test in this package, so an immediate read can still be serving an
	// earlier frame's bounds — non-zero, so a "wait for non-zero" check passes, and
	// wrong, so the click misses. That is #434, and #447 closed it by routing every
	// click assertion through this retry's cross-frame consistency check.
	hit := waitAppZone(t, h, listRowZoneIDFor(want))

	// The render under test has to actually be a memo hit, or this asserts nothing
	// about the memo. Counting from here proves the next frame skipped the scan.
	n := countingScan(t)
	h.viewContent()
	require.Zero(t, *n, "precondition: the repeat render must be served from the memo")

	click := testutil.MouseClick(hit.StartX, hit.StartY, tea.MouseLeft)
	require.Same(t, want, h.list.InstanceAtZone(click),
		"a click inside row 2 must still resolve to row 2 after a memoized render")
}

// listRowZoneIDFor mirrors ui.listRowZoneID, which is unexported. Kept beside its
// only use so the duplication is visible: if the id scheme changes, this test stops
// finding a zone and fails loudly rather than silently asserting nothing.
func listRowZoneIDFor(i *session.Instance) string {
	return "list-row-" + i.GroupKey() + "\x00" + i.Title
}
