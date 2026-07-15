package ui

import (
	"sort"

	"github.com/ZviBaratz/atrium/session"
)

// Within-group sort mode (creation vs status) and the manual-reorder ordering
// it toggles: applying the sort, persisting the manual order, and the slice
// helpers that keep same-repo sessions grouped.

// groupModeAccount is the account-clustering group mode (config.GroupModeAccount),
// compared as a bare literal so ui needs no config import — mirroring how sortMode
// uses "status"/"creation".
const groupModeAccount = "account"

// sortActive reports whether a non-creation within-group sort mode is in effect.
func (l *List) sortActive() bool {
	return l.sortMode != "" && l.sortMode != "creation"
}

// accountGrouped reports whether the top-level grouping clusters by Claude account.
func (l *List) accountGrouped() bool {
	return l.groupMode == groupModeAccount
}

// viewActive reports whether any display transform (within-group sort or account
// clustering) is deriving items from the manual snapshot. It generalizes the former
// sortActive() gate: manual is populated and items is a computed view whenever this
// is true.
func (l *List) viewActive() bool {
	return l.sortActive() || l.accountGrouped()
}

// ManualReorderEnabled reports whether J/K within-group reordering applies. Only a
// within-group status sort computes that order, so J/K is disabled there (the app
// surfaces a hint). Account clustering only reorders whole repo blocks and never
// touches within-block order, so it leaves J/K available: a swap is mirrored into
// the manual snapshot and the view rebuilt (see MoveUp/MoveDown).
func (l *List) ManualReorderEnabled() bool {
	return !l.sortActive()
}

// InstancesForPersist returns the instances in canonical (manual) order so a view
// transform never overwrites the user's manual order on disk. With no view active
// the canonical order is simply items.
func (l *List) InstancesForPersist() []*session.Instance {
	if l.viewActive() {
		return l.manual
	}
	return l.items
}

// enterView snapshots the current items into manual the first time any view
// transform becomes active. Idempotent: a second transform activating while one is
// already active must not re-snapshot (manual already holds the canonical order).
func (l *List) enterView() {
	if l.manual == nil {
		l.manual = make([]*session.Instance, len(l.items))
		copy(l.manual, l.items)
	}
}

// exitViewInactive restores items from the manual snapshot and drops it once no view
// transform remains active. The selection is preserved by identity.
func (l *List) exitViewInactive() {
	if l.viewActive() || l.manual == nil {
		return
	}
	sel := l.GetSelectedInstance()
	l.items = l.manual
	l.manual = nil
	if sel != nil {
		l.SelectInstance(sel)
	}
}

// SetSortMode switches the within-group ordering. "" / "creation" restores the
// manual order; any other value sorts each repo group by action-priority. The
// selected session is preserved by identity.
func (l *List) SetSortMode(mode string) {
	if mode == "" {
		mode = "creation"
	}
	if mode == l.sortMode {
		return
	}
	l.sortMode = mode
	if l.viewActive() {
		l.enterView()
		l.rebuildView()
	} else {
		l.exitViewInactive()
	}
}

// SetGroupMode switches the top-level grouping. "" / "repo" restores repo groups in
// manual order; "account" clusters repo blocks by Claude account. The selected
// session is preserved by identity.
func (l *List) SetGroupMode(mode string) {
	if mode == "" {
		mode = "repo"
	}
	if mode == l.groupMode {
		return
	}
	l.groupMode = mode
	if l.viewActive() {
		l.enterView()
		l.rebuildView()
	} else {
		l.exitViewInactive()
	}
}

// ApplySort re-derives the status-sensitive view from the latest statuses; the
// metadata poll calls it each tick. Only the within-group status sort shifts as
// statuses update, so this is a no-op unless a sort mode is active: account
// clustering keys on each session's account and repo, both fixed at creation and
// changed only via Add/Kill (which rebuild the view directly), so re-clustering on
// every tick would allocate and recompute a result that never moved. Returns whether
// the order changed.
func (l *List) ApplySort() bool {
	if !l.sortActive() {
		return false
	}
	return l.rebuildView()
}

// rebuildView rebuilds items as the canonical manual order transformed by the active
// view(s): first clustered by account (if account-grouped), then sorted within each
// repo group (if a sort mode is active). It is the single writer of items while a
// view is active; the selected instance is preserved by identity. Returns whether
// items changed.
func (l *List) rebuildView() bool {
	if !l.viewActive() || l.manual == nil {
		return false
	}
	sel := l.GetSelectedInstance()
	next := make([]*session.Instance, len(l.manual))
	copy(next, l.manual)
	if l.accountGrouped() {
		next = clusterByAccount(next, l.accountOrder)
	}
	if l.sortActive() {
		sortWithinRepoGroups(next)
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

// forEachRepoBlock calls fn once per maximal contiguous run of items sharing a
// repoKey — the repo-block primitive the manual invariant guarantees. It is the one
// definition of "walk the repo blocks left to right"; sortWithinRepoGroups and
// clusterByAccount both build on it so the block-detection logic lives in a single
// place. (groupBounds walks outward from a known index, and String's render loop
// interleaves rendering, so those keep their own traversal.)
func forEachRepoBlock(items []*session.Instance, fn func(start, end int)) {
	for start := 0; start < len(items); {
		key := repoKey(items[start])
		end := start + 1
		for end < len(items) && repoKey(items[end]) == key {
			end++
		}
		fn(start, end)
		start = end
	}
}

// sortWithinRepoGroups stable-sorts each contiguous repo block of items by action-
// priority (NeedsInput first, then unread Ready, …), leaving block order and
// membership untouched. Extracted from the former applySort.
func sortWithinRepoGroups(items []*session.Instance) {
	forEachRepoBlock(items, func(start, end int) {
		grp := items[start:end]
		sort.SliceStable(grp, func(i, j int) bool {
			return session.StatusUrgency(grp[i].GetStatus(), grp[i].Unread()) <
				session.StatusUrgency(grp[j].GetStatus(), grp[j].Unread())
		})
	})
}

// clusterByAccount reorders whole repo blocks so blocks sharing a Claude account are
// contiguous, without disturbing any block's internal order. Input must be repo-
// contiguous (the manual invariant). Repo blocks within an account keep their
// first-appearance order. Pure function — the view deriver, not a state mutator.
//
// Cluster order is the user's chosen order (see List.accountOrder) intersected with the
// accounts actually present, followed by any unlisted account in first-appearance order,
// with the no-account ("") bucket trailing last unless it is itself listed. A nil/empty
// order therefore reproduces the original first-appearance rule exactly — which is what
// makes a state file predating the order behave as it always did.
func clusterByAccount(items []*session.Instance, order []string) []*session.Instance {
	type block struct {
		items []*session.Instance
		acct  string
	}
	var blocks []block
	forEachRepoBlock(items, func(start, end int) {
		blocks = append(blocks, block{items: items[start:end], acct: accountKey(items[start])})
	})
	present := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		present[b.acct] = true
	}
	seq := make([]string, 0, len(blocks))
	emitted := map[string]bool{}
	emit := func(acct string) {
		if !emitted[acct] {
			emitted[acct] = true
			seq = append(seq, acct)
		}
	}
	for _, acct := range order { // the chosen order leads, in its own sequence
		if present[acct] {
			emit(acct)
		}
	}
	for _, b := range blocks { // then anything unlisted, by first appearance
		if b.acct != "" {
			emit(b.acct)
		}
	}
	if present[""] { // "" trails last unless the chosen order placed it
		emit("")
	}
	out := make([]*session.Instance, 0, len(items))
	for _, acct := range seq {
		for _, b := range blocks {
			if b.acct == acct {
				out = append(out, b.items...)
			}
		}
	}
	return out
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

// swapInManual swaps the positions of instances a and b in the manual snapshot,
// mirroring a within-block J/K swap made on the displayed items into the canonical
// order. A no-op when no snapshot is held (creation/repo mode, where items is
// canonical) or when either instance is absent. The caller rebuilds the view so the
// two orders stay consistent — a swap that changes a block's anchor account (a mixed-
// account repo) re-clusters the block, which only rebuildView can reflect.
func (l *List) swapInManual(a, b *session.Instance) {
	if l.manual == nil {
		return
	}
	ia, ib := -1, -1
	for i, it := range l.manual {
		switch it {
		case a:
			ia = i
		case b:
			ib = i
		}
	}
	if ia >= 0 && ib >= 0 {
		l.manual[ia], l.manual[ib] = l.manual[ib], l.manual[ia]
	}
}

// blockRange returns the [start, end) range of the contiguous repo block whose
// repoKey is key, or (-1, -1) if no instance carries it. Repo blocks are contiguous
// in both manual and items (the repo-contiguous invariant), so each key maps to at
// most one range. Built on forEachRepoBlock so block detection keeps a single
// definition (the start < 0 guard just keeps the first — and only — matching block).
func blockRange(items []*session.Instance, key string) (start, end int) {
	start, end = -1, -1
	forEachRepoBlock(items, func(s, e int) {
		if start < 0 && repoKey(items[s]) == key {
			start, end = s, e
		}
	})
	return start, end
}

// transposeBlocksInManual swaps the positions of two whole repo blocks (identified
// by their repoKeys) in place, leaving every other block — including any blocks
// interleaved between the two — untouched, and preserving each block's internal
// order. It reflects a whole-group move made on the displayed items back into the
// canonical order after either a status sort or account clustering.
//
// In-place transposition (as opposed to removing a block and reinserting it beside
// the pivot) is what keeps account clustering stable: clusterByAccount falls back to
// first-appearance in manual for any account the chosen order does not list, so
// vacating a block's slot could hand an earlier index to a different unlisted account
// and yank an unrelated cluster. Transposing keeps the leading slot's account fixed
// and every interior block interior, so no account's first-appearance order changes —
// only the two blocks' relative order within their own cluster flips. For a status
// sort the two blocks are already adjacent in manual, so this degenerates to a single
// adjacent swap.
func transposeBlocksInManual(items []*session.Instance, keyA, keyB string) []*session.Instance {
	a0, a1 := blockRange(items, keyA)
	b0, b1 := blockRange(items, keyB)
	if a0 < 0 || b0 < 0 {
		return items
	}
	if a0 > b0 { // order so the lo block precedes the hi block
		a0, a1, b0, b1 = b0, b1, a0, a1
	}
	out := make([]*session.Instance, 0, len(items))
	out = append(out, items[:a0]...)   // before the lo block
	out = append(out, items[b0:b1]...) // hi block takes the lo slot
	out = append(out, items[a1:b0]...) // interleaved blocks stay put
	out = append(out, items[a0:a1]...) // lo block takes the hi slot
	out = append(out, items[b1:]...)   // after the hi block
	return out
}
