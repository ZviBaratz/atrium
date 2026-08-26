package app

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/retire"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// retireDrainBudget is how many teardowns one tick may dispatch.
//
// One, and it is not a throughput knob. A kill is roughly six subprocesses plus a
// recursive worktree delete that routinely takes seconds on a fat tree, and a pause is
// the same order; two dispatched on one tick would race each other for m.list and
// storage from two goroutines. The serialisation is not really this constant either —
// both verbs set a flag that makes retireDrainHeld true for the whole of their I/O, so
// the next tick holds regardless. This is what stops a single tick from getting there
// first.
//
// Refusals are deliberately not charged against it. Answering a record costs a receipt
// and an unlink, so a spool full of stale requests is cleared in one pass rather than
// starving the one good record behind them for a tick apiece.
const retireDrainBudget = 1

// drainRetireRequests executes the retirements spooled by `atrium kill` and
// `atrium pause` (#835), returning a command that performs one of them plus a notice,
// or nil when there was nothing to do.
//
// Every record is judged twice, and the second judgement is not redundant. `atrium
// kill` recomputes the target's tree and refuses unless nothing is at risk, but at
// least a poll tick passes between that check and this walk and the target agent keeps
// working through it — so a session that was clean when the CLI looked can be dirty by
// now. This side re-runs the same predicate (internal/retire, shared so the two cannot
// disagree about what the numbers mean) on what the model currently knows, which the
// poll refreshes every sweep precisely so the figures a teardown reads stay fresh.
//
// A pause is not gated on either side. It keeps the branch and commits what was
// uncommitted, so there is nothing at risk to gate on — which is what makes it the
// verb an orchestrator can still reach for when a kill is refused.
func (m *home) drainRetireRequests() tea.Cmd {
	if m.retireDrainHeld() {
		return nil
	}

	entries, err := outbox.ListRetires()
	if err != nil {
		log.ErrorLog.Printf("failed to read the retire spool: %v", err)
		return nil
	}
	if len(entries) == 0 {
		return nil
	}

	now := time.Now()
	var dispatched int
	var cmds []tea.Cmd
	// path -> the reason its producer should read back. Every terminal outcome that is
	// not a dispatch goes here, so a `--wait` never sees an unlink it would read as a
	// successful teardown.
	rejected := map[string]string{}

	for _, e := range entries {
		if m.outboxPoisoned[e.Path] {
			continue
		}
		if dispatched >= retireDrainBudget {
			// Left queued, not refused: the next tick acts on it. Breaking rather than
			// continuing keeps the spool in order, so the oldest record is always the
			// next one dispatched.
			break
		}

		switch {
		case e.Err != nil:
			// Unreadable, or from a different atrium. Discarding is the only way out: a
			// file nobody can decode and nobody deletes would be re-read on every tick
			// forever. ListRetires only ever surfaces files matching the spool's own name
			// format, so this can only discard our own.
			log.ErrorLog.Printf("discarding an unreadable retirement: %v", e.Err)
			rejected[e.Path] = "the retirement could not be read"

		case e.Retire.Expired(now):
			age := now.Sub(e.Retire.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a %s for %q: spooled %s ago, past the %s horizon",
				e.Retire.Mode, e.Retire.Title, age, outbox.TTL)
			rejected[e.Path] = fmt.Sprintf("the %s was spooled %s ago, past the %s horizon",
				e.Retire.Mode, age, outbox.TTL)

		default:
			// Matched on the (Title, Path) pair, never the title alone: titles are unique
			// only within a repo group, so a same-titled session in another repo must
			// never be the one torn down.
			inst := m.findInstanceByIdentity(e.Retire.Title, e.Retire.Path)
			if inst == nil {
				log.WarningLog.Printf("discarding a %s for %q (%s): no such session",
					e.Retire.Mode, e.Retire.Title, e.Retire.Path)
				rejected[e.Path] = fmt.Sprintf("no session %q in %s — it may have been retired "+
					"since the request was made", e.Retire.Title, e.Retire.Path)
				continue
			}
			cmd, reason := m.executeRetirement(e.Retire.Mode, inst)
			if reason != "" {
				log.InfoLog.Printf("refusing a %s for %q: %s", e.Retire.Mode, inst.Title(), reason)
				rejected[e.Path] = reason
				continue
			}
			cmds = append(cmds, cmd)
			dispatched++
			m.discardSpoolFile(e.Path, func() error { return outbox.Remove(e.Path) })
		}
	}

	for path, reason := range rejected {
		// A receipt rather than a bare unlink, so `kill --wait` reports the refusal
		// instead of reading the record's disappearance as a teardown.
		m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
	}

	return tea.Batch(cmds...)
}

// retireDrainHeld reports whether this tick must not act on a retirement.
//
// The first four holds are createDrainHeld's, and they are the right four here for the
// same reasons — a staged spawn plan means a human is at a dialog, a pending quit is
// waiting on starts, and an in-flight async action or teardown must not interleave with
// another mutation of the list. The last of those is also what makes the budget above
// hold across ticks rather than only within one.
//
// The fifth is this drain's own, and it covers a gap none of the four reach. A kill
// confirmation captures its instance when the dialog is STAGED, and nothing marks that
// instance retiring until the dialog is accepted (armTeardown runs on the accept). So a
// teardown dispatched underneath an open dialog leaves that dialog's accept to run
// killIOCmd against a session that is already gone. Any open confirmation holds, not
// just a kill's: the state is what is observable here, the cost of holding is one tick,
// and enumerating which dialogs are dangerous is a list that would fall behind.
func (m *home) retireDrainHeld() bool {
	return m.createDrainHeld() || m.state == stateConfirm
}

// executeRetirement dispatches one retirement, or returns the reason it will not.
// Exactly one of the two results is ever set.
//
// Both verbs go through the path the TUI's own key already uses — killIOCmd and
// pauseIOCmd — rather than growing a second teardown. For a kill that is not a style
// preference: the undo journal is written inside killIOCmd, deliberately, so that no
// entry point can retire a session without recording how to get it back. A retirement
// that bypassed it would be the one kind of kill `U` cannot undo, and nothing would
// say so.
func (m *home) executeRetirement(mode outbox.Mode, inst *session.Instance) (tea.Cmd, string) {
	label := fmt.Sprintf("%s '%s'…", mode.Gerund(), inst.DisplayName())
	if mode == outbox.ModePause {
		// Ungated, but Instance.Pause still refuses a direct session and the drain
		// reports that through the ordinary failure path, as the confirmation does.
		return m.beginAsyncAction(label, pauseIOCmd([]*session.Instance{inst})), ""
	}
	if v := m.retireVerdict(inst); !v.Allowed() {
		return nil, v.Reason()
	}
	arm, action := m.armTeardown([]*session.Instance{inst}, killIOCmd(m, inst))
	// On the update thread, which is the only place the retiring mark and the
	// WaitGroup may be touched — the same place and the same order the accepted
	// confirmation applies them in.
	arm()
	return m.beginAsyncAction(label, action), ""
}

// retireVerdict re-runs the safety gate on what the model currently knows about inst.
//
// GetDiffStats may be nil — a session whose stats have not been computed yet — and
// that reaches the gate as nil rather than being smoothed into a zero DiffStats,
// because the gate's whole job is to tell "nothing at risk" from "nothing measured".
func (m *home) retireVerdict(inst *session.Instance) retire.Verdict {
	return retire.Gate(inst.GetDiffStats(), agentWorking(inst.GetStatus()))
}

// agentWorking reports whether a status means the agent has a turn or background work
// in flight, which is the "busy" input the gate takes.
//
// Pending counts, and it is the one that would be missed: the main turn has ended, so
// the row reads as finished, while sub-agents or a background shell the turn left
// running are still going. Loading counts because a session still starting up has not
// yet had a chance to produce the work a kill would discard. Ready and NeedsInput are
// idle — a session blocked on a permission prompt is waiting on a person, not working.
func agentWorking(s session.Status) bool {
	switch s {
	case session.Running, session.Loading, session.Pending:
		return true
	default:
		return false
	}
}
