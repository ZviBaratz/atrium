package session

import (
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// Status/unread tracking: the mu-guarded live-status accessors and the
// turn-end edge detection that maintains the unread bit. The Status enum and
// StatusUrgency sort policy live with the struct in instance.go.
//
// "Turn-end" is the edge, not the status. Entering Ready is the usual one and the only
// one the status alone identifies; setStatusTurnEnded exists because chip-held Pending
// (tmux.PaneBackground) is a real turn-end that Ready never sees — the agent stopped and
// wrote to the user, but work it launched is still running, so the row stays Pending.
// Everything downstream keys off the unread bit rather than off Ready, so routing that
// edge here is what keeps the finish/asked notification (#289/#571), the unread glyph,
// NextUnread and the queued-prompt question hold working for those sessions.

// GetStatus returns the instance status under the read lock.
func (i *Instance) GetStatus() Status {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.status
}

// SetStatus updates the instance status under the write lock, recording the change
// (see recordStatusChange). It also edge-detects transitions into Ready to maintain
// the unread bit: a non-Ready→Ready transition flags unread (the agent finished a
// turn) unless a one-shot suppression is armed (a synthetic lifecycle transition —
// see suppressNextUnread); any non-Ready write clears a pending suppression, since an
// observed working phase means the next completion is genuine.
//
// It edge-detects Paused the same way, to drop the cached pane frame. That belongs
// on the edge rather than in pause(): parking does its git I/O while the session is
// still un-paused, so the capture chain is still targeting it and a frame landing
// mid-pause would refill a cache cleared any earlier. Once the status is Paused, no
// further capture is armed (resolveFrameTarget skips paused sessions), so this is
// the point after which the drop sticks — and it covers RecoverLostSession's park
// as well as the user's.
func (i *Instance) SetStatus(status Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setStatusLocked(status, true, false)
}

// setStatusTurnEnded writes an observed status that IS a turn-end even though it is not
// Ready, raising the unread bit exactly as an into-Ready write does.
//
// Only ApplyPaneState's PaneBackground arm calls it, and only on the edge into a
// chip-held Pending. The caller owns that edge because neither the status nor its
// predecessor can carry it: both Pending producers write the same value (so
// "i.status != status" misses a set-driven Pending handing over to a chip-driven one as
// the sub-agents drain), and a session whose Monitor runs all session long sits Pending
// across the very turn boundaries it needs to announce. Instance.pendingSource is what
// resolves the edge; see ApplyPaneState.
//
// Without this the row would go quiet for as long as the background work runs — no ding,
// no unread glyph, skipped by NextUnread, and the #571 question hold silently inert
// because it releases on Unread(). That is strictly worse than the pre-Pending behaviour
// it replaced, which at least rang once per turn.
func (i *Instance) setStatusTurnEnded(status Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setStatusLocked(status, true, true)
}

// setStatusReattached writes the synthetic status reattach uses to mark a
// successfully-reattached session live. It keeps SetStatus's unread and pane-frame
// edges but deliberately skips recordStatusChange, because this write is our
// bookkeeping rather than anything the agent did: statusChangedAt has to survive it,
// or the stamp FromInstanceData just restored from state.json is destroyed before the
// first poll ever runs, and a session that has been waiting on the user for six hours
// reads as having changed at startup.
//
// It arms awaitingReattachSettle so the first real observation knows the status it is
// replacing was ours — see recordStatusChange, which measures that observation against
// the saved status rather than against the synthetic one.
func (i *Instance) setStatusReattached(status Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reattachSavedStatus = i.status
	i.awaitingReattachSettle = true
	i.setStatusLocked(status, false, false)
}

// setStatusLocked is the shared body of SetStatus, setStatusTurnEnded and
// setStatusReattached. observed says whether this write is an observation of the agent,
// and so whether it counts as a status change; only a caller that is writing a synthetic
// status passes false. turnEnded says the caller has already resolved that this write is
// a turn-end edge for a status that is not Ready — only setStatusTurnEnded passes true,
// and passing it on a repeat write would re-ring the same turn every poll.
func (i *Instance) setStatusLocked(status Status, observed, turnEnded bool) {
	switch {
	case turnEnded, status == Ready && i.status != Ready:
		if i.suppressNextUnread {
			i.suppressNextUnread = false
		} else {
			i.unread = true
			i.unreadAt = time.Now()
		}
	case status != Ready:
		i.suppressNextUnread = false
	}
	// Keep the Pending producer clock true for EVERY writer, not just the poller's two
	// producers: leaving Pending releases it, and entering Pending by any other route
	// (a restore, a hand-built row) still gets a stamp, so PendingSince is never zero on a
	// row the list is about to render an elapsed cue for. The producers themselves claim
	// their slot before writing, so this only ever fills a gap — it never overwrites a live
	// hold, which is what makes the cap and the cue measure one continuous hold.
	switch {
	case status != Pending:
		i.pendingSource, i.pendingSince = pendingNone, time.Time{}
	case i.pendingSince.IsZero():
		i.pendingSince = time.Now()
	}
	if status == Paused {
		i.clearPaneFrameLocked()
	}
	if observed {
		i.recordStatusChange(status)
	}
	i.status = status
}

// recordStatusChange stamps statusChangedAt, marks the instance dirty for the next
// state.json write, and appends a transition entry when the status actually changes
// (or on the first observation), emitting a durable trace line to the atrium log. It
// must be called under i.mu with i.status still holding the prior value —
// setStatusLocked overwrites i.status immediately after.
func (i *Instance) recordStatusChange(to Status) {
	from := i.status
	now := time.Now()
	if i.awaitingReattachSettle {
		// The status this observation replaces was written by reattach, not by the
		// agent, so measure against what the session was doing at save time instead.
		// Settling back to it is then correctly no transition at all — the session
		// never left that state, and it keeps the persisted stamp — while anything
		// else is recorded as the change it really is. Spent on the first observation
		// either way.
		i.awaitingReattachSettle = false
		from = i.reattachSavedStatus
	}
	if from == to {
		// No transition. Still stamp the clock the first time we ever see a status so
		// StatusChangedAt is meaningful from launch rather than reading as the zero time.
		if i.statusChangedAt.IsZero() {
			i.statusChangedAt = now
			i.statusDirty = true
		}
		return
	}
	var held time.Duration
	if !i.statusChangedAt.IsZero() {
		held = now.Sub(i.statusChangedAt)
	}
	i.statusChangedAt = now
	i.statusDirty = true
	i.statusHistory = append(i.statusHistory, StatusTransition{From: from, To: to, At: now})
	if len(i.statusHistory) > statusHistoryMax {
		// Drop the oldest entries, keeping the newest statusHistoryMax. A fresh backing
		// array avoids pinning the trimmed-off headroom of the old one.
		i.statusHistory = append([]StatusTransition(nil), i.statusHistory[len(i.statusHistory)-statusHistoryMax:]...)
	}
	log.InfoLog.Printf("status-change %q %s→%s (held %s)", i.Title, from, to, held.Round(time.Millisecond))
}

// StatusDirty reports whether this instance's status or its stamp has changed since
// the last time it was written to state.json, under the read lock.
//
// The bit lives on the instance, not on the observer, because the observers are
// plural: the metadata sweep, the off-cadence selection poll and the attach keeper all
// apply pane states, on different goroutines and different cadences. A before/after
// comparison made by any one of them sees only its own transitions, and misses the
// ones another had already applied in memory — silently, because by then the "old"
// status it snapshots is already the new one.
func (i *Instance) StatusDirty() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.statusDirty
}

// ClearStatusDirty records that this instance's status has reached state.json. Called
// only after a save succeeds, so a failed write leaves the bit set and the next sweep
// retries rather than dropping the change.
func (i *Instance) ClearStatusDirty() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.statusDirty = false
}

// StatusChangedAt returns when the instance's status last actually changed, or was
// first observed, under the read lock. Zero only before the first observed status —
// reattach's synthetic write is not one, and deliberately leaves this alone.
func (i *Instance) StatusChangedAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.statusChangedAt
}

// StatusHistory returns a copy of the instance's recent status-transition ring buffer
// (newest last), under the read lock, for the debug/diagnostic surface. Returns nil
// when no transition has been recorded yet.
func (i *Instance) StatusHistory() []StatusTransition {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(i.statusHistory) == 0 {
		return nil
	}
	return append([]StatusTransition(nil), i.statusHistory...)
}

// Unread reports whether the session reached Ready without the user having
// visited it since, under the read lock.
func (i *Instance) Unread() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.unread
}

// Muted reports whether the user has silenced notifications for this session, under
// the read lock.
func (i *Instance) Muted() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.muted
}

// SetMuted silences (or unsilences) notifications for this session, under the write
// lock. The caller persists the change (see home.toggleMuteSelected).
func (i *Instance) SetMuted(v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.muted = v
}

// AwaitingSetup reports whether the session is blocked on a one-time startup/trust
// gate (see the awaitingSetup field), under the read lock. The row uses it to show a
// "waiting on setup screen" hint alongside the NeedsInput status. It is gated on the
// live status being NeedsInput so a flag left set by a path that bypasses ApplyPaneState
// — a pause or a lost-session recovery to Paused, where PaneDead is a no-op — can never
// surface the hint on a row that is no longer actually blocked.
func (i *Instance) AwaitingSetup() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.awaitingSetup && i.status == NeedsInput
}

// setAwaitingSetup records whether the session is sitting on a startup gate, under the
// write lock. Called only from ApplyPaneState, which recomputes it every poll.
func (i *Instance) setAwaitingSetup(v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.awaitingSetup = v
}

// UnreadAt returns when the unread bit was last flagged, under the read lock.
// Zero if it has never been flagged in this process.
func (i *Instance) UnreadAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.unreadAt
}

// MarkSeen clears the unread bit: the user has visited the session (attached,
// or dwelled on its row with the live preview showing).
func (i *Instance) MarkSeen() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.unread = false
}

// ArmReadySuppression arms the one-shot guard so the next transition into Ready
// does not flag unread. Called after synthetic SetStatus(Running) writes
// (restore-reattach, recoverInPlace, Resume, post-detach refresh) — never after
// an observed working phase.
func (i *Instance) ArmReadySuppression() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.suppressNextUnread = true
}
