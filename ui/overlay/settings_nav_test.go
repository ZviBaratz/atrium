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

	// The last two entries own no settingRows, but for different reasons: Profiles has a pane
	// of its own (PR D's editor) and Accounts hands off to the @ overlay.
	assert.Equal(t, "Profiles", entries[11].label)
	assert.Equal(t, railProfiles, entries[11].kind)
	assert.Empty(t, entries[11].note, "an entry with a pane of its own has no note to render")
	assert.Equal(t, HandoffNone, entries[11].opens, "the editor is not a handoff")
	assert.Equal(t, "Accounts", entries[12].label)
	assert.Equal(t, railHandoff, entries[12].kind)
}

// TestEveryHandoffEntryNamesItsSurface pins that a rail entry owning no rows and no pane
// still says where its config lives. An entry that renders an empty pane teaches the user
// nothing and reads as a bug, so the note is the whole content of that pane — and the
// forward key that note names must actually open something.
func TestEveryHandoffEntryNamesItsSurface(t *testing.T) {
	handoffs := 0
	for _, e := range railEntries() {
		if e.kind != railHandoff {
			assert.Emptyf(t, e.note, "only a handoff entry carries a note: %q", e.label)
			continue
		}
		handoffs++
		assert.NotEmptyf(t, e.note, "handoff entry %q must name the surface that owns it", e.label)
		assert.NotEqualf(t, HandoffNone, e.opens,
			"handoff entry %q opens nothing — an entry with no rows, no pane and no surface has "+
				"no reason to exist, and railHintLadder now assumes it cannot", e.label)
	}
	// Without this the loop could stop running and the test would still pass. Accounts is the
	// only handoff left: PR D replaced the Profiles note with an editor, and the assert.Emptyf
	// above is what stops that note being left behind on the new kind.
	require.Equal(t, 1, handoffs, "Accounts is the only handoff")
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
// its enum value, which is what OpenAt relies on to sync the rail
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

// TestRightFocusesTheRowsPaneFromTheRail pins the rail's three forward keys on an entry that
// owns rows.
func TestRightFocusesTheRowsPaneFromTheRail(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(key)
		assert.Equalf(t, focusRows, o.focus, "%v must focus the rows pane", key)
	}
}

// TestAccountsEntryHandsOffToTheAccountsOverlay is spec §4's handoff and §7's rail row: all
// three forward keys ask home to open the @ overlay, and the panel closes to make way. The
// overlay cannot open a sibling, so a request plus closed=true is the whole protocol.
func TestAccountsEntryHandsOffToTheAccountsOverlay(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetRailIndex(len(railEntries()) - 1)
		require.Equal(t, "Accounts", o.selectedEntry().label, "precondition: the last entry is Accounts")
		require.Equal(t, HandoffNone, o.Handoff(), "precondition: nothing requested yet")

		closed, changed := o.HandleKeyPress(key)
		assert.Truef(t, closed, "%v on Accounts closes the panel to make way", key)
		assert.Empty(t, changed, "a handoff changes no setting")
		assert.Equal(t, HandoffAccounts, o.Handoff())
		assert.Equal(t, focusRail, o.focus, "focus never moves into an entry with no rows")
	}
}

// TestRailHintNamesWhatTheForwardKeyDoes: the hint differs per entry because the forward key
// does three different things — focus the rows, open another overlay, or nothing at all.
// Advertising "→ rows" on an entry with no rows is the same class of lie as a static esc hint
// (spec §15).
func TestRailHintNamesWhatTheForwardKeyDoes(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)

	require.Equal(t, railCategory, o.selectedEntry().kind)
	assert.Contains(t, stripANSI(o.hintLine()), "→ rows")

	o.SetRailIndex(len(railEntries()) - 1)
	accounts := stripANSI(o.hintLine())
	assert.Contains(t, accounts, "↵ accounts")
	assert.NotContains(t, accounts, "→ rows", "Accounts has no rows to focus")

	o.SetRailIndex(profilesRailIndex())
	profiles := stripANSI(o.hintLine())
	assert.Contains(t, profiles, "→ profiles", "the forward key opens the editor, and says so")
	assert.NotContains(t, profiles, "→ rows", "Profiles owns no settingRows")
	assert.NotContains(t, profiles, "↵ accounts", "the editor is not a handoff")
	assert.Contains(t, profiles, "esc close")
}

// TestRailHintNeverPromisesAPaneSwapWithoutRows holds "⇥ pane" and "→ rows" to spec §15's
// standard, restated for PR D's fourth rail kind.
//
// Before the editor the two promises had ONE discriminator: railHandoff was exactly the no-rows
// case, so an entry with no rows also had no pane to swap into. railProfiles splits them. It
// owns a focusable pane — tab genuinely swaps into it, so "⇥ pane" is honest — while owning no
// settingRows, so "→ rows" is not. The invariant is therefore two facts, not one:
//
//   - "→ rows" requires settingRows: exactly railAll and railCategory have them.
//   - "⇥ pane" requires a pane the forward key can focus: everything except railHandoff.
func TestRailHintNeverPromisesAPaneSwapWithoutRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	withRows, panes, handoffs := 0, 0, 0
	for _, e := range railEntries() {
		start, end := o.rowRange(e)
		owns := end > start
		require.Equalf(t, e.kind == railAll || e.kind == railCategory, owns,
			"entry %q: settingRows belong to railAll and railCategory alone", e.label)
		focusable := e.kind != railHandoff
		if owns {
			withRows++
		}
		if focusable {
			panes++
		} else {
			handoffs++
		}
		for i, rung := range railHintLadder(e) {
			if !owns {
				assert.NotContainsf(t, rung, "→ rows",
					"entry %q rung %d promises rows it does not own: %q", e.label, i, rung)
			}
			if !focusable {
				assert.NotContainsf(t, rung, "⇥ pane",
					"entry %q rung %d promises a pane swap it cannot do: %q", e.label, i, rung)
			}
		}
	}
	// Without these the loop could stop covering a side and the test would still pass.
	require.Equal(t, 11, withRows, "All settings plus the ten categories own rows")
	require.Equal(t, 12, panes, "every entry but Accounts owns a focusable pane")
	require.Equal(t, 1, handoffs, "Accounts is the only handoff")
	// The positive half, on both kinds that can swap panes.
	assert.Contains(t, railHintLadder(railEntries()[railDefaultIndex()])[0], "⇥ pane")
	assert.Contains(t, railHintLadder(railEntries()[profilesRailIndex()])[0], "⇥ pane",
		"the editor's pane is focusable, so its widest rung says so")
}

// TestEveryWiredHandoffNamesItsForwardKey is the drift guard for PR D. handoffHint maps a
// handoff to its wording, and a handoff missing from it renders a ladder naming no forward key
// at all — the panel would offer Enter with nothing on screen saying so.
func TestEveryWiredHandoffNamesItsForwardKey(t *testing.T) {
	wired := 0
	for _, e := range railEntries() {
		if e.kind != railHandoff || e.opens == HandoffNone {
			continue
		}
		wired++
		hint := handoffHint(e.opens)
		require.NotEmptyf(t, hint, "handoff entry %q is wired but its forward key is unnamed", e.label)
		assert.Containsf(t, railHintLadder(e)[0], hint,
			"entry %q's widest rung must name its forward key", e.label)
	}
	require.Equal(t, 1, wired, "Accounts is the only handoff left")
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

// TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused is spec §13's guard 11, swept over the
// whole schema: the deep-link primitive must land the cursor on the row, focus the rows pane,
// and sync the rail to that row's category. Selecting a row the pane is not showing would
// leave the cursor invisible — the composite behavior IS the contract.
func TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.Truef(t, o.OpenAt(r.key), "no row %q", r.key)
		assert.Equalf(t, r.key, o.selectedRow().key,
			"OpenAt(%q) must land the cursor on that row", r.key)
		assert.Equalf(t, focusRows, o.focus, "OpenAt(%q) must focus the rows pane", r.key)
		assert.Equalf(t, r.category, o.selectedEntry().category,
			"OpenAt(%q) must sync the rail to its category", r.key)
		start, end := o.rowRange(o.selectedEntry())
		assert.GreaterOrEqualf(t, o.cursor, start, "OpenAt(%q) left the cursor outside the pane", r.key)
		assert.Lessf(t, o.cursor, end, "OpenAt(%q) left the cursor outside the pane", r.key)
	}
	assert.False(t, o.OpenAt("not_a_row"), "an unknown key reports not-found")
}

// TestOpenAtClearsTransientState pins the half a deep link only needs when the panel is
// already open: landing while an editor or the ? view is up would put the cursor somewhere
// the user cannot see. Today's call sites open a fresh panel — which is exactly why this
// belongs in OpenAt rather than at the call sites, where omitting it would stay invisible
// until the third one.
func TestOpenAtClearsTransientState(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "branch_prefix")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // opens the inline editor
	require.True(t, o.editing, "precondition: an editor is open")

	require.True(t, o.OpenAt("max_sessions"))
	assert.False(t, o.editing, "a deep link must not land inside another row's editor")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen, "precondition: the ? view is open")
	require.True(t, o.OpenAt("theme"))
	assert.False(t, o.helpOpen, "a deep link must not land behind the ? view")
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

// typeFilter sends each rune of s to the panel as its own key press, which is how a real
// filter is typed — sending them as one KeyRunes would hide a per-keystroke bug.
func typeFilter(o *SettingsOverlay, s string) {
	for _, r := range s {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// resultKeys is the search result list as row keys, which is what the assertions are about.
func resultKeys(o *SettingsOverlay) []string {
	out := make([]string, 0, len(o.searchResults()))
	for _, i := range o.searchResults() {
		out = append(out, o.rows[i].key)
	}
	return out
}

// TestSearchFindsARowByKeyByLabelAndBySummaryWord is spec §13's guard 9. Four query shapes,
// because the whole point of matching four fields is that a user who remembers any one of
// them finds the row.
func TestSearchFindsARowByKeyByLabelAndBySummaryWord(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"by key", "notify_command", "notify_command"},
		{"by label", "Glyph set", "glyph_set"},
		{"by a word from the summary", "taskbar", "os_chrome"},
		{"by category name", "Worktrees", "branch_prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, tc.query)
			assert.Containsf(t, resultKeys(o), tc.want, "%q must find %q", tc.query, tc.want)
		})
	}
}

// TestSearchRanksTheLabelAndKeyHitFirst pins the ranking bonus, on the one query shape that
// can tell the difference: without it, "agent" is a three-way tie at 60 that stable-sorts to
// default_program — which matches only through its summary ("Agent command new sessions
// launch") — ahead of the row actually called Agent OOM margin. With the label and key
// bonuses that row scores 180 and leads.
//
// "theme" is NOT the query for this: measured, the theme row wins 60-to-40 on the haystack
// alone, so the bonus changes nothing and a mutation deleting it would pass.
func TestSearchRanksTheLabelAndKeyHitFirst(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "agent")
	require.NotEmpty(t, resultKeys(o))
	assert.Equal(t, "agent_oom_margin", resultKeys(o)[0],
		"a label-and-key hit leads a search over a row that only matches in its summary")
}

// TestSearchFlattensAcrossCategories is spec §8's shape: results ignore the rail entry
// entirely. A filter that only searched the current category would be a category filter, not
// a search.
func TestSearchFlattensAcrossCategories(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, catSessions, o.selectedEntry().category, "precondition: the landing category")

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "in")
	seen := map[settingCategory]bool{}
	for _, i := range o.searchResults() {
		seen[o.rows[i].category] = true
	}
	// Named categories, not len(seen) > 1: asserting the latter after requiring it would be
	// true by construction. Measured, "in" returns 36 rows spanning all ten categories.
	assert.True(t, seen[catSessions], "the landing category")
	assert.True(t, seen[catAppearance], "a category the rail is not on")
	assert.True(t, seen[catAdvanced], "and the far end of the rail")
}

// TestSlashFocusesTheRowsPaneFromEitherPane is spec §8's first focus rule, stated because it
// is the detail that gets guessed wrong: / works from the rail as well as the rows.
func TestSlashFocusesTheRowsPaneFromEitherPane(t *testing.T) {
	for _, from := range []settingsFocus{focusRail, focusRows} {
		o := NewSettingsOverlay(config.DefaultConfig())
		if from == focusRows {
			o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
		}
		require.Equal(t, from, o.focus)

		o.HandleKeyPress(keyRunes("/"))
		assert.True(t, o.searching(), "/ opens the filter from either pane")
		assert.Equal(t, focusRows, o.focus, "/ moves focus to the results")
	}
}

// TestSlashDoesNotMoveTheCursorBeforeAnythingIsTyped. An empty query matches all 38 rows at
// score 0, so a naive sync snaps the cursor to row 0 and the rail to Sessions the moment `/`
// is pressed — and the Esc that "lands you on the row you found" then lands you on the top of
// the schema instead. Opened from the landing category the bug is invisible (row 0 is already
// the cursor), which is exactly why this opens from Advanced.
func TestSlashDoesNotMoveTheCursorBeforeAnythingIsTyped(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "agent_oom_margin")
	require.Equal(t, catAdvanced, o.selectedEntry().category, "precondition: away from the landing")

	o.HandleKeyPress(keyRunes("/"))
	assert.Equal(t, "agent_oom_margin", o.selectedRow().key, "/ must not move the cursor")
	assert.Equal(t, catAdvanced, o.selectedEntry().category, "nor the rail")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, "agent_oom_margin", o.selectedRow().key, "esc on an untyped filter is a no-op")
	assert.Equal(t, catAdvanced, o.selectedEntry().category)
}

// TestRunesTypeWhileTheFilterHasFocus is spec §8's second rule and the one most likely to be
// implemented backwards: j and k are letters in a search box, not navigation. r is here too —
// the reset key must not fire mid-query — and space extends the filter rather than toggling
// the highlighted bool.
func TestRunesTypeWhileTheFilterHasFocus(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Theme = "gruvbox" // any non-default value; r would clear it
	o := NewSettingsOverlay(cfg)
	o.HandleKeyPress(keyRunes("/"))
	before := rowValues(o)

	typeFilter(o, "jkr")
	assert.Equal(t, "jkr", o.search.filter, "j, k and r type; they do not navigate or reset")
	// Every row, not just theme: the cursor is wherever the filter left it, so asserting on
	// the theme row alone would hold even if r had reset whatever row IS highlighted.
	assert.Equal(t, before, rowValues(o), "r must not have reset anything")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "jkr ", o.search.filter, "space extends the filter")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "jkr", o.search.filter)
}

// TestArrowsMoveTheResultCursor is spec §8's third rule: ↑/↓ still navigate while the filter
// types. It also pins the coupling the rest of the panel depends on — s.cursor is the global
// row index and must track the picker's cursor, or the help pane describes one row while the
// list highlights another.
func TestArrowsMoveTheResultCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "in")
	results := o.searchResults()
	require.Greater(t, len(results), 2, "the query must return enough rows to move within")

	require.Equal(t, results[0], o.cursor, "the cursor starts on the best match")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, results[1], o.cursor)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, results[0], o.cursor)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, results[0], o.cursor, "up at the first result clamps")
}

// TestTheRailFollowsTheHighlightedResult: the rail cannot take keys while filtering, so its
// marker must mean something else — which category the current hit lives in. It is also what
// makes Esc's landing predictable, since clearing the filter leaves the rail already synced.
func TestTheRailFollowsTheHighlightedResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, catSessions, o.selectedEntry().category)

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "glyph")
	require.Equal(t, "glyph_set", o.selectedRow().key)
	assert.Equal(t, catAppearance, o.selectedEntry().category,
		"the rail marks the highlighted result's category")
}

// TestEditingAMatchedRowWorksAndKeepsItInTheResults is spec §8's fourth rule. The result set
// is derived from label/key/summary/category — never from the value — so an edit cannot make
// the row you are editing disappear from under you.
func TestEditingAMatchedRowWorksAndKeepsItInTheResults(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "notifications")
	require.Equal(t, "notifications", o.selectedRow().key)
	before := resultKeys(o)

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications", changed, "→ cycles the value, exactly as unfiltered")
	assert.Equal(t, before, resultKeys(o), "the row stays in the result list after an edit")
	assert.Equal(t, "notifications", o.selectedRow().key, "and stays highlighted")

	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "notifications", changed, "↵ cycles an enum, exactly as unfiltered")
}

// TestEnterOpensTheLineEditorFromASearchResult: an int/text row edits the same way from a
// filtered list, and the editor — not the filter — takes the keystrokes while it is open.
func TestEnterOpensTheLineEditorFromASearchResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "branch_prefix")
	require.Equal(t, "branch_prefix", o.selectedRow().key)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, o.editing, "↵ opens the inline editor")
	typeFilter(o, "zz")
	assert.Equal(t, "branch_prefix", o.search.filter,
		"an open editor swallows runes; the filter must not grow behind it")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, o.editing)
	assert.True(t, o.searching(), "cancelling the edit returns to the filtered list")
}

// TestEscIsThreeLayeredWithAFilter is spec §8's dismissal rule and §15's warning made
// concrete: clear, back, close. Each level is advertised by hintLine.
func TestEscIsThreeLayeredWithAFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.True(t, o.searching())

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc clears the filter")
	assert.False(t, o.searching())
	assert.Equal(t, focusRows, o.focus, "and keeps the rows pane focused")
	assert.Equal(t, "theme", o.selectedRow().key, "landing on the row the search found")
	assert.Equal(t, catAppearance, o.selectedEntry().category)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the second esc backs out to the rail")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the third esc closes the panel")
}

// TestQuestionMarkOpensHelpForTheHighlightedResult: ? is the one rune the filter does not
// get. Spec §8 assigns it to the expanded help while also saying runes type, and no row's
// label, key, summary or category contains a question mark — so reserving it costs the search
// nothing.
func TestQuestionMarkOpensHelpForTheHighlightedResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "clustering")
	require.Equal(t, "group_mode", o.selectedRow().key)

	o.HandleKeyPress(keyRunes("?"))
	assert.True(t, o.helpOpen, "? opens the expanded help")
	assert.Equal(t, "clustering", o.search.filter, "? did not land in the filter")
	assert.Contains(t, o.expandedHelpContent(o.cursor), "Account clustering")

	o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen)
	assert.True(t, o.searching(), "? returns to the filtered list it was opened from")
}

// TestNoRowContainsAQuestionMark is the premise the reservation above rests on, asserted
// rather than assumed — a future summary using one would silently make it unsearchable.
func TestNoRowContainsAQuestionMark(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range o.rows {
		assert.NotContainsf(t, o.searchHaystack(r), "?",
			"row %q would be unreachable: ? is reserved for the expanded help", r.key)
	}
}

// TestZeroMatchesIsStableAndRecoverable: a query matching nothing must not panic, must not
// move the cursor onto a row it cannot justify, and must be typed out of.
func TestZeroMatchesIsStableAndRecoverable(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.Equal(t, "theme", o.selectedRow().key)

	typeFilter(o, "zzzz")
	require.Empty(t, o.searchResults(), "precondition: nothing matches")
	assert.Equal(t, "theme", o.selectedRow().key, "the cursor holds its last valid row")

	for range 4 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	assert.Equal(t, "theme", o.selectedRow().key, "backspacing back to a match recovers")
}

// TestTabLeavesTheSearchForTheRail: the rail is inert while filtering, so Tab cannot focus it
// with the filter still applied. It clears and moves — the two escs in one key — rather than
// being a dead key that has an obvious meaning on a keyboard.
func TestTabLeavesTheSearchForTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.False(t, o.searching())
	assert.Equal(t, focusRail, o.focus)
	assert.Equal(t, catAppearance, o.selectedEntry().category, "on the category the search found")
}

// TestOpenAtClearsAnActiveFilter completes TestOpenAtClearsTransientState: a deep link into
// an open, filtered panel must show the row it names, not a filtered list that may exclude it.
func TestOpenAtClearsAnActiveFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.True(t, o.searching())

	require.True(t, o.OpenAt("max_sessions"))
	assert.False(t, o.searching(), "a deep link clears the filter that would hide its row")
	assert.Equal(t, "max_sessions", o.selectedRow().key)
	assert.Equal(t, catSessions, o.selectedEntry().category)
}
