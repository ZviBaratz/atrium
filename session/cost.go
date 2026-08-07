package session

// Per-session cost: what a session has spent, estimated offline from its
// transcripts at published list rates (#392). The cumulative sibling of
// usage.go's context reading, and it mirrors that file's shape deliberately —
// same trio, same threading contract, same ambiguity key — so the two can be
// read against each other.
//
// One reading, no launch-flag fallback, exactly as for occupancy: a session with
// no transcript has no cost, and the UI shows nothing rather than $0.00.

import "github.com/ZviBaratz/atrium/session/transcript"

// CostInfo returns the session's last known spend estimate. A zero USD means "no
// estimate": a non-claude program, a session that has not talked to the model
// yet, or one whose reading was refused. Callers render nothing for it.
func (i *Instance) CostInfo() transcript.Cost { return i.cost }

// SetCostMeta records a cost-extraction result and the cursor it should resume
// from. Main thread only (like SetUsageMeta).
//
// Unlike SetUsageMeta there is no same-file/different-file rule to apply, and
// the reason is worth stating because the asymmetry looks like an oversight. An
// occupancy reading is scoped to ONE transcript, so a new path means a new
// conversation whose occupancy the old number does not describe. A cost is
// scoped to the whole project DIRECTORY, so a new conversation inside it is
// simply more spend in the same total — there is nothing to invalidate. The
// reader has already done the only bookkeeping the change requires: it dropped
// the subtotals of files that vanished and kept those of files that did not.
//
// The cursor is stored unconditionally, including alongside a zero cost, so the
// next tick resumes rather than re-reading from byte zero.
func (i *Instance) SetCostMeta(c transcript.Cost, cursor transcript.CostCursor) {
	i.cost = c
	i.costCursor = cursor
}

// ClearCost drops the stored estimate and its cursor. Main thread only.
//
// Same contract as ClearUsage: the poll layer calls it for a session that must
// not hold an estimate at all — an ambiguous transcript source, or the chip
// switched away from cost — and clearing rather than merely declining to refresh
// is what stops a value that was never trustworthy from resurfacing the moment
// the condition that hid it goes away.
//
// The cursor goes with it because the cursor IS the estimate: it carries a
// per-file dollar subtotal, so a session that kept its cursor would still be
// holding the number it was told not to hold, one field over. That is the whole
// reason, and it is worth being precise about because the reason that suggests
// itself is a different, false one — "a stale cursor would resume from bytes
// whose subtotal was discarded and report a fraction of the truth". It would
// not: the subtotal travels in the cursor, so a retained cursor produces the
// correct total. Dropping it costs one re-read and buys the invariant, which is
// the trade being made here rather than a correctness fix.
func (i *Instance) ClearCost() {
	i.cost = transcript.Cost{}
	i.costCursor = transcript.CostCursor{}
}

// ComputeCost re-reads the session's transcripts off the main thread (the
// metadata-poll goroutine), resuming from the stored cursor so an idle session
// costs one ReadDir plus one Stat per transcript and opens nothing. ok=false
// means nothing to apply: unstarted/paused, a non-claude program, or an
// unreadable project directory.
//
// Like ComputeUsage it derives its lifecycle context from i.baseContext() rather
// than taking a ctx parameter, so app shutdown cancels an in-flight read.
//
// Three edges it does not close, all inherited from "the project directory is
// the session" and all left open deliberately:
//
//   - Renaming a session MOVES its worktree (git.Worktree.Rename), which changes
//     the sanitized path Claude Code derives its project directory from. Spend
//     under the old name is stranded and the total restarts. Following it would
//     mean remembering every path a session has ever had, and a rename is rare
//     enough that the restart is the cheaper surprise — but it IS a surprise, so
//     it is written down here rather than left to be rediscovered.
//   - Kill a session and create a new one whose title yields the same worktree
//     path, and the new session inherits the old one's spend. The nanosecond
//     suffix git.resolveWorktreePaths appends makes this almost unreachable for a
//     git-backed session; a direct session has no such suffix, which is part of
//     why usagePolicy refuses shared working directories outright.
//   - A read error leaves the last total standing rather than clearing it. Same
//     contract as ComputeUsage and ComputeModel: errors here are dominated by
//     "no transcript yet", where clearing is a no-op, and by transient mid-write
//     states, where clearing would flicker the row.
func (i *Instance) ComputeCost() (cost transcript.Cost, cursor transcript.CostCursor, ok bool) {
	if !i.isStarted() || i.Paused() {
		return transcript.Cost{}, transcript.CostCursor{}, false
	}
	c, cur, err := transcript.LatestCost(i.baseContext(), i.Program, i.WorkingDir(), i.costCursor,
		transcript.Options{Root: i.claudeConfigDir})
	if err != nil {
		return transcript.Cost{}, transcript.CostCursor{}, false
	}
	return c, cur, true
}
