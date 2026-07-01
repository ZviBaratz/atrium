package ui

import (
	"sort"

	"github.com/ZviBaratz/atrium/session"
)

// Within-group sort mode (creation vs status) and the manual-reorder ordering
// it toggles: applying the sort, persisting the manual order, and the slice
// helpers that keep same-repo sessions grouped.

// sortActive reports whether a non-creation sort mode is in effect — i.e. items is
// a sorted view of the canonical manual order. "" and "creation" are not active.
func (l *List) sortActive() bool {
	return l.sortMode != "" && l.sortMode != "creation"
}

// ManualReorderEnabled reports whether J/K within-group manual reordering applies.
// It is true only in creation mode; under a sort mode the order is computed, so the
// app turns J/K into a no-op hint instead.
func (l *List) ManualReorderEnabled() bool {
	return !l.sortActive()
}

// InstancesForPersist returns the instances in canonical (manual/creation) order so
// a sort mode never overwrites the user's manual order on disk. In creation mode the
// canonical order is simply items.
func (l *List) InstancesForPersist() []*session.Instance {
	if l.sortActive() {
		return l.manual
	}
	return l.items
}

// SetSortMode switches the within-group ordering. "" / "creation" restores the
// manual order captured when the sort mode was entered; any other value snapshots
// the current manual order once and sorts. The selected session is preserved by
// identity. Calling with the same mode re-applies it (cheap, no-op if unchanged).
func (l *List) SetSortMode(mode string) {
	if mode == "" {
		mode = "creation"
	}
	wasActive := l.sortActive()
	if mode == "creation" {
		if wasActive {
			sel := l.GetSelectedInstance()
			l.items = l.manual
			l.manual = nil
			l.sortMode = mode
			if sel != nil {
				l.SelectInstance(sel)
			}
			return
		}
		l.sortMode = mode
		return
	}
	if !wasActive {
		// Entering a sort mode from creation: snapshot the canonical order once.
		l.manual = make([]*session.Instance, len(l.items))
		copy(l.manual, l.items)
	}
	l.sortMode = mode
	l.applySort()
}

// ApplySort re-derives the sorted order from the latest statuses; the metadata poll
// calls it each tick. No-op in creation mode; reorders only when the computed order
// actually changed. Returns whether the order changed.
func (l *List) ApplySort() bool {
	return l.applySort()
}

// applySort rebuilds items as the canonical manual order sorted within each repo
// group by the active mode's priority, preserving the selected instance by identity.
// It is the single writer of items while a sort mode is active: group order and
// membership come from manual and are left intact (only within-group order changes).
// Returns whether items changed.
func (l *List) applySort() bool {
	if !l.sortActive() {
		return false
	}
	sel := l.GetSelectedInstance()
	next := make([]*session.Instance, len(l.manual))
	copy(next, l.manual)
	for start := 0; start < len(next); {
		key := repoKey(next[start])
		end := start + 1
		for end < len(next) && repoKey(next[end]) == key {
			end++
		}
		grp := next[start:end]
		sort.SliceStable(grp, func(i, j int) bool {
			return session.StatusUrgency(grp[i].GetStatus(), grp[i].Unread()) <
				session.StatusUrgency(grp[j].GetStatus(), grp[j].Unread())
		})
		start = end
	}
	if sameOrder(l.items, next) {
		return false
	}
	l.items = next
	if sel != nil {
		l.SelectInstance(sel)
	} else {
		l.clampSelectionToNavigable()
	}
	return true
}

// sameOrder reports whether a and b hold the same instances in the same positions.
func sameOrder(a, b []*session.Instance) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// insertByRepo inserts inst into items immediately after the last existing entry
// sharing its repoKey (appending a new group if none), mirroring AddInstance's
// placement. Keeps the canonical manual order in step while a sort mode is active.
func insertByRepo(items []*session.Instance, inst *session.Instance) []*session.Instance {
	key := repoKey(inst)
	insertAt := len(items)
	for i, item := range items {
		if repoKey(item) == key {
			insertAt = i + 1
		}
	}
	items = append(items, nil)
	copy(items[insertAt+1:], items[insertAt:])
	items[insertAt] = inst
	return items
}

// removeInstance drops the first occurrence of target from items, returning the
// shortened slice (unchanged if target is absent).
func removeInstance(items []*session.Instance, target *session.Instance) []*session.Instance {
	for i, it := range items {
		if it == target {
			return append(items[:i], items[i+1:]...)
		}
	}
	return items
}

// regroupManualLike reorders manual's repo-group blocks to match the group order in
// like (the just-reordered display list), preserving manual's within-group order.
// Used after a whole-group move so the canonical order tracks the new group sequence
// without disturbing the within-group (manual) order.
//
// like and manual hold the same instances (a whole-group move only permutes group
// order), so every manual group is normally covered by like. The trailing pass that
// appends any group key absent from like is a safety net: should the two ever diverge
// on membership, those sessions are kept (appended in manual order) rather than
// silently dropped from the canonical order and then persisted away.
func regroupManualLike(manual, like []*session.Instance) []*session.Instance {
	byKey := map[string][]*session.Instance{}
	keyOrder := make([]string, 0)
	for _, it := range manual {
		k := repoKey(it)
		if _, ok := byKey[k]; !ok {
			keyOrder = append(keyOrder, k)
		}
		byKey[k] = append(byKey[k], it)
	}
	out := make([]*session.Instance, 0, len(manual))
	seen := map[string]bool{}
	emit := func(k string) {
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, byKey[k]...)
	}
	for _, it := range like {
		emit(repoKey(it))
	}
	// Keep any manual group like never mentioned (see doc comment).
	for _, k := range keyOrder {
		emit(k)
	}
	return out
}
