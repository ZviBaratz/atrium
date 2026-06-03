package ui

import (
	"context"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
	"github.com/stretchr/testify/require"
)

// clickAt builds a left-button press at the given absolute frame coordinates.
func clickAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

// waitZone scans the rendered frame and waits for bubblezone's async worker to
// register the given zone, returning it. Scan() hands marks to a background
// goroutine, so a single Scan+Get can race it; re-scanning each tick is
// idempotent and converges quickly. In the live TUI this race never bites
// because Get happens a full event-loop tick after the View() that scanned.
func waitZone(t *testing.T, render func() string, id string) *zone.ZoneInfo {
	t.Helper()
	var z *zone.ZoneInfo
	require.Eventually(t, func() bool {
		zone.Scan(render())
		z = zone.Get(id)
		return !z.IsZero()
	}, time.Second, 5*time.Millisecond, "zone %q never registered", id)
	return z
}

// TestListInstanceAtZone verifies that a click landing inside a row's registered
// click region resolves to that row's instance, and a click outside every row
// resolves to nil. Coordinates come from each zone's own reported bounds so the
// test does not hard-code the panel layout.
func TestListInstanceAtZone(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	a := instWithStatus(t, "alpha", session.Ready)
	b := instWithStatus(t, "bravo", session.Ready)
	l.AddInstance(a)()
	l.AddInstance(b)()
	l.SetSize(40, 14)

	for _, inst := range []*session.Instance{a, b} {
		z := waitZone(t, l.String, listRowZoneID(inst.Title))
		got := l.InstanceAtZone(clickAt(z.StartX, z.StartY))
		require.Same(t, inst, got, "click inside %q's zone should resolve to it", inst.Title)
	}

	// A click far outside the panel hits no row.
	require.Nil(t, l.InstanceAtZone(clickAt(9999, 9999)))
}

// TestTabAtZone verifies tab click regions resolve to the right tab index.
func TestTabAtZone(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(60, 20)

	for i := range []int{PreviewTab, DiffTab, TerminalTab} {
		z := waitZone(t, w.String, tabZoneID(i))
		got, ok := w.TabAtZone(clickAt(z.StartX, z.StartY))
		require.True(t, ok, "click inside tab %d should hit a tab", i)
		require.Equal(t, i, got)
	}
}
