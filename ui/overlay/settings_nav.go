package overlay

import (
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/internal/fuzzy"
)

// SettingsHandoff names a surface the settings panel has asked the home model to open in its
// place. The panel cannot open a sibling overlay itself, so a rail entry that owns no rows
// records a request and closes; home reads it as it tears the panel down.
type SettingsHandoff int

const (
	// HandoffNone means the panel closed on its own terms.
	HandoffNone SettingsHandoff = iota
	// HandoffAccounts asks home to open the Claude/GitHub/Antigravity account manager — the
	// same overlay the '@' key opens from the session list.
	HandoffAccounts
)

// railKind distinguishes the four things a settings rail entry can be. Ten of the
// thirteen entries project a settingCategory; the other three do not, which is why the
// rail is its own vocabulary rather than allCategories() alone.
//
// "Owns no rows" stopped being one fact when railProfiles arrived: it owns a focusable
// pane over cfg.Profiles, while railHandoff owns no pane either. That split is stated by
// TestRailHintNeverPromisesAPaneSwapWithoutRows.
type railKind int

const (
	// railAll shows every row grouped under category headers — the shape of the
	// pre-redesign list, preserved for auditing and muscle memory (spec §4). It is the
	// one entry whose pane scrolls far, and the only one that is a *view* rather than an
	// assignment: each row still belongs to exactly one real category.
	railAll railKind = iota
	// railCategory shows one category's rows.
	railCategory
	// railProfiles is the Profiles record editor (spec §9). It owns a focusable pane exactly as
	// a category does, but that pane is over cfg.Profiles rather than over settingRows — so
	// rowRange reports zero rows for it, newSettingRows never sees it, and
	// TestEveryScalarConfigFieldHasARow's `profiles` exemption stays true. PR D.
	railProfiles
	// railHandoff owns no rows AND no pane: that config lives on another surface, and the
	// forward key opens it. PR B renders these dimmed with the handoff glyph and their note;
	// after PR D, Accounts is the only one.
	railHandoff
)

// railEntry is one line of the left rail.
type railEntry struct {
	label string
	kind  railKind
	// category is the rows this entry shows; meaningful only when kind == railCategory.
	category settingCategory
	// note is the single line a handoff entry's pane shows, naming the surface that owns
	// its config. Empty for every other kind (TestEveryHandoffEntryNamesItsSurface).
	note string
	// opens is the surface this entry hands off to. Meaningful only when kind ==
	// railHandoff, and every one of those has a surface: an entry with no rows, no pane and
	// nothing to open would have no reason to exist, which is what
	// TestEveryHandoffEntryNamesItsSurface asserts. Every other kind leaves it HandoffNone.
	opens SettingsHandoff
}

// railEntries returns the rail in display order: the flat view, the ten scalar
// categories, then the Profiles editor and the Accounts handoff. Thirteen entries fit the
// 80x24 pane budget exactly (spec §4's invariant, pinned by
// TestRailFitsUnscrolledAtTheFloor) — a fourteenth has to displace another rather than
// start the rail scrolling.
func railEntries() []railEntry {
	entries := make([]railEntry, 0, len(allCategories())+3)
	entries = append(entries, railEntry{label: "All settings", kind: railAll})
	for _, c := range allCategories() {
		entries = append(entries, railEntry{label: c.label(), kind: railCategory, category: c})
	}
	return append(entries,
		railEntry{label: "Profiles", kind: railProfiles},
		railEntry{
			label: "Accounts", kind: railHandoff, opens: HandoffAccounts,
			note: "Claude, GitHub and Antigravity accounts — press ↵ to open the accounts overlay.",
		},
	)
}

// railDefaultIndex is the entry the panel opens on: the first real category. Spec §4 is
// explicit that All settings is not the landing — it is the audit view, not the default
// way to browse. Derived rather than hardcoded so reordering the rail cannot land the
// panel on a handoff.
func railDefaultIndex() int {
	for i, e := range railEntries() {
		if e.kind == railCategory {
			return i
		}
	}
	return 0
}

// railIndexForCategory returns the rail index showing the given category, falling back
// to the All settings view when no entry claims it — which cannot happen while
// TestRailIndexForCategoryFindsEveryCategory holds.
func railIndexForCategory(c settingCategory) int {
	for i, e := range railEntries() {
		if e.kind == railCategory && e.category == c {
			return i
		}
	}
	return 0
}

// settingsFocus selects which pane consumes navigation keys. It is a closed pair rather
// than a bool so the switch statements read as what they are.
type settingsFocus int

const (
	focusRail settingsFocus = iota
	focusRows
)

// selectedEntry is the rail entry the cursor is on.
func (s *SettingsOverlay) selectedEntry() railEntry { return railEntries()[s.railCursor] }

// selectedRow is the row the rows pane has highlighted.
func (s *SettingsOverlay) selectedRow() settingRow { return s.rows[s.cursor] }

// rowRange returns the [start,end) slice of s.rows the given rail entry shows.
//
// Contiguity is safe to rely on: TestRowsAreGroupedByCategory pins that every category's
// rows form one unbroken block in allCategories() order, which is also what lets the rows
// pane bound up/down with two integers instead of a filtered slice.
func (s *SettingsOverlay) rowRange(e railEntry) (start, end int) {
	switch e.kind {
	case railAll:
		return 0, len(s.rows)
	case railCategory:
		start, end = -1, -1
		for i, r := range s.rows {
			if r.category != e.category {
				continue
			}
			if start < 0 {
				start = i
			}
			end = i + 1
		}
		if start < 0 {
			return 0, 0
		}
		return start, end
	}
	return 0, 0 // railProfiles and railHandoff own no settingRows
}

// syncCursorToRail pulls the row cursor into the current entry's range, so moving the
// rail leaves the rows pane with a valid selection and the help pane describing a row
// that is actually visible. A handoff entry owns no rows, so the cursor is left where it
// was rather than clamped to a meaningless index.
//
// Note what it does NOT do: entering All settings preserves the cursor, because its range
// spans every row. Moving from Appearance to All settings and back keeps your place.
func (s *SettingsOverlay) syncCursorToRail() {
	s.lastErr = ""
	s.resetProfileTransients()
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		return
	}
	if s.cursor < start || s.cursor >= end {
		s.cursor = start
	}
}

// handleRailKey routes a key while the rail has focus. Moving the cursor re-renders the
// right pane immediately — the rail live-previews, so there is no hidden state and no
// drill-in feeling on a wide terminal (spec 3).
func (s *SettingsOverlay) handleRailKey(msg tea.KeyPressMsg) (closed bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "up", "k":
		if s.railCursor > 0 {
			s.railCursor--
			s.syncCursorToRail()
		}
	case "down", "j":
		if s.railCursor < len(railEntries())-1 {
			s.railCursor++
			s.syncCursorToRail()
		}
	case "/":
		s.startSearch()
	case "right", "tab", "enter":
		if start, end := s.rowRange(s.selectedEntry()); end > start {
			s.focus = focusRows
			return false
		}
		if s.selectedEntry().kind == railProfiles {
			// The editor owns a pane the same way a category does; it just is not driven by
			// settingRows, so the rowRange gate above cannot see it. Focus moves in and the
			// panel stays open — unlike a handoff, which gives the screen to another overlay.
			s.focus = focusRows
			s.clampProfileCursor()
			return false
		}
		// An entry with no rows and no pane hands off to another surface. The panel closes so
		// the surface it names takes the screen; focus never moves into an empty pane.
		if opens := s.selectedEntry().opens; opens != HandoffNone {
			s.handoff = opens
			return true
		}
	}
	return false
}

// handleRowsKey routes a key while the rows pane has focus. Left/right are ALWAYS the
// value — spec 7's one real collision: they cycle enums today and the hint line says so,
// so they cannot double as a pane switch. Tab does that.
func (s *SettingsOverlay) handleRowsKey(msg tea.KeyPressMsg) (closed bool, changedKey string) {
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		// Defensive: focus can only reach focusRows on an entry that owns rows.
		s.focus = focusRail
		return false, ""
	}
	row := &s.rows[s.cursor]
	switch msg.String() {
	case "esc", "ctrl+c":
		// Layered: back to the rail first, close from there. Advertised as "esc back".
		s.focus = focusRail
		s.lastErr = ""
	case "tab", "shift+tab":
		s.focus = focusRail
	case "up", "k":
		if s.cursor > start {
			s.cursor--
			s.lastErr = ""
		}
	case "down", "j":
		if s.cursor < end-1 {
			s.cursor++
			s.lastErr = ""
		}
	case "pgup", "pgdown", "home", "end":
		// The rest of D3: reaching the last row of the old flat list took 36 keypresses,
		// and the rail only fixes the "jump to a section" half. Spec 7's table omits these,
		// but D3 names them explicitly, and the expanded-help view scrolls with the same
		// keys — a panel where PgDn works there and not in the list is just inconsistent.
		s.cursor = clamp(s.pagedCursor(msg.String(), start, end), start, end-1)
		s.lastErr = ""
	case "left":
		return false, s.cycleEnum(row, -1)
	case "right":
		return false, s.cycleEnum(row, +1)
	// "space", not " ": Bubble Tea v2 names the space bar where v1 reported a
	// literal space. Both are accepted so a caller synthesising the older spelling
	// still toggles.
	case "space", " ":
		if row.kind == kindBool {
			return false, s.toggleBool(row)
		}
	case "r":
		return false, s.resetRow(row)
	case "/":
		s.startSearch()
	case "?":
		s.helpOpen = true
		s.helpScroll = 0
	case "enter":
		switch row.kind {
		case kindBool:
			return false, s.toggleBool(row)
		case kindEnum:
			return false, s.cycleEnum(row, +1)
		case kindInt, kindText:
			s.startEdit(row)
		}
	}
	return false, ""
}

// searching reports whether the `/` filter is active. The picker's own focus flag is the
// single source of truth, so there is no second bool to fall out of step with it.
func (s *SettingsOverlay) searching() bool { return s.search.IsFocused() }

// startSearch opens the filter and moves focus to the results (spec §8: `/` works from either
// pane). It always starts from an empty query — a `/` that resumed the last search would
// surprise a user who pressed it to look for something else.
//
// The picker's cursor is seeded FROM the current row, not the other way round. An empty query
// matches every row at score 0 (fuzzy.Match returns true for ""), so syncing in the usual
// direction would snap the cursor to row 0 and the rail to Sessions the instant `/` was
// pressed — before a character was typed — and Esc would then "return" you to the top of the
// schema rather than where you were. Seeding keeps both cursors in step, so the position
// readout is honest on the first frame too.
func (s *SettingsOverlay) startSearch() {
	s.search = newPicker(false)
	s.search.Focus()
	s.focus = focusRows
	s.lastErr = ""
	for i, row := range s.searchResults() {
		if row == s.cursor {
			s.search.cursor = i
			break
		}
	}
	s.syncCursorToSearch()
}

// clearSearch drops the filter, leaving the cursor on whatever row was highlighted and the
// rail already synced to its category — because syncCursorToSearch kept it there throughout.
// That is what makes Esc land you on the row you found rather than back where the search
// started.
func (s *SettingsOverlay) clearSearch() {
	s.search = newPicker(false) // a fresh sync picker is already blurred and unfiltered
	s.lastErr = ""
}

// searchHaystack is the text one row is matched against: its label, its key, its summary and
// its category name, so a user who remembers any one of them finds the row (spec §8).
func (s *SettingsOverlay) searchHaystack(r settingRow) string {
	return r.label + " " + r.key + " " + r.summary + " " + r.category.label()
}

// searchResults returns the indices of the rows matching the current filter, best first.
//
// The matcher is internal/fuzzy — the one subsequence matcher in the tree (#373) — and this
// is its ranking helper for settings rows, exactly as rankCandidates is the pickers' for
// paths. The bonus is the same idea as that function's basename bonus: a hit on the label or
// the key outranks an equal-scoring hit buried in a summary, because those are what a user
// types. An empty filter matches everything at score 0, so the list stays in schema order.
func (s *SettingsOverlay) searchResults() []int {
	q := s.search.filter
	type scored struct{ idx, score int }
	matches := make([]scored, 0, len(s.rows))
	for i, r := range s.rows {
		ok, score := fuzzy.Match(q, s.searchHaystack(r))
		if !ok {
			continue
		}
		if ok, bonus := fuzzy.Match(q, r.label); ok {
			score += bonus
		}
		if ok, bonus := fuzzy.Match(q, r.key); ok {
			score += bonus
		}
		matches = append(matches, scored{i, score})
	}
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].score > matches[b].score })
	out := make([]int, len(matches))
	for i, m := range matches {
		out[i] = m.idx
	}
	return out
}

// syncCursorToSearch pulls the global row cursor — and with it the rail — onto the
// highlighted result.
//
// s.cursor stays the index into s.rows because isModified, inertReason, renderRowLine and
// expandedHelpContent all read it; the picker's cursor indexes the result list. Keeping the
// rail on the result's category is not decoration: the rail takes no keys while filtering, so
// its marker has to mean something else, and it is what lets clearSearch leave the user on
// the row they found.
//
// With no results the cursor is left where it was. It is still a valid row index — nothing
// renders it as selected, because the pane draws the no-match line instead.
func (s *SettingsOverlay) syncCursorToSearch() {
	results := s.searchResults()
	if len(results) == 0 {
		return
	}
	s.search.clampCursor(len(results))
	s.cursor = results[s.search.cursor]
	s.railCursor = railIndexForCategory(s.rows[s.cursor].category)
	s.lastErr = ""
}

// visibleRowIndices is the set of rows the rows pane is showing: the search results while a
// filter is active, else the current rail entry's own contiguous range. Every width and
// content decision that used to read rowRange directly goes through this, so the search
// inherits them instead of duplicating them.
func (s *SettingsOverlay) visibleRowIndices() []int {
	if s.searching() {
		return s.searchResults()
	}
	start, end := s.rowRange(s.selectedEntry())
	out := make([]int, 0, max(0, end-start))
	for i := start; i < end; i++ {
		out = append(out, i)
	}
	return out
}

// handleSearchKey routes a key while the `/` filter is active (spec §8).
//
// The division is: the filter owns every rune, space and backspace — j and k are letters in a
// search box, and r must not reset a row mid-query — while the value keys (enter, left,
// right) still edit the highlighted row exactly as they do unfiltered, and up/down still move
// the result cursor. Space is the one casualty of that split: it extends the filter rather
// than toggling a bool, and enter is the toggle while filtering.
//
// `?` is the single rune the filter does not get, because spec §8 also assigns it to the
// expanded help. TestNoRowContainsAQuestionMark pins the premise that makes the reservation
// free.
//
// Keys the shared Picker does not consume — it takes only up, down, backspace,
// space and printable text — fall through to nothing and are deliberately inert. One
// consequence worth knowing: bubbletea reports ctrl+h separately from backspace, so ctrl+h does
// not delete here even though the session-list filter treats it as backspace. That is the
// shared Picker's behavior across every picker in the tree, not this panel's, so it is left
// alone rather than special-cased in one place.
func (s *SettingsOverlay) handleSearchKey(msg tea.KeyPressMsg) (closed bool, changedKey string) {
	results := s.searchResults()
	switch msg.String() {
	case "esc", "ctrl+c":
		// Layer one of three: clear, back to the rail, close (spec §8).
		s.clearSearch()
		return false, ""
	case "tab", "shift+tab":
		// The rail is inert while a filter is active, so Tab cannot focus it with the filter
		// still applied — it is the two escs in one key.
		s.clearSearch()
		s.focus = focusRail
		return false, ""
	case "?":
		if len(results) > 0 {
			s.helpOpen, s.helpScroll = true, 0
		}
		return false, ""
	case "pgup", "pgdown", "home", "end":
		if len(results) > 0 {
			s.search.cursor = clamp(s.pagedSearchCursor(msg.String(), len(results)), 0, len(results)-1)
			s.syncCursorToSearch()
		}
		return false, ""
	}
	if len(results) > 0 {
		row := &s.rows[s.cursor]
		switch msg.String() {
		case "left":
			return false, s.cycleEnum(row, -1)
		case "right":
			return false, s.cycleEnum(row, +1)
		case "enter":
			switch row.kind {
			case kindBool:
				return false, s.toggleBool(row)
			case kindEnum:
				return false, s.cycleEnum(row, +1)
			case kindInt, kindText:
				s.startEdit(row)
			}
			return false, ""
		}
	}
	if consumed, filterChanged, cursorMoved := s.search.handleKey(msg, len(results)); consumed &&
		(filterChanged || cursorMoved) {
		s.syncCursorToSearch()
	}
	return false, ""
}

// pagedSearchCursor resolves a paging key to a result index, mirroring pagedCursor's rule for
// the unfiltered list so PgDn does not mean two different distances in one panel.
func (s *SettingsOverlay) pagedSearchCursor(key string, count int) int {
	page := max(1, s.paneHeight()-1) // overlap one row so context is never lost
	switch key {
	case "pgup":
		return s.search.cursor - page
	case "pgdown":
		return s.search.cursor + page
	case "home":
		return 0
	default: // "end"
		return count - 1
	}
}

// resetRow restores a row to its built-in default and reports its key, or "" when nothing
// changed — the same contract toggleBool and cycleEnum have, so home persists and
// live-applies a reset exactly like an edit (spec §13's guard 8).
//
// The before/after comparison is what makes the reported key mean "this value just changed".
// Reporting unconditionally would rewrite config.json and re-run the field's live-apply hook
// — for theme, a full ClearScreen repaint — every time r is pressed on a row already at its
// default.
//
// A nil reset is a silent no-op, not an error: kindReadOnly has nothing to set, and
// default_program and branch_prefix have no fixed default to return to (spec §5). There is
// deliberately no arm for r on the rail either: category reset is spec §2's non-goal, and the
// absence of the arm is the implementation (TestResetOnTheRailIsASilentNoOp).
func (s *SettingsOverlay) resetRow(row *settingRow) string {
	s.lastErr = ""
	if row.reset == nil {
		return ""
	}
	before := row.get(s.cfg)
	row.reset(s.cfg)
	if row.get(s.cfg) == before {
		return ""
	}
	return row.key
}

// pagedCursor resolves a paging key to a target row index within [start,end). It is a
// separate function only so the four keys read as one rule instead of four cases.
func (s *SettingsOverlay) pagedCursor(key string, start, end int) int {
	page := max(1, s.paneHeight()-1) // overlap one row so context is never lost
	switch key {
	case "pgup":
		return s.cursor - page
	case "pgdown":
		return s.cursor + page
	case "home":
		return start
	default: // "end"
		return end - 1
	}
}

// handleHelpKey routes a key while `?` is open: up/down and PgUp/PgDn scroll, esc or a second ?
// returns to whatever was focused before (spec §8).
//
// Unlike TextOverlay — where any unrecognized key dismisses — a stray keystroke here is
// ignored. The settings panel is a working surface with a rail position and a row cursor worth
// keeping; dismissing on an accidental key would lose both.
func (s *SettingsOverlay) handleHelpKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc", "ctrl+c", "?":
		s.helpOpen = false
		s.helpScroll = 0
	case "up", "k":
		s.helpScroll = max(0, s.helpScroll-1)
	case "down", "j":
		s.helpScroll = min(s.maxHelpScroll(), s.helpScroll+1)
	case "pgup":
		s.helpScroll = max(0, s.helpScroll-s.paneHeight())
	case "pgdown":
		s.helpScroll = min(s.maxHelpScroll(), s.helpScroll+s.paneHeight())
	}
}
