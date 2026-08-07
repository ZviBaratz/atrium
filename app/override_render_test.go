package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	ansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rebind has to reach the generated surfaces, not just the dispatch map. That
// is the whole reason the override layer could be built cheaply — every surface
// is already a projection — and it is what would fail silently if one of them
// ever went back to a literal.
//
// Written as unit tests on the two generators rather than as a frameStates
// entry: an override in that fixture would cost three golden re-baselines (see
// the drift-sites skill) to assert something neither the frame nor its colour
// fingerprint is about.
func TestOverride_ReachesTheCheatsheetAndTheHintBar(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{"new": {Keys: []string{"ctrl+n"}}})
	defer restore()
	require.Empty(t, problems)

	sheet := ansi.Strip(helpTypeGeneral{}.toContent())
	assert.Contains(t, sheet, "ctrl-n", "the cheatsheet must show the rebound key")
	assert.NotRegexp(t, `(?m)^\s*n\s`, sheet, "the cheatsheet must not still offer the old key")

	m := ui.NewMenu()
	m.SetSize(200, 1)
	bar := ansi.Strip(m.String())
	assert.Contains(t, bar, "ctrl-n new", "the hint bar must show the rebound key")
}

// An unbound action leaves the bar and the cheatsheet with nothing to teach, so
// neither may render a bare separator, a stray space, or a description with no
// key beside it.
func TestOverride_UnboundActionLeavesNoOrphanedChrome(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{"new": {Disabled: true}})
	defer restore()
	require.Empty(t, problems)

	m := ui.NewMenu()
	m.SetSize(200, 1)
	bar := ansi.Strip(m.String())
	assert.NotContains(t, bar, "new", "an unbound action must leave the hint bar entirely")
	assert.NotContains(t, bar, " · ·", "dropping an entry must not leave its separators behind")
	assert.False(t, strings.HasPrefix(strings.TrimSpace(bar), "·"),
		"dropping the first entry must not leave a leading separator")
}

// The cheatsheet still documents an unbound action, with an empty key column:
// the row is how a user finds out the action exists and can be reached from the
// palette. What it must not do is print a key that no longer works.
func TestHelpScreen_OmitsAnUnboundActionsKey(t *testing.T) {
	before := ansi.Strip(helpTypeGeneral{}.toContent())
	require.Contains(t, before, "undo the last kill")

	problems, restore := keys.Apply(map[string]keys.Spec{"undo_kill": {Disabled: true}})
	defer restore()
	require.Empty(t, problems)

	after := ansi.Strip(helpTypeGeneral{}.toContent())
	assert.Contains(t, after, "undo the last kill", "the row stays — the palette still runs it")
	for _, line := range strings.Split(after, "\n") {
		if strings.Contains(line, "undo the last kill") {
			assert.NotContains(t, line, "U ",
				"the row must not still name the key the user unbound: %q", line)
		}
	}
}

// A rebindable key is a key an override can land on y or n, and the
// over-capacity dialog answers on both. handleConfirmState reads the settings
// key ahead of the overlay, so `{"settings": "y"}` — legal, warned about by
// nothing — turned the dialog's own "Create it anyway? y/n" into a settings
// jump that discarded the staged session without a word. The dialog answers for
// the keys it prints.
func TestConfirmDialog_KeepsItsOwnKeysFromARebind(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{
		"copy_branch": {Disabled: true}, // y's default owner, or the override collides
		"settings":    {Keys: []string{"y"}},
	})
	defer restore()
	require.Empty(t, problems, "the override is legal, which is what makes this reachable")

	h := newCreateFormHome(t)
	ran := false
	h.pendingConfirmSettingKey = "max_sessions"
	h.confirmationOverlay = overlay.NewConfirmationOverlay("Create it anyway?")
	h.pendingConfirmAction = func() tea.Msg { ran = true; return nil }
	h.state = stateConfirm

	_, cmd := h.handleConfirmState(textMsg("y"))
	require.NotNil(t, cmd, "y must still confirm the dialog")
	cmd()
	assert.True(t, ran, "y is the dialog's confirm key — it must run the staged action, not open settings")
	assert.NotEqual(t, stateSettings, h.state, "the settings panel must not steal the confirmation")
}

// The over-capacity dialog's tail is an offer, not a description: with settings
// unbound there is no key to press, and handleConfirmState's deep link is inert
// too. Interpolating the label unconditionally advertised "(unbound)".
func TestOverCapMessage_DropsTheTailWhenSettingsIsUnbound(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{"settings": {Disabled: true}})
	defer restore()
	require.Empty(t, problems)

	for _, msg := range []string{overCapMessage(2, 1, 1), overCapMessage(2, 1, 3)} {
		assert.NotContains(t, msg, "(unbound)", "a dialog must not offer a key that does not exist")
		assert.NotContains(t, msg, "to change the limit")
		assert.Contains(t, msg, "anyway?", "the question itself still has to be asked")
	}
}

// Prose that reads Help().Key directly loses its key entirely when the action is
// unbound, leaving a sentence with a hole in it. LabelOf is what says the true
// thing instead — and these two are the sites the prose guard's single-literal
// regex cannot see, because the sentence is built by concatenation.
func TestUnboundAction_LeavesNoHoleInProse(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{
		"new":       {Disabled: true},
		"undo_kill": {Disabled: true},
	})
	defer restore()
	require.Empty(t, problems)

	assert.Contains(t, killedNotice("doomed", true), "(unbound)",
		"the undo advert must name the truth, not render 'killed x ·  to undo'")

	l := ui.NewList(nil)
	l.SetSize(60, 20)
	cta := ansi.Strip(l.String())
	require.Contains(t, cta, "start your first agent", "this is the empty-list CTA")
	assert.Contains(t, cta, "(unbound)",
		"the only call to action on a fresh screen must not read 'Press  to start your first agent'")
}
