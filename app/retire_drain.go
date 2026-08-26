package app

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/retire"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
)

// retireDrainBudget is how many teardowns one tick may dispatch.
//
// One, and it is not a throughput knob. A kill is roughly six subprocesses plus a
// recursive worktree delete that routinely takes seconds on a fat tree, and a pause is
// the same order; two dispatched on one tick would race each other for m.list and
// storage from two goroutines. The serialisation is not really this constant either —
// a dispatch claims its record until the teardown reports back, and retireDrainHeld
// holds on an unsettled claim, so the next tick holds regardless. This is what stops a
// single tick from getting there first.
const retireDrainBudget = 1

// retireDisposalBudget caps how many records one tick may answer without dispatching
// anything: the expired ones, the undecodable ones, and the ones naming a session that
// is not there.
//
// It exists for createDisposalBudget's reason, and the numbers here are worse than
// they look. Answering a record is outbox.Reject, which is config.WriteFileAtomic —
// CreateTemp, Write, fsync, Close, Chmod, Rename, then an fsync of the directory — so
// roughly two fsyncs and seven syscalls apiece, all synchronous on the Bubble Tea
// update goroutine. Unbounded that is fine for the three or four records a person
// generates and a freeze for what a machine does: this drain is suspended for the whole
// of an attach, so an orchestrator retrying `atrium kill <title>` every few seconds
// through a one-hour session leaves several hundred records to answer on the first tick
// after detach, with input and rendering blocked for all of it. Milliseconds on NVMe,
// seconds on a spinning disk, a container bind mount, or a data dir on NFS.
//
// It bounds the writes, not the reads: ListRetires decodes every file in the spool
// before any budget applies, so a backlog still costs one ReadFile and one Unmarshal
// per record per tick until it clears.
const retireDisposalBudget = 50

// retireSettleGrace is how long a dispatched retirement may go unreported before the
// drain stops waiting for it.
//
// It is a backstop, not a timeout anything is expected to hit. Every dispatch is
// answered by killDoneMsg or batchPauseDoneMsg, both of which the runtime delivers on
// every outcome including a refusal — but the hold that makes the claim safe is also
// what would make an unanswered claim permanent, wedging the spool until the TTL
// discards everything behind it. Generous on purpose: the slowest legitimate case is a
// recursive worktree delete of a tree carrying node_modules on a cold mount, and
// treating that as lost would report a teardown as failed while it was still running.
const retireSettleGrace = 15 * time.Minute

// pendingRetirement is the spool record for the retirement currently between its
// dispatch and its outcome.
//
// It exists because "the drain decided to try" and "the session was retired" are
// different events, and awaitSpool can only observe one thing: whether the record is
// still there. The first implementation unlinked at dispatch, synchronously, before the
// returned tea.Cmd had run a line — so `atrium kill x --wait 60s` exited 0 the instant
// the drain picked the record up. That is wrong in both directions a teardown can go
// wrong: killIOCmd still refuses a branch checked out in the base repo and returns
// having touched nothing, and every Instance.Pause failure is collected into
// batchPauseDoneMsg.failures. In both cases the session is still there and its producer
// was told it was gone.
//
// One field rather than a map because retireDrainBudget is one and retireDrainHeld
// holds while this is set, so there is never a second.
type pendingRetirement struct {
	// record is the spool file, still on disk, waiting to be answered by an outcome.
	record string
	// mode is which verb was dispatched, for the log line.
	mode outbox.Mode
	// inst keys the claim. The done handlers fire for every kill and pause, including
	// the ones a keypress started, so an outcome only settles this if it is for the
	// instance this dispatch named.
	inst *session.Instance
	// at is when the dispatch happened, read only by the retireSettleGrace backstop.
	at time.Time
}

// drainRetireRequests executes the retirements spooled by `atrium kill` and
// `atrium pause` (#835), returning a command that performs one of them plus any
// notices, or nil when there was nothing to do.
//
// Every record is judged twice, and the second judgement is not redundant. `atrium
// kill` recomputes the target's tree and refuses unless nothing is at risk, but at
// least a poll tick passes between that check and this walk and the target agent keeps
// working through it — so a session that was clean when the CLI looked can be dirty by
// now. Both of the command's checks run again here, against what the model currently
// knows: retire.Admits for the lifecycle states no verb can act on, and retire.Gate for
// the tree. Shared with the command rather than restated, so the two sides cannot
// disagree about either rule.
//
// A pause is not gated on the TREE, on either side. It keeps the branch and commits
// what was uncommitted, so there is nothing at risk to gate on — which is what makes it
// the verb an orchestrator can still reach for when a kill is refused. It is still
// subject to retire.Admits, which is about what a park can act on at all rather than
// about work at risk.
func (m *home) drainRetireRequests() tea.Cmd {
	// Before the hold, not after, and that ordering is the whole point of the backstop:
	// an unsettled claim is itself one of the holds, so a claim whose outcome never
	// arrived would make every later tick return here and never reach the release.
	now := time.Now()
	m.abandonStuckRetirement(now)

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

	// tmux missing holds every teardown in this tick rather than refusing it, and is
	// probed here rather than inside executeRetirement for drainCreateRequests' reason:
	// every check there is a fact about the session, which stays true until something
	// changes it, while tmux being off PATH is a fact about the machine that the very
	// next tick can see cleared.
	//
	// A kill dispatched in that window is worse than a create lost in it. Instance.Kill
	// wraps the kill-session failure and goes on to the worktree cleanup
	// unconditionally, and applyKillDone removes the row and the storage entry whatever
	// the outcome — so the branch is deleted, the row is gone, and the agent is still
	// running on the socket with nothing left that owns it.
	//
	// Once per tick, and only with a non-empty spool. Disposals are deliberately not
	// held: an expired or undecodable record needs no tmux to answer, and its producer
	// is owed the receipt whatever the machine is doing.
	tmuxDown := tmuxAvailable()
	switch {
	case tmuxDown != nil && !m.retireTmuxHeld:
		m.retireTmuxHeld = true
		log.WarningLog.Printf("holding retirements until tmux is usable again "+
			"(%d record(s) in the spool): %v", len(entries), tmuxDown)
	case tmuxDown == nil && m.retireTmuxHeld:
		m.retireTmuxHeld = false
		log.InfoLog.Printf("tmux is usable again; resuming retirements")
	}

	var dispatched, disposed int
	var cmds []tea.Cmd
	// path -> the reason its producer should read back. Every terminal outcome that is
	// not a dispatch goes here, so a `--wait` never sees an unlink it would read as a
	// successful teardown.
	rejected := map[string]string{}
	// The refusals a person at the keyboard should see. A retirement an agent asked for
	// and this drain declined is invisible to both parties otherwise: the agent may not
	// have passed --wait, and the log is not somewhere anyone is looking.
	//
	// Refusals only, never disposals — the split drainCreateRequests draws for its own
	// notice, and for its reason. A refusal names something at the TUI that a person can
	// act on (resume the session, push the branch, wait for the turn); an expired or
	// undecodable record is nobody's to fix from here, and counting one would paint a red
	// error over the frame for as long as a backlog takes to clear.
	var refusals []string
	var firstRefusal string

	for _, e := range entries {
		if m.outboxPoisoned[e.Path] {
			continue
		}
		// Already answered, and durably so. outboxPoisoned covers the same record for
		// the rest of THIS run and dies with the process, which is not long enough: a
		// refusal whose receipt landed but whose unlink failed — ENOSPC, EROFS, a
		// Windows or NFS lock — is still on disk for the next TUI to read, and by then
		// the condition that justified the refusal may have cleared. Re-judging it
		// would tear down a session whose producer was told, up to a TTL earlier, that
		// the request had been refused. The create drain probes its disclosure mark
		// here for the same reason. Retry the unlink rather than only skipping, so the
		// record leaves as soon as whatever blocked it lets go.
		if outbox.Receipted(e.Path) {
			m.discardSpoolFile(e.Path, func() error { return outbox.Remove(e.Path) })
			continue
		}
		if disposed >= retireDisposalBudget {
			// Every remaining arm either disposes or dispatches, and a dispatch cannot
			// happen without also being able to dispose. Stop rather than continue: the
			// spool stays in order, so the oldest record is still the next one up.
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
			disposed++

		case e.Retire.Expired(now):
			age := now.Sub(e.Retire.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a %s for %q: spooled %s ago, past the %s horizon",
				e.Retire.Mode, e.Retire.Title, age, outbox.TTL)
			rejected[e.Path] = fmt.Sprintf("the %s was spooled %s ago, past the %s horizon",
				e.Retire.Mode, age, outbox.TTL)
			disposed++

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
				disposed++
				continue
			}
			// Past this point the record either dispatches or is refused for a reason
			// about the session, and both need the budget: a refusal writes a receipt.
			if tmuxDown != nil || dispatched >= retireDrainBudget {
				// Held rather than refused, and left queued in order. A dispatch already
				// spent claims the record it dispatched, so this one waits for the tick
				// after that teardown reports.
				continue
			}
			cmd, verdict := m.executeRetirement(e.Retire.Mode, inst)
			if !verdict.Allowed() {
				if verdict.Transient() {
					// Held, not answered, and left queued in order. A session still
					// starting finishes starting, and a tree whose numbers could not be
					// taken is re-measured by the next poll — so answering either would
					// refuse a durable request for a condition that clears itself, which
					// is what the first tick after a launch does to every row still
					// coming online. No log line: this repeats every tick for as long as
					// it lasts, and the record staying in the spool is the observable
					// part. A producer in --wait sees the timeout, and the TTL is the
					// ceiling if it never clears.
					continue
				}
				reason := verdict.Reason()
				log.InfoLog.Printf("refusing a %s for %q: %s", e.Retire.Mode, inst.Title(), reason)
				rejected[e.Path] = reason
				// The reason, never the session's name. A display name has no length
				// ceiling and the notice row truncates its tail, while every reason here
				// comes from retire.Verdict's fixed set — so this is the half that
				// survives the row. The log line above and the producer's receipt carry
				// which session.
				refusals = append(refusals, reason)
				if firstRefusal == "" {
					firstRefusal = fmt.Sprintf("refused to %s a session: %s", e.Retire.Mode, reason)
				}
				disposed++
				continue
			}
			cmds = append(cmds, cmd)
			dispatched++
			// Claimed, not answered. The record stays on disk until killDoneMsg or
			// batchPauseDoneMsg says what became of the session — see pendingRetirement.
			m.pendingRetirement = &pendingRetirement{
				record: e.Path, mode: e.Retire.Mode, inst: inst, at: now,
			}
		}
	}

	for path, reason := range rejected {
		// A receipt rather than a bare unlink, so `kill --wait` reports the refusal
		// instead of reading the record's disappearance as a teardown.
		m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
	}
	switch {
	case len(refusals) == 1:
		// flashNotice rather than handleError, which for a long message opens a modal.
		// A modal is the wrong surface for a refusal nobody at the keyboard asked for:
		// it covers the frame and waits for a dismissal to answer a question the
		// producer's receipt already answered. The outcome handlers make the same
		// choice for the same reason, through settleRetirement's report of whose
		// retirement an outcome was.
		cmds = append(cmds, m.flashNotice(firstRefusal, ui.NoticeError))
	case len(refusals) > 1:
		// A count rather than a list. Several refusals in one tick means a backlog, and
		// what a reader needs is that it was a backlog — the receipts and the log carry
		// the individual reasons.
		cmds = append(cmds, m.flashNotice(fmt.Sprintf(
			"refused %d retirements from atrium kill/pause", len(refusals)), ui.NoticeError))
	}

	return tea.Batch(cmds...)
}

// settleRetirement answers the spooled record for a retirement that has now reported
// back, if the outcome belongs to the retirement this drain dispatched.
//
// err distinguishes the two things awaitSpool cannot tell apart on its own: nil means
// the session was retired and the record goes, while a non-nil err means it is still
// there and the record leaves a receipt carrying the reason. Called from the done
// handlers rather than from the drain, because those are the only places that know.
//
// Keyed on inst because those handlers fire for every kill and pause the app performs,
// keypress-driven ones included. Without the check, a user pausing a row by hand would
// answer whatever record the drain happened to be holding.
//
// The bool is that same question asked out loud: it reports whether this outcome
// belonged to a retirement the drain dispatched, which is what the done handlers need in
// order to choose a surface for it. Nobody at the keyboard asked for a background
// retirement, so nobody at the keyboard is owed a modal about it.
func (m *home) settleRetirement(inst *session.Instance, err error) bool {
	p := m.pendingRetirement
	if p == nil || inst == nil || p.inst != inst {
		return false
	}
	m.pendingRetirement = nil
	if err != nil {
		log.WarningLog.Printf("the %s of %q did not complete: %v", p.mode, inst.Title(), err)
		m.discardSpoolFile(p.record, func() error { return outbox.Reject(p.record, err.Error()) })
		return true
	}
	m.discardSpoolFile(p.record, func() error { return outbox.Remove(p.record) })
	return true
}

// abandonStuckRetirement releases a claim whose outcome never arrived, so one lost
// message cannot wedge the spool for the rest of the process's life.
//
// The receipt says the outcome is unknown rather than guessing at one, because it is:
// the dispatch happened, so the teardown may well have too. A caller told "refused"
// would go looking for a session that is gone, and one told "retired" would stop
// watching a session that is still running.
//
// Everything the dispatch armed is released, not just the claim, and the claim is the
// least of them. executeRetirement puts the app behind beginAsyncAction for either
// verb and behind armTeardown for a kill, and the outcome message this retirement
// never sent is what clears both — so releasing the record alone would leave
// actionInFlight and the retiring mark set for the rest of the process. createDrainHeld
// reads actionInFlight and retireDrainHeld reads createDrainHeld, so `atrium new` and
// both retire verbs would stop draining: the wedge this function exists to prevent,
// moved one field along. The grace period is what makes the release safe rather than a
// race — fifteen minutes is long past the slowest legitimate teardown — and a message
// that does arrive afterwards finds this idempotent and the claim already gone.
func (m *home) abandonStuckRetirement(now time.Time) {
	p := m.pendingRetirement
	if p == nil || now.Sub(p.at) <= retireSettleGrace {
		return
	}
	log.ErrorLog.Printf("the %s of %q never reported back after %s; releasing its record",
		p.mode, p.inst.Title(), retireSettleGrace)
	m.pendingRetirement = nil
	m.endTeardown([]*session.Instance{p.inst})
	m.endAsyncAction()
	m.discardSpoolFile(p.record, func() error {
		return outbox.Reject(p.record, fmt.Sprintf(
			"atrium dispatched the %s but it never reported back within %s, so whether the "+
				"session was retired is unknown — check `atrium ls`", p.mode, retireSettleGrace))
	})
}

// retireDrainHeld reports whether this tick must not act on a retirement.
//
// Three groups. The first is createDrainHeld's four, which are the right four here for
// the same reasons — a staged spawn plan means a human is at a dialog, a pending quit is
// waiting on starts, and an in-flight async action or teardown must not interleave with
// another mutation of the list.
//
// The second is an unsettled claim, and it does two jobs. It is what makes
// retireDrainBudget hold across ticks rather than only within one, and it closes a
// window a drained PAUSE leaves that none of the four reach: a kill is covered by the
// retiring mark for its whole async window, but a pause sets only actionInFlight, and
// the asyncActionDoneMsg handler clears that and then re-feeds the inner result as a
// SEPARATE later message. Between those two messages neither inherited hold is true.
//
// The third is the overlays that CAPTURE AN INSTANCE: a confirmation stashes its
// teardown action, renameTarget holds the row being relabelled, queueTarget mirrors it.
// A teardown dispatched underneath any of them leaves that close to act on a session
// that is already gone — with the deep-rename toggle on, a tmux rename, a
// `git branch -m` and a worktree move against a session whose branch was just deleted.
// All three are cleared on the accepting and the cancelling path alike, so the hold is
// as reliable as the overlay is.
//
// Each is named rather than standing in for it with "anything but the default frame",
// which is the gate createDrainHeld's own comment gives as the thing that deadlocked
// that drain. The frame carries states no retirement can disturb, and two of them never
// end on their own: a fresh install sits in stateWelcome until someone answers the
// modal, and nothing answers it on a machine driven only by `atrium new`, where
// markWelcomeSeen is deliberately skipped for a background spawn. A blanket state gate
// therefore means `atrium kill --wait` always times out and every record expires at the
// TTL — on exactly the headless machine these verbs exist for. stateInfo is the same
// trap arriving later: a drained retirement that fails raises one, and the drain would
// then hold on the modal its own failure put up. A list that can fall behind is the
// lesser risk; the falling-behind is one hold too many, and this is one hold too few.
func (m *home) retireDrainHeld() bool {
	return m.createDrainHeld() || m.pendingRetirement != nil || m.overlayHoldsAnInstance()
}

// overlayHoldsAnInstance reports whether an overlay is open that captured a specific
// row to act on when it closes. See retireDrainHeld, its only caller, for why the three
// are named individually.
func (m *home) overlayHoldsAnInstance() bool {
	return m.pendingConfirmAction != nil || m.renameTarget != nil || m.queueTarget != nil
}

// executeRetirement dispatches one retirement, or returns the verdict that stopped it.
// Exactly one of the two results is ever set: a cleared verdict comes with a command.
//
// The verdict rather than its wording, because the caller has to tell a refusal that
// answers the request from one that only describes this moment — see Verdict.Transient.
//
// Both verbs go through the path the TUI's own single-session key already uses —
// killIOCmd and pauseIOCmd — rather than growing a second teardown. For a kill that is
// not a style preference: killIOCmd writes the undo journal itself, so reaching a kill
// through it is what makes a drained retirement as recoverable as a pressed one. A
// retirement that grew its own teardown would be the one kind of kill `U` cannot undo,
// and nothing would say so. (The batch kill is the one other teardown, and it journals in
// its own loop for the same reason — see killIOCmd.)
//
// Both verbs are also screened by retire.Admits first. That rule is about what a verb
// can act on at all rather than about work at risk, so it applies to the ungated verb
// too: a pause of a session whose Start is still in flight would kill the tmux session
// that Start is creating and remove the directory its Setup is still populating.
func (m *home) executeRetirement(mode outbox.Mode, inst *session.Instance) (tea.Cmd, retire.Verdict) {
	cleared := retire.Verdict{Condition: retire.Clear}
	if v := retire.Admits(retireVerb(mode), instanceState(inst)); !v.Allowed() {
		return nil, v
	}
	label := fmt.Sprintf("%s '%s'…", mode.Gerund(), inst.DisplayName())
	if mode == outbox.ModePause {
		return m.beginAsyncAction(label, pauseIOCmd([]*session.Instance{inst})), cleared
	}
	if v := m.retireVerdict(inst); !v.Allowed() {
		return nil, v
	}
	arm, action := m.armTeardown([]*session.Instance{inst}, killIOCmd(m, inst))
	// On the update thread, which is the only place the retiring mark and the
	// WaitGroup may be touched — the same place and the same order the accepted
	// confirmation applies them in.
	arm()
	return m.beginAsyncAction(label, action), cleared
}

// retireVerb translates a spool record's mode into the verb the shared rules take. Two
// enums rather than one because internal/retire must not import the spool: the decision
// is meant to be reachable from a table test with no data dir in sight.
func retireVerb(mode outbox.Mode) retire.Verb {
	switch mode {
	case outbox.ModeKill:
		return retire.Kill
	case outbox.ModePause:
		return retire.Pause
	default:
		// Not a fallthrough to either verb: retire.Admits refuses an unnamed one, which
		// is the answer a record asking for something this build does not recognise
		// should get. readRetire screens the same field, so reaching here means a new
		// mode was added without teaching this.
		return retire.VerbUnknown
	}
}

// instanceState reads the three lifecycle facts retire.Admits decides on off a live
// instance. The command's side reads the same three off a decoded InstanceData; this is
// the translation, and the rule itself is shared.
func instanceState(inst *session.Instance) retire.State {
	return retire.State{
		Direct:  inst.IsDirect(),
		Paused:  inst.GetStatus() == session.Paused,
		Loading: inst.GetStatus() == session.Loading,
	}
}

// retireVerdict re-runs the safety gate on what the model currently knows about inst.
//
// GetDiffStats may be nil — a session whose stats have not been computed yet — and
// that reaches the gate as nil rather than being smoothed into a zero DiffStats,
// because the gate's whole job is to tell "nothing at risk" from "nothing measured".
// A DiffStats whose git commands failed says so through BranchStatsMeasured, which the
// gate reads for the same reason.
func (m *home) retireVerdict(inst *session.Instance) retire.Verdict {
	return retire.Gate(inst.GetDiffStats(), agentWorking(inst.GetStatus()))
}

// agentWorking reports whether a status means the agent has a turn or background work
// in flight, which is the "busy" input the gate takes.
//
// Pending counts, and it is the one that would be missed: the main turn has ended, so
// the row reads as finished, while sub-agents or a background shell the turn left
// running are still going. Ready and NeedsInput are idle — a session blocked on a
// permission prompt is waiting on a person, not working. Loading is not here because
// retire.Admits refuses it outright, with a reason that names the actual hazard (a
// teardown racing the Start) rather than calling a session that has not begun busy.
func agentWorking(s session.Status) bool {
	switch s {
	case session.Running, session.Pending:
		return true
	default:
		return false
	}
}
