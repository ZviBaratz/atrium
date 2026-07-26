package overlay

import (
	tea "github.com/charmbracelet/bubbletea"
)

// railKind distinguishes the three things a settings rail entry can be. Ten of the
// thirteen entries project a settingCategory; the other three own no rows of their own,
// which is why the rail is its own vocabulary rather than allCategories() alone.
type railKind int

const (
	// railAll shows every row grouped under category headers — the shape of the
	// pre-redesign list, preserved for auditing and muscle memory (spec §4). It is the
	// one entry whose pane scrolls far, and the only one that is a *view* rather than an
	// assignment: each row still belongs to exactly one real category.
	railAll railKind = iota
	// railCategory shows one category's rows.
	railCategory
	// railHandoff owns no rows: that config lives on another surface. PR B renders these
	// dimmed with the handoff glyph and their note; PR C wires Accounts to the @ overlay
	// and PR D builds the Profiles editor.
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
}

// railEntries returns the rail in display order: the flat view, the ten scalar
// categories, then the two handoffs. Thirteen entries fit the 80x24 pane budget exactly
// (spec §4's invariant, pinned by TestRailFitsUnscrolledAtTheFloor) — a fourteenth has
// to displace another rather than start the rail scrolling.
func railEntries() []railEntry {
	entries := make([]railEntry, 0, len(allCategories())+3)
	entries = append(entries, railEntry{label: "All settings", kind: railAll})
	for _, c := range allCategories() {
		entries = append(entries, railEntry{label: c.label(), kind: railCategory, category: c})
	}
	return append(entries,
		railEntry{
			label: "Profiles", kind: railHandoff,
			// Stated as a plain fact about where the data lives, not as a roadmap promise:
			// PR D replaces this entry with an editor, and a note saying "not yet" would be
			// the first thing to go stale.
			note: "Agent profiles are edited in config.json, under the profiles key.",
		},
		railEntry{
			label: "Accounts", kind: railHandoff,
			note: "Managed in the accounts overlay — press @ from the session list.",
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
	return 0, 0 // railHandoff owns no rows
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
func (s *SettingsOverlay) handleRailKey(msg tea.KeyMsg) (closed bool) {
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
	case "right", "tab", "enter":
		// A handoff entry owns no rows, so there is nothing to focus. PR C wires Enter on
		// Accounts to the @ overlay; until then this is deliberately a no-op rather than
		// focus on an empty pane.
		if start, end := s.rowRange(s.selectedEntry()); end > start {
			s.focus = focusRows
		}
	}
	return false
}

// handleRowsKey routes a key while the rows pane has focus. Left/right are ALWAYS the
// value — spec 7's one real collision: they cycle enums today and the hint line says so,
// so they cannot double as a pane switch. Tab does that.
func (s *SettingsOverlay) handleRowsKey(msg tea.KeyMsg) (closed bool, changedKey string) {
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
	case " ":
		if row.kind == kindBool {
			return false, s.toggleBool(row)
		}
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
