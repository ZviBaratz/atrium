package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryCategoryHasALabel pins that allCategories is the complete ordered
// vocabulary and that every member resolves to a non-empty rail label. A category
// added to the enum without a label case would render as a blank section header.
func TestEveryCategoryHasALabel(t *testing.T) {
	cats := allCategories()
	require.Len(t, cats, 10, "the spec's taxonomy is ten scalar categories (spec §4)")

	seen := make(map[string]bool, len(cats))
	for _, c := range cats {
		label := c.label()
		require.NotEmptyf(t, label, "category %d has no label", int(c))
		require.Falsef(t, seen[label], "duplicate category label %q", label)
		seen[label] = true
	}
}

// TestCategoryCountFitsTheRailBudget pins the spec §4 invariant: the rail must fit
// unscrolled at the project's 80x24 degradation floor. Budget = 24 - (border 2 +
// padding 2 + title 1 + blank 1 + separator 1 + help 3 + hint 1) = 13 rows. PR B
// adds three non-scalar rail entries (All settings, Profiles, Accounts), so the
// scalar categories may not exceed 10 without displacing one of those.
func TestCategoryCountFitsTheRailBudget(t *testing.T) {
	const railBudget = 13
	const nonScalarRailEntries = 3 // All settings, Profiles, Accounts (PR B/D)
	assert.LessOrEqual(t, len(allCategories())+nonScalarRailEntries, railBudget,
		"a new category must displace another or the rail scrolls at 80x24 (spec §4)")
}

// TestApplyTimingProjections pins both projections of the closed timing enum: the
// footer note the single-column renderer appends today (empty for live, so 25 of 37
// rows stay unannotated) and the right-aligned chip PR B adds.
func TestApplyTimingProjections(t *testing.T) {
	assert.Equal(t, "", timingLive.footerNote(), "live needs no footer note")
	assert.Equal(t, "affects new sessions", timingNewSessions.footerNote())
	assert.Equal(t, "applies on restart", timingRestart.footerNote())

	assert.Equal(t, "live", timingLive.badge())
	assert.Equal(t, "new sessions", timingNewSessions.badge())
	assert.Equal(t, "restart", timingRestart.badge())
}
