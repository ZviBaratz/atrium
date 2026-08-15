package ui

import (
	"sort"

	"github.com/ZviBaratz/atrium/session"
)

// Repo-group fold state: collapse/expand a group, collapse-all toggling, and
// the persisted set of collapsed repo keys.

// Collapse folds the selected session's repo group, snapping the selection to the group
// anchor. It is a no-op (returns false) when the group is already folded — so the caller can
// skip the persistence write — or when fewer than two repos are present, since folding is
// meaningless there.
func (l *List) Collapse() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	key := repoKey(l.items[l.selectedIdx])
	if l.collapsed[key] {
		return false
	}
	l.collapsed[key] = true
	l.clampSelectionToNavigable()
	return true
}

// Expand unfolds the selected (folded) repo group, leaving the selection on the anchor.
// It is a no-op (returns false) when the group is already expanded or with fewer than two
// repos, mirroring Collapse.
func (l *List) Expand() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	key := repoKey(l.items[l.selectedIdx])
	if !l.collapsed[key] {
		return false
	}
	delete(l.collapsed, key)
	l.clampSelectionToNavigable()
	return true
}

// ToggleCollapseAll folds every group if any is currently expanded, otherwise unfolds every
// group. No-op (returns false) with fewer than two repos.
func (l *List) ToggleCollapseAll() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	anyExpanded := false
	for i := 0; i < len(l.items); {
		_, end := l.groupBounds(i)
		if !l.collapsed[repoKey(l.items[i])] {
			anyExpanded = true
		}
		i = end
	}
	if anyExpanded {
		for i := 0; i < len(l.items); {
			_, end := l.groupBounds(i)
			l.collapsed[repoKey(l.items[i])] = true
			i = end
		}
	} else {
		l.collapsed = map[string]bool{}
	}
	l.clampSelectionToNavigable()
	return true
}

// HasMultipleGroups reports whether the list has more than one repo group — the
// condition under which Collapse/Expand/ToggleCollapseAll can meaningfully act.
// Used by callers to distinguish "already in that state" from "nothing to fold".
func (l *List) HasMultipleGroups() bool {
	return l.distinctRepoCount() > 1
}

// AddInstanceKeepingFolds adds instance without unfolding its repo group, for a
// creation nobody is watching (`atrium new`, #703). AddInstance's unconditional
// unfold is right for a keypress — a session you just made must not land hidden —
// and wrong for a background create, which reorganises nothing a human arranged.
//
// It lives here, as the exact inverse of the one `delete(l.collapsed, key)` it
// undoes, rather than in the caller as a CollapsedRepos/SetCollapsedRepos round
// trip: that round trip is lossy, because CollapsedRepos prunes to keys present in
// the list and would drop a fold held for a repo whose sessions are all gone.
func (l *List) AddInstanceKeepingFolds(instance *session.Instance) (finalize func()) {
	key := repoKey(instance)
	wasCollapsed := l.collapsed[key]
	finalize = l.AddInstance(instance)
	if wasCollapsed {
		l.collapsed[key] = true
		// The new row is hidden again, and AddInstance may have shifted the
		// selection index past it; re-establish the navigable-selection invariant
		// through the one function that owns it.
		l.clampSelectionToNavigable()
	}
	return finalize
}

// CollapsedRepos returns the collapsed repo keys still present in the list, sorted for stable
// output. Pruning to live keys happens here (at save time) only — never on load, where the
// instance set is still being assembled.
func (l *List) CollapsedRepos() []string {
	present := map[string]struct{}{}
	for _, item := range l.items {
		present[repoKey(item)] = struct{}{}
	}
	keys := make([]string, 0, len(l.collapsed))
	for k := range l.collapsed {
		if _, ok := present[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// SetCollapsedRepos replaces the collapsed set (used to restore persisted state on startup).
func (l *List) SetCollapsedRepos(keys []string) {
	l.collapsed = make(map[string]bool, len(keys))
	for _, k := range keys {
		l.collapsed[k] = true
	}
	l.clampSelectionToNavigable()
}
