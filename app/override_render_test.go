package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"

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
