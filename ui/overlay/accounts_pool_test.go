package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
)

func TestPoolGutter(t *testing.T) {
	t.Run("two adjacent members are bracketed head and tail", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "work-2", Pool: "work"},
		}
		assert.Equal(t, []string{"┌ ", "└ "}, poolGutter(accts))
	})

	t.Run("three adjacent members get a middle connector", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "work-2", Pool: "work"},
			{Name: "work-3", Pool: "work"},
		}
		assert.Equal(t, []string{"┌ ", "│ ", "└ "}, poolGutter(accts))
	})

	t.Run("pooled and unpooled mix only brackets the run", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "solo"},
			{Name: "work-1", Pool: "work"},
			{Name: "work-2", Pool: "work"},
			{Name: "other"},
		}
		assert.Equal(t, []string{"  ", "┌ ", "└ ", "  "}, poolGutter(accts))
	})

	t.Run("a lone member of a pool has no run to bracket", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "solo", Pool: "work"},
			{Name: "other"},
		}
		assert.Nil(t, poolGutter(accts), "one member alone in its pool: no run of 2, no gutter column at all")
	})

	t.Run("no pools configured", func(t *testing.T) {
		accts := []config.ClaudeAccount{{Name: "a"}, {Name: "b"}}
		assert.Nil(t, poolGutter(accts))
	})

	t.Run("two adjacent empty pools are not a pool", func(t *testing.T) {
		// Distinct from "no pools configured" above: this fixture pins that adjacent
		// empty-pool accounts don't bracket WHILE a real run elsewhere in the same
		// slice does — a version that only ever saw an all-empty slice couldn't tell
		// "Pool == \"\" is exempt from run-forming" apart from "poolGutter never runs
		// on this input at all".
		accts := []config.ClaudeAccount{
			{Name: "a"}, {Name: "b"},
			{Name: "w1", Pool: "work"}, {Name: "w2", Pool: "work"},
		}
		assert.Equal(t, []string{"  ", "  ", "┌ ", "└ "}, poolGutter(accts),
			"the two empty-pool accounts stay blank while the real run is bracketed")
	})

	t.Run("two separate runs of the same pool are both bracketed", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "work-2", Pool: "work"},
			{Name: "mid", Pool: "other"},
			{Name: "work-3", Pool: "work"},
			{Name: "work-4", Pool: "work"},
		}
		assert.Equal(t, []string{"┌ ", "└ ", "  ", "┌ ", "└ "}, poolGutter(accts))
	})

	t.Run("empty slice", func(t *testing.T) {
		assert.Nil(t, poolGutter(nil))
	})
}

func TestSplitPools(t *testing.T) {
	t.Run("members at non-adjacent positions are split", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "personal"},
			{Name: "work-2", Pool: "work"},
		}
		assert.Equal(t, []string{"work"}, splitPools(accts))
	})

	t.Run("adjacent members are not split", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "work-2", Pool: "work"},
		}
		assert.Nil(t, splitPools(accts))
	})

	t.Run("two split pools in first-appearance order", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"},
			{Name: "home-1", Pool: "home"},
			{Name: "work-2", Pool: "work"},
			{Name: "home-2", Pool: "home"},
		}
		assert.Equal(t, []string{"work", "home"}, splitPools(accts))
	})

	t.Run("a single-member pool is never split", func(t *testing.T) {
		accts := []config.ClaudeAccount{
			{Name: "solo", Pool: "work"},
			{Name: "other"},
		}
		assert.Nil(t, splitPools(accts))
	})
}

func TestSplitPoolNote(t *testing.T) {
	t.Run("one name", func(t *testing.T) {
		assert.Equal(t, "pool 'work' is split — J/K to group its members", splitPoolNote([]string{"work"}, 74))
	})

	t.Run("two names", func(t *testing.T) {
		assert.Equal(t, "pools 'work', 'home' are split — J/K to group their members", splitPoolNote([]string{"work", "home"}, 74))
	})

	t.Run("more than two falls back to the bounded count form", func(t *testing.T) {
		assert.Equal(t, "3 pools are split — J/K to group their members", splitPoolNote([]string{"work", "home", "side"}, 74))
	})

	t.Run("naming form too wide for the given width falls back to the count form", func(t *testing.T) {
		assert.Equal(t, "2 pools are split — J/K to group their members", splitPoolNote([]string{"work", "home"}, 10))
	})

	// One split pool whose name is long enough to overflow a real terminal width
	// (a 60-column terminal has inner() == 54): the naming form (57 cols with this
	// name) no longer fits, so it must fall back to a grammatically singular count
	// form, not the "1 pools are split" bug.
	t.Run("one name too wide for a 60-column terminal falls back to the singular count form", func(t *testing.T) {
		assert.Equal(t, "1 pool is split — J/K to group its members",
			splitPoolNote([]string{"quantivly-work"}, 54))
	})

	// Genuinely narrow terminal (inner() == 38): even the singular count form (42
	// cols) doesn't fit, so it must degrade further to a terse last resort rather
	// than wrap.
	t.Run("one name at a genuinely narrow width falls back to the terse form", func(t *testing.T) {
		assert.Equal(t, "pool split — J/K to group", splitPoolNote([]string{"quantivly-work"}, 38))
	})
}
