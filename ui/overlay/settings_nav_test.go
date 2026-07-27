package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
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

// TestRowRangeCoversEveryRowExactlyOnce pins that the category entries partition the row
// slice and the All settings view spans all of it. A gap would make a row unreachable
// from the rail; an overlap would show it twice.
func TestRowRangeCoversEveryRowExactlyOnce(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())

	start, end := o.rowRange(railEntries()[0])
	assert.Equal(t, 0, start, "All settings starts at the first row")
	assert.Equal(t, len(o.rows), end, "All settings spans every row")

	seen := make([]int, len(o.rows))
	for _, e := range railEntries() {
		if e.kind != railCategory {
			continue
		}
		s, en := o.rowRange(e)
		require.Lessf(t, s, en, "category %q has no rows", e.label)
		for i := s; i < en; i++ {
			seen[i]++
		}
	}
	for i, n := range seen {
		assert.Equalf(t, 1, n, "row %q is claimed by %d category entries", o.rows[i].key, n)
	}
}

// TestHandoffEntryHasNoRows pins that a handoff's range is empty, which is what makes
// →/Tab/Enter a no-op on it rather than focusing an empty pane.
func TestHandoffEntryHasNoRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, e := range railEntries() {
		if e.kind != railHandoff {
			continue
		}
		start, end := o.rowRange(e)
		assert.Equalf(t, end, start, "handoff entry %q must own no rows", e.label)
	}
}

// TestPanelOpensOnTheRail pins the initial focus and cursor: the rail, on the first real
// category, with the row cursor pulled into it. Opening focused on the rows pane would
// make ↑/↓ move rows before the user has chosen a category.
func TestPanelOpensOnTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	assert.Equal(t, focusRail, o.focus)
	assert.Equal(t, railDefaultIndex(), o.railCursor)

	start, end := o.rowRange(o.selectedEntry())
	assert.GreaterOrEqual(t, o.cursor, start)
	assert.Less(t, o.cursor, end, "the row cursor must start inside the landing category")
}

// TestRailNavigationMovesTheRailNotTheRows pins that ↑/↓ on the rail change the category
// and pull the row cursor with them — the live-preview behavior of spec §3, where moving
// the rail immediately re-renders the right pane so there is no hidden state.
func TestRailNavigationMovesTheRailNotTheRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	before := o.railCursor

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, before+1, o.railCursor)
	start, end := o.rowRange(o.selectedEntry())
	assert.GreaterOrEqualf(t, o.cursor, start, "the row cursor must follow the rail")
	assert.Less(t, o.cursor, end)

	// j/k navigate too.
	o.HandleKeyPress(keyRunes("k"))
	assert.Equal(t, before, o.railCursor)
}

// TestRailNavigationClampsAtEnds pins that the rail does not wrap: at the top, up is a
// no-op; at the bottom, down is.
func TestRailNavigationClampsAtEnds(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for range railEntries() {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Equal(t, 0, o.railCursor, "up at the top clamps")

	for range railEntries() {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, len(railEntries())-1, o.railCursor, "down at the bottom clamps")
}

// TestRowNavigationStaysWithinTheCategory pins that ↑/↓ in the rows pane cannot walk out
// of the visible category — the cursor would leave the pane and the help text would
// describe a row nobody can see.
func TestRowNavigationStaysWithinTheCategory(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme") // Appearance: 5 rows
	start, end := o.rowRange(o.selectedEntry())
	require.Equal(t, start, o.cursor, "theme is Appearance's first row")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, start, o.cursor, "up at the category's first row clamps")

	for range o.rows {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, end-1, o.cursor, "down stops at the category's last row")
}

// TestPagingKeysStayWithinTheCategory pins the rest of D3: reaching the last row of the old
// flat list took 36 keypresses, and the rail only fixes the "jump to a section" half.
func TestPagingKeysStayWithinTheCategory(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	settingsAt(t, o, "theme") // Appearance
	start, end := o.rowRange(o.selectedEntry())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnd})
	assert.Equal(t, end-1, o.cursor, "end goes to the category's last row")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyHome})
	assert.Equal(t, start, o.cursor, "home goes to its first")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyPgDown})
	assert.LessOrEqual(t, o.cursor, end-1, "pgdown clamps inside the category")
	assert.Greater(t, o.cursor, start, "and actually moves")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyPgUp})
	assert.Equal(t, start, o.cursor, "pgup clamps at the first row")
}

// TestArrowsAreAlwaysTheValueNeverAPaneSwitch pins spec §7's one real collision. ←/→
// cycle enum values — that is today's grammar and what the hint line advertises — so
// they cannot double as "switch pane". Tab does that instead.
func TestArrowsAreAlwaysTheValueNeverAPaneSwitch(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "notifications")
	require.Equal(t, focusRows, o.focus)

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications", changed, "→ must cycle the value")
	assert.Equal(t, focusRows, o.focus, "→ must not move focus")

	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "notifications", changed, "← must cycle the value")
	assert.Equal(t, focusRows, o.focus, "← must not move focus")
}

// TestTabSwitchesPanes pins the key that does move focus, in both directions.
func TestTabSwitchesPanes(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, focusRail, o.focus)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, focusRows, o.focus)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, focusRail, o.focus)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyShiftTab})
	assert.Equal(t, focusRail, o.focus, "shift+tab switches panes too")
}

// TestRightFocusesTheRowsPaneFromTheRail pins the rail's forward keys. On a handoff entry
// they are no-ops: there are no rows to focus, and PR C is what wires Enter to the
// accounts overlay.
func TestRightFocusesTheRowsPaneFromTheRail(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(key)
		assert.Equalf(t, focusRows, o.focus, "%v must focus the rows pane", key)
	}

	o := NewSettingsOverlay(config.DefaultConfig())
	o.railCursor = len(railEntries()) - 1 // Accounts, a handoff
	require.Equal(t, railHandoff, o.selectedEntry().kind)
	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, focusRail, o.focus, "a handoff entry has no rows to focus")
	assert.False(t, closed)
	assert.Empty(t, changed)
}

// TestEscIsLayered pins spec §7's layered Esc: from the rows pane it backs out to the
// rail, and only a second Esc closes. The hint line says "esc back" in the rows pane and
// "esc close" on the rail, so the extra level is advertised rather than surprising
// (spec §15).
func TestEscIsLayered(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme")
	require.Equal(t, focusRows, o.focus)

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc backs out of the rows pane")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the second esc closes the panel")
}

// TestSelectRowFocusesTheRowsPaneAndSyncsTheRail is spec §13's guard 11 in the form PR B
// can test it: the deep-link primitive lands the cursor on the row with the rows pane
// focused and the rail showing that row's category. Selecting a row the pane is not
// showing would leave the cursor invisible.
//
// PR C promotes this exact behavior to OpenAt(category, key) and adds the two real call
// sites (the session-cap dialog and the manual-reorder notice); the behavior is proven
// here so that promotion is a rename rather than new semantics.
func TestSelectRowFocusesTheRowsPaneAndSyncsTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.Truef(t, o.SelectRow(r.key), "no row %q", r.key)
		assert.Equalf(t, r.key, o.selectedRow().key,
			"SelectRow(%q) must land the cursor on that row", r.key)
		assert.Equalf(t, focusRows, o.focus, "SelectRow(%q) must focus the rows pane", r.key)
		assert.Equalf(t, r.category, o.selectedEntry().category,
			"SelectRow(%q) must sync the rail to its category", r.key)
		start, end := o.rowRange(o.selectedEntry())
		assert.GreaterOrEqualf(t, o.cursor, start, "SelectRow(%q) left the cursor outside the pane", r.key)
		assert.Lessf(t, o.cursor, end, "SelectRow(%q) left the cursor outside the pane", r.key)
	}
	assert.False(t, o.SelectRow("not_a_row"), "an unknown key reports not-found")
}

// rowValues snapshots every row's displayed value, so a "nothing changed" assertion is about
// what the panel shows rather than about struct equality — a Config holds slices and
// pointers, and a deep compare of it answers a different question.
func rowValues(o *SettingsOverlay) []string {
	out := make([]string, len(o.rows))
	for i, r := range o.rows {
		out[i] = r.get(o.cfg)
	}
	return out
}

// TestResetRestoresTheDefaultAndReportsTheKey is spec §13's guard 8. r must behave exactly
// like an edit: restore the built-in default AND report the changed key, so home persists the
// config and runs that field's live-apply hook. A reset that changed the config without
// reporting would leave disk and screen disagreeing until the next unrelated edit.
func TestResetRestoresTheDefaultAndReportsTheKey(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "theme")
	require.False(t, o.isModified(o.cursor), "precondition: a fresh config starts unmodified")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // cycle off the default
	require.Equal(t, "theme", changed)
	require.True(t, o.isModified(o.cursor), "precondition: the row is modified before reset")

	_, changed = o.HandleKeyPress(keyRunes("r"))
	assert.Equal(t, "theme", changed, "r reports the key so home persists and live-applies")
	assert.False(t, o.isModified(o.cursor), "r restored the default")
}

// TestResetIsSilentOnAnUnmodifiedRow pins that the reported key means "this value just
// changed". Reporting unconditionally would rewrite config.json and re-run the live-apply
// hook — for theme, a full ClearScreen repaint — on every press of a key that did nothing.
func TestResetIsSilentOnAnUnmodifiedRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme")
	require.False(t, o.isModified(o.cursor))

	_, changed := o.HandleKeyPress(keyRunes("r"))
	assert.Empty(t, changed, "a reset that changed nothing reports nothing")
}

// The two schema-level guards r rests on are PR A's and already pass unchanged:
// TestResetRestoresTheDefault (every reset produces the advertised default and clears the
// modified marker) and TestResetIsPresentWhereverADefaultIs (defaultDisplay and reset travel
// together; a read-only row has neither), both in settings_schema_test.go. The tests here are
// only about the key that reaches them.

// TestResetOnARowWithNoFixedDefaultIsASilentNoOp covers the two rows spec §5 makes nil by
// design — default_program (the first *detected* agent) and branch_prefix (the OS username).
// They have nowhere to go back to, so r must not pretend otherwise.
func TestResetOnARowWithNoFixedDefaultIsASilentNoOp(t *testing.T) {
	for _, key := range []string{"default_program", "branch_prefix"} {
		cfg := config.DefaultConfig()
		o := NewSettingsOverlay(cfg)
		settingsAt(t, o, key)
		require.Nil(t, o.rows[o.cursor].reset, "precondition: %q declares no reset", key)

		before := rowValues(o)
		_, changed := o.HandleKeyPress(keyRunes("r"))
		assert.Emptyf(t, changed, "r on %q must report nothing", key)
		assert.Equalf(t, before, rowValues(o), "r on %q must change nothing", key)
	}
}

// TestResetOnTheRailIsASilentNoOp is spec §2's non-goal, made structural: there is no
// category reset. Pressing r with the rail focused must not clear a category's worth of
// settings, and must not say it did.
func TestResetOnTheRailIsASilentNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Theme = "gruvbox" // any non-default value a category reset would have destroyed
	o := NewSettingsOverlay(cfg)
	o.SetRailIndex(railIndexForCategory(catAppearance))
	require.Equal(t, focusRail, o.focus)

	before := rowValues(o)
	closed, changed := o.HandleKeyPress(keyRunes("r"))
	assert.False(t, closed)
	assert.Empty(t, changed)
	assert.Equal(t, before, rowValues(o), "r on the rail must not touch a single row")
}

// TestResetOnTheReadOnlyRowIsASilentNoOp: the resolved config.json path has no setter and no
// default, and every edit key is a no-op on it (spec §5's kindReadOnly).
func TestResetOnTheReadOnlyRowIsASilentNoOp(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "config_file")
	require.Equal(t, kindReadOnly, o.rows[o.cursor].kind)

	_, changed := o.HandleKeyPress(keyRunes("r"))
	assert.Empty(t, changed)
}
