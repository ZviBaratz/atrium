package app

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
	"github.com/stretchr/testify/require"
)

// synthKeyMsg must produce a message that stringifies back to the key it was
// given: the dispatch path keys off msg.String(), so a mismatch would fire the
// wrong action (or none). Covers every special key a bar can show, plus a rune.
func TestSynthKeyMsg_RoundTrips(t *testing.T) {
	for _, k := range []string{"enter", "esc", " ", "ctrl+x", "shift+up", "shift+down", "n", "?", "q", "s"} {
		msg, ok := synthKeyMsg(k)
		require.True(t, ok, "synthKeyMsg(%q) should succeed", k)
		require.Equal(t, k, msg.String(), "synthesized key must stringify back to %q", k)
	}
	// A range/compound label maps to no single key, so it is not synthesizable —
	// the click is a no-op rather than a wrong action.
	_, ok := synthKeyMsg("a–z")
	require.False(t, ok)
}

// hintBarClickState gates hint-bar clicks to the states where the bar is the
// live surface (default + the three modal bars); every other state has an
// overlay (or a non-key progress bar) owning the screen, so a click on the
// bar's stale zones must be ignored.
func TestHintBarClickState(t *testing.T) {
	h := &home{}
	for _, s := range []state{stateDefault, stateFilter, stateHints, stateVisual} {
		h.state = s
		require.Truef(t, h.hintBarClickState(), "state %d should accept hint-bar clicks", s)
	}
	for _, s := range []state{
		statePrompt, stateHelp, stateConfirm, stateRename, stateQueue, stateInfo,
		stateSettings, stateWelcome, stateAccounts, stateScreensaver,
	} {
		h.state = s
		require.Falsef(t, h.hintBarClickState(), "state %d must ignore hint-bar clicks", s)
	}
}

// A left-click on a hint-bar entry performs the same action as pressing its
// key: clicking "? help" on the empty bar opens the help overlay, exactly like
// pressing ?. This drives the whole path — handleMouse → Menu.KeyAtZone →
// synthKeyMsg → handleKeyPress.
func TestHintBarClick_MirrorsKeyPress(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.menu.SetInstance(nil) // empty bar: n / ? / q

	// The bar marks each entry as zone "hintbar:<key>" (ui.hintZoneID). View()
	// scans the composed frame itself (app.go), so just render until the ? entry's
	// bounds register — scanning its stripped output again would corrupt them.
	const helpZone = "hintbar:?"
	var zi *zone.ZoneInfo
	require.Eventually(t, func() bool {
		_ = h.View()
		zi = zone.Get(helpZone)
		return !zi.IsZero()
	}, time.Second, 5*time.Millisecond, "hint-bar ? zone never registered")

	h.Update(tea.MouseMsg{X: zi.StartX, Y: zi.StartY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	require.Equal(t, stateHelp, h.state, "clicking the ? hint must open help, like pressing ?")
}

// A click that lands on no hint-bar entry falls through to the normal row/tab
// handling and changes no state — the bar's zones don't swallow stray clicks.
func TestHintBarClick_MissIsInert(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.menu.SetInstance(nil)
	_ = h.View() // internally scans the frame's zones

	h.Update(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	require.Equal(t, stateDefault, h.state, "a click off every hint entry must not open an overlay")
}
