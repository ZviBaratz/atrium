package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountPicker_SelectionAndPreselect(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"}, // no matches → inferred default
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "first account selected by default")
	assert.True(t, ap.HasMultiple())

	ap.SelectByName("quantivly")
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "preselect by name")

	ap.Focus()
	ap.HandleKeyPress(keyMsg("up"))
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "Up moves to previous")

	var empty AccountPicker
	assert.Equal(t, config.ClaudeAccount{}, empty.GetSelectedAccount(), "zero picker is safe")
}

// The cursor wraps at both ends so one keypress reaches the opposite end.
func TestAccountPicker_WrapsAtEnds(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	require.Equal(t, "personal", ap.GetSelectedAccount().Name, "first account selected by default")

	ap.HandleKeyPress(keyMsg("left"))
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "← from the first wraps to the last")

	ap.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "personal", ap.GetSelectedAccount().Name, "→ from the last wraps to the first")
}

// touched distinguishes an auto-routed preselection (which the form may revise as
// the target project changes) from a deliberate user override (which must stick).
func TestAccountPicker_TouchedTracksInteraction(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.False(t, ap.Touched(), "a fresh picker is untouched")

	ap.SelectByName("quantivly")
	assert.False(t, ap.Touched(), "programmatic preselect does not count as a user touch")

	ap.HandleKeyPress(keyMsg("left"))
	assert.True(t, ap.Touched(), "a navigation keypress marks the picker touched")
}

// Once the user has taken control, auto-routing's preselect must not clobber it.
func TestAccountPicker_PreselectNoopAfterTouch(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	ap.HandleKeyPress(keyMsg("right")) // user picks quantivly
	require.Equal(t, "quantivly", ap.GetSelectedAccount().Name)

	ap.SelectByName("personal") // a later auto-route attempt
	assert.Equal(t, "quantivly", ap.GetSelectedAccount().Name, "explicit choice survives auto-route")
}

func (ap *AccountPicker) entryLabels() []string {
	out := make([]string, len(ap.entries))
	for i, e := range ap.entries {
		out[i] = e.label
	}
	return out
}
func (ap *AccountPicker) selectIndex(i int) { ap.cursor = i; ap.touched = true }

func TestAccountPicker_PoolEntries(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}
	ap := NewAccountPicker(accounts)

	// A pool contributes one rotating entry then one entry per member; ungrouped
	// accounts contribute one entry each.
	labels := ap.entryLabels()
	require.Equal(t, []string{"work ⇄", "  work-1", "  work-2", "personal"}, labels)

	// The ⇄ entry rotates the pool (no Member); a member entry pins with its pool.
	ap.selectIndex(0)
	e := ap.Selected()
	assert.Equal(t, "work", e.pool)
	assert.Nil(t, e.member)

	ap.selectIndex(1)
	e = ap.Selected()
	assert.Equal(t, "work", e.pool)
	require.NotNil(t, e.member)
	assert.Equal(t, "work-1", e.member.Name)

	ap.selectIndex(3)
	e = ap.Selected()
	assert.Equal(t, "", e.pool)
	require.NotNil(t, e.member)
	assert.Equal(t, "personal", e.member.Name)
}

func TestAccountPicker_NoPoolsUnchanged(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.Equal(t, []string{"personal", "quantivly"}, ap.entryLabels(), "no pools: one entry per account, config order")
}

func TestAccountPicker_PreselectPool(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "work-1", Pool: "work"}, {Name: "work-2", Pool: "work"}, {Name: "personal"},
	}
	ap := NewAccountPicker(accounts)
	ap.SelectByName("work") // preselect the pool ⇄ entry
	e := ap.Selected()
	assert.Equal(t, "work", e.pool)
	assert.Nil(t, e.member, "preselecting a pool lands on its rotating entry")
}
