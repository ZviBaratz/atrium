package session

import "github.com/ZviBaratz/atrium/config"

// Startup recovery rationing: how many dead agents one load may relaunch.
//
// Reattaching a surviving tmux session adds no host load — the agent is already
// running and cs detaches rather than kills. A session whose server is gone is a
// different matter: reattach brings it back through recoverInPlace, which calls
// startResuming, i.e. a *fresh agent process*. So a reboot with more
// persisted-live sessions than the host budget would start them all at once,
// silently — the load the interactive paths were capped against in #360/#463,
// reached without a keypress (#474).
//
// This is the last path that starts agents, and the only one with nobody to ask:
// the decision is made inside LoadInstances, before assembleHome sets the app's
// hostCap, and the autoyes daemon reaches the same code with no UI at all. Hence a
// budget here rather than a confirmation dialog, and a report the caller may
// surface (DeferredRecovery) rather than a question it must answer.

// bringOnline reattaches or recovers every rehydrated instance in insts, rationing
// the relaunches by sc, and reports what it had to park. insts is in stored order,
// which is the order the budget is spent in: that slice is the user's own arranged
// order (the list persists InstancesForPersist, which J/K rewrites), so "the top of
// your list came back" is both deterministic and the priority they set. The
// alternatives are worse signals, not just different ones — UpdatedAt is stamped at
// serialization, so it is near-identical fleet-wide, and a session that was Running
// at save time has no work in flight to protect either, its agent having died with
// the server.
//
// Split out of Storage.LoadInstances so the rationing can be driven with injected
// tmux/pty deps: what it must be tested against is which sessions actually launched
// an agent, and the loader builds production sessions from serialized data.
func bringOnline(insts []*Instance, sc config.SessionCap) DeferredRecovery {
	budget := newRecoveryBudget(sc)
	// Classify first, and reserve a slot for every session that is already live,
	// before granting a single relaunch below — see recoveryBudget for why the
	// ordering is load-bearing rather than tidy. Reattach then takes the answer
	// instead of asking again, so the probe costs one tmux call per session, as it
	// did when reattach probed for itself.
	alive := make([]bool, len(insts))
	for i, inst := range insts {
		if alive[i] = inst.paneSurvived(); alive[i] {
			budget.reserve()
		}
	}
	for i, inst := range insts {
		inst.reattach(alive[i], budget)
	}
	return budget.result()
}

// ParkedSession identifies one session a load parked: the (Title, Path) pair, which
// is what Storage matches instances on (see DeleteInstance). The title alone is not
// enough — titles are unique only within a repo group, so a same-titled session in
// another repo could answer for this one. That matters because a report can outlive
// the process that made it (internal/parkreport): a reader reconciling it against a
// later fleet needs to know which row it is talking about.
type ParkedSession struct {
	Title string
	Path  string
}

// DeferredRecovery reports the sessions a load left parked because relaunching
// their agents would have exceeded the host session budget, together with the
// budget it measured them against — so a caller can explain the park in the
// loader's own numbers instead of re-deriving a limit that might not match the one
// actually applied.
//
// The zero value means nothing was deferred. That covers both the fleet that fit
// and the load that rationed nothing at all (an explicit max_sessions — see
// newRecoveryBudget), which is why callers test len(Sessions) rather than Limit.
//
// It carries no live count, deliberately. Neither consumer — the startup toast, and
// the spool that carries the report across processes — has room for a second number on
// a row that truncates its tail, and what is running is on the rows in front of the
// user. A field held for a future caller is a field nothing keeps honest.
type DeferredRecovery struct {
	// Sessions are the parked sessions, in stored order.
	Sessions []ParkedSession
	// Limit is the cap that was applied.
	Limit int
}

// recoveryBudget rations agent relaunches across one load.
//
// It is deliberately two-phase — reserve every survivor, then spend on the dead —
// rather than one counter walked in list order. A live session cannot be refused
// (killing a working agent to make room for a dead one is not a trade Atrium may
// make), so all of them have to be counted before the first spend decision.
// Reserving as they are met instead would let a dead session early in the list take
// a slot that a later survivor then exceeds anyway: at a cap of 2, the stored order
// [dead, dead, alive] would relaunch both dead sessions and still reattach the
// third, ending on 3 live.
//
// A nil *recoveryBudget is unlimited. That is the shape an unrationed load takes,
// and it lets every caller that is not about the cap pass nil.
type recoveryBudget struct {
	limit    int             // slots available; spending stops once live reaches it
	live     int             // slots taken, by reserved survivors and granted relaunches alike
	deferred []ParkedSession // sessions refused, in the order they were met
}

// newRecoveryBudget resolves the cap that rations one load's relaunches, or nil for
// an unrationed load.
//
// Only the host-derived soft cap participates. An explicit positive max_sessions is
// a hard cap over *every* session, paused ones included — capCount measures it with
// NumInstances() — and recovery only flips Paused to Running, so it changes no total
// and cannot cross that cap. Gating on it would refuse to restore work the user's
// own setting says is allowed, in the one state where it could ever bite (a
// max_sessions lowered under an existing fleet), which is exactly what #463 declined
// to do for resume. An explicit non-positive value is the documented escape hatch and
// stays silent by definition.
func newRecoveryBudget(sc config.SessionCap) *recoveryBudget {
	if !sc.Soft || sc.Limit <= 0 {
		return nil
	}
	return &recoveryBudget{limit: sc.Limit}
}

// reserve takes a slot for a session that is already live. It cannot be refused, so
// it returns nothing and may carry live past limit — a fleet that survived a TUI
// restart is not asked to justify itself, and nothing it does adds load.
func (b *recoveryBudget) reserve() {
	if b == nil {
		return
	}
	b.live++
}

// spend grants inst a relaunch if the budget has room, recording the refusal
// otherwise. An unrationed load (nil) grants everything.
//
// It takes the instance rather than its title so a refusal is recorded as the
// (Title, Path) pair — see ParkedSession for why a title alone cannot identify the row
// a report is about.
func (b *recoveryBudget) spend(inst *Instance) bool {
	if b == nil {
		return true
	}
	if b.live >= b.limit {
		b.deferred = append(b.deferred, ParkedSession{Title: inst.Title, Path: inst.Path})
		return false
	}
	b.live++
	return true
}

// refund hands back a slot whose relaunch did not end up live — recoverInPlace
// degraded to Paused (an orphaned worktree, a launch that failed). That slot never
// became host load, so the next candidate may have it rather than being parked
// behind a session that is not running either.
func (b *recoveryBudget) refund() {
	if b == nil {
		return
	}
	b.live--
}

// result is what the load reports to its caller: the parked sessions and the cap they
// were measured against, or the zero value when nothing was refused.
func (b *recoveryBudget) result() DeferredRecovery {
	if b == nil || len(b.deferred) == 0 {
		return DeferredRecovery{}
	}
	return DeferredRecovery{Sessions: b.deferred, Limit: b.limit}
}
