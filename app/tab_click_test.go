package app

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/ui"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/stretchr/testify/require"
)

// tabZoneID mirrors the unexported constant in the ui package, like the panel
// IDs in wheel_test.go: a renamed ID fails loudly here (the zone never
// registers) rather than silently un-routing the click.
func tabZoneID(i int) string { return fmt.Sprintf("tab-%d", i) }

// waitTabZone renders until the given tab's zone is registered and consistent
// with the current frame. waitAppZone's panel checks are not enough on their
// own: a tab zone recorded from an earlier test's differently-sized frame is
// non-zero too, so the zone must also sit inside the current tabbed panel, on
// the strip's rows.
func waitTabZone(t *testing.T, h *home, tab int) *zone.ZoneInfo {
	t.Helper()
	id := tabZoneID(tab)
	var z *zone.ZoneInfo
	require.Eventually(t, func() bool {
		_ = h.View().Content
		tabbed := zone.Get(tabbedWindowZoneID)
		if tabbed.IsZero() || tabbed.EndX != h.windowWidth-1 {
			return false
		}
		z = zone.Get(id)
		return !z.IsZero() &&
			z.StartX >= tabbed.StartX && z.EndX <= tabbed.EndX &&
			z.StartY == tabbed.StartY && z.EndY <= tabbed.StartY+2
	}, testutil.ZoneClickTimeout, testutil.ZoneClickPoll, "tab zone %s never consistently registered", id)
	return z
}

// A tab click runs tabChanged, the shared tail of every tab switch, exactly as
// the jump keys do. The click arm used to call instanceChanged directly (#862),
// which skipped the hint bar's tab sync — so the bar kept the previous tab's
// hints (the diff tab's scroll hint gates on the menu's tab copy) until the
// next keyboard switch resynced it. The keyboard path is the reference: the
// hint bar after a click must be byte-identical to the bar after the key.
func TestTabClickRunsTheSharedTabSwitchTail(t *testing.T) {
	h := newWheelHome(t)

	press(t, h, runeKey("2"))
	require.Equal(t, ui.DiffTab, h.tabbedWindow.GetActiveTab())
	wantMenu := h.menu.String()
	press(t, h, runeKey("1"))
	require.Equal(t, ui.PreviewTab, h.tabbedWindow.GetActiveTab())
	require.NotEqual(t, wantMenu, h.menu.String(),
		"the hint bar must differ between the two tabs, or the equality below proves nothing")

	z := waitTabZone(t, h, ui.DiffTab)
	_, _ = h.Update(testutil.MouseClick((z.StartX+z.EndX)/2, (z.StartY+z.EndY)/2, tea.MouseLeft))

	require.Equal(t, ui.DiffTab, h.tabbedWindow.GetActiveTab(), "the click must land on its own tab")
	require.Equal(t, wantMenu, h.menu.String(),
		"a tab click must leave the hint bar exactly as the tab key does")
}
