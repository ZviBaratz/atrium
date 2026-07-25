package overlay

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
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

// TestEveryScalarConfigFieldHasARow is the panel twin of
// config.TestReadmeDocumentsEveryConfigField: a new scalar config key must not ship
// reachable only by hand-editing config.json, because that makes it invisible to
// every user who configures Atrium through the panel.
//
// Exempt are the four list-of-record keys, which a one-value-per-row panel cannot
// express (accounts are managed from the Accounts overlay; profiles get their own
// editor in PR D, which is not a settingRow either), and the deprecated nerd_font,
// superseded by glyph_set.
func TestEveryScalarConfigFieldHasARow(t *testing.T) {
	exempt := map[string]string{
		"profiles":        "list of records — Profiles editor (PR D), not a settingRow",
		"claude_accounts": "list of records — Accounts overlay",
		"gh_accounts":     "list of records — Accounts overlay",
		"agy_accounts":    "list of records — Accounts overlay",
		"nerd_font":       "deprecated, superseded by glyph_set",
	}

	count := map[string]int{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		count[r.key]++
	}

	tp := reflect.TypeOf(config.Config{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if reason, ok := exempt[name]; ok {
			assert.Zerof(t, count[name], "%s is exempt (%s) but has a settings row", name, reason)
			continue
		}
		assert.Equalf(t, 1, count[name],
			"config field %s (json:%q) must have exactly one settings row", tp.Field(i).Name, name)
	}
}

// TestEveryRowKeyIsAConfigFieldOrReadOnly is the reverse direction: a row whose key
// matches no config field would persist nothing, and applySettingChange would switch
// on a key that can never arrive. kindReadOnly rows are exempt — they display a
// resolved fact (the config.json path) rather than a config value.
func TestEveryRowKeyIsAConfigFieldOrReadOnly(t *testing.T) {
	fields := map[string]bool{}
	tp := reflect.TypeOf(config.Config{})
	for i := 0; i < tp.NumField(); i++ {
		if name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]; name != "" && name != "-" {
			fields[name] = true
		}
	}

	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.kind == kindReadOnly {
			assert.Nil(t, r.set, "a read-only row must have no setter: %s", r.key)
			continue
		}
		assert.Truef(t, fields[r.key], "row %q matches no config.Config json field", r.key)
	}
}
