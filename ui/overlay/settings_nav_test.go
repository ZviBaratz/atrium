package overlay

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRailEntriesAreTheThirteen pins the rail's exact contents and order (spec §3/§4):
// All settings, the ten scalar categories in allCategories() order, then the two
// handoffs. The count is the one TestCategoryCountFitsTheRailBudget already reserves
// budget for, so this is the test that spends it.
func TestRailEntriesAreTheThirteen(t *testing.T) {
	entries := railEntries()
	require.Len(t, entries, 13, "spec §4: ten categories plus All settings, Profiles, Accounts")

	assert.Equal(t, "All settings", entries[0].label)
	assert.Equal(t, railAll, entries[0].kind)

	for i, c := range allCategories() {
		e := entries[i+1]
		assert.Equalf(t, railCategory, e.kind, "entry %d must project a category", i+1)
		assert.Equalf(t, c, e.category, "entry %d must be %q", i+1, c.label())
		assert.Equalf(t, c.label(), e.label, "a rail label must be the category's own label")
	}

	for _, e := range entries[11:] {
		assert.Equalf(t, railHandoff, e.kind, "%q must be a handoff", e.label)
	}
	assert.Equal(t, "Profiles", entries[11].label)
	assert.Equal(t, "Accounts", entries[12].label)
}

// TestEveryHandoffEntryNamesItsSurface pins that a rail entry owning no rows still says
// where its config lives. An entry that renders an empty pane teaches the user nothing
// and reads as a bug; PR C wires Accounts to the @ overlay and PR D builds the Profiles
// editor, so until then the note is the whole content of that pane.
func TestEveryHandoffEntryNamesItsSurface(t *testing.T) {
	handoffs := 0
	for _, e := range railEntries() {
		if e.kind != railHandoff {
			assert.Emptyf(t, e.note, "only a handoff entry carries a note: %q", e.label)
			continue
		}
		handoffs++
		assert.NotEmptyf(t, e.note, "handoff entry %q must name the surface that owns it", e.label)
	}
	// Without this the loop could stop running and the test would still pass.
	require.Equal(t, 2, handoffs, "Profiles and Accounts are the two handoffs")
}

// TestRailDefaultIndexIsTheFirstCategory pins the landing entry. Spec §4 is explicit
// that All settings is NOT the default landing — it is the flat audit view, preserved
// for muscle memory, not the browsing default. Derived rather than a literal so
// reordering the rail cannot silently land the panel on a handoff.
func TestRailDefaultIndexIsTheFirstCategory(t *testing.T) {
	entries := railEntries()
	i := railDefaultIndex()
	require.Less(t, i, len(entries))
	assert.Equal(t, railCategory, entries[i].kind, "the panel must not land on a view or a handoff")
	assert.Equal(t, "Sessions", entries[i].label)
}

// TestRailIndexForCategoryFindsEveryCategory pins that every category is reachable from
// its enum value, which is what SelectRow (and PR C's OpenAt) rely on to sync the rail
// to a deep-linked row.
func TestRailIndexForCategoryFindsEveryCategory(t *testing.T) {
	for _, c := range allCategories() {
		i := railIndexForCategory(c)
		e := railEntries()[i]
		assert.Equalf(t, railCategory, e.kind, "category %q resolved to a non-category entry", c.label())
		assert.Equalf(t, c, e.category, "category %q resolved to entry %q", c.label(), e.label)
	}
}

// TestRailWidthTracksItsLongestLabel pins that railWidth() is MEASURED rather than written
// down, which is what makes the degradation threshold move when a category is renamed
// (see TestThresholdIsDerivedFromTheParts). Asserting that each label fits railWidth()
// would be a tautology — railWidth() is defined as the max of those very labels — so what
// is pinned here is the derivation, and TestRailRendersEveryLabelWhole pins that nothing
// truncates in practice.
func TestRailWidthTracksItsLongestLabel(t *testing.T) {
	widest, label := -1, ""
	for _, e := range railEntries() {
		if n := ansi.StringWidth(e.label); n > widest {
			widest, label = n, e.label
		}
	}
	assert.Equal(t, railMarkerCells+widest+railTrailCells, railWidth())
	assert.Equal(t, "Worktrees & git", label,
		"the widest rail label today; if this changed, the threshold moved with it")
}
