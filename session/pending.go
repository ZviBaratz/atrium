package session

import (
	"sync/atomic"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
)

// Pending reconciliation (#290 Phase 2).
//
// A session enters Pending when the poller sees the hook record latched "ready" with a
// non-empty in-flight sub-agent set: the main turn ended, but background work is still
// outstanding.
//
// Pending now has a SECOND producer. tmux.PaneBackground raises the same status from
// claude's footer chips, for work that is not a sub-agent at all (a background Bash, a
// Monitor) and therefore never reaches the set. It is deliberately NOT reconciled here —
// see the watchdog note in item 2 — and it is raised from ApplyPaneState, not from this
// file. What lives here for BOTH of them is pendingProducer: which one currently holds the
// row, which is the question the status itself cannot answer. The reconciliation order
// below concerns the set alone.
//
// Pending is advisory — it must never become a permanently-stuck row — so it
// is reconciled in a strict, load-bearing priority order:
//
//  1. Explicit terminal status, gated on the set being empty. A Stop with the set empty is
//     the ONLY thing that yields done/idle (handled in poll.go: ready+empty → PaneIdle →
//     Ready). A Stop with the set non-empty is Pending, never done. Never inferred from
//     silence or a stale pane.
//  2. Wall-clock watchdog (this file). A session held Pending past a generous, agent-tunable
//     cap is force-reconciled to done EVEN IF the pane is still alive — the alive-but-stuck
//     case, where a SubagentStop never fired so the set never drained. Checked before
//     liveness precisely because liveness would answer "alive → keep waiting" and never time
//     it out. The cap is generous because a background sub-agent legitimately runs long;
//     liveness (below) carries the common, fast failure. It caps ONLY the set: a
//     chip-driven Pending is exempt, because a scraped chip cannot go stuck the way a
//     latched id can — it is re-read every poll and gone when the work exits — and capping
//     it would re-commit the false "done" at the cap. The clock is per-producer for the
//     same reason (Instance.pendingSource / pendingSince): both write the status Pending,
//     and recordStatusChange does not re-stamp on from == to, so a shared stamp would let
//     a long chip hold expire the next genuine sub-agent run on its first tick.
//  3. Liveness. A dead tmux pane is caught by Poll's has-session check (→ PaneDead →
//     recoverLostInstances → Paused) before the record is ever read, so a crash mid-sub-agent
//     can't strand a Pending row. This needs no code here — it is the existing machinery.
//  4. Freshness (heartbeat). A hook heartbeat now HOLDS working while fresh (poll.go, #311),
//     but only for the empty-set case — it never reconciles Pending and never declares
//     done/dead (that stays with 1–3 above). So it does not affect this order: a non-empty
//     set is still Pending regardless of heartbeat freshness. `working_stale`/keepalive
//     remain unbuilt (the animation-gated spinner already covers a long silent tool).
//
// Two invariants keep this free of the #46 oscillation: "done" is only ever an explicit
// ready with an empty set (never inferred), and the watchdog's reconciliation
// DETERMINISTICALLY clears the stuck set (ClearInflight) so the next poll sees ready+empty
// → idle and stays there, instead of re-classifying ready+non-empty → Pending and flapping.

// DefaultPendingWatchdog is the wall-clock cap a session may sit Pending before the
// watchdog force-reconciles it to done, absent any override. Deliberately generous: this
// backstops only the rare alive-but-stuck case (a SubagentStop that never fired on a
// still-live pane) — tmux liveness already catches the common dead-pane failure within a
// couple of ticks — so the cap is tuned so a legitimately long-running background
// sub-agent never trips it (a false "done" is worse than a row that reads "busy" a while
// longer).
const DefaultPendingWatchdog = 30 * time.Minute

// configuredPendingWatchdog holds the user's pending_watchdog_minutes as nanoseconds, or
// 0 for "not configured". Atomic rather than a plain var because the write and one of the
// reads are on different goroutines: SetPendingWatchdog runs on the TUI's Update thread
// (assembleHome, then every settings change), while applyPending — and through it
// pendingWatchdogCap — is reached from app's attachKeeper, which services instances from a
// goroutine of its own. The TUI's own metadata path is main-thread (applyMetadataResults
// applies what the poll goroutines collected), so the keeper is the whole reason this is
// not a plain var.
var configuredPendingWatchdog atomic.Int64

// SetPendingWatchdog installs the user-configured Pending cap, which outranks both an
// agent adapter's override and the package default. A non-positive duration clears it, so
// the ladder falls back to adapter → DefaultPendingWatchdog.
//
// Callers pass an already-clamped value (config.GetPendingWatchdogMinutes); this refuses
// only the non-positive case, which would make every Pending row expire on its first poll.
func SetPendingWatchdog(d time.Duration) {
	if d <= 0 {
		configuredPendingWatchdog.Store(0)
		return
	}
	configuredPendingWatchdog.Store(int64(d))
}

// PendingWatchdog reports the cap SetPendingWatchdog installed, or 0 when none is. It is
// the setter's counterpart: package-level mutable state that nothing can read back is
// state nothing can assert about, and the cap's whole contract is which of three values is
// currently in force.
func PendingWatchdog() time.Duration {
	return time.Duration(configuredPendingWatchdog.Load())
}

// pendingWatchdogCap is this instance's Pending cap, resolved down a three-rung ladder:
// the user's configured value, then the agent's override, then the package default.
//
// The user outranks the adapter deliberately (#799). The reverse order preserves
// per-agent tuning, but it also means a user who raises the cap because THEIR agent runs
// long sub-agents gets no effect and no explanation — a knob that is silently inert for
// the agent that motivated it. An adapter override still carries every session whose user
// has not expressed an opinion, which is the case it was written for.
func (i *Instance) pendingWatchdogCap() time.Duration {
	if d := configuredPendingWatchdog.Load(); d > 0 {
		return time.Duration(d)
	}
	if d := agent.Resolve(i.Program).PendingWatchdog; d > 0 {
		return d
	}
	return DefaultPendingWatchdog
}

// applyPending maps a PanePending poll onto the instance's status, running the wall-clock
// watchdog. On entry it claims the set producer, which starts the watchdog's own clock
// (pendingSince) so the cap measures how long the SET has held the row and not the prior
// state's age — statusChangedAt cannot serve here, because it does not re-stamp when a
// chip-driven Pending hands over to a set-driven one. On a subsequent poll where the set
// has held longer than the cap, it reconciles: clear the stuck in-flight set (the
// deterministic latch-clear that prevents re-entry), then commit Ready. The commit flags
// unread like any real completion, so a session that was stuck pending surfaces as
// finished-and-unseen rather than silently vanishing.
func (i *Instance) applyPending() {
	i.setPendingSource(pendingInflight)
	if i.pendingExpired() {
		if ts := i.tmux(); ts != nil {
			// Clear the set before committing done so the next poll reads ready+empty → idle
			// and stays (the anti-oscillation clear). A persistent clear failure (broken FS)
			// degrades to a bounded re-reconcile — the row flips back to Pending next poll and
			// the watchdog retries a cap later — never a permanently-stuck row.
			if err := ts.ClearInflight(); err != nil {
				log.WarningLog.Printf("pending watchdog: failed to clear in-flight set for %q: %v", i.Title, err)
			}
		}
		i.SetStatus(Ready) // a non-Pending write releases the producer and its clock
		log.InfoLog.Printf("pending watchdog: %q held pending past %s, reconciled to ready", i.Title, i.pendingWatchdogCap())
		return
	}
	i.SetStatus(Pending)
}

// pendingProducer names which of Pending's two producers is currently holding a row, so
// the things that differ between them can key off it. Both write the same Status, so the
// status cannot answer this and neither can statusChangedAt.
//
// Three consumers need the distinction: the watchdog caps only the set (a latched id can
// leak; a scraped chip cannot), the row's elapsed cue must show the age of the hold the
// user is looking at rather than its predecessor's, and the turn-end edge that raises the
// unread bit fires on the handover INTO the chip producer — including the set → chip
// handover, where the status never changes at all.
type pendingProducer uint8

const (
	pendingNone       pendingProducer = iota // not held Pending
	pendingInflight                          // a non-empty hook in-flight sub-agent set (#290)
	pendingBackground                        // claude's footer shell/monitor chips (tmux.PaneBackground)
)

// setPendingSource claims src as the current Pending producer and reports whether that
// was a CHANGE — the handover edge. On a change it restamps pendingSince, so each
// producer's hold is measured from its own start; on a repeat it leaves the stamp alone,
// so the clock spans the whole continuous hold rather than restarting every poll.
func (i *Instance) setPendingSource(src pendingProducer) (changed bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pendingSource == src {
		return false
	}
	i.pendingSource = src
	i.pendingSince = time.Now()
	return true
}

// pendingSinceOnRestore derives a restored row's Pending clock from the persisted
// statusChangedAt, and only when the restored status is actually Pending. Anything else
// gets the zero time, which is what "not held" means everywhere else in this file.
func pendingSinceOnRestore(status Status, statusChangedAt time.Time) time.Time {
	if status != Pending {
		return time.Time{}
	}
	return statusChangedAt
}

// PendingSince reports when the CURRENT Pending hold began, or the zero time when the row
// is not held. It is what the list's elapsed cue must render: statusChangedAt does not
// move when one producer hands over to the other (recordStatusChange returns early on
// from == to), so a row that sat 40 minutes on a Monitor chip and then started a genuine
// sub-agent would otherwise claim the sub-agent had been running for 40 minutes.
func (i *Instance) PendingSince() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.pendingSince
}

// pendingExpired reports whether the instance has been continuously held Pending BY THE
// IN-FLIGHT SET for longer than its watchdog cap. Gated on the current status already being
// Pending, so the tick that first enters Pending can never trip it, and on the set being the
// current producer — a chip-driven hold is exempt by design and must not accrue against a cap
// it can never legitimately trip.
func (i *Instance) pendingExpired() bool {
	if i.GetStatus() != Pending {
		return false
	}
	i.mu.RLock()
	src, since := i.pendingSource, i.pendingSince
	i.mu.RUnlock()
	return src == pendingInflight && !since.IsZero() && time.Since(since) > i.pendingWatchdogCap()
}
