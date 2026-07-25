package overlay

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
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

// TestNoRowIsModifiedOnAFreshConfig is the cheapest guard on defaultDisplay: on a
// default config, nothing may claim to be changed. It catches every default that was
// transcribed wrong, in one assertion per row, without enumerating the expected
// values a second time (which would just move the transcription risk).
func TestNoRowIsModifiedOnAFreshConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	for i, r := range o.rows {
		if r.defaultDisplay == nil {
			continue // default_program and branch_prefix — machine-derived, spec §5
		}
		assert.Falsef(t, o.isModified(i),
			"row %q is marked modified on a fresh config: value %q vs default %q",
			r.key, r.get(cfg), r.defaultDisplay())
	}
}

// TestOnlyMachineDerivedRowsOptOutOfDefaults pins *which* rows are allowed to have no
// default, so a future row cannot quietly skip the marker by leaving the field nil.
func TestOnlyMachineDerivedRowsOptOutOfDefaults(t *testing.T) {
	var optedOut []string
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.defaultDisplay == nil && r.kind != kindReadOnly {
			optedOut = append(optedOut, r.key)
		}
	}
	assert.ElementsMatch(t, []string{"default_program", "branch_prefix"}, optedOut,
		"only the two machine-derived rows may have no default (spec §5)")
}

// TestModifiedTracksAnEdit pins the positive direction: after a change, the row does
// report modified. Without this the suite would pass with isModified hardwired false.
func TestModifiedTracksAnEdit(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "mouse")
	i := o.cursor

	require.False(t, o.isModified(i), "mouse starts at its default")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace}) // toggle off
	assert.True(t, o.isModified(i), "a toggled row reports modified")
}

// TestResetRestoresTheDefault pins that every resettable row's reset returns it to the
// value defaultDisplay advertises — the two must agree, or `r` would leave a row still
// marked modified.
func TestResetRestoresTheDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	for i, r := range o.rows {
		if r.reset == nil || r.defaultDisplay == nil {
			continue
		}
		r.reset(cfg)
		assert.Equalf(t, r.defaultDisplay(), r.get(cfg),
			"row %q: reset must produce the advertised default", r.key)
		assert.Falsef(t, o.isModified(i), "row %q is still modified after reset", r.key)
	}
}

// TestResetIsPresentWhereverADefaultIs pins that the two mechanisms travel together:
// a row advertising a default the user can diverge from must offer the way back, and
// a read-only row must offer neither.
func TestResetIsPresentWhereverADefaultIs(t *testing.T) {
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.kind == kindReadOnly {
			assert.Nilf(t, r.defaultDisplay, "read-only row %q must have no default", r.key)
			assert.Nilf(t, r.reset, "read-only row %q must have no reset", r.key)
			continue
		}
		assert.Equalf(t, r.defaultDisplay == nil, r.reset == nil,
			"row %q must declare defaultDisplay and reset together", r.key)
	}
}

// rowByKey returns the row with the given key, failing the test when absent.
func rowByKey(t *testing.T, cfg *config.Config, key string) settingRow {
	t.Helper()
	for _, r := range newSettingRows(cfg) {
		if r.key == key {
			return r
		}
	}
	t.Fatalf("no settings row with key %q", key)
	return settingRow{}
}

// TestInertPredicates pins each activeWhen from spec §5: a row is inert exactly when
// changing it cannot currently do anything. Each case toggles the parent and asserts
// both directions, so a predicate stuck at true or false fails.
func TestInertPredicates(t *testing.T) {
	tests := []struct {
		name       string
		row        string
		makeInert  func(*config.Config)
		makeActive func(*config.Config)
	}{
		{
			name:       "finished turns follows notifications",
			row:        "notifications_finished",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsOff },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsBell },
		},
		{
			name:       "notify when focused follows notifications",
			row:        "notify_when_focused",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsOff },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsBell },
		},
		{
			name:       "notify command needs desktop mode specifically",
			row:        "notify_command",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsBell },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsDesktop },
		},
		{
			name:       "fast-forward follows update base on create",
			row:        "fast_forward_local_base",
			makeInert:  func(c *config.Config) { f := false; c.UpdateBaseOnCreate = &f },
			makeActive: func(c *config.Config) { tr := true; c.UpdateBaseOnCreate = &tr },
		},
		{
			name:       "poll interval follows auto-yes",
			row:        "daemon_poll_interval",
			makeInert:  func(c *config.Config) { c.AutoYes = false },
			makeActive: func(c *config.Config) { c.AutoYes = true },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			row := rowByKey(t, cfg, tc.row)
			require.NotNil(t, row.activeWhen, "row %q must declare activeWhen", tc.row)

			tc.makeInert(cfg)
			assert.False(t, row.activeWhen(cfg), "expected inert")
			tc.makeActive(cfg)
			assert.True(t, row.activeWhen(cfg), "expected active")
		})
	}
}

// TestInertRowsStayEditable pins the rule from spec §5: inert means "changing this has
// no effect right now", never "you may not touch this" — a user may configure ahead of
// enabling the parent. An inert row keeps its setter and its reset.
func TestInertRowsStayEditable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	row := rowByKey(t, cfg, "notifications_finished")

	require.False(t, row.activeWhen(cfg), "the row is inert with notifications off")
	require.NoError(t, row.set(cfg, config.NotificationsBell), "an inert row is still settable")
	assert.Equal(t, config.NotificationsBell, cfg.NotificationsFinished)
	assert.NotNil(t, row.reset, "an inert row keeps its reset")
}

// TestOOMMarginIsLinuxOnly pins the one platform predicate. It asserts against the
// build's own GOOS rather than a hardcoded expectation, so it is meaningful on the
// macOS CI job too.
func TestOOMMarginIsLinuxOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	row := rowByKey(t, cfg, "agent_oom_margin")
	require.NotNil(t, row.activeWhen)
	assert.Equal(t, runtime.GOOS == "linux", row.activeWhen(cfg))
}

// TestGroupModeHasNoConfigOnlyInertPredicate pins a deliberate *absence*, because it is
// the one predicate spec §5 derived from prose rather than code — and the code disagrees.
//
// The spec proposed `len(cfg.ClaudeAccounts) >= 2` with the reason chip "needs 2+
// accounts". ui.List's actual visual gate is
//
//	accountGroupingVisible := l.accountGrouped() && l.distinctAccountCount() > 1
//
// (ui/list_render.go), where distinctAccountCount counts distinct
// Instance.AccountClusterKey() values over the *live session list* — and that key is the
// session's rotation *pool* when it has one, else its account. So the configured account
// count is the wrong thing on both axes:
//
//   - Several configured accounts sharing one pool collapse to a single key, so
//     clustering is a visual no-op while len(ClaudeAccounts) >= 2 would call it active.
//   - Sessions with no account attribution key on "" and still form a second cluster, so
//     clustering can be visible with fewer than two accounts configured.
//
// A settingRow predicate only sees *config.Config, never the session list, so the honest
// gate is not expressible here and group_mode stays always-active rather than shipping a
// chip that is wrong in both directions. PR B owns the reason strings and has the list in
// hand; the gate belongs there. (ui.List.AccountReorderEnabled uses a *third* count —
// clusters, not accounts — so "cluster count != account count" holds here too.)
func TestGroupModeHasNoConfigOnlyInertPredicate(t *testing.T) {
	row := rowByKey(t, config.DefaultConfig(), "group_mode")
	assert.Nil(t, row.activeWhen,
		"group_mode's real gate is session-derived (see this test's comment); a "+
			"config-only predicate would be wrong in both directions")
}

// TestDetailRetainsTheMovedProse pins the specific facts that moved from the old
// one-paragraph descriptions into detail. Each was the only place Atrium documented
// something a user cannot discover from the UI, and PR A does not render detail yet —
// so without this test, losing one in transcription is invisible until PR B ships.
func TestDetailRetainsTheMovedProse(t *testing.T) {
	want := map[string][]string{
		"group_mode": {
			"an account boundary is refused", // the {/} reorder rule
			"`[` / `]`",                      // the cluster-reorder keys
		},
		"link_paths": {
			"no trailing slash", // the ignore-pattern trap from #471
		},
		"agent_oom_margin": {
			"Linux only",
			"oom_score_adj", // names the kernel knob so the setting is searchable
		},
		"max_sessions": {
			"paused ones included", // the hard-cap contract
		},
		"notify_command": {
			"ATRIUM_SESSION", // the env contract
		},
		"mouse": {
			"Shift+drag", // the per-gesture escape hatch
		},
		"notifications": {
			"muted", // which sessions stay silent
		},
		"fast_forward_local_base": {
			"writes outside a session worktree", // the caution that was an applyNote
		},
		"carry_files": {
			"explicit opt-out", // the nil-vs-empty contract
		},
		"session_context_bar": {
			"when a server starts", // why a running session keeps its old bar
		},
	}

	rows := newSettingRows(config.DefaultConfig())
	byKey := make(map[string]settingRow, len(rows))
	for _, r := range rows {
		byKey[r.key] = r
	}

	for key, phrases := range want {
		row, ok := byKey[key]
		require.Truef(t, ok, "no row %q", key)
		for _, p := range phrases {
			assert.Containsf(t, row.detail, p,
				"row %q lost %q from its help — it documents something the UI cannot show", key, p)
		}
	}
}
