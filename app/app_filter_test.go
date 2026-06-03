package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// newFilterHome builds a home with just enough wired up to exercise the filter key routing
// (instanceChanged touches list/menu/tabbedWindow). The list is left empty — filter routing
// does not depend on a selected instance.
func newFilterHome() *home {
	sp := spinner.New()
	l := ui.NewList(&sp)
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		list:         l,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
	}
}

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func press(t *testing.T, h *home, msg tea.KeyMsg) {
	t.Helper()
	model, _ := h.handleKeyPress(msg)
	_, ok := model.(*home)
	require.True(t, ok)
}

// enterFilter presses '/' from the default state. Global-map keys take two passes there: the
// first highlights the menu entry and re-sends the key, the second actually handles it (see
// handleMenuHighlighting). Keys typed *within* filter mode skip that, so they need only one press.
func enterFilter(t *testing.T, h *home) {
	t.Helper()
	press(t, h, runeKey("/")) // highlight pass (re-sends the key)
	press(t, h, runeKey("/")) // actual KeyFilter handling
	require.Equal(t, stateFilter, h.state, "precondition: entered filter mode")
}

// '/' enters filter mode with an empty query and the list source of truth reflects it.
func TestFilterKeys_SlashEntersFilterMode(t *testing.T) {
	h := newFilterHome()

	enterFilter(t, h)

	require.Equal(t, "", h.list.FilterQuery())
}

// Letters — including 'j' and 'k', which double as navigation elsewhere — must extend the query
// while typing, not commit the filter. This is the regression guard for the 'j'-can't-be-typed bug.
func TestFilterKeys_LettersExtendQueryIncludingJ(t *testing.T) {
	h := newFilterHome()
	enterFilter(t, h)

	for _, ch := range []string{"j", "s", "o", "n", "k"} {
		press(t, h, runeKey(ch))
	}

	require.Equal(t, stateFilter, h.state, "typing must not leave filter mode")
	require.Equal(t, "jsonk", h.list.FilterQuery())
}

// Backspace trims the last rune from the query held by the list (the single source of truth).
func TestFilterKeys_BackspaceTrims(t *testing.T) {
	h := newFilterHome()
	enterFilter(t, h)
	press(t, h, runeKey("a"))
	press(t, h, runeKey("b"))

	press(t, h, tea.KeyMsg{Type: tea.KeyBackspace})

	require.Equal(t, "a", h.list.FilterQuery())
}

// Enter commits: it leaves filter mode but keeps the query applied to the list.
func TestFilterKeys_EnterCommitsAndKeepsQuery(t *testing.T) {
	h := newFilterHome()
	enterFilter(t, h)
	press(t, h, runeKey("a"))

	press(t, h, tea.KeyMsg{Type: tea.KeyEnter})

	require.Equal(t, stateDefault, h.state)
	require.Equal(t, "a", h.list.FilterQuery(), "committed filter stays applied")
}

// Esc clears the filter entirely and returns to the default state.
func TestFilterKeys_EscClears(t *testing.T) {
	h := newFilterHome()
	enterFilter(t, h)
	press(t, h, runeKey("a"))

	press(t, h, tea.KeyMsg{Type: tea.KeyEscape})

	require.Equal(t, stateDefault, h.state)
	require.Equal(t, "", h.list.FilterQuery())
}
