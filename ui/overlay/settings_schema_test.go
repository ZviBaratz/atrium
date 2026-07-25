package overlay

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/mattn/go-runewidth"
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

// summaryBudget is the widest a summary may be: the panel's inner width at the
// project's 80-column floor once PR B widens the box to min(96, width-2) — a 78-cell
// box, inner 74. Today's boxWidth is capped at 64 (inner 60), so a summary near this
// bound wraps onto a second footer line under the current renderer; that is harmless
// while the footer is still variable-height, and PR B's wider box plus fixed-height
// help pane makes it one line. The bound is enforced now so the copy does not have to
// be revisited when the box grows — do not lower it to 60.
const summaryBudget = 74

// TestSummaryFitsOneLine pins the summary bound from spec §6. A summary that wraps
// is not a defect on its own, but the bound is what keeps the fixed-height help pane
// PR B introduces from needing to scroll for an ordinary row.
func TestSummaryFitsOneLine(t *testing.T) {
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.NotEmptyf(t, r.summary, "row %q has no summary", r.key)
		assert.LessOrEqualf(t, runewidth.StringWidth(r.summary), summaryBudget,
			"row %q summary is %d cells, over the %d-cell budget: %q",
			r.key, runewidth.StringWidth(r.summary), summaryBudget, r.summary)
	}
}

// TestEveryRowHasAKnownCategoryAndScope pins that no row carries a category outside
// allCategories() (which would render under no section header at all) and that the
// scope seam is uniform. The scope assertion is also what keeps the `unused` linter
// from flagging a field PR A stores but does not yet render.
func TestEveryRowHasAKnownCategoryAndScope(t *testing.T) {
	known := map[settingCategory]bool{}
	for _, c := range allCategories() {
		known[c] = true
	}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		assert.Truef(t, known[r.category], "row %q has a category outside allCategories()", r.key)
		assert.Equalf(t, scopeGlobal, r.scope, "row %q must be scopeGlobal until #477 adds a layer", r.key)
	}
}

// glossExemptRows are the enum rows whose options carry no gloss, each for one of two
// reasons: the vocabulary is *dynamic* (it grows when a profile, theme or splash
// variant is added, so an exhaustive map would rot silently), or it is a bare on/off
// pair that glosses itself and whose meaning is already in the row's summary.
//
// Spec §6 supplies glosses for the five enums whose options are a fixed, non-obvious
// vocabulary — those are exactly where the 300-443-char run-on descriptions lived, so
// the guard below stays strict where it earns its keep.
var glossExemptRows = map[string]string{
	"default_program":      "dynamic: option list is the user's profile names",
	"theme":                "dynamic: grows with every theme added to the registry",
	"splash":               "dynamic: grows with every splash variant (random is glossed)",
	"group_mode":           "self-glossing on/off; the summary carries the meaning",
	"model_indicator":      "self-glossing on/off",
	"effort_indicator":     "self-glossing on/off",
	"permission_indicator": "self-glossing on/off",
}

// TestEnumRowsGlossEveryOption pins that each fixed-vocabulary enum option carries a
// one-line gloss. This is what replaced the run-on descriptions: if an option has no
// gloss, its meaning went missing rather than moving.
func TestEnumRowsGlossEveryOption(t *testing.T) {
	cfg := config.DefaultConfig()
	seenExempt := map[string]bool{}
	for _, r := range newSettingRows(cfg) {
		if r.kind != kindEnum {
			assert.NotContainsf(t, glossExemptRows, r.key,
				"row %q is exempted from the gloss guard but is not an enum row", r.key)
			continue
		}
		if reason, ok := glossExemptRows[r.key]; ok {
			seenExempt[r.key] = true
			t.Logf("row %q exempt from the gloss guard: %s", r.key, reason)
			continue
		}
		for _, opt := range r.options(cfg) {
			assert.NotEmptyf(t, r.gloss[opt], "enum row %q has no gloss for option %q", r.key, opt)
		}
	}
	// The exemption list must not outlive the rows it names, or it would silently
	// excuse a future row that reuses a retired key.
	for key := range glossExemptRows {
		assert.Truef(t, seenExempt[key], "glossExemptRows names %q, which is no longer an enum row", key)
	}
}

// TestCategoryRowCounts pins the spec §4 taxonomy row-by-row, so a row cannot drift
// to a neighbouring category unnoticed during a later refactor. The Advanced count
// includes the read-only config-file row.
func TestCategoryRowCounts(t *testing.T) {
	want := map[settingCategory]int{
		catSessions:      4,
		catWorktrees:     6,
		catAppearance:    5,
		catSessionList:   5,
		catNotifications: 4,
		catAutomation:    4,
		catInput:         3,
		catProjects:      2,
		catUpdates:       2,
		catAdvanced:      3, // 2 settings + the read-only config-file row
	}
	got := map[settingCategory]int{}
	total := 0
	for _, r := range newSettingRows(config.DefaultConfig()) {
		got[r.category]++
		total++
	}
	assert.Equal(t, 38, total, "37 config rows plus the read-only config-file row")
	for _, c := range allCategories() {
		assert.Equalf(t, want[c], got[c], "category %q row count", c.label())
	}
}

// TestRowsAreGroupedByCategory pins that rows are declared in contiguous category
// blocks, in allCategories() order. renderBody derives its section headers from
// consecutive rows sharing a category, so a row declared out of position would render
// a second header for a category that already had one.
func TestRowsAreGroupedByCategory(t *testing.T) {
	var order []settingCategory
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if len(order) == 0 || order[len(order)-1] != r.category {
			order = append(order, r.category)
		}
	}
	assert.Equal(t, allCategories(), order,
		"rows must be declared in contiguous blocks, in allCategories() order")
}
