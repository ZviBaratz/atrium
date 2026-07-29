package overlay

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
)

// The accounts and settings panels are the two remaining surfaces that take
// typing, and both dispatch on msg.String() — so both had to grow a paste path
// rather than inherit one. A config dir and a setting value are paths and
// fragments: exactly what arrives via the clipboard.

// TestAccountsFormPasteFillsTheFocusedField pins that a paste reaches the record
// form and never answers its enter/esc grammar.
func TestAccountsFormPasteFillsTheFocusedField(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(textMsg("n")) // open the new-record form
	require.Equal(t, modeEdit, o.mode)

	o.HandlePaste(tea.PasteMsg{Content: "work"})

	require.Equal(t, "work", o.form.inputs[fldName].Value())
	require.Equal(t, modeEdit, o.mode, "a paste must not leave the form")
}

// TestAccountsFormPasteOfAControlWordDoesNotCommit is the destructive case: the
// form's key switch maps "enter" to submit and "esc" to cancel.
func TestAccountsFormPasteOfAControlWordDoesNotCommit(t *testing.T) {
	for _, word := range []string{"enter", "esc", "ctrl+c"} {
		t.Run(word, func(t *testing.T) {
			o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
			o.SetSize(80, 24)
			o.HandleKeyPress(textMsg("n"))

			o.HandlePaste(tea.PasteMsg{Content: word})

			require.Equal(t, modeEdit, o.mode, "a pasted %q must not close the form", word)
			require.Equal(t, word, o.form.inputs[fldName].Value(),
				"a pasted %q belongs in the field as text", word)
		})
	}
}

// TestAccountsPasteIsInertInTheList pins the other half: the list takes no text,
// so a paste there must not be read as the keys its characters spell.
func TestAccountsPasteIsInertInTheList(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	require.Equal(t, modeList, o.mode)

	o.HandlePaste(tea.PasteMsg{Content: "n"})

	require.Equal(t, modeList, o.mode, "a paste in the list must not open the form")
	require.Nil(t, o.form)
}

// TestSettingsSearchPasteNarrowsTheFilter covers the settings panel's `/` filter,
// which is a Picker and so shared the space-truncation defect too.
func TestSettingsSearchPasteNarrowsTheFilter(t *testing.T) {
	s := NewSettingsOverlay(config.DefaultConfig())
	s.SetSize(100, 30)
	s.HandleKeyPress(keyMsg("/"))
	require.True(t, s.searching(), "the filter must be open for this test to bite")

	s.HandlePaste(tea.PasteMsg{Content: " auto attach"})

	require.Equal(t, " auto attach", s.search.filter,
		"the whole paste, leading space included, must reach the filter")
}

// TestSettingsPasteIsInertOnTheRowList pins that the row list — which reads j/k and
// enter — takes no pasted text.
func TestSettingsPasteIsInertOnTheRowList(t *testing.T) {
	s := NewSettingsOverlay(config.DefaultConfig())
	s.SetSize(100, 30)
	require.False(t, s.searching())
	before := s.cursor

	s.HandlePaste(tea.PasteMsg{Content: "j"})

	require.Equal(t, before, s.cursor, "a pasted \"j\" must not move the row cursor")
	require.False(t, s.editing, "a paste must not open the inline editor")
}
