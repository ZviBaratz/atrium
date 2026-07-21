package app

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With the always-on hint bar enabled (the default), a transient error rides
// the bar's reserved row instead of claiming a new one — the frame height must
// not change when feedback appears.
func TestHandleError_MenuCarriesToastWithoutLayoutShift(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	before := lipgloss.Height(h.View())

	h.handleError(fmt.Errorf("session is paused"))

	assert.True(t, h.menu.HasNotice(), "the hint bar must carry the toast")
	assert.False(t, h.errBox.HasError(), "the error box must not claim a second row")
	assert.Equal(t, before, lipgloss.Height(h.View()), "feedback must never move the layout")
}

// With the hint bar off, a short error rides the always-reserved menu row in
// stateDefault (blank until the notice lands) rather than growing a separate
// errBox row — so feedback never reflows the panes (#438).
func TestHandleError_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	h.handleError(fmt.Errorf("boom"))

	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the error")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}

// Neutral acknowledgments ("branch copied") use the info level on the same row.
func TestHandleInfoNotice_MenuCarriesIt(t *testing.T) {
	h := newCreateFormHome(t)

	cmd := h.handleInfoNotice("branch 'zvi/foo' copied")

	require.NotNil(t, cmd, "an info notice schedules its own hide")
	assert.True(t, h.menu.HasNotice())
	assert.False(t, h.errBox.HasError(), "info must never look like an error")
}

// Info acknowledgments with the hint bar off ride the reserved menu row too —
// shown, not dropped (#287), and without a reflow (#438).
func TestHandleInfoNotice_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	cmd := h.handleInfoNotice("branch copied")

	require.NotNil(t, cmd, "a fallen-back info notice still schedules its own hide")
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the ack")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}

// pressKey drives a single rune keybinding through the default-state handler.
func pressKey(h *home, r rune) {
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// newPausedHome builds a stateDefault home whose selected session is paused.
func newPausedHome(t *testing.T) *home {
	t.Helper()
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "paused", "zvi/feat")
	inst.SetStatus(session.Paused)
	h.list.AddInstance(inst)
	require.NotNil(t, h.list.GetSelectedInstance())
	return h
}

// Pressing s (quick-send) on a paused session used to silently do nothing —
// indistinguishable from a frozen app. It must explain itself.
func TestQuickSend_PausedSessionExplains(t *testing.T) {
	h := newPausedHome(t)

	pressKey(h, 's')

	assert.Equal(t, stateDefault, h.state, "the compose box must not open for a paused session")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "paused")
}

// Pressing enter (attach) on a paused session likewise explains the guard and
// points at the resume key.
func TestEnter_PausedSessionExplains(t *testing.T) {
	h := newPausedHome(t)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "resume")
}

// A hide timer from an older toast must not clear a newer one: each notice
// bumps a generation, and only the matching hide message clears the row.
func TestHideNotice_StaleGenerationIgnored(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleError(fmt.Errorf("first"))
	firstGen := h.noticeGen
	h.handleError(fmt.Errorf("second"))

	h.Update(hideErrMsg{gen: firstGen})
	assert.True(t, h.menu.HasNotice(), "a stale hide must not clear the newer notice")

	h.Update(hideErrMsg{gen: h.noticeGen})
	assert.False(t, h.menu.HasNotice(), "the matching hide clears the notice")
}

// The missing-program warning with the hint bar off rides the reserved menu row
// (error-level) rather than a separate errBox row (#287/#438).
func TestWarnMissingProgram_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	cmd := h.warnMissingProgram("definitely-not-a-real-program")

	require.NotNil(t, cmd, "the warning schedules its own hide")
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the warning")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}

// flashNotice holds only one transient surface at a time. A notice raised while an
// overlay owns the screen (menuVisible false) falls back to the errBox row; when a
// later notice can ride the menu row it must clear that stale errBox row AND
// recompute, so the frame never renders a line short (#287).
func TestFlashNotice_MenuNoticeClearsStaleErrBox(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	// An overlay-state notice takes the errBox fallback (the menu row is hidden
	// behind the overlay there).
	h.state = stateHelp
	h.flashNotice("earlier notice", ui.NoticeInfo)
	require.True(t, h.errBox.HasContent(), "an overlay-state notice falls back to the errBox row")

	// Back in stateDefault the next notice rides the menu row; the stale errBox row
	// must be dropped and the panes must grow back so the frame stays 24 rows.
	h.state = stateDefault
	h.flashNotice("pushed changes", ui.NoticeInfo)
	assert.True(t, h.menu.HasNotice(), "the new notice rides the menu row")
	assert.False(t, h.errBox.HasContent(), "the stale errBox row must be cleared")
	assert.Equal(t, 24, lipgloss.Height(h.View()),
		"reclaiming the errBox row must recompute the layout, not leave the frame a line short")
}

// #438: with the hint bar hidden, a transient user-action ack must not shift the
// pane content. The bottom row is always reserved in stateDefault (blank when the
// bar is off), so the notice rides it instead of growing an errBox row —
// paneContentHeight is identical before, during, and after the notice.
func TestHandleInfoNotice_HintBarOffNoLayoutShift(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true) // assembleHome seeds this from hint_bar in production
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	before := h.paneContentHeight()

	cmd := h.handleInfoNotice("branch copied")
	require.NotNil(t, cmd)
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the notice")
	assert.False(t, h.errBox.HasContent(), "no errBox row is claimed, so nothing reflows")
	assert.Equal(t, before, h.paneContentHeight(), "the panes must not shrink when the notice appears")

	h.Update(hideErrMsg{gen: h.noticeGen})
	assert.False(t, h.menu.HasNotice(), "the notice clears")
	assert.Equal(t, before, h.paneContentHeight(), "the panes must not grow when the notice clears")
}

// paneContentHeight must count the errBox row for ANY notice level, matching the
// real widget-sizing budget in updateHandleWindowSizeEvent (both use HasContent).
// It previously used HasError, so an info-level notice riding the errBox silently
// did not shrink the divider Y-bound — a one-row hit-testing drift (#438 review).
// Driven from an overlay state so menuVisible() is false and only the errBox row
// is in question.
func TestPaneContentHeight_CountsInfoNoticeRow(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateHelp // menuVisible() is false here, isolating the errBox row
	h.windowWidth, h.windowHeight = 80, 24

	base := h.paneContentHeight()
	h.errBox.SetNotice("branch copied", ui.NoticeInfo)
	assert.Equal(t, base-1, h.paneContentHeight(),
		"an info-level errBox notice must claim a pane row (HasContent, not HasError)")
}
