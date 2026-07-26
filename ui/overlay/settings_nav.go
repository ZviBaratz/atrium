package overlay

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
