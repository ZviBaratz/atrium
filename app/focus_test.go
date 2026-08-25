package app

// The esc-ladder order tests and the focus-router tests (#803). Every keypress
// here — the fixture-arming ones included — drives home.Update, not a handler
// directly, so a rung or router that ships wired but unreachable fails (the
// routing-coverage discipline #856 records). Each pairwise test arms TWO rungs
// and asserts which one a single esc fires — swapping those rungs in escLadder
// turns exactly that test red, which is the mutation the ladder's order
// comment promises to catch.

import (
	"fmt"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// scrollHome is a preset home with the preview pane frozen in scroll mode on
// tall content, the way SetPreviewScrollContent simulates a scrolled session
// without tmux. The window is already sized (newPresetHome), which the
// viewport needs to render non-empty content.
func scrollHome(t *testing.T) *home {
	t.Helper()
	h := newPresetHome(t)
	// Production runs instanceChanged on every poll tick and from most key
	// handlers (app_poll.go), which is what puts the menu in its default
	// (session-selected) state; run it once so the bar renders the way it
	// does live.
	h.instanceChanged()
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	h.tabbedWindow.SetPreviewScrollContent(h.list.GetSelectedInstance(), b.String())
	require.True(t, h.tabbedWindow.IsPreviewInScrollMode(), "fixture must start in scroll mode")
	return h
}

// Rung order: scroll exit fires before the committed-filter clear. One esc
// resumes the live view and leaves the filter narrowing the list; the next
// clears it.
func TestEsc_ScrollExitsBeforeFilterClears(t *testing.T) {
	h := scrollHome(t)
	h.list.SetFilter("alpha")

	h.Update(keyMsg("esc"))
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode(), "first esc must exit scroll mode")
	require.Equal(t, "alpha", h.list.FilterQuery(), "the filter must survive the scroll-exit esc")

	h.Update(keyMsg("esc"))
	require.Empty(t, h.list.FilterQuery(), "second esc must clear the committed filter")
}

// Rung order: scroll exit fires before the explicit-focus pop. One esc resumes
// the live view with focus still explicitly on the tabs; the next pops it.
func TestEsc_ScrollExitsBeforeFocusPop(t *testing.T) {
	h := scrollHome(t)
	h.focus = focusTabs

	h.Update(keyMsg("esc"))
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode(), "first esc must exit scroll mode")
	require.Equal(t, focusTabs, h.focus, "explicit focus must survive the scroll-exit esc")

	h.Update(keyMsg("esc"))
	require.Equal(t, focusList, h.focus, "second esc must pop explicit focus")
}

// Rung order: the explicit-focus pop fires before the committed-filter clear.
func TestEsc_FocusPopsBeforeFilterClears(t *testing.T) {
	h := newPresetHome(t)
	h.focus = focusTabs
	h.list.SetFilter("alpha")

	h.Update(keyMsg("esc"))
	require.Equal(t, focusList, h.focus, "first esc must pop explicit focus")
	require.Equal(t, "alpha", h.list.FilterQuery(), "the filter must survive the focus-pop esc")

	h.Update(keyMsg("esc"))
	require.Empty(t, h.list.FilterQuery(), "second esc must clear the committed filter")
}

// Rung order: the committed-filter clear fires before the focus-layout exit.
// One esc un-narrows the list while the layout keeps it hidden; the next backs
// out of the layout.
func TestEsc_FilterClearsBeforeLayoutExit(t *testing.T) {
	h := newPresetHome(t)
	// Armed through Update like every press in this file (cycleTo is
	// handler-direct, so it would falsify the header's coverage claim).
	for i := 0; i < len(layoutPresets) && h.currentPreset().name != "focus"; i++ {
		h.Update(runeKey(cycleLayoutKey))
	}
	require.True(t, h.listHidden())
	h.list.SetFilter("alpha")

	h.Update(keyMsg("esc"))
	require.Empty(t, h.list.FilterQuery(), "first esc must clear the committed filter")
	require.True(t, h.listHidden(), "the focus layout must survive the filter-clear esc")

	h.Update(keyMsg("esc"))
	require.False(t, h.listHidden(), "second esc must leave the focus layout")
}

// The focus-pop rung alone: esc hands explicit focus back to the list, and a
// further esc with nothing left to unwind is inert (KeyEscape is DocOnly, so
// it dies at the dispatch lookup rather than reaching an action).
func TestEsc_PopsExplicitFocusToList(t *testing.T) {
	h := newPresetHome(t)
	h.focus = focusTabs

	h.Update(keyMsg("esc"))
	require.Equal(t, focusList, h.focus, "esc must pop explicit focus")

	h.Update(keyMsg("esc"))
	require.Equal(t, stateDefault, h.state, "an esc with nothing to unwind must be inert")
	require.Equal(t, focusList, h.focus)
}

// While the pane holds focus (scroll mode), up/down scroll the snapshot
// instead of moving the list selection — the pane-local nav keys the focus
// model exists to route. The down press is the discriminating one: routed to
// the list it would move the selection off the scrolled instance.
func TestFocusTabs_UpDownScrollPaneNotList(t *testing.T) {
	h := scrollHome(t)
	selected := h.list.GetSelectedInstance()
	bottom, ok := h.tabbedWindow.PreviewScrollContent()
	require.True(t, ok)

	h.Update(keyMsg("up"))
	moved, ok := h.tabbedWindow.PreviewScrollContent()
	require.True(t, ok, "up must keep the pane in scroll mode")
	require.NotEqual(t, bottom, moved, "up must move the snapshot viewport")
	require.Same(t, selected, h.list.GetSelectedInstance(), "up must not move the list selection")

	h.Update(keyMsg("down"))
	back, ok := h.tabbedWindow.PreviewScrollContent()
	require.True(t, ok, "down above the bottom must scroll, not exit")
	require.Equal(t, bottom, back, "down must move the viewport back to the bottom")
	require.Same(t, selected, h.list.GetSelectedInstance(), "down must not move the list selection")
}

// Only the nav keys are consumed under pane focus: everything else falls
// through to global dispatch, so n still opens the create form mid-scroll
// (and by the same route q would quit, the palette opens, ...).
func TestFocusTabs_OtherKeysFallThroughToDispatch(t *testing.T) {
	h := scrollHome(t)

	h.Update(keyMsg("n"))
	require.Equal(t, statePrompt, h.state, "n must fall through the focus router to global dispatch")
}

// The hint bar teaches the pane vocabulary while the pane holds focus and
// restores the context hints when esc hands focus back — the render-time
// SetPaneFocus push in viewContent, driven end to end.
func TestFocusTabs_BarSwapsWithScroll(t *testing.T) {
	h := scrollHome(t)

	frame := xansi.Strip(h.View().Content)
	require.Contains(t, frame, "exit scroll", "the bar must teach the pane vocabulary in scroll mode")

	h.Update(keyMsg("esc"))
	frame = xansi.Strip(h.View().Content)
	require.NotContains(t, frame, "exit scroll", "the bar must revert when focus returns to the list")
}

// Down at the snapshot's bottom is consumed and held, not passed to the pane —
// scroll-mode entry lands at the bottom, and the pane's own bottom exit plus a
// fall-through to the list would turn a held j into a selection switch on the
// press after the silent exit. Three presses stand in for autorepeat; esc must
// then still be the working exit the bar advertises.
func TestFocusTabs_DownAtBottomHoldsScroll(t *testing.T) {
	h := scrollHome(t)
	selected := h.list.GetSelectedInstance()

	for i := 0; i < 3; i++ {
		h.Update(keyMsg("down"))
		require.True(t, h.tabbedWindow.IsPreviewInScrollMode(),
			"down at the bottom must hold scroll mode, not exit it (press %d)", i+1)
		require.Same(t, selected, h.list.GetSelectedInstance(),
			"down at the bottom must never reach the list (press %d)", i+1)
	}

	h.Update(keyMsg("esc"))
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode(), "esc must still exit scroll mode")
}

// Explicit focusTabs on a live pane: down has no snapshot to move but is still
// consumed — the pane owns the nav keys, so the list selection must not move.
func TestFocusTabs_ExplicitFocusDownOnLivePaneIsInert(t *testing.T) {
	h := newPresetHome(t)
	h.focus = focusTabs
	selected := h.list.GetSelectedInstance()

	h.Update(keyMsg("down"))
	require.False(t, h.tabbedWindow.IsPreviewInScrollMode())
	require.Same(t, selected, h.list.GetSelectedInstance(),
		"down under explicit pane focus must not move the list selection")
}

// focusInspector routes nothing until an inspector pane exists: nav keys fall
// through to global dispatch (the list moves), and esc pops the focus like any
// explicit target.
func TestFocusInspector_NavFallsThroughAndEscPops(t *testing.T) {
	h := newPresetHome(t)
	h.focus = focusInspector
	selected := h.list.GetSelectedInstance()

	h.Update(keyMsg("down"))
	require.NotSame(t, selected, h.list.GetSelectedInstance(),
		"inspector focus must not consume nav keys while no inspector exists")

	h.Update(keyMsg("esc"))
	require.Equal(t, focusList, h.focus, "esc must pop inspector focus")
}

// A minimal stateDefault home with no tabbed window — the settings/account
// fixtures' shape — must absorb esc the way it absorbs every other key: each
// ladder rung guards its own derefs the way currentFocus guards its own.
func TestEsc_MinimalHomeIsInert(t *testing.T) {
	h := newSettingsTestHome()
	h.Update(keyMsg("esc"))
	require.Equal(t, stateDefault, h.state)
}
