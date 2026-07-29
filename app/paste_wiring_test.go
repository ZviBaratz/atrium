package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// handlePaste enumerates the states where text can land, so every overlay that
// grew a HandlePaste needs a wire from that switch — and a wire with no test is
// how a surface ships silently dead. The tests in ui/overlay prove each
// HandlePaste works; these prove home.Update actually reaches them, and assert
// through the rendered panel rather than a test-only accessor.

func TestPasteReachesTheRenameOverlay(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateRename
	h.renameOverlay = overlay.NewRenameOverlay("old", "", false)

	h.Update(tea.PasteMsg{Content: "-renamed"})

	require.Equal(t, "old-renamed", h.renameOverlay.Value(),
		"a paste must reach the rename field")
	require.Equal(t, stateRename, h.state, "a paste must not close the rename dialog")
}

func TestPasteNarrowsTheCommandPalette(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateCommandPalette
	h.commandPaletteOverlay = overlay.NewCommandPaletteOverlay([]overlay.PaletteAction{
		{Label: "attach"}, {Label: "quit"},
	})

	h.Update(tea.PasteMsg{Content: "att"})

	rendered := h.commandPaletteOverlay.Render()
	require.Contains(t, rendered, "attach", "the matching action must survive the filter")
	require.NotContains(t, rendered, "quit",
		"a paste must actually narrow the palette, not just be swallowed")
	require.Equal(t, stateCommandPalette, h.state)
}

func TestPasteReachesTheSettingsSearch(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateSettings
	h.settingsOverlay = overlay.NewSettingsOverlay(h.appConfig)
	h.settingsOverlay.SetSize(100, 30)
	h.settingsOverlay.HandleKeyPress(keyMsg("/"))

	// A string no setting name contains: it can only appear in the rendered panel
	// by way of the filter the paste edited.
	h.Update(tea.PasteMsg{Content: "zzqq"})

	require.Contains(t, h.settingsOverlay.Render(), "zzqq",
		"a paste must reach the settings filter")
	require.Equal(t, stateSettings, h.state)
}

func TestPasteReachesTheAccountsForm(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateAccounts
	h.accountsOverlay = overlay.NewAccountsOverlay(&config.Config{}, config.DefaultState())
	h.accountsOverlay.SetSize(80, 24)
	h.accountsOverlay.HandleKeyPress(keyMsg("n")) // open the record form

	h.Update(tea.PasteMsg{Content: "workacct"})

	require.Contains(t, h.accountsOverlay.Render(), "workacct",
		"a paste must reach the accounts record form")
	require.Equal(t, stateAccounts, h.state)
}

// TestPasteIntoTheListFilterSurvivesRender is the session filter's wire. The
// filter is Atrium's own buffer rather than a bubbles model, so it takes the
// paste by string concatenation and has no overlay to prove it through.
func TestPasteIntoTheListFilterSurvivesRender(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateFilter
	h.list.SetFilterActive(true)

	h.Update(tea.PasteMsg{Content: "feature/"})
	h.Update(tea.PasteMsg{Content: "login"})

	require.Equal(t, "feature/login", h.list.FilterQuery(),
		"consecutive pastes must accumulate the way typing does")
}

// A paste arriving while a state's overlay is absent must be dropped, not
// dereferenced: paste is the one input that can arrive without a keystroke to
// precede it, and a dropped paste beats a crash.
func TestPasteOnAMissingOverlayIsDropped(t *testing.T) {
	for name, st := range map[string]state{
		"prompt":   statePrompt,
		"rename":   stateRename,
		"palette":  stateCommandPalette,
		"settings": stateSettings,
		"accounts": stateAccounts,
	} {
		t.Run(name, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.state = st

			require.NotPanics(t, func() {
				h.Update(tea.PasteMsg{Content: "text"})
			}, "a paste with no overlay to receive it must be dropped")
		})
	}
}

// TestPasteIsIgnoredWhenEmpty pins the early return: an empty clipboard must not
// be reported as an edit anywhere, since a filter edit resets picker cursors.
func TestPasteIsIgnoredWhenEmpty(t *testing.T) {
	h := newCreateFormHome(t)
	h.state = stateFilter
	h.list.SetFilterActive(true)
	h.list.SetFilter("keep")

	_, cmd := h.Update(tea.PasteMsg{Content: ""})

	require.Nil(t, cmd, "an empty paste is not an edit and needs no refresh")
	require.Equal(t, "keep", h.list.FilterQuery())
}
