package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
	"github.com/stretchr/testify/require"
)

// Panel zone IDs, mirroring the unexported constants in the ui package. The
// routing tests resolve wheel coordinates from these zones, so a renamed ID
// fails loudly here rather than silently un-routing the wheel.
const (
	listPanelZoneID    = "list-panel"
	tabbedWindowZoneID = "tabbed-window"
)

// wheelAt builds a wheel mouse event at (x, y).
func wheelAt(x, y int, btn tea.MouseButton) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: btn}
}

// newWheelHome builds a sized home with two selectable sessions, ready for
// zone-based wheel routing tests. The instances are never started, so no tmux
// or git is touched.
func newWheelHome(t *testing.T) *home {
	t.Helper()
	// All app tests share the package-global zone manager (zone_test.go's
	// init). Never close or swap it here: zone.Scan emits into the manager's
	// bounded channel, so a closed manager left behind as DefaultManager
	// silently buffers later tests' View() scans and then deadlocks the
	// package (see TestGlobalZoneManagerSurvivesWheelTests). Stale zones from
	// earlier tests' frames are handled by waitAppZone's consistency check
	// instead.
	h := newCreateFormHome(t)
	for _, title := range []string{"alpha", "bravo"} {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   title,
			Path:    t.TempDir(),
			Program: "echo",
		})
		require.NoError(t, err)
		h.list.AddInstance(inst)
	}
	h.list.SetSelectedInstance(0)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	return h
}

// waitAppZone renders the home view until the panel zones are registered and
// consistent with the current frame, then returns the requested zone's bounds.
// Scan hands zone info to an async worker, so a single Scan+Get can race; and
// because the zone manager is shared across the whole test package, Get can
// briefly return a zone recorded from a previous test's differently-sized
// frame. Accepting any non-zero zone here once routed wheel events into the
// wrong panel (~10% of -shuffle runs), so poll until the layout adds up.
func waitAppZone(t *testing.T, h *home, id string) *zone.ZoneInfo {
	t.Helper()
	var z *zone.ZoneInfo
	require.Eventually(t, func() bool {
		_ = h.View()
		list := zone.Get(listPanelZoneID)
		tabbed := zone.Get(tabbedWindowZoneID)
		// Both panels must come from the current frame: the tabbed window
		// reaches the right edge of this home's width, and the panels are
		// disjoint (a stale zone from another frame size fails one of these).
		if list.IsZero() || tabbed.IsZero() ||
			tabbed.EndX != h.windowWidth-1 || list.EndX >= tabbed.StartX {
			return false
		}
		z = zone.Get(id)
		return !z.IsZero()
	}, time.Second, 5*time.Millisecond, "zone %s never consistently registered", id)
	return z
}

// TestGlobalZoneManagerSurvivesWheelTests pins the zone manager lifecycle: no
// test may leave zone.DefaultManager closed. The manager's Scan feeds an async
// worker through a bounded channel, so a closed (reader-less) manager doesn't
// fail fast — it silently absorbs ~200 zone emissions from later tests'
// View() calls and then deadlocks whichever test sends next. This test runs a
// wheel home to completion (subtest scope fires its cleanups), then proves the
// shared manager still drains well past the channel capacity.
func TestGlobalZoneManagerSurvivesWheelTests(t *testing.T) {
	t.Run("wheel home lifecycle", func(t *testing.T) { _ = newWheelHome(t) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 250; i++ {
			_ = zone.Scan(zone.Mark("zone-lifecycle-probe", "x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("global zone manager worker is dead: zone.Scan blocked on a full channel")
	}
}

// TestWheelOverListMovesSelectionWithoutScrollMode is the core routing
// regression: a wheel event over the session list must move the selection like
// ↑/↓ — not scroll the right pane, and never enter snapshot scroll mode (the
// accidental-entry vector behind the stuck-preview bug).
func TestWheelOverListMovesSelectionWithoutScrollMode(t *testing.T) {
	h := newWheelHome(t)
	z := waitAppZone(t, h, listPanelZoneID)
	first := h.list.GetSelectedInstance()
	require.NotNil(t, first)

	// Inside the panel, clear of the border row/column.
	x, y := z.StartX+2, z.StartY+2

	_, _ = h.Update(wheelAt(x, y, tea.MouseButtonWheelDown))
	require.NotSame(t, first, h.list.GetSelectedInstance(),
		"wheel-down over the list must move the selection down")
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode(),
		"wheel over the list must not enter preview scroll mode")
	require.False(t, h.tabbedWindow.IsTerminalInScrollMode(),
		"wheel over the list must not enter terminal scroll mode")

	_, _ = h.Update(wheelAt(x, y, tea.MouseButtonWheelUp))
	require.Same(t, first, h.list.GetSelectedInstance(),
		"wheel-up over the list must move the selection back up")
}

// TestWheelOverTabbedWindowDoesNotMoveSelection pins the other side of the
// routing: a wheel event over the right pane scrolls that pane (or no-ops),
// never the list selection.
func TestWheelOverTabbedWindowDoesNotMoveSelection(t *testing.T) {
	h := newWheelHome(t)
	z := waitAppZone(t, h, tabbedWindowZoneID)
	before := h.list.GetSelectedInstance()

	_, _ = h.Update(wheelAt(z.StartX+2, z.StartY+2, tea.MouseButtonWheelDown))
	require.Same(t, before, h.list.GetSelectedInstance(),
		"wheel over the right pane must not move the list selection")
}

// TestWheelOutsideBothPanesDoesNothing: wheel events over neither panel (menu /
// hint bar rows, or out of frame) are ignored.
func TestWheelOutsideBothPanesDoesNothing(t *testing.T) {
	h := newWheelHome(t)
	waitAppZone(t, h, listPanelZoneID) // ensure the frame is scanned
	before := h.list.GetSelectedInstance()

	_, _ = h.Update(wheelAt(9999, 9999, tea.MouseButtonWheelDown))
	require.Same(t, before, h.list.GetSelectedInstance())
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode())
	require.False(t, h.tabbedWindow.IsTerminalInScrollMode())
}

// TestWheelInNonDefaultStateDoesNothing: with an overlay up, the wheel is dead —
// matching the existing left-click gating.
func TestWheelInNonDefaultStateDoesNothing(t *testing.T) {
	h := newWheelHome(t)
	z := waitAppZone(t, h, listPanelZoneID)
	before := h.list.GetSelectedInstance()

	h.state = statePrompt
	_, _ = h.Update(wheelAt(z.StartX+2, z.StartY+2, tea.MouseButtonWheelDown))
	require.Same(t, before, h.list.GetSelectedInstance(),
		"wheel must be ignored while an overlay owns the screen")
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode())
	require.False(t, h.tabbedWindow.IsTerminalInScrollMode())
}
