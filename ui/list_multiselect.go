package ui

import "github.com/ZviBaratz/atrium/session"

// Multi-select batch scope: the in-view paused/active filters that batch
// actions operate over, and the per-instance mark set.

// PausedInstancesInView returns every Paused instance that passes the active
// filter (all paused when no filter is set), in list order. Collapsed groups
// are included — folding is a display state, not a scope boundary — so a batch
// "resume all" restores paused sessions the user can't currently see folded
// away, which is what they expect after a reboot parked everything.
func (l *List) PausedInstancesInView() []*session.Instance {
	var out []*session.Instance
	for _, it := range l.items {
		if it.GetStatus() == session.Paused && l.filterMatches(it) {
			out = append(out, it)
		}
	}
	return out
}

// ActiveInstancesInView returns every pausable instance that passes the active
// filter (all of them when no filter is set), in list order — the scope of a
// batch "pause all". An instance is pausable when it is:
//   - not already Paused (nothing to park),
//   - not Loading (its Start() is still building the worktree/tmux and the
//     Loading→Running transition is still pending on the main loop; pausing now
//     would race that setup, exactly why single-pause refuses a Loading session),
//   - not direct (a direct session has no worktree to free, so it cannot be parked).
//
// Like PausedInstancesInView, collapsed groups are included: folding is a display
// state, not a scope boundary, so a pre-restart "pause all" parks sessions the
// user has folded away too. A Loading session left unparked is no gap — the
// post-restart recovery loop is the safety net for it.
func (l *List) ActiveInstancesInView() []*session.Instance {
	var out []*session.Instance
	for _, it := range l.items {
		status := it.GetStatus()
		if status != session.Paused && status != session.Loading && !it.IsDirect() && l.filterMatches(it) {
			out = append(out, it)
		}
	}
	return out
}

// ToggleMark flips the multi-select mark on inst (no-op for nil), lazily
// allocating the set on first use.
func (l *List) ToggleMark(inst *session.Instance) {
	if inst == nil {
		return
	}
	if l.marked == nil {
		l.marked = map[*session.Instance]bool{}
	}
	if l.marked[inst] {
		delete(l.marked, inst)
	} else {
		l.marked[inst] = true
	}
}

// IsMarked reports whether inst is currently marked in multi-select mode.
func (l *List) IsMarked(inst *session.Instance) bool {
	return l.marked[inst]
}

// ClearMarks drops every multi-select mark (mode exit / post-action reset).
func (l *List) ClearMarks() {
	l.marked = nil
}

// MarkedCount returns the number of marked instances, counting only those still
// present in the list (a marked instance removed since is not counted).
func (l *List) MarkedCount() int {
	return len(l.MarkedInstancesInView())
}

// MarkedInstancesInView returns every marked instance that passes the active
// filter, in list order. Iterating items (not the marked map) keeps the order
// stable and drops any instance removed since it was marked — mirroring
// PausedInstancesInView / ActiveInstancesInView.
func (l *List) MarkedInstancesInView() []*session.Instance {
	if len(l.marked) == 0 {
		return nil
	}
	var out []*session.Instance
	for _, it := range l.items {
		if l.marked[it] && l.filterMatches(it) {
			out = append(out, it)
		}
	}
	return out
}
