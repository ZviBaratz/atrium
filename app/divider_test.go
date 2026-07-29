package app

import (
	"github.com/ZviBaratz/atrium/internal/testutil"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// minListRatioTest / maxListRatioTest mirror the unexported clamp bounds in the
// config package (config/state.go). If those bounds change, these assertions
// fail loudly rather than silently drifting.
const (
	minListRatioTest = 0.15
	maxListRatioTest = 0.60
)

// layoutListWidth mirrors the split math in updateHandleWindowSizeEvent so tests
// can assert the divider column without reaching into the panes' render output.
func layoutListWidth(h *home) int {
	return int(float32(h.windowWidth) * float32(h.listRatio))
}

// pressAt / motionAt / releaseAt build the three mouse phases of a divider drag.
func pressAt(x, y int) tea.MouseMsg {
	return testutil.MouseClick(x, y, tea.MouseLeft)
}
func motionAt(x, y int) tea.MouseMsg {
	return testutil.MouseMotion(x, y, tea.MouseLeft)
}
func releaseAt(x, y int) tea.MouseMsg {
	return testutil.MouseRelease(x, y, tea.MouseLeft)
}

// TestAdjustListColsStepsExactlyOneColumn: each < / > press moves the divider by
// exactly one terminal column, and the step is stable across repeated presses
// (the centering guards against float32 rounding snapping a step back).
func TestAdjustListColsStepsExactlyOneColumn(t *testing.T) {
	h := newWheelHome(t) // 120x30, default ratio 0.30 -> listWidth 36
	start := layoutListWidth(h)

	for i := 1; i <= 5; i++ {
		_, _ = h.handleKeyPress(runeKey(">"))
		require.Equal(t, start+i, layoutListWidth(h),
			"> press %d must grow the list by exactly one column", i)
	}
	for i := 1; i <= 5; i++ {
		_, _ = h.handleKeyPress(runeKey("<"))
		require.Equal(t, start+5-i, layoutListWidth(h),
			"< press %d must shrink the list by exactly one column", i)
	}
}

// TestAdjustListColsClampsAtBounds: hammering a direction stops at the persisted
// [0.15, 0.60] ratio bounds rather than collapsing or overflowing a pane.
func TestAdjustListColsClampsAtBounds(t *testing.T) {
	h := newWheelHome(t)
	minW := int(120 * minListRatioTest)
	maxW := int(120 * maxListRatioTest)

	for i := 0; i < 200; i++ {
		_, _ = h.handleKeyPress(runeKey(">"))
	}
	require.LessOrEqual(t, layoutListWidth(h), maxW, "growth must clamp at maxListRatio")

	for i := 0; i < 200; i++ {
		_, _ = h.handleKeyPress(runeKey("<"))
	}
	require.GreaterOrEqual(t, layoutListWidth(h), minW, "shrink must clamp at minListRatio")
}

// TestAdjustListColsPreSizeFallback: before the first window-size event there is
// no column basis, so a press falls back to a ratio nudge without dividing by
// zero or panicking.
func TestAdjustListColsPreSizeFallback(t *testing.T) {
	h := newCreateFormHome(t) // never sized: windowWidth == 0
	before := h.appState.GetListRatio()
	require.NotPanics(t, func() { _ = h.adjustListCols(+1) })
	require.Greater(t, h.appState.GetListRatio(), before,
		"a pre-size > press must still widen the list via the fallback")
}

// TestDividerDragUpdatesLiveThenPersistsOnRelease: pressing the seam begins a
// drag; motion tracks the cursor column live WITHOUT writing state.json; release
// persists the final ratio once and ends the drag.
func TestDividerDragUpdatesLiveThenPersistsOnRelease(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)
	persistedBefore := h.appState.GetListRatio()

	// Press on the seam starts the drag; nothing persisted yet.
	_, _ = h.Update(pressAt(seam, 5))
	require.True(t, h.draggingDivider, "a left press on the seam must begin a divider drag")
	require.Equal(t, persistedBefore, h.appState.GetListRatio(),
		"beginning a drag must not write state")

	// Motion updates the live ratio toward X/width but does not persist.
	_, _ = h.Update(motionAt(60, 5))
	require.InDelta(t, 0.5, h.listRatio, 1e-9, "motion must map the cursor column to the live ratio")
	require.Equal(t, persistedBefore, h.appState.GetListRatio(),
		"motion must not write state.json on every event")

	// Release persists the final ratio and ends the drag.
	_, _ = h.Update(releaseAt(60, 5))
	require.False(t, h.draggingDivider, "release must end the drag")
	require.InDelta(t, 0.5, h.appState.GetListRatio(), 1e-9,
		"release must persist the final ratio")
}

// TestDividerDragClampsAtEdges: dragging past either edge clamps to the same
// [0.15, 0.60] bounds the keyboard path uses, so neither pane collapses.
func TestDividerDragClampsAtEdges(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	_, _ = h.Update(pressAt(seam, 5))
	_, _ = h.Update(motionAt(2, 5)) // far left
	require.InDelta(t, minListRatioTest, h.listRatio, 1e-9, "dragging left clamps at minListRatio")

	_, _ = h.Update(motionAt(118, 5)) // far right
	require.InDelta(t, maxListRatioTest, h.listRatio, 1e-9, "dragging right clamps at maxListRatio")
	_, _ = h.Update(releaseAt(118, 5))
}

// TestDividerPressAwayFromSeamDoesNotDrag: a press clear of the seam must not
// start a drag (it falls through to the normal row/tab click handling).
func TestDividerPressAwayFromSeamDoesNotDrag(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	_, _ = h.Update(pressAt(seam+10, 5)) // well inside the preview pane
	require.False(t, h.draggingDivider, "a press away from the seam must not begin a drag")

	_, _ = h.Update(pressAt(3, 5)) // inside the list, over a row
	require.False(t, h.draggingDivider, "a press over a list row must not begin a drag")
}

// TestDividerDragIgnoredInNonDefaultState: with an overlay up the seam is inert,
// mirroring the wheel/left-click gating.
func TestDividerDragIgnoredInNonDefaultState(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	h.state = statePrompt
	_, _ = h.Update(pressAt(seam, 5))
	require.False(t, h.draggingDivider, "the seam must not be draggable while an overlay owns the screen")
}

// TestDividerPressBelowPanesDoesNotDrag: a press at the seam column but on the
// hint/error strip below the panes must not start a drag — that row belongs to
// the menu, not the divider.
func TestDividerPressBelowPanesDoesNotDrag(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	belowPanes := h.paneContentHeight() // first row below the panes (the menu strip)
	require.Less(t, belowPanes, h.windowHeight, "test needs a menu/error row below the panes")

	_, _ = h.Update(pressAt(seam, belowPanes))
	require.False(t, h.draggingDivider, "a seam-column press on the menu strip must not begin a drag")
}

// TestDividerDragAbandonedOnLostRelease: if the release that should end a drag is
// never delivered (button came up off-screen), a later press must clear the stale
// drag and be handled normally — not be swallowed, and not let the next motion
// snap the divider to the cursor.
func TestDividerDragAbandonedOnLostRelease(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	_, _ = h.Update(pressAt(seam, 5))
	require.True(t, h.draggingDivider, "press on the seam begins a drag")

	// The release is lost; the next thing we see is a fresh press elsewhere.
	before := h.listRatio
	_, _ = h.Update(pressAt(seam+20, 5)) // well inside the preview, not on the seam
	require.False(t, h.draggingDivider, "a fresh press must abandon the stale drag")

	// A subsequent button-held motion must NOT move the divider — the drag is over.
	_, _ = h.Update(motionAt(90, 5))
	require.InDelta(t, before, h.listRatio, 1e-9,
		"motion after an abandoned drag must not move the divider")
}

// TestDividerDragAbandonedWhenStateChanges: if an overlay takes the screen while a
// drag is in flight, subsequent motion must not keep resizing the panes behind it.
func TestDividerDragAbandonedWhenStateChanges(t *testing.T) {
	h := newWheelHome(t)
	seam := layoutListWidth(h)

	_, _ = h.Update(pressAt(seam, 5))
	require.True(t, h.draggingDivider, "press on the seam begins a drag")

	before := h.listRatio
	h.state = statePrompt // an overlay opened mid-drag
	_, _ = h.Update(motionAt(90, 5))
	require.False(t, h.draggingDivider, "a state change must abandon the drag")
	require.InDelta(t, before, h.listRatio, 1e-9,
		"motion behind an overlay must not move the divider")
}
