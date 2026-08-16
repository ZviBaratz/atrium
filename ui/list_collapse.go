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
	// The row the human left the cursor on, captured before the add shifts indices.
	var selected *session.Instance
	if l.selectedIdx >= 0 && l.selectedIdx < len(l.items) {
		selected = l.items[l.selectedIdx]
	}

	finalize = l.AddInstance(instance)
	if wasCollapsed {
		l.collapsed[key] = true
	}

	// Keep that row visible, rather than hiding it here and letting the clamp move the
	// cursor off it.
	//
	// The case is not this row's own group. It is the *first* row of a new repo, which
	// takes distinctRepoCount from one to two and so makes every stale fold effective at
	// once (effectiveCollapsed) — including one a user set back when two groups last
	// existed, for a group this call never touched. Reachable: fold repo A while B
	// exists, kill all of B (RemoveInstance never prunes l.collapsed, and at one group
	// the fold is inert so A's rows stay visible), leave the cursor on a non-anchor row
	// of A, then `atrium new --path <repo C>`.
	//
	// Both alternatives are the same hazard in different clothes: a cursor resting on an
	// invisible row, or a cursor silently moved to a different *session.Instance that
	// instanceChanged then repoints preview, diff and menu at. Every destructive key
	// targets the selection, so either one means the next keypress can land somewhere the
	// user did not put it — a background create must not be able to arrange that (#439).
	// Dropping the one fold that would have hidden the selection is the smallest thing
	// that removes it: every other fold survives, and so does the cursor.
	//
	// Not while filtering, though. isHidden gates on the filter ahead of folds and
	// returns its answer there, so a selection hidden during a filter is hidden because
	// it does not MATCH — dropping the fold would not reveal it, and would destroy a
	// persisted fold the user set to buy nothing. That case belongs to the clamp below.
	if selected != nil && !l.Filtering() {
		if idx := l.indexOfInstance(selected); idx >= 0 && l.isHidden(idx) {
			delete(l.collapsed, repoKey(selected))
		}
	}
	// Backstop for what unfolding cannot reach: the filter just described, and a
	// selectedIdx left out of range.
	l.clampSelectionToNavigable()
	return finalize
}

// indexOfInstance returns target's index in the list, or -1 when it is not present.
// Identity, not (Title, Path): two rows can share those, and the caller is asking about
// one specific object it already holds.
func (l *List) indexOfInstance(target *session.Instance) int {
	for i, inst := range l.items {
		if inst == target {
			return i
		}
	}
	return -1
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
