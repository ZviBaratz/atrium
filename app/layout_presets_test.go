package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// escKey builds the Esc key event (runeKey only covers printable runes).
func escKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

// cycleLayoutKey is the single key that steps the layout presets. Kept next to
// the tests so a rebind of KeyLayoutPreset fails here loudly rather than
// silently exercising nothing.
const cycleLayoutKey = "\\"

// newPresetHome builds a sized, populated home seated on the default preset —
// what a fresh install lands on. newWheelHome is a bare struct literal (layout
// fields zero, i.e. index 0 = monitor), so seat it deliberately, mirroring how
// assembleHome seeds a real launch.
func newPresetHome(t *testing.T) *home {
	t.Helper()
	h := newWheelHome(t) // 2 instances, 120x30
	h.layoutIndex = defaultPresetIndex
	h.layoutPrev = defaultPresetIndex
	h.layoutCustom = false
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	return h
}

// cycleTo presses the layout key until the named preset is active (bounded by
// the ring length so a typo can't loop forever).
func cycleTo(t *testing.T, h *home, name string) {
	t.Helper()
	for i := 0; i < len(layoutPresets) && h.currentPreset().name != name; i++ {
		h.handleKeyPress(runeKey(cycleLayoutKey))
	}
	require.Equal(t, name, h.currentPreset().name, "failed to cycle to %q", name)
}

// TestLayoutCycleOrder: one key steps the full ring in order and wraps. Starting
// from default the visited sequence covers monitor / review / focus (acceptance 1).
func TestLayoutCycleOrder(t *testing.T) {
	h := newPresetHome(t)
	require.Equal(t, "default", h.currentPreset().name)

	want := []string{"review", "focus", "monitor", "default"}
	for _, name := range want {
		h.handleKeyPress(runeKey(cycleLayoutKey))
		require.Equal(t, name, h.currentPreset().name)
	}
}

// TestLayoutPresetAppliesRatioLive: cycling to a preset immediately re-proportions
// the split to that preset's ratio (not only after a relaunch) — the core switch.
func TestLayoutPresetAppliesRatioLive(t *testing.T) {
	h := newPresetHome(t)
	cycleTo(t, h, "monitor")
	require.InDelta(t, config.MaxListRatio, h.listRatio, 1e-9, "monitor must widen the list live")
	require.Equal(t, int(float32(h.windowWidth)*float32(config.MaxListRatio)), layoutListWidth(h))

	cycleTo(t, h, "review")
	require.InDelta(t, config.MinListRatio, h.listRatio, 1e-9, "review must thin the list live")
}

// TestLayoutFocusHidesListAndFillsWidth: focus hides the list and hands its whole
// column to the tabbed window (acceptance 1).
func TestLayoutFocusHidesListAndFillsWidth(t *testing.T) {
	h := newPresetHome(t)
	require.False(t, h.listHidden())
	listCol := int(float32(h.windowWidth) * float32(h.listRatio)) // 120*0.30 = 36
	require.Greater(t, listCol, 0)
	defW, _ := h.tabbedWindow.GetPreviewSize()
	require.Contains(t, xansi.Strip(h.View()), "Sessions", "the list panel renders outside focus")

	cycleTo(t, h, "focus")
	require.True(t, h.listHidden(), "focus must hide the list")

	focusW, _ := h.tabbedWindow.GetPreviewSize()
	require.Equal(t, defW+listCol, focusW, "the tabbed window must reclaim the full list column in focus")
	require.NotContains(t, xansi.Strip(h.View()), "Sessions", "the list panel must not render in focus")
}

// TestLayoutReviewFocusesDiffTab: the review preset jumps to the Diff tab
// (per the issue's "Diff tab focused").
func TestLayoutReviewFocusesDiffTab(t *testing.T) {
	h := newPresetHome(t)
	require.NotEqual(t, ui.DiffTab, h.tabbedWindow.GetActiveTab())
	cycleTo(t, h, "review")
	require.Equal(t, ui.DiffTab, h.tabbedWindow.GetActiveTab(), "review must focus the Diff tab")
}

// TestLayoutCycleFlashesPresetName: switching presets flashes the preset name
// through the notice ladder so the switch is legible (hint bar on by default, so
// the toast rides the menu row).
func TestLayoutCycleFlashesPresetName(t *testing.T) {
	h := newPresetHome(t)
	h.handleKeyPress(runeKey(cycleLayoutKey)) // → review
	require.Equal(t, "review", h.currentPreset().name)
	require.Contains(t, h.menu.NoticeText(), "review", "the active preset name must flash on switch")
}

// TestLayoutCustomRatioDoesNotBreakCycle: < / > drop into a custom override of
// the base preset (still adjusting the split by one column), and the cycle key
// keeps advancing from that base — the two never fight (acceptance 2).
func TestLayoutCustomRatioDoesNotBreakCycle(t *testing.T) {
	h := newPresetHome(t)
	require.Equal(t, "default", h.currentPreset().name)
	require.False(t, h.layoutCustom)

	before := layoutListWidth(h)
	h.handleKeyPress(runeKey(">"))
	require.True(t, h.layoutCustom, "> must set a custom override")
	require.Equal(t, "default", h.currentPreset().name, "> must not change the base preset")
	require.Equal(t, before+1, layoutListWidth(h), "> must still grow the list by exactly one column")

	h.handleKeyPress(runeKey(cycleLayoutKey))
	require.False(t, h.layoutCustom, "the cycle key clears the custom override")
	require.Equal(t, "review", h.currentPreset().name, "the cycle advances to the preset after the base")
}

// TestLayoutCustomRatioUnhidesFocus: adjusting the split in focus is an explicit
// request for a list, so the custom override un-hides it — < / > is never a dead
// key in focus.
func TestLayoutCustomRatioUnhidesFocus(t *testing.T) {
	h := newPresetHome(t)
	cycleTo(t, h, "focus")
	require.True(t, h.listHidden())

	h.handleKeyPress(runeKey(">"))
	require.True(t, h.layoutCustom)
	require.False(t, h.listHidden(), "a custom split must show the list even on the focus base")
	require.Contains(t, xansi.Strip(h.View()), "Sessions", "the list must render once dragged back out of focus")
}

// TestEscLeavesFocusToPriorPreset: Esc backs focus out to the preset that
// preceded it rather than dead-ending; the cycle key instead continues the ring.
// Both leave focus (acceptance 3).
func TestEscLeavesFocusToPriorPreset(t *testing.T) {
	h := newPresetHome(t)
	h.handleKeyPress(runeKey(cycleLayoutKey)) // → review
	h.handleKeyPress(runeKey(cycleLayoutKey)) // → focus
	require.Equal(t, "focus", h.currentPreset().name)
	require.True(t, h.listHidden())

	h.handleKeyPress(escKey())
	require.Equal(t, "review", h.currentPreset().name, "esc must return to the preset before focus")
	require.False(t, h.listHidden(), "esc must leave focus mode")
}

// TestCycleKeyLeavesFocus: the cycle key from focus continues the ring (to
// monitor) rather than dead-ending — the other half of acceptance 3.
func TestCycleKeyLeavesFocus(t *testing.T) {
	h := newPresetHome(t)
	cycleTo(t, h, "focus")
	h.handleKeyPress(runeKey(cycleLayoutKey))
	require.Equal(t, "monitor", h.currentPreset().name)
	require.False(t, h.listHidden(), "cycling onward from focus must leave focus mode")
}

// TestExitFocusLayoutDirect pins the state contract of exitFocusLayout one layer
// below TestEscLeavesFocusToPriorPreset (which goes through key dispatch): the
// function itself must restore layoutIndex to layoutPrev, leave focus (listHidden
// false), and land on the preset that preceded focus. The override-clearing half
// of the contract needs a custom override to actually clear, which un-hides the
// list and so cannot coexist with the listHidden assertion here — it lives in
// TestExitFocusLayoutClearsCustomOverride.
func TestExitFocusLayoutDirect(t *testing.T) {
	h := newPresetHome(t)

	cycleTo(t, h, "review") // layoutPrev = default index, layoutIndex = review index
	cycleTo(t, h, "focus")  // layoutPrev = review index, layoutIndex = focus index
	prevIdx := h.layoutPrev // capture review index before the call
	require.True(t, h.listHidden(), "focus must hide the list going in, so the assertion below can catch the leave")

	_ = h.exitFocusLayout()

	require.Equal(t, prevIdx, h.layoutIndex, "exitFocusLayout must restore layoutIndex to layoutPrev")
	require.False(t, h.listHidden(), "exitFocusLayout must leave focus mode")
	require.Equal(t, "review", h.currentPreset().name, "the active preset must be the one that preceded focus")
}

// TestExitFocusLayoutClearsCustomOverride pins the one leg of exitFocusLayout's
// contract that TestExitFocusLayoutDirect structurally cannot: clearing
// layoutCustom. It needs the flag actually set first — cycleLayoutPreset clears it
// on every step, so cycling into focus alone leaves nothing to clear and the
// assertion would hold even with the clear deleted.
func TestExitFocusLayoutClearsCustomOverride(t *testing.T) {
	h := newPresetHome(t)

	cycleTo(t, h, "review")
	cycleTo(t, h, "focus")
	h.handleKeyPress(runeKey(">")) // an explicit split adjustment sets the override
	require.True(t, h.layoutCustom, "precondition: > must set the custom override")

	_ = h.exitFocusLayout()

	require.False(t, h.layoutCustom, "exitFocusLayout must clear the custom override")
	require.Equal(t, "review", h.currentPreset().name, "clearing the override must not disturb the restored preset")
}

// TestFocusModeSeamIsInert: in focus the list is hidden, so there is no visible
// seam to grab — a press at the column the seam would occupy must not start a
// divider drag (the !listHidden guard on the grab).
func TestFocusModeSeamIsInert(t *testing.T) {
	h := newPresetHome(t)
	cycleTo(t, h, "focus")
	require.True(t, h.listHidden())
	// The grab is computed from the live listRatio, not the hidden-adjusted 0.
	seam := int(float32(h.windowWidth) * float32(h.listRatio))
	_, _ = h.Update(pressAt(seam, 5))
	require.False(t, h.draggingDivider, "focus mode must not grab an invisible seam")
}

// TestLayoutPresetPersistsAcrossRelaunch: the active preset (and its ratio)
// survive a save→load→reassemble round-trip through state.json (acceptance 2).
func TestLayoutPresetPersistsAcrossRelaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate state.json from the shared sandbox
	cfg := config.DefaultConfig()
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	h := assembleHome(context.Background(), "echo", false, "v", "atr", cfg, st, storage, nil)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.Equal(t, "default", h.currentPreset().name)

	cycleTo(t, h, "review") // persists via SetLayout

	reloaded := config.LoadState()
	require.Equal(t, "review", reloaded.GetLayoutPreset(), "the preset name must be written to disk")

	storage2, err := session.NewStorage(reloaded)
	require.NoError(t, err)
	h2 := assembleHome(context.Background(), "echo", false, "v", "atr", cfg, reloaded, storage2, nil)
	require.Equal(t, "review", h2.currentPreset().name, "the active preset must survive relaunch")
	require.InDelta(t, config.MinListRatio, h2.listRatio, 1e-9, "review's ratio must be restored")
}

// TestLayoutFocusPersistsAcrossRelaunch: focus (the list-hidden preset) restores
// as hidden after a round-trip (acceptance 2 + 1).
func TestLayoutFocusPersistsAcrossRelaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	h := assembleHome(context.Background(), "echo", false, "v", "atr", cfg, st, storage, nil)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	cycleTo(t, h, "focus")

	reloaded := config.LoadState()
	storage2, err := session.NewStorage(reloaded)
	require.NoError(t, err)
	h2 := assembleHome(context.Background(), "echo", false, "v", "atr", cfg, reloaded, storage2, nil)
	require.Equal(t, "focus", h2.currentPreset().name)
	require.True(t, h2.listHidden(), "focus must restore as list-hidden")
}

// TestLayoutPresetsDegradeAt80x24: every preset renders a frame that fits an
// 80x24 terminal exactly — no line wider than the terminal, no more rows than it
// has (acceptance 4). Mirrors TestViewFitsTerminalBounds' invariants, applied to
// each preset in turn (focus included, where the list is hidden).
func TestLayoutPresetsDegradeAt80x24(t *testing.T) {
	const w, ht = 80, 24
	h := newPresetHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: ht})

	for i := 0; i < len(layoutPresets); i++ {
		name := h.currentPreset().name
		lines := strings.Split(h.View(), "\n")
		require.LessOrEqualf(t, len(lines), ht, "preset %q: %d rows exceeds height %d", name, len(lines), ht)
		for j, l := range lines {
			require.Equalf(t, w, ansi.PrintableRuneWidth(l), "preset %q: line %d width", name, j)
		}
		h.handleKeyPress(runeKey(cycleLayoutKey))
	}
}
