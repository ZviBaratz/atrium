package ui

import (
	"context"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/session"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/stretchr/testify/require"
)

// clickAt builds a left-button press at the given absolute frame coordinates.
func clickAt(x, y int) tea.MouseMsg {
	return testutil.MouseClick(x, y, tea.MouseLeft)
}

// clickZone re-scans the rendered frame, resolves the given zone, and runs act
// against its bounds — all inside one retry loop.
//
// Waiting only for a *non-zero* zone is not enough (issue #434). Scan() hands
// marks to a background goroutine, and the zone manager is shared across the
// whole package, so before the worker drains the scan above, Get can still be
// serving bounds another test's differently-sized frame registered. Those bounds
// are non-zero but wrong, and acting on them misses. Folding the action in means
// a miss re-renders and retries instead of failing the assertion outright.
//
// act must therefore be inert when it misses — every caller here only reads a
// zone lookup — and reports whether it hit, so a stale-bounds miss is a retry.
// In the live TUI none of this bites: Get happens a full event-loop tick after
// the View() that scanned.
func clickZone(t *testing.T, render func() string, id string, act func(*zone.ZoneInfo) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		zone.Scan(render())
		z := zone.Get(id)
		return !z.IsZero() && act(z)
	}, time.Second, 5*time.Millisecond, "a click on zone %q never resolved to its own target", id)
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
		// The click must resolve to this row and no other — a stale-bounds hit on
		// the neighbouring row reads as a miss and retries.
		clickZone(t, l.String, listRowZoneID(inst), func(z *zone.ZoneInfo) bool {
			return l.InstanceAtZone(clickAt(z.StartX, z.StartY)) == inst
		})
	}

	// A click far outside the panel hits no row.
	require.Nil(t, l.InstanceAtZone(clickAt(9999, 9999)))
}

// Two sessions may share a title across repo groups; their click zones must not
// share an id, or a click on one row selects whichever registered first.
func TestListInstanceAtZone_SameTitleAcrossGroups(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	mk := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "same", Path: t.TempDir(), Program: "echo", Direct: true,
		})
		require.NoError(t, err)
		inst.SetStatus(session.Ready)
		return inst
	}
	a, b := mk(), mk()
	l.AddInstance(a)()
	l.AddInstance(b)()
	l.SetSize(40, 14)

	require.NotEqual(t, listRowZoneID(a), listRowZoneID(b),
		"same-titled rows in different groups need distinct zone ids")
	for _, inst := range []*session.Instance{a, b} {
		// Same-titled rows sit adjacent, so this is also the assertion that the
		// two ids address distinct regions rather than collapsing onto one.
		clickZone(t, l.String, listRowZoneID(inst), func(z *zone.ZoneInfo) bool {
			return l.InstanceAtZone(clickAt(z.StartX, z.StartY)) == inst
		})
	}
}

// TestTabAtZone verifies tab click regions resolve to the right tab index.
func TestTabAtZone(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(60, 20)

	for i := range []int{PreviewTab, DiffTab, TerminalTab} {
		// Landing on a *different* tab is a miss, not a pass: the tabs abut, so
		// stale bounds resolve to a neighbour rather than to nothing.
		clickZone(t, w.String, tabZoneID(i), func(z *zone.ZoneInfo) bool {
			got, ok := w.TabAtZone(clickAt(z.StartX, z.StartY))
			return ok && got == i
		})
	}
}

// TestHeaderAtZone_ClickTogglesFold verifies that a click landing on a repo-group
// header resolves to that group's key, and that ClickHeader folds/unfolds the
// group like the ←/→ keyboard fold, snapping the selection to the group anchor.
func TestHeaderAtZone_ClickTogglesFold(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	l.SetSelectedInstance(2) // selection elsewhere, so the snap is observable

	var key string
	clickZone(t, l.String, listHeaderZoneID("repoA"), func(z *zone.ZoneInfo) bool {
		k, ok := l.HeaderAtZone(clickAt(z.StartX, z.StartY))
		key = k
		return ok && k == "repoA"
	})

	// Folding runs off the resolved key, not off zone bounds, so it stays outside
	// the retry loop — it must happen exactly once per click.
	// First click folds the group and moves the selection to its anchor.
	require.True(t, l.ClickHeader(key))
	require.True(t, l.collapsed["repoA"], "first click collapses the group")
	require.Same(t, l.items[0], l.GetSelectedInstance(), "selection snaps to the group anchor")

	// Second click unfolds it again.
	require.True(t, l.ClickHeader(key))
	require.False(t, l.collapsed["repoA"], "second click expands the group")

	// A click outside every header hits nothing.
	_, ok := l.HeaderAtZone(clickAt(9999, 9999))
	require.False(t, ok)
}

// ClickHeader is inert when only one repo is present (headers don't render there)
// and for keys that aren't in the list.
func TestClickHeader_SingleRepoAndUnknownKeyAreInert(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA")
	require.False(t, l.ClickHeader("repoA"), "folding is meaningless with one repo")

	multi := newGroupList(t, "/x/repoA", "/x/repoB")
	require.False(t, multi.ClickHeader("nope"), "unknown keys change nothing")
}
