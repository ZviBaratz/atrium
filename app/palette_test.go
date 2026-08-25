package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openPalette opens the palette the way a user does — through the key, not the
// opener — and sizes it. Every test here goes through handleKeyPress because the
// binding itself has no structural guard: a key can be registered, documented and
// dead, and only pressing it proves otherwise.
func openPalette(t *testing.T, h *home) {
	t.Helper()
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, _ = h.handleKeyPress(keyMsg("ctrl+k"))
	require.Equal(t, stateCommandPalette, h.state, "ctrl+k must open the palette")
	require.NotNil(t, h.commandPaletteOverlay)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
}

// typeQuery sends each rune of q to the open palette.
func typeQuery(t *testing.T, h *home, q string) {
	t.Helper()
	for _, r := range q {
		_, _ = h.handleKeyPress(textMsg(string(r)))
	}
}

// The palette's central claim: it is generated, so it lists exactly the actions
// that can be run by name — no hand-maintained inventory to drift, and no row
// that does nothing. Both directions, because each fails differently: a missing
// row is an action you cannot find, and an extra row is a promise that breaks on
// enter.
func TestPaletteListsEveryDispatchableAction(t *testing.T) {
	listed := map[keys.KeyName]bool{}
	for _, row := range paletteRows() {
		assert.Falsef(t, listed[row.name], "%v appears twice in the palette", row.name)
		listed[row.name] = true
	}

	for _, e := range keys.Registry {
		want := !e.DocOnly && dispatchExempt[e.Name] == ""
		assert.Equalf(t, want, listed[e.Name],
			"palette listing for %q disagrees with whether it can be dispatched by name "+
				"(DocOnly=%v, exempt=%q)", strings.Join(e.Binding.Keys(), "/"), e.DocOnly, dispatchExempt[e.Name])
	}
}

// Every row must carry the two things that make it worth showing: the key it
// substitutes for, and prose beyond the two-word hint-bar label. A row generated
// without its cheatsheet prose still renders — it just stops teaching, which no
// count-based check would notice.
func TestPaletteRowsCarryTheirKeyAndProse(t *testing.T) {
	rows := paletteRows()
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.NotEmptyf(t, row.action.Key, "%v has no key label", row.name)
		assert.NotEmptyf(t, row.action.Label, "%v has no verb", row.name)
		assert.NotEmptyf(t, row.action.Detail, "%v has no prose — its cheatsheet row went missing", row.name)
		assert.NotEmptyf(t, row.action.Group, "%v has no section", row.name)
	}
}

// The easter egg's exclusion is structural: KeyScreensaver has no Registry entry,
// so a palette generated from the registry cannot list it. That is the contract
// the ? screen already keeps (TestHelpScreen_OmitsScreensaverKey); the palette is
// a second generated surface and inherits it the same way.
func TestPaletteOmitsTheScreensaver(t *testing.T) {
	for _, row := range paletteRows() {
		assert.NotEqual(t, keys.KeyScreensaver, row.name, "the palette must not list the easter egg")
		assert.NotContains(t, strings.ToLower(row.action.Key), "`", "no row may advertise the backtick")
	}

	h := newCreateFormHome(t)
	openPalette(t, h)
	assert.NotContains(t, strings.ToLower(h.commandPaletteOverlay.Render()), "screensaver")
}

// Press it. The binding, the overlay, the filter, the chosen index and the
// dispatch have to hold hands end to end — every one of them is green in a suite
// that never runs this path.
func TestPaletteRunsTheChosenAction(t *testing.T) {
	h := newCreateFormHome(t)
	openPalette(t, h)
	typeQuery(t, h, "settings")

	_, _ = h.handleKeyPress(keyMsg("enter"))

	assert.Equal(t, stateSettings, h.state, "enter must run the highlighted action, not just close")
	assert.NotNil(t, h.settingsOverlay)
	assert.Nil(t, h.commandPaletteOverlay, "the palette must be gone before the action's own overlay opens")
	assert.Nil(t, h.paletteRows, "a closed palette must not leave row indices behind to resolve later")
}

// Esc is the other way out, and it must run nothing. A palette that acted on the
// highlighted row when the user backed out would be worse than no palette.
func TestPaletteEscapeRunsNothing(t *testing.T) {
	h := newCreateFormHome(t)
	openPalette(t, h)
	typeQuery(t, h, "settings")

	_, _ = h.handleKeyPress(keyMsg("esc"))

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.settingsOverlay, "esc must not run the highlighted action")
	assert.Nil(t, h.commandPaletteOverlay)
}

// q types into the filter rather than quitting: the palette's prelude branch runs
// before the global quit handling, and the ordering is the whole reason that
// branch is where it is.
func TestPaletteSwallowsTheQuitKey(t *testing.T) {
	h := newCreateFormHome(t)
	openPalette(t, h)

	_, cmd := h.handleKeyPress(textMsg("q"))

	require.Equal(t, stateCommandPalette, h.state, "q must narrow the filter, not quit the app")
	assert.False(t, isQuit(cmd))
	assert.Equal(t, "q", h.commandPaletteOverlay.Query())
}

// The busy gate has to be re-applied by hand, because dispatchAction sits below
// the one in handleKeyPress. Both halves matter: the palette cannot be opened
// mid-action (it is an overlay opener, so the existing gate covers that), and an
// action chosen from it cannot slip through either.
func TestPaletteRespectsTheBusyGate(t *testing.T) {
	h := newCreateFormHome(t)
	h.actionInFlight = true

	_, _ = h.handleKeyPress(keyMsg("ctrl+k"))
	assert.Equal(t, stateDefault, h.state, "the palette is an overlay opener; busy must swallow it")

	// And a chosen action, for the window where an action goes in flight while the
	// palette is already open. Driven through the real path — the notice surface
	// depends on the palette already being dismissed, so calling runPaletteAction
	// with the overlay still up would test a state the app never reaches.
	h.actionInFlight = false
	openPalette(t, h)
	typeQuery(t, h, "settings")
	h.actionInFlight = true
	_, _ = h.handleKeyPress(keyMsg("enter"))

	assert.Nil(t, h.settingsOverlay, "a busy palette must not open the settings panel")
	assert.True(t, h.menu.HasNotice(), "and it must say why, rather than silently doing nothing")
	assert.Contains(t, h.menu.String(), "busy")
}

// A navigation key is on the busy allowlist, so choosing it from the palette must
// still work — the gate is a filter, not a blanket.
func TestPaletteAllowsANavigationActionWhileBusy(t *testing.T) {
	h := newCreateFormHome(t)
	h.actionInFlight = true

	_, _ = h.runPaletteAction(keys.KeyTab)

	assert.False(t, h.menu.HasNotice(), "tab is allowed while busy; the palette must not refuse it")
}

// The palette runs exactly what the key runs, focus included: while the pane
// holds focus, the palette's up/down rows scroll the snapshot the way the keys
// do, instead of moving the list and killing the snapshot via the selection's
// owner change. runPaletteAction is called directly because that is the state
// the app reaches — the palette is dismissed (stateDefault restored) before
// the chosen action runs.
func TestPaletteNavActionScrollsPaneUnderFocus(t *testing.T) {
	h := scrollHome(t)
	selected := h.list.GetSelectedInstance()
	bottom, ok := h.tabbedWindow.PreviewScrollContent()
	require.True(t, ok)

	_, _ = h.runPaletteAction(keys.KeyUp)
	moved, ok := h.tabbedWindow.PreviewScrollContent()
	require.True(t, ok, "the palette's up must keep the pane in scroll mode")
	require.NotEqual(t, bottom, moved, "the palette's up must scroll the snapshot, like the key")
	require.Same(t, selected, h.list.GetSelectedInstance(), "the palette's up must not move the list selection")

	_, _ = h.runPaletteAction(keys.KeyDown)
	_, _ = h.runPaletteAction(keys.KeyDown)
	require.True(t, h.tabbedWindow.IsPreviewInScrollMode(),
		"the palette's down must hold at the bottom, like the key")
	require.Same(t, selected, h.list.GetSelectedInstance())
}
