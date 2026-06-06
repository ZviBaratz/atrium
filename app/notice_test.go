package app

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
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

// When the user disabled the hint bar (chrome-free mode) there is no reserved
// row to ride, so errors fall back to the pre-existing error-box row.
func TestHandleError_HintBarOffFallsBackToErrRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off

	h.handleError(fmt.Errorf("boom"))

	assert.True(t, h.errBox.HasError())
	assert.False(t, h.menu.HasNotice())
}

// Neutral acknowledgments ("branch copied") use the info level on the same row.
func TestHandleInfoNotice_MenuCarriesIt(t *testing.T) {
	h := newCreateFormHome(t)

	cmd := h.handleInfoNotice("branch 'zvi/foo' copied")

	require.NotNil(t, cmd, "an info notice schedules its own hide")
	assert.True(t, h.menu.HasNotice())
	assert.False(t, h.errBox.HasError(), "info must never look like an error")
}

// Info acknowledgments are chrome; with the hint bar off they are dropped
// rather than claiming a row (errors, by contrast, always surface).
func TestHandleInfoNotice_HintBarOffDropsIt(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off

	cmd := h.handleInfoNotice("branch copied")

	assert.Nil(t, cmd)
	assert.False(t, h.menu.HasNotice())
	assert.False(t, h.errBox.HasError())
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
