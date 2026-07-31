package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/require"

	tea "charm.land/bubbletea/v2"

	zone "github.com/lrstanley/bubblezone/v2"
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

// An unchanged frame is scanned once, not once per render.
//
// This is the whole point: Bubble Tea calls View() after every message and an idle
// Atrium produces ~32 of those a second, so without the memo the scan ran 32 times
// a second over a frame that never changed.
func TestScanCached_IdenticalFrameScansOnce(t *testing.T) {
	h := newBenchHome(t, 3)
	n := countingScan(t)

	first := h.viewContent()
	for range 9 {
		require.Equal(t, first, h.viewContent(), "an unchanged model must render an identical frame")
	}

	require.Equal(t, 1, *n, "10 renders of an unchanged frame must cost exactly one scan")
}

// A changed frame is rescanned — the negative control. Without it, the test above
// passes on a memo that never invalidates, which would freeze every click target
// at its first-frame position.
func TestScanCached_ChangedFrameRescans(t *testing.T) {
	h := newBenchHome(t, 3)
	n := countingScan(t)

	h.viewContent()
	require.Equal(t, 1, *n)

	// Move the selection: a different row is highlighted, so the frame differs.
	h.list.SetSelectedInstance(1)
	h.viewContent()

	require.Equal(t, 2, *n, "a changed frame must be scanned again")
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
func TestScanCached_ClickStillRoutesToTheRightRowAfterAMemoizedRender(t *testing.T) {
	h := newBenchHome(t, 4)

	h.viewContent() // real scan
	rows := h.list.GetInstances()
	want := rows[2]
	hit := zone.Get(listRowZoneIDFor(want))
	require.False(t, hit.IsZero(), "precondition: row 2's zone must be registered")

	h.viewContent() // served from the memo

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
