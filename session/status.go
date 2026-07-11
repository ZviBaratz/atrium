package session

import (
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// Status/unread tracking: the mu-guarded live-status accessors and the
// into-Ready edge detection that maintains the unread bit. The Status enum and
// StatusUrgency sort policy live with the struct in instance.go.

// GetStatus returns the instance status under the read lock.
func (i *Instance) GetStatus() Status {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.status
}

// SetStatus updates the instance status under the write lock. It also edge-detects
// transitions into Ready to maintain the unread bit: a non-Ready→Ready transition
// flags unread (the agent finished a turn) unless a one-shot suppression is armed
// (a synthetic lifecycle transition — see suppressNextUnread); any non-Ready write
// clears a pending suppression, since an observed working phase means the next
// completion is genuine.
func (i *Instance) SetStatus(status Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if status == Ready && i.status != Ready {
		if i.suppressNextUnread {
			i.suppressNextUnread = false
		} else {
			i.unread = true
			i.unreadAt = time.Now()
		}
	} else if status != Ready {
		i.suppressNextUnread = false
	}
	i.recordStatusChange(status)
	i.status = status
}

// recordStatusChange stamps statusChangedAt and appends a transition entry when the
// status actually changes (or on the first observation), and emits a durable trace
// line to the atrium log. It must be called under i.mu with i.status still holding the
// prior value — SetStatus overwrites i.status immediately after.
func (i *Instance) recordStatusChange(to Status) {
	from := i.status
	now := time.Now()
	if from == to {
		// No transition. Still stamp the clock the first time we ever see a status so
		// StatusChangedAt is meaningful from launch rather than reading as the zero time.
		if i.statusChangedAt.IsZero() {
			i.statusChangedAt = now
		}
		return
	}
	var held time.Duration
	if !i.statusChangedAt.IsZero() {
		held = now.Sub(i.statusChangedAt)
	}
	i.statusChangedAt = now
	i.statusHistory = append(i.statusHistory, StatusTransition{From: from, To: to, At: now})
	if len(i.statusHistory) > statusHistoryMax {
		// Drop the oldest entries, keeping the newest statusHistoryMax. A fresh backing
		// array avoids pinning the trimmed-off headroom of the old one.
		i.statusHistory = append([]StatusTransition(nil), i.statusHistory[len(i.statusHistory)-statusHistoryMax:]...)
	}
	log.InfoLog.Printf("status-change %q %s→%s (held %s)", i.Title, from, to, held.Round(time.Millisecond))
}

// StatusChangedAt returns when the instance's status last actually changed, or was
// first observed, under the read lock. Zero only before the very first SetStatus.
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
