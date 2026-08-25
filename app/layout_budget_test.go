package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui"
)

// TestComputeBudget drives every row of the partition on and off and checks
// the body is what remains, floored at one. Mutations this kills: dropping the
// banner, menu or err term (the armed rows), and dropping the max(1, ·) floor
// (the two-row terminal).
func TestComputeBudget(t *testing.T) {
	h := newCreateFormHome(t)

	cases := []struct {
		name          string
		state         state
		autoYes       bool
		notice        bool
		height        int
		banner, body  int
		menu, errRows int
	}{
		{name: "plain navigation charges the menu row", state: stateDefault,
			height: 24, banner: 0, menu: 1, errRows: 0, body: 23},
		{name: "an overlay reclaims the menu row", state: stateHelp,
			height: 24, banner: 0, menu: 0, errRows: 0, body: 24},
		{name: "a notice charges the err row", state: stateHelp, notice: true,
			height: 24, banner: 0, menu: 0, errRows: 1, body: 23},
		{name: "armed auto-accept charges the banner row", state: stateDefault, autoYes: true,
			height: 24, banner: 1, menu: 1, errRows: 0, body: 22},
		{name: "all rows at once", state: stateDefault, autoYes: true, notice: true,
			height: 24, banner: 1, menu: 1, errRows: 1, body: 21},
		{name: "the body never drops below one row", state: stateDefault, autoYes: true, notice: true,
			height: 2, banner: 1, menu: 1, errRows: 1, body: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.state = tc.state
			h.autoYes = tc.autoYes
			h.errBox.Clear()
			if tc.notice {
				h.errBox.SetNotice("saved", ui.NoticeInfo)
			}
			b := h.computeBudget(tc.height)
			require.Equal(t, frameBudget{banner: tc.banner, body: tc.body, menu: tc.menu, err: tc.errRows}, b)
		})
	}
}

// TestComputeRegions pins the split invariants: the regions always sum to the
// width handed in, the list column is the ratio truncation the goldens encode,
// and the focus preset zeroes the list so the tabbed window takes the whole
// width. Mutations this kills: dropping the hidden-zeroing, and tabs computed
// from anything but the remainder.
func TestComputeRegions(t *testing.T) {
	h := newPresetHome(t) // 120 wide, default ratio 0.30

	r := h.computeRegions(120)
	require.Equal(t, 36, r.list, "the list column is int(float32(width) * float32(ratio))")
	require.Equal(t, 120, r.list+r.tabs+r.inspector, "the regions partition the width exactly")

	cycleTo(t, h, "focus")
	r = h.computeRegions(120)
	require.Equal(t, bodyRegions{list: 0, tabs: 120}, r,
		"focus hides the list, so its whole column belongs to the tabbed window")
}

// TestAdjustListColsEscapesFocusFromRememberedSplit: < / > step from the
// remembered ratio, not from the focus preset's hidden (zero-width) list —
// adjusting the divider is an explicit request for a list. This is why
// adjustListCols reads listCols rather than computeRegions: a version that
// stepped from the hidden-zeroed region would resurrect the list at about one
// column instead of the remembered split, which is the mutation this kills.
func TestAdjustListColsEscapesFocusFromRememberedSplit(t *testing.T) {
	h := newPresetHome(t)
	cycleTo(t, h, "focus")
	require.True(t, h.listHidden(), "focus hides the list")
	require.Equal(t, 0, h.computeRegions(h.windowWidth).list)

	// The remembered split is whatever ratio survived into focus (the cycle
	// passes through review on the way, so it is review's, not default's) —
	// listCols reads it raw, past the hidden-zeroing.
	remembered := h.listCols(h.windowWidth)
	require.Greater(t, remembered, 1)

	h.handleKeyPress(runeKey(">"))
	require.False(t, h.listHidden(), "a divider nudge escapes focus")
	require.Equal(t, remembered+1, layoutListWidth(h),
		"the escaped list must step from the remembered split, not from zero")
}

// The struct-literal fallback: a home built without a size event still lands on
// the persisted ratio rather than a collapsed list (the guard at the top of
// updateHandleWindowSizeEvent).
func TestUpdateWindowSizeSeedsZeroRatio(t *testing.T) {
	h := newCreateFormHome(t)
	h.listRatio = 0
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 100, Height: 24})
	require.Greater(t, h.computeRegions(100).list, 0,
		"a zero ratio must be reseeded from appState before the split is taken")
}
