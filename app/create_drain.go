package app

// create_drain.go — creating the sessions requested by `atrium new` (#703).
//
// The external command is a pure producer, for outbox_drain.go's reason: it drops
// a request into <data dir>/outbox/create and exits, never touching state.json,
// because that file has exactly one writer at any instant. This is the consumer
// side, and it runs on the Bubble Tea update goroutine — the only place model
// state may be mutated — so the TUI remains both the single writer and the sole
// session creator, which is what lets the autoyes daemon keep snapshotting the
// instance list once for its lifetime.
//
// Creation goes through startNewSession, the same form-free core the create form
// and smart auto-dispatch reach, so a spooled request inherits every gate they
// enforce rather than a second opinion about them. What it cannot inherit is the
// two gates that ask the user a question: a headless request answers those in
// advance with Force, or is refused.

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// createStartBudget caps how many SPOOLED session starts may be in flight at once,
// counting the ones still running from earlier ticks — not how many this tick may
// begin. One, where the prompt drain's budget is 50: queuing a prompt is a map write,
// while creating a session builds a git worktree, runs the repo's setup script and
// launches a program in a pty. A script that spooled twenty would otherwise have twenty
// worktree setups running at once, contending on the same index.lock and coming back
// to their callers as arbitrary-looking failures.
//
// Spooled, and only spooled: it is seeded from createsInFlight, which holdCreateRequest
// alone writes, so it does not see the TUI's own starts. A variant fan-out from the
// KEYPRESS spawns up to session.MaxVariantBatch sessions inside one Update, all in one
// repo, and this constant neither counts nor limits them — a human asking for twenty is
// not the accident this guards against, and refusing a spooled create because a fan-out
// is mid-flight would make `atrium new` fail for a reason its caller cannot see. The
// number to hold in mind is therefore "one from the spool, plus whatever the keyboard
// started".
//
// A spooled fan-out (`atrium new --variants`) is on the other side of that line, and
// deliberately: its members are ordinary records, so they are counted and limited here
// like any other, one per tick. Deliberate rather than accidental they may be, but they
// are also N worktree setups the caller is not watching, and staggering them is what
// keeps them off one index.lock. What it costs is stated where the caller can act on
// it — a --wait over a batch has to be sized for N builds in series, which is newCmd's
// Long to say.
//
// Counting in-flight starts is the whole point. A per-tick budget looks identical in a
// single-tick test and delivers only a ~500ms stagger in production, because tick N+1
// skips the still-running request without spending anything and starts the next one
// anyway.
const createStartBudget = 1

// createGateBudget caps how many requests one tick may run through the creation gates.
//
// One, because those gates are git. Before executeCreateRequest can know whether a
// request ends in a start or a refusal it runs targetValidity (git.IsGitRepo, then
// git.CurrentBranchName), git.RepoGroupKey (rev-parse --show-toplevel), and — for a
// non-direct target — createConflictIn (git.LocalBranchExists) and allExhausted,
// whose resolveSpawnPool adds git.GetRemoteURL. Five subprocess round trips,
// synchronously, on the Bubble Tea update goroutine, before a verdict exists.
//
// A direct target spends three, not five. It skips CurrentBranchName (targetValidity
// returns before it for a non-repo), LocalBranchExists (branchSlugConflict returns ""
// for direct — no worktree, no branch to collide with) and GetRemoteURL
// (resolveSpawnPool guards it on !plan.direct, app_session.go), and adds
// git.ProbeGitRepo, which confirms the direct verdict rather than inheriting IsGitRepo's
// guess (see executeCreateRequest). An Adopt request — one the startup reconcile
// re-queued to finish an interrupted build — reaches a different total by a different
// route: a git target's five, minus the LocalBranchExists it skips deliberately rather
// than for want of a branch, so four when its pin holds and five when it does not,
// because the withdrawal puts LocalBranchExists back. The pin re-check itself is one more
// and is charged elsewhere — see createRecheckBudget.
//
// Redundancy in that count is deliberate. IsGitRepo, RepoGroupKey and — on the direct
// path — ProbeGitRepo each run `rev-parse --show-toplevel` on the same path, and
// CurrentBranchName resolves a head branch this path discards (it is the branch
// picker's default label, which a headless request has no use for). Collapsing them
// means the drain computing "is this a git target, and what group is it" by hand
// instead of through the helpers the create form uses — a fourth hand-copied
// pre-flight, which is the more expensive mistake.
//
// The ACCEPT path adds more, and only for a git target. startNewSession resolves
// GetRemoteURL a second time (guarded on !direct, app_session.go, the same guard
// resolveSpawnPool carries), and holdCreateRequest runs git.LocalBranchExists again to
// record what was true of the session branch at the instant it claimed the request
// (#716) — guarded on !inst.IsDirect(), having no branch. So two more for a git target
// and ZERO for a direct one: both additions are on the same side of the same
// discriminator. Neither is in the counts above, which are what reaching a VERDICT
// costs; a refusal pays for neither.
//
// Bounding starts alone does not bound that work: a refusal spends no start budget, so
// a backlog refused for a full cap would gate every one of fifty requests inside a
// single Update. At one, a tick evaluates no more of it than submitting the create form
// does, which is the only comparable synchronous git this loop already carries.
const createGateBudget = 1

// createDisposalBudget caps how many requests one tick may discard without gating them:
// unreadable files and expired ones, which cost a terminal mark, a receipt and an unlink
// apiece. Larger than the budgets above because there is no git in that path — but not
// unbounded: a two-day-old cron backlog would otherwise unlink thousands of expired requests
// inside one Update, freezing the UI. Bounded, a backlog clears at 50 a tick.
//
// It bounds the writes, not the reads. ListCreates is called before any budget applies
// and decodes every file in the spool, so an N-request backlog still costs N ReadFile +
// N Unmarshal on this goroutine, twice a second, until it clears. That is the honest
// shape of "stays responsive" here: the unlink storm is spread out, the decode is not.
//
// A refusal that came out of the gates does not draw on it. The expensive part is
// already paid by then, and withholding the receipt would only mean paying it again
// next tick while the caller's --wait keeps blocking.
const createDisposalBudget = 50

// createRecheckBudget caps how many adoption pins one tick re-checks against git
// (recheckAdoption). One for-each-ref apiece, on the update goroutine, so it needs a bound
// for createGateBudget's reason.
//
// Its own budget rather than a share of that one, and the difference is starvation. A
// re-check that CANNOT be answered holds its request without producing a verdict, and the
// spool is walked oldest-first, so a single repo git cannot read at the head of the queue
// would spend the tick's one gate on every tick while every ordinary `atrium new` behind it
// waited — for up to the 24h TTL, with no receipt and no notice. Charged separately, the
// unanswerable request costs a re-check and the request behind it is still created.
//
// Two rather than one, and the second slot is reachable only on the failing path: a re-check
// that SUCCEEDS goes straight on to the gates, and createGateBudget allows one of those per
// tick, so a tick that answers one adoption cannot reach a second whatever this is set to.
// What two buys is a tick whose FIRST re-check could not be answered still reaching the next
// record — one unreadable repo does not stall the queue behind it for a whole tick. Small
// because the number of Adopt records in flight is bounded by how many claims have accumulated
// across crashes, which is a handful and not a backlog.
//
// What it does not fix, and what is worth naming rather than implying: the walk is
// oldest-first and remembers nothing about which records it could not answer for, so two
// repos git cannot be RUN for at the head of the queue hold every later ADOPTION behind them
// until one of the three reaches the TTL. Only git failing to run gets there — a repo that has
// been deleted, or is no longer a repository, is something git answers, and recheckAdoption
// reads an answer as a fact about the request and lets the gates settle it in one tick. That
// leaves head-of-line blocking among recovery requests waiting on a broken git only; an
// ordinary `atrium new` carries no pin, needs no re-check, and is gated in the same tick.
// Accepted rather than solved, because solving it means per-record state about a condition
// that is usually a drive coming back in a few seconds.
const createRecheckBudget = 2

// createBatchRefusalBudget caps how many of one batch's other members a single cap
// refusal answers in one tick (#761).
//
// A batch that does not fit has to be refused whole, or the cap creates it from the
// tail: charged for the pending members, member 1 is refused, then member 2, and then
// member 3 fits in the room the first two did not take. So the refusal fans out — and
// the fan-out is receipts and unlinks rather than git, which is why it is not charged
// to createGateBudget.
//
// It is bounded anyway, at the largest batch this atrium's own `atrium new` can mint.
// Anything wider was hand-written or came from a build with a different limit, and
// answering five hundred records inside one Update — a receipt and an unlink apiece,
// through outbox.Reject — is the freeze createDisposalBudget exists to prevent.
//
// A batch wider than the budget is therefore NOT refused whole, and the honest statement
// of what happens is the opposite of reassuring: each later tick re-charges the cap for
// what is still pending, so a batch of 26 under a cap with room for 5 has 21 members
// answered on the first tick and the remaining 5 fit — and are created, from the tail,
// with 21 callers holding receipts saying the batch was refused whole. Nothing this
// atrium can produce reaches that, because parseVariantSpec is held to the same constant;
// a hand-written spool can, and #783 is where raising the ceiling has to answer for it.
const createBatchRefusalBudget = session.MaxVariantBatch

// createBatchAssemblyWindow is how long a batch may be short of the size its author
// declared before the drain gates it on what is actually there.
//
// It bounds the hold that closes the publish race outbox.Request.BatchSize documents, and
// every way it can be wrong is a wait rather than a wrong verdict. A batch genuinely still
// being written finishes inside it by orders of magnitude — a handful of atomic writes
// against a window measured in seconds. A batch that will never reach its declared size
// waits this out and is then charged for its real membership: a rollback whose withdrawal
// failed, a member the disposal arm answered, a sibling too corrupt to decode, or the
// ordinary mid-batch case where a member is already a live session. That last one is why
// the hold cannot simply be "wait until the batch is complete" — for a batch the drain is
// partway through, complete never comes again.
const createBatchAssemblyWindow = 3 * time.Second

// batchStillAssembling reports whether the pending members of one batch are only part of
// the batch its author committed, because the rest of it is still being written.
//
// It answers about the BATCH and not about whichever member the walk is holding, which is
// what makes one verdict cover the whole of it on one tick. Asked per member instead, it
// says "wait" for the head and "go ahead" for everything behind it — so the walk holds the
// head, reaches member 2, gates that, and refuses the batch it had just decided to wait
// for. The sibs slice is the same list for every member, so every member of a batch gets
// the same answer without a per-tick set to remember it in.
//
// Two conditions, and neither is enough alone. A batch is short of its declared size for
// most of its own life — the drain builds one member per tick, so a batch of three spends
// two ticks short — and holding for that would stall the feature rather than protect it.
// What separates "still arriving" from "being consumed" is the HEAD: members are written
// in order, so an incomplete batch always still has member 1 pending, and a batch the
// drain has started on never does. Hence the BatchIndex test, which is a property of the
// records and needs nothing remembered between ticks.
//
// The clock is the head's own CreatedAt, which is the batch's first write. It bounds the
// hold for a batch that will never reach its declared size — a rollback whose withdrawal
// failed, a sibling too corrupt to decode, a member an earlier launch gave up on and
// pendingBatchMembers therefore does not count — which would otherwise wait for its TTL.
//
// Nothing is logged for a hold. It is normally shorter than one poll tick, so a line per
// tick per assembling batch would be noise about a condition that resolves before anyone
// could act on it; and the hold spends no budget, runs no git and writes nothing, so there
// is no side effect for a reader to have to account for later.
func batchStillAssembling(sibs []outbox.CreateEntry, now time.Time) bool {
	if len(sibs) == 0 {
		return false
	}
	// The oldest pending member: pendingBatchMembers preserves ListCreates' oldest-first
	// order, and WriteCreate names each record after its own CreatedAt.
	head := sibs[0].Request
	if head.BatchIndex != 1 || head.BatchSize <= len(sibs) {
		return false
	}
	return now.Sub(head.CreatedAt) < createBatchAssemblyWindow
}

// drainCreateRequests creates the sessions spooled by `atrium new` and returns a
// command that boots them plus a notice, or nil when there was nothing to do.
//
// It runs in every UI state, deliberately. An earlier version skipped anything but
// stateDefault, on the theory that creating a session under an open overlay could
// surprise it; driving a real first-run TUI showed what that actually buys — the
// welcome modal is a state, and a fresh install sits in it until someone answers
// it, so `atrium new` could not create the first session on a machine nobody had
// used interactively. That is the deadlock the whole feature exists to remove.
//
// Nothing needed the gate. Confirmations do not re-read the cursor: confirmKill and
// confirmWorktreeAction capture their instance when the dialog is staged, so an
// accepted dialog acts on the session it named whatever the list did. The create form
// has live per-keystroke verdicts about a title and a cap, but re-runs both at submit
// (createSessionFromForm), so a session appearing underneath is refused there rather
// than let through. And for a spawnBackground create startNewSession leaves the cursor
// on the row it was on and asks for no resize. (It does still run instanceChanged, which
// repoints preview, diff and menu at the selection — but the selection did not move, and
// the 100ms preview tick runs the same call regardless.)
//
// "Leaves the cursor" is the guarantee, and folds are subordinate to it rather than
// equally guaranteed: AddInstanceKeepingFolds keeps the fold set as it found it in every
// case except the one where honouring a fold would have hidden the selected row, where
// it drops that single fold to keep the row. Read the inverse — a spawnBackground create
// never moves the cursor to a different session — since that is the half a destructive
// keypress depends on.
//
// The one thing a state can still notice is the notice itself, which is why the two are
// not raised the same way. menuVisible is false in all fifteen modal states, so
// flashNotice falls back to the errBox row and recomputes the layout — the panes shrink
// by a row under the open overlay and grow back when the toast expires, which is the
// #518 shape. A create nobody asked for must not do that, and does not need to: its
// evidence is the row now in the list. A refusal is the opposite case — invisible at the
// TUI otherwise, and actionable only by the person there — so it keeps the fallback and
// pays the row.
//
// Four things do hold it, none of them a state: a staged spawn plan, a pending quit, an
// in-flight async action and an in-flight teardown. See createDrainHeld.
func (m *home) drainCreateRequests() tea.Cmd {
	if m.createDrainHeld() {
		return nil
	}

	entries, err := outbox.ListCreates()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool: %v", err)
		return nil
	}

	if len(entries) == 0 {
		// Through the transition rather than around it: an empty spool holds nothing, so
		// this is where a hold whose request expired or was reset away is lifted. Both
		// holds, because both are lifted by the record going away and neither can see
		// that from inside a walk that does not run.
		m.noteAdoptHold(nil, false)
		m.noteUnreadableMark("", false)
		return nil
	}

	// tmux missing holds every startable request in this tick instead of refusing it,
	// and so is probed here rather than taking its turn among the gates in
	// executeCreateRequest. Every gate there is a fact about the *request* — a taken
	// title, a full cap, a path that is not a directory — which stays true until a
	// person changes it, and that is what makes a receipt-and-unlink the right answer
	// for one. tmux being off PATH is a fact about the machine, no fault of the request,
	// and tmux.Available re-runs exec.LookPath on every call rather than caching, so the
	// very next tick can see it come back. Refused, the window of a `brew upgrade tmux`
	// would destroy every queued create in it — receipt written, record unlinked, for a
	// condition that cleared seconds later and that no caller can act on anyway. Holding
	// costs the request nothing: same TTL, created when tmux returns.
	//
	// Once per tick, not once per entry, and only with a non-empty spool: an idle Atrium
	// pays no probe at all, and a backlog pays one rather than one apiece.
	//
	// Disposals are deliberately not held. An expired or undecodable record needs no
	// tmux to discard, and its caller is owed the receipt whatever the machine is doing.
	tmuxDown := tmuxAvailable()
	switch {
	case tmuxDown != nil && !m.createTmuxHeld:
		m.createTmuxHeld = true
		// The count is of the SPOOL, and says so. It was written as the number of requests
		// held, which it is not: the disposals the paragraph above exempts are in it, and
		// they are answered on this very tick. Naming the spool depth is the true statement
		// and the useful one — it is what an operator wants to know while tmux is down.
		log.WarningLog.Printf("holding create requests until tmux is usable again "+
			"(%d record(s) in the spool): %v", len(entries), tmuxDown)
	case tmuxDown == nil && m.createTmuxHeld:
		m.createTmuxHeld = false
		log.InfoLog.Printf("tmux is usable again; resuming create requests")
	}

	now := time.Now()
	// Seeded with the starts still running from earlier ticks: the budget is on
	// concurrency, not on arrivals.
	started := len(m.createsInFlight)
	var gated, disposed, rechecked int
	// The re-check that could not run this tick, if any. Reported after the walk rather
	// than at the call, so the hold is logged on its transitions the way the tmux one is
	// — and so a held request that later expires out of the spool lifts the hold instead
	// of leaving a bool nothing ever clears.
	var adoptHoldErr error
	// Whether any Adopt record is still in the spool that this tick did not re-check. It
	// is the other half of lifting the hold: adoptHoldErr going nil says nothing on its
	// own, because every skip below reaches the end of the walk with it nil — tmux off
	// PATH, a spent budget, a poisoned path, an unwalked tail. Reading that as "the
	// re-check works again" logs a resume for a request still held and leaves the flag
	// clear for the next genuine hold to go unlogged.
	var adoptPending bool
	// The record whose terminal mark could not be looked for, if any. Reported after the
	// walk on its transitions, for adoptHoldErr's reason: the condition is a broken spool
	// rather than anything about the request, and the walk runs twice a second.
	var markUnreadable string
	// Whether any record reached the end of this walk without its mark being established —
	// adoptPending's job for the other flag, and needed for the same reason. markUnreadable
	// going empty says nothing on its own: the composite break below leaves the whole tail
	// of the spool unprobed and a poisoned path is never probed at all, so both arrive here
	// with it empty while a record is still held. Read as "the spool can be stat'd again",
	// that logs a resume for a request nobody looked at and clears the flag the next genuine
	// hold needed.
	var markPending bool
	anyAdopt := func(rest []outbox.CreateEntry) bool {
		for _, e := range rest {
			if e.Err == nil && e.Request.Adopt {
				return true
			}
		}
		return false
	}
	var cmds []tea.Cmd
	// Gate refusals only. A disposal is charged to disposed and stays off this counter
	// deliberately — see the notice below for why an expired file must not raise one.
	var refused int
	// refusedRecords counts the requests answered, where refused counts the gate
	// verdicts that answered them. They are the same number until a batch is refused
	// for the session cap, which is one verdict fanned out across every pending member
	// of that batch — so the notice reads this one and the budget reasoning above still
	// reads the other.
	var refusedRecords int
	// The batch members a cap refusal has already answered on this tick, so the rest of
	// the walk does not re-judge them from the listing it was handed before the refusal.
	answeredInBatch := map[string]bool{}

	for i, e := range entries {
		// Nothing more can be gated (so nothing more can be started or refused) and
		// nothing more can be discarded: the rest of the spool is next tick's.
		//
		// Only a spool with 50+ disposable records reaches this; a healthy backlog
		// leaves disposed at 0 and is bounded by the per-arm `continue`s below instead,
		// walking the remaining entries to a map lookup and one os.Stat apiece — the
		// terminal-mark probe, which is above every budget except this break because what
		// it decides is whether the record may be touched at all. Not worth a cleverer
		// condition while ListCreates has already opened and decoded every one of them
		// anyway.
		//
		// This break is what makes the two hold flags need their pending companions: the
		// tail it leaves is neither re-checked nor probed, so both conditions arrive at
		// the notices below looking clear.
		if (started >= createStartBudget || gated >= createGateBudget) && disposed >= createDisposalBudget {
			adoptPending = adoptPending || anyAdopt(entries[i:])
			markPending = true
			break
		}
		// No in-flight check here, and that is the claim doing the work rather than an
		// omission: holdCreateRequest renames an accepted request out of the record
		// name format, so ListCreates does not return one that is still starting. What
		// used to be a linear scan of createsInFlight is now that rename — and the arm
		// it guarded that actually bit was the expiry one below, where a start crossing
		// the 24h horizon mid-flight was rejected and unlinked out from under its own
		// running session. A claimed request is not walked at all, so it cannot expire
		// while it is being built.
		//
		// "Does not" rather than "cannot", because the rename can fail. holdCreateRequest
		// builds the session anyway rather than refusing one whose worktree is already
		// going up, and poisons the path on its way past — which is why the skip below
		// is load-bearing for that case and not only for an unlink that failed.
		if m.outboxPoisoned[e.Path] {
			adoptPending = adoptPending || anyAdopt(entries[i:i+1])
			markPending = true
			continue
		}

		// Already answered by a batch refusal earlier in THIS walk. The listing was taken
		// before the fan-out ran, so the siblings it refused are still in it — and reaching
		// them again would find the mark it just wrote and take the arm below for a request
		// this tick gave up on a moment ago: a second receipt for a caller that may already
		// have read and cleared the first, a disposal budget spent undoing this tick's own
		// work, and a log line saying "atrium had already given up on" a refusal it is still
		// in the middle of making. Their marks are left to be cleared where every other
		// inventory-less mark is, at the next launch's flush.
		if answeredInBatch[e.Path] {
			continue
		}

		// A disclosure beside the record says this request has already been given up on
		// and its caller answered — by an earlier tick whose unlink failed, or by an
		// earlier process. It is the record-shaped twin of the state claimAnswered covers
		// for a claim, and it outranks every arm below for that arm's reason: the rest of
		// this walk asks whether the request CAN be executed, and this says it must not
		// be, whatever the answer. Without it the terminal mark guards only the claim, and
		// a refusal whose Reject could not unlink builds the session on the next tick.
		//
		// The receipt is re-written rather than assumed, for applyCreateClaim's reason:
		// the window Disclose is ordered ahead of Reject to survive is exactly the one
		// where no receipt was written at all, and an unlink with nothing beside it is
		// what awaitSpool reads as success.
		//
		// A mark that could not be looked for holds the record instead of guessing, which is
		// the tmux probe's distinction one more time: destroying it would answer a caller
		// with a reason nobody wrote, and executing it would build the session an earlier
		// launch may already have refused. Held, the record keeps its place and its TTL.
		//
		// Its TTL, and that is why the hold is conditional on the record not being past it.
		// The hold is a `continue` ABOVE every arm below, so a record it covers reaches no
		// disposal either: nothing sweeps a record (sweepSuffixed only ever walks the two
		// suffixed kinds) and nothing poisons its path, so a spool the stat keeps failing on
		// held it for the life of this TUI and of every one after it — re-listed and
		// re-probed twice a second, with `atrium reset` the only way out. Past the horizon
		// the disposal wins instead: a second receipt for a caller that may already have one
		// is a duplicate 24 hours late, against a file no atrium can ever let go.
		//
		// "Past the horizon" needs a timestamp to be past it, which is why this reads e.Err
		// rather than folding it in as another way of being disposable. An undecodable record
		// has no CreatedAt — e.Request is the zero value — and reading that as "spooled at the
		// epoch, therefore expired" is the mistake this whole probe exists to stop, one type
		// down: it converts "I could not find out how old this is" into the answer that
		// destroys the record. So a record that can be decoded gets the horizon the paragraph
		// above promises, and one that cannot, whose mark ALSO cannot be stat'd, is held
		// indefinitely and `atrium reset` is its remedy. Two independent failures of the same
		// spool are what it takes to reach that, and the cost of being there is two syscalls
		// per tick rather than anything destroyed.
		//
		// Probed rather than read, and the body fetched only by the arm that uses it. The
		// walk stats every record in the spool twice a second while its disposal budget
		// skips most of a backlog, so folding the read in charged a ReadFile and an
		// Unmarshal per skipped record for a Disclosure it discarded (outbox.DisclosureMark).
		disposable := e.Err == nil && e.Request.Expired(now)
		mark := outbox.DisclosureMark(e.Path)
		if mark == outbox.DisclosureUnknown {
			markPending = true
			if !disposable {
				markUnreadable = e.Path
				adoptPending = adoptPending || e.Request.Adopt
				continue
			}
		}
		if mark == outbox.HasDisclosure {
			// Charged here rather than in the skip below, because the DROP is the path that
			// needs it: discardSpoolFile's unlink is the very call that failed when this mark
			// was written, so the record routinely survives this arm and is never re-checked
			// — the arm returns above the re-check. Read from the skip alone, a failed drop
			// logged a resume for a request still held and left the flag clear for the next
			// genuine hold to go unlogged. Over-reporting pending costs one resume line the
			// log never prints, which is the direction noteAdoptHold already documents.
			adoptPending = adoptPending || e.Request.Adopt
			if disposed >= createDisposalBudget {
				continue
			}
			// NoDisclosure here means the file went between the probe and the read — another
			// atrium's reader clearing a delivered mark — so there is no mark after all and
			// the ordinary arms below judge the record. Answering it terminal on the strength
			// of a stat that is no longer true would hand a healthy request a receipt saying
			// an earlier atrium gave up without recording why.
			if d, state := outbox.DisclosureFor(e.Path); state != outbox.NoDisclosure {
				reason := answeredReason(d)
				// The record's own title, falling back to the mark's. An undecodable record
				// has none — e.Request is the zero value on that arm — and this line is the
				// only account of the drop that survives it: the record and the mark are both
				// destroyed two calls later.
				title := e.Request.Title
				if title == "" {
					title = d.Title
				}
				log.WarningLog.Printf("dropping a create request for %q that atrium had already "+
					"given up on: %s", title, reason)
				m.discardSpoolFile(e.Path, func() error { return outbox.Reject(e.Path, reason) })
				// Recorded before the mark is cleared, and that order is the whole point:
				// clearMarkOverADroppedRecord is about to make this path read NoDisclosure,
				// so a batch gated LATER in this same walk would find nothing to skip it by
				// and refuse it a second time — replacing the account whoever gave up wrote
				// with a cap reason, at a path whose record is already gone. The mark skip
				// in pendingBatchMembers cannot cover this one; only the walk knows.
				answeredInBatch[e.Path] = true
				m.clearMarkOverADroppedRecord(e.Path)
				disposed++
				continue
			}
		}

		// Discards, gate evaluations and starts draw on separate budgets, so a backlog
		// of expired requests cannot spend the one start this tick was allowed to make —
		// and cannot buy itself unbounded git either. Each arm charges its own budget,
		// so which one paid is read at the call site rather than through a parameter.
		// The request as THIS tick will execute it, plus the branch a refusal of it owes a
		// disclosure. Adopt can be withdrawn and the branch usually cannot: the licence to
		// skip the branch gate is about now, while the branch itself was made by an
		// interrupted build whose worktree registration applyCreateClaim already released
		// on this request's behalf. Reading the flag for both — which this did — makes
		// every withdrawal a silent orphan, which is the hole this is here to close (see
		// rejectCreateRequest). The one withdrawal that clears the branch too is the one
		// where git says it is gone.
		//
		// So the branch comes from the evidence block and not from the flag, and it has to:
		// outbox.WithdrawAdoption persists the cleared flag, so a record that reaches a
		// later tick or a later launch after a withdrawal landed reads Adopt false while
		// its branch is still standing. Guarded on the flag, that record named no branch
		// anywhere — and it is the one the arm below has just released the registration
		// for. What the evidence block over-reports instead is a branch a successful
		// teardown deleted, which costs the reader one row saying so.
		req := e.Request
		orphan := claimedBranch(req)
		reject := func(reason string) {
			m.rejectCreateRequest(e.Path, req, orphan, reason)
		}
		// The disposal arms answer differently. See disposeCreateRequest: they are terminal
		// however often they repeat, so a mark buys them no guard, and one per record per
		// tick is what a cron backlog would mint.
		dispose := func(reason string) {
			m.disposeCreateRequest(e.Path, req, orphan, reason)
		}

		switch {
		case e.Err != nil:
			if disposed >= createDisposalBudget {
				continue
			}
			// Unreadable, or from a newer atrium. Discarding is the only way out: a
			// file nobody can decode and nobody deletes would be re-read on every
			// tick forever. ListCreates only ever surfaces files matching the
			// spool's own name format, so this can only discard our own.
			//
			// e.Request is the zero value on this arm, so orphan is empty and stays
			// that way: a record nobody can decode names no branch to disclose. With
			// nothing to report and no guard to buy, this arm writes no mark at all.
			log.ErrorLog.Printf("discarding an unreadable create request: %v", e.Err)
			dispose("the request could not be read")
			disposed++

		case e.Request.Expired(now):
			if disposed >= createDisposalBudget {
				adoptPending = adoptPending || req.Adopt
				continue
			}
			// Disclosed without a re-check, deliberately. This arm is the one that
			// destroys the record permanently — the recovery is one-shot — so a branch it
			// does not name is named nowhere ever again, while a branch named after
			// somebody deleted it costs the reader one row that says so. The asymmetry is
			// the whole argument for over-reporting (see outbox.Disclosure). It is also
			// why this arm keeps its disclosure while the mark it no longer writes goes:
			// what it has is a report, and the report is owed.
			age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a create request for %q: spooled %s ago, past the %s horizon",
				e.Request.Title, age, outbox.TTL)
			dispose(fmt.Sprintf("the request was spooled %s ago, past the %s horizon", age, outbox.TTL))
			disposed++

		default:
			// Held, not refused — see the probe above.
			if tmuxDown != nil {
				adoptPending = adoptPending || req.Adopt
				continue
			}
			// Both budgets, before the gates rather than after: whether this request
			// ends in a session or a receipt, finding out costs the same git either
			// way (see createGateBudget).
			if started >= createStartBudget || gated >= createGateBudget {
				adoptPending = adoptPending || req.Adopt
				continue
			}
			// The adoption pin, re-checked here rather than trusted from the record.
			// Before the gates, because what it decides is which gate applies.
			//
			// Held, not refused, when git cannot answer — the tmux probe's distinction,
			// and for the same reason twice over. A repo that cannot be read is a fact
			// about the machine, not about the request; and this particular request is
			// the second attempt at a build that already made a branch, so refusing it
			// spends the one recovery a stranded create gets on a condition that may
			// clear on the next tick.
			if req.Adopt {
				// Charged before the call, and to a counter of its own. The re-check IS
				// git, and the `continue` its failure takes skips the rest of this entry
				// — so a counter incremented below it never moves, and a backlog of
				// Adopt records git cannot answer for spends one subprocess each, on
				// every tick, for up to the 24h TTL. See createRecheckBudget for why the
				// gate budget is the wrong one to charge it to.
				if rechecked >= createRecheckBudget {
					adoptPending = true
					continue
				}
				rechecked++
				state, err := m.recheckAdoption(req)
				if err != nil {
					adoptHoldErr = err
					continue
				}
				if state != adoptionHolds {
					// The branch is not the one the reconcile vetted, so the licence to
					// skip the branch gate is withdrawn and the ordinary gate applies:
					// a branch that has since gone refuses nothing and the session is
					// created fresh, while one that came back under the same name with
					// somebody else's commits is refused for it. Both are what would
					// have happened had the crash never occurred.
					req.Adopt = false
					req.AdoptTip = ""
					// On disk as well as in this copy, because outbox.Claim re-reads the
					// record: a withdrawal kept here alone writes a claim carrying a
					// licence this build did not use, and the next launch reads that
					// licence to tell "this session's own half-built branch" from
					// "somebody else's branch is in the way" (classifyCreateClaim's
					// BranchExisted arm). Logged rather than refused if it fails — the
					// build below is correct either way, and spending a stranded create's
					// one recovery on a bookkeeping write would not be.
					if err := outbox.WithdrawAdoption(e.Path); err != nil {
						log.ErrorLog.Printf("failed to record that the create request for %q "+
							"is no longer adopting a branch: %v", req.Title, err)
					}
				}
				if state == adoptionGone {
					// And only here is the branch off the disclosure too: git has just
					// said there is nothing at that name, so a refusal for some other
					// reason has no leftovers of its own to report.
					orphan = ""
				}
			}
			// The cap is charged to the batch, not to the member. A fan-out's records
			// are ordinary requests everywhere else in this walk; this is the one place
			// they are read as a group, so a batch that does not fit is refused whole
			// rather than created from the tail as the cap closes (#761).
			//
			// Never for a member that is adopting, though, and that exception is the
			// same one refuseBatchSiblings makes for a sibling: the batch charge exists
			// to produce a refusal, and refusing an adopting request is destructive
			// beyond the record (see rejectCreateRequest) with a one-shot recovery.
			// Charging one for it is what this line did before batches existed, so an
			// adopting request re-queued into a batch — Requeue rewrites in place and
			// keeps its Batch — is gated on exactly the terms it was gated on before.
			// It costs the batch's whole-or-nothing promise for that member, which is
			// the trade the sibling skip already makes and which #782 records.
			adding := 1
			var sibs []outbox.CreateEntry
			if req.Batch != "" && !req.Adopt {
				sibs = m.pendingBatchMembers(entries, req.Batch, now, answeredInBatch)
				adding = len(sibs)
				// Held rather than gated, and held before the gate is charged: no git
				// ran, no budget was spent and nothing was decided, so the next tick
				// judges this member against a batch that has finished arriving. The
				// alternative is to charge the cap for a fraction of a batch, which is
				// precisely how a whole-or-nothing gate creates one from the head.
				if batchStillAssembling(sibs, now) {
					continue
				}
			}
			gated++
			inst, cmd, verdict := m.executeCreateRequest(req, adding)
			if verdict.reason != "" {
				reject(verdict.reason)
				refused++
				refusedRecords++
				// adding > 1 rather than req.Batch != "", and the difference is the case
				// that matters: a batch whose other members have all been claimed,
				// created or disposed of has nobody left to answer, and adding == 1 says
				// exactly that without a second predicate to keep in step.
				if verdict.bySessionCap && adding > 1 {
					sibsRefused := m.refuseBatchSiblings(sibs, e.Path, verdict.reason, answeredInBatch)
					refusedRecords += sibsRefused
					log.WarningLog.Printf("refusing %d create requests from one atrium new batch, "+
						"starting with %q: %s", sibsRefused+1, req.Title, verdict.reason)
				} else {
					log.WarningLog.Printf("refusing a create request for %q: %s", req.Title, verdict.reason)
				}
				continue
			}
			// The request deliberately stays on disk — as a claim — until the start
			// lands and the row is persisted, so `atrium new --wait` reading the
			// absence of both means "created and recorded" rather than "consumed".
			m.holdCreateRequest(e.Path, req, inst)
			started++
			cmds = append(cmds, cmd)
		}
	}

	m.noteAdoptHold(adoptHoldErr, adoptPending)
	m.noteUnreadableMark(markUnreadable, markPending)

	// No title in either notice: a request's title has no length ceiling this side of
	// the wire format, and the notice row truncates its tail. Prose says why; the log
	// (and the caller's rejection receipt) says which.
	//
	// One caveat this cannot fix from here: drainOutbox runs first on the same tick and
	// may have flashed its own notice, which flashNotice — one surface at a time —
	// overwrites. A tick that both delivered a prompt and created a session shows only
	// the create. Both events still reach the log and the callers' receipts.
	switch {
	case len(cmds) > 0:
		// showMenuNotice, not flashNotice: behind an overlay it shows nothing rather than
		// taking the errBox row and reflowing the frame under it (see the header). Its
		// nil is fine — cmds is non-empty.
		//
		// What that trades away is smaller than "nothing", but it is not nothing. In
		// plain navigation the toast shows and the row is there to see. Behind a modal
		// there is no toast, and if the target repo's group is folded there is no visible
		// row either — AddInstanceKeepingFolds keeps that fold precisely so a background
		// create does not reorganise it, and isHidden then suppresses every non-anchor
		// member. What still moves is the folded header's own `NAME (n)` count
		// (renderRepoHeader), so the create is visible on the frame rather than invisible;
		// it is just not announced. The log line and the caller's --wait are the records
		// that do not depend on where the cursor or the folds happen to be.
		//
		// No "and N refused" clause either, because a tick cannot do both: reaching
		// either outcome costs a gate evaluation, and createGateBudget allows one per
		// tick, so refused is necessarily 0 here — and so is refusedRecords, which only
		// ever grows alongside it. A disposal can share the tick, but that is
		// deliberately not an event this notice reports (see below).
		cmds = append(cmds, m.showMenuNotice("created a session from atrium new", ui.NoticeInfo))
	case refusedRecords > 1:
		// A whole batch, answered by one verdict. It gets its own spelling rather than a
		// count for the reason the singular one below gives: nothing bounds a batch this
		// side of the wire format, so an interpolated number is one the row's truncation
		// could take. Naming the batch is what a reader needs — a cap they can raise
		// refused several requests at once, not one they might scroll past.
		return m.flashNotice("refused a batch of create requests from atrium new", ui.NoticeError)
	case refused > 0:
		// A refusal reaches the person who ran `atrium new` as a receipt, but the
		// person at the TUI is the one who can fix a cap or a taken title, and to them
		// a silent tick is indistinguishable from no request at all.
		//
		// Singular, with no count, because this arm is reached only when exactly one
		// REQUEST was answered. refused — the verdicts — can still only ever be 1: the
		// default arm spends a gate to reach a refusal and createGateBudget allows one
		// per tick, the same fact the create branch above uses to know it is 0 there.
		// What #761 separated out is that one verdict can now answer several requests,
		// which is what refusedRecords measures and what the arm above reports; an
		// unrelated backlog is still refused one per tick, each replacing this toast
		// rather than adding to it.
		//
		// That argument is what scopes this to gate refusals. A disposal — an expired
		// or undecodable file — is nobody's to fix, least of all from here, so counting
		// one would paint a red error over whatever drainOutbox flashed for the whole
		// time a cron backlog takes to clear at createDisposalBudget a tick. Disposals
		// get their log line and their receipt instead.
		return m.flashNotice("refused a create request from atrium new", ui.NoticeError)
	default:
		return nil
	}
	return tea.Batch(cmds...)
}

// createDrainHeld reports whether this tick must not create. All four cases are keyed
// on what is actually true of the model rather than on a UI state — a state gate is
// what deadlocked this drain behind the welcome modal.
//
// The third, actionInFlight, holds the drain to the bar handleKeyPress already holds a
// keypress to: while an async action runs, every mutating key is refused with a busy
// notice, so creating from the spool meanwhile would make `atrium new` the one way to
// mutate the list during a freeze that exists because those operations must not
// interleave. The deep rename is the case with teeth — renameIOCmd does the tmux
// rename, the `git branch -m` and the worktree move off-thread, and AdoptRename lands
// only afterwards, so mid-flight the instance still answers with its OLD title,
// variantTitleConflictIn sees no conflict for the new one, and a create that wins the
// branch check but loses the `git branch -m` reaches Worktree.Setup's existing-branch
// arm and adopts the branch the rename just made.
//
// The fourth, retiring, is what actionInFlight alone does not cover, and the pairing is
// shellStartRefused's (app_frames.go): asyncActionDoneMsg clears actionInFlight one
// message BEFORE killDoneMsg reaches the reap in applyKillDone, and messages are
// processed in between. A tick landing in that window sees a list that still holds the
// row being torn down, so a request reusing its title is told "already used" and any
// other request is gated against a capCount that still counts it. Either way the
// mismatch does not defer the request, it REFUSES it — receipt written, record
// unlinked — for a condition that is false one message later. That asymmetry is why
// this predicate has to be conservative where a key gate could afford not to be.
func (m *home) createDrainHeld() bool {
	return m.stagedSpawnPlan() || m.quitPending() || m.actionInFlight || m.teardownInFlight()
}

// teardownInFlight reports whether any row's kill is still between its dispatch and its
// reap. Bounded and reliably cleared: armTeardown marks only on an accepted confirm and
// endTeardown unmarks on every outcome, refusals included, both on the update thread.
func (m *home) teardownInFlight() bool {
	for _, retiring := range m.retiring {
		if retiring {
			return true
		}
	}
	return false
}

// quitPending reports whether the user has asked to quit and is waiting on a start.
//
// A deferred quit completes when nothing is Loading (resumeQuitAfterStart), so every
// session this drain starts postpones it by another Start. Before #703 that was
// bounded by what the user had submitted themselves; a spool is not bounded by
// anything, so a queue of twenty would keep building worktrees for a while after they
// pressed q — and each completion would re-arm the "waiting for startup" notice.
//
// Holding costs the requests nothing: they stay queued, inside the same TTL, and are
// created by the next Atrium — which is exactly what `atrium new` promises when no TUI
// is running at all.
func (m *home) quitPending() bool { return m.quitRequested }

// stagedSpawnPlan reports whether a confirmation dialog is holding a spawn plan that
// has already passed its title and cap checks.
//
// Of the four holds this is the one about corrupting another surface rather than about
// interleaving with work already under way.
// Accepting either confirm goes straight to spawnVariants, which re-validates nothing:
// creating a session in between would let the accepted plan spawn a duplicate title —
// and therefore a second session deriving one branch slug, which Worktree.Setup treats
// as a resume — or spawn past the cap the user was shown. Deferring is safe where a
// state gate was not: a staged plan means a human is looking at a dialog right now, so
// the wait is seconds, and the request is retried on the next tick either way.
func (m *home) stagedSpawnPlan() bool {
	return m.pendingOverCap != nil || m.pendingExhausted != nil
}

// pendingBatchMembers returns the entries of this tick's listing that belong to batch
// id and are still awaiting a verdict, including the one being gated — so the count is
// never zero and an ordinary request is never charged as a batch of none.
//
// Three kinds of entry are in the listing and are not going to become sessions, and
// counting one charges the cap for a session nobody will ever create:
//
//   - an undecodable record, which has no Batch to read at all and is therefore
//     invisible here rather than excluded. That is the one direction this cannot fix: a
//     corrupted sibling silently shrinks its own batch, so a batch of five is charged
//     for four and creates four. The disposal arm answers that member with a receipt,
//     so the caller is told; the batch is simply smaller than it asked to be.
//   - an expired one, which this tick's disposal arm is about to discard.
//   - a poisoned one, whose session is already being built and already counts toward
//     capCount. Poisoning lasts for the life of the process, so counting one would
//     over-charge every later member of that batch on every tick until the TTL.
//
// Two more are excluded for the same reason, and both cost a lookup rather than being
// read off the entry:
//
//   - one this walk has already answered, which is still in the listing because the
//     listing was taken before the walk began.
//   - one carrying a terminal mark, which an earlier tick or an earlier process gave up
//     on and whose caller has already been told.
//
// Statting for the mark is not the extravagance an earlier version of this comment
// called it: it is a read per member of ONE batch, on the one tick that batch is gated,
// not a read per record on every walk. It buys the thing the count and the refusal have
// to agree about — refuseBatchSiblings skips exactly these, so counting them would
// charge the cap for members it will not answer, and leave the log announcing a fan-out
// wider than the one it made.
//
// Over-counting is not the safe direction, either, whatever a gate's usual arithmetic
// says. The refusal this count produces is destructive — a receipt written and the
// record unlinked, per rejectCreateRequest — so charging for a member nobody will create
// does not withhold a session, it destroys a request that had room for one. #701 is the
// entry for that shape: a false-positive gate whose abort is destructive is not
// conservative.
func (m *home) pendingBatchMembers(
	entries []outbox.CreateEntry, id string, now time.Time, answered map[string]bool,
) []outbox.CreateEntry {
	if id == "" {
		return nil
	}
	members := make([]outbox.CreateEntry, 0, len(entries))
	for _, e := range entries {
		if e.Err != nil || e.Request.Batch != id {
			continue
		}
		if e.Request.Expired(now) || m.outboxPoisoned[e.Path] || answered[e.Path] {
			continue
		}
		if outbox.DisclosureMark(e.Path) != outbox.NoDisclosure {
			continue
		}
		members = append(members, e)
	}
	return members
}

// refuseBatchSiblings answers every other pending member of a batch the session cap has
// just refused, with the reason the gated member was given, and returns how many it
// answered.
//
// The fan-out is the whole of "a batch either fits or is refused whole". Refuse only the
// member that was gated and the cap creates the batch from its tail: charged for three
// pending members it refuses the first, then charged for two it refuses the second, and
// then charged for one the third fits in the room the other two did not take.
//
// One member is skipped, and the skip is about not destroying something:
//
//   - one carrying Adopt. Refusing an adopting request is destructive beyond the record
//     (see rejectCreateRequest): its worktree registration has already been released, and
//     the recovery is one-shot. The gated member's own path re-checks the pin before it
//     refuses; a sibling reached from here has not, so it is left for its own gate, where
//     recheckAdoption runs with the evidence in hand.
//
//     What that skip does NOT buy is a later refusal. On its own tick the adopting
//     sibling is the only member of its batch still pending, so it is charged as a batch
//     of one and a cap that blocked N can admit it — one session out of a batch whose
//     every receipt says it was refused whole, with a hole in its numbering. That is the
//     price of not spending a one-shot recovery, it is paid the same way at the gated
//     member's own charge, and #782 is where it is recorded rather than left to be
//     discovered.
//
// A member carrying a terminal mark, and one this walk already answered, are skipped too
// — but not here. pendingBatchMembers excludes both before this list is built, so
// re-checking them here is unreachable rather than defensive: a per-data-dir flock allows
// one TUI, and this process is inside executeCreateRequest between the two calls, so
// nothing else can write a mark in that window. Both guards were written here as well and
// both survived mutation, each masked by the other; the count is the one that has to be
// right, since it decides what the cap is charged for, so the count owns the rule and this
// reads what it was handed.
//
// answeredInBatch is still WRITTEN here, and that half is load-bearing: every sibling
// refused below is still in the listing the walk has yet to reach, and it must not be
// judged a second time.
func (m *home) refuseBatchSiblings(
	sibs []outbox.CreateEntry, gated, reason string, answeredInBatch map[string]bool,
) int {
	answered := 0
	for _, sib := range sibs {
		if sib.Path == gated || sib.Request.Adopt {
			continue
		}
		if answered >= createBatchRefusalBudget {
			break
		}
		// Recorded before the refusal rather than after it: what the caller must not do is
		// reach this entry again from the listing it is walking, and a discard that fails
		// leaves the record there to be reached.
		answeredInBatch[sib.Path] = true
		// Per member, never copied from the gated one: an adopting member carries its own
		// orphan, and the gated member's has already been cleared by the adoptionGone arm
		// where git said there was nothing to name.
		m.rejectCreateRequest(sib.Path, sib.Request, claimedBranch(sib.Request), reason)
		answered++
	}
	return answered
}

// createVerdict is what executeCreateRequest decided about one request.
//
// Two fields rather than the bare reason string it used to return, because the caller
// has a second question the string cannot answer without being parsed: was it the
// SESSION CAP that refused? That is the one refusal a batch shares — a cap is a fact
// about the fleet, and a batch charged for its pending members either fits or does not
// — so it is the one the drain fans out across the rest of the batch. Every other
// refusal here is a fact about one request (its title, its path, its account pool) and
// answers only the member it was reached for.
type createVerdict struct {
	// reason is the wording the caller's rejection receipt carries. "" is the success
	// sentinel, unchanged: executeCreateRequest's zero verdict means it started.
	reason string
	// bySessionCap says the session cap refused. False on the zero value, so a verdict
	// built anywhere but the two cap gates fans out to nobody.
	bySessionCap bool
}

// createRefusal is a verdict nobody but this request is owed.
func createRefusal(reason string) createVerdict { return createVerdict{reason: reason} }

// createCapRefusal is a verdict the whole batch is owed. Reached only from the two session-cap
// gates: an exhausted account pool is deliberately NOT one of them, because --force
// answers it per request and the pool it names may differ between members of a mixed
// fan-out.
func createCapRefusal(reason string) createVerdict {
	return createVerdict{reason: reason, bySessionCap: true}
}

// hardCapReason is the explicit-cap refusal a spooled request's receipt carries.
//
// Byte-identical to hardCapMessage for a request charged for itself, which is what keeps
// that wording's other call sites and this one from drifting apart for the ordinary case.
// A batch adds what a batch needs and hardCapMessage cannot carry: how many sessions this
// refusal is answering, how much room there is, and that they were refused together —
// because a caller told only "you can't create more than 4 sessions" cannot tell a
// refusal of its batch from a refusal of one variant.
//
// No width budget applies. The ~32-cell bound the create form's variant refusals are
// held to is the overlay's; this is a receipt, printed by `atrium new --wait` into a
// terminal.
func hardCapReason(limit, count, adding int) string {
	if adding <= 1 {
		return hardCapMessage(limit)
	}
	return fmt.Sprintf("%s, and %s: they were refused together rather than created until the cap closed",
		hardCapMessage(limit), capRoomClause(limit, count, adding))
}

// softCapReason is the host-capacity refusal, on hardCapReason's terms.
func softCapReason(limit, count, adding int) string {
	if adding <= 1 {
		return fmt.Sprintf("%s — pass --force to create anyway", hostCapacityLine(limit, count))
	}
	return fmt.Sprintf("%s, and %s — pass --force to create them anyway",
		hostCapacityLine(limit, count), capRoomClause(limit, count, adding))
}

// capRoomClause states what this refusal is answering against the room left, written once
// for both reasons above.
//
// "still queued" rather than "asked for", because adding counts the members of the batch
// that are still PENDING, which is the only number the drain has. Once any member has
// been created — the room taken by somebody at the keyboard between two ticks, which
// TestCreateDrainRefusesTheRemainderWhenCapacityIsTakenMidBatch is exactly — the count
// this receipt carries is smaller than what the command line asked for, and claiming
// otherwise would have a script sizing its retry off a number that frees the wrong number
// of slots and leaks the created member's worktree and branch. For the same reason the
// reasons above say the pending members were refused TOGETHER rather than that the whole
// batch was: the second is a claim about members this drain may never have seen.
//
// The room is floored at zero: count can exceed limit (a cap lowered under a running
// fleet, or a soft cap already crossed with --force), and "-2 free" would read as a
// number the caller could act on.
func capRoomClause(limit, count, adding int) string {
	free := limit - count
	if free < 0 {
		free = 0
	}
	return fmt.Sprintf("%d sessions from this atrium new are still queued with room for %d", adding, free)
}

// executeCreateRequest runs every gate the create form runs and, if they all
// pass, starts the session. It returns the boot command, or a reason the request
// was refused — a reason written for the person who ran `atrium new`, because it
// is what a rejection receipt hands back to their `--wait`.
//
// Gate order is hard cap → all-exhausted → soft cap, matching
// createSessionFromForm and autoDispatch. It is load-bearing there because
// neither TUI accept path re-checks the other gate; it is kept here so all three
// creation paths refuse for the same reason in the same order.
// tmux is NOT among the gates: it is the one condition here that is about the machine
// rather than about the request, so drainCreateRequests holds on it instead of calling
// this at all. Everything below refuses, and a refusal here is destructive.
func (m *home) executeCreateRequest(r outbox.Request, adding int) (*session.Instance, tea.Cmd, createVerdict) {
	// The CLI bounds the title too, and that is the check a caller normally meets.
	// This one is for what the CLI cannot speak for: a spool file written by a build
	// of atrium whose limit differs from this one's. Unbounded, an over-long title
	// reaches the list as a row nothing can render sensibly. A blank title and a blank
	// path are refused by readCreate before they get here, mirroring WriteCreate.
	if n := len([]rune(r.Title)); n > session.MaxTitleLen {
		return nil, nil, createRefusal(fmt.Sprintf("the title is %d characters; the limit is %d", n, session.MaxTitleLen))
	}

	// Absolute, because session.NewInstance stores the absolute path. The CLI already
	// sends one; doing it again here means a spool file written elsewhere cannot
	// desynchronise the two.
	path, err := filepath.Abs(r.Path)
	if err != nil {
		return nil, nil, createRefusal(fmt.Sprintf("%q is not a usable path: %v", r.Path, err))
	}

	// A non-git directory is not an error: it becomes a direct session, exactly as
	// it does in the form. It simply has no worktree and no branch.
	valid, direct, _ := targetValidity(m.ctx, path)
	if !valid {
		return nil, nil, createRefusal(fmt.Sprintf("%q is not a directory", path))
	}

	// A direct verdict has to be confirmed, never inferred from a failure to answer.
	// targetValidity reads it off git.IsGitRepo, which returns `err == nil` and so folds
	// every way the probe can fail — git off PATH mid-upgrade, a fork failure under
	// memory pressure, gitLocalTimeout on a cold repo, a cancelled m.ctx — into the same
	// false it gives a plain directory. At the create form that costs nothing: a human is
	// reading the verdict as a live label and can cancel. Here it silently decides
	// whether this session gets an isolated worktree and branch or runs the agent loose
	// in the caller's own checkout, which is the isolation Atrium exists to provide, and
	// `atrium new` has no operator to notice. So: git must have *said* no.
	//
	// Refusing on "don't know" is the conservative direction here because the refusal is
	// a receipt, not a loss of work — the caller is told to retry, and nothing was built.
	// Guessing wrong is unrecoverable and invisible: the agent is already editing the
	// user's working tree by the time anyone could look.
	if direct {
		if _, known := git.ProbeGitRepo(m.ctx, path); !known {
			return nil, nil, createRefusal(fmt.Sprintf(
				"could not determine whether %q is a git repository, and creating a session there "+
					"would have run the agent in it directly rather than in a worktree; nothing was "+
					"created, so retry", path))
		}
	}

	// A base branch for a target with no branches is a contradiction, and a silent one:
	// startNewSession would drop it, Start would base on nothing, and --wait would print
	// `created "x"` with no branch clause — which reads exactly like a legitimate direct
	// session. Refusing gives the caller the one signal that separates the two.
	if direct && r.Branch != "" {
		return nil, nil, createRefusal(fmt.Sprintf(
			"%q has no git repository to take branch %q from", path, r.Branch))
	}

	// The group is derived here rather than read from m.newSessionGroup, which is
	// create-form state this path must not touch — see titleConflictIn.
	group := git.RepoGroupKey(m.ctx, path)
	if conflict := m.createConflictIn(group, r, path, direct); conflict != "" {
		return nil, nil, createRefusal(fmt.Sprintf("%s (%q in %s)", conflict, r.Title, path))
	}

	program := r.Program
	if program == "" {
		// The draining TUI's own default, so an `atrium new` with no --program is
		// what pressing the new-session key gives. autoDispatch does the same.
		program = m.program
	}

	plan := spawnPlan{
		titles: []string{r.Title}, path: path, direct: direct,
		programs: []string{program}, branch: r.Branch, prompt: r.Prompt,
	}

	sc := m.sessionCap()
	count := m.capCount(sc)
	verdict := capVerdict(sc, count, adding)
	if verdict == capBlock {
		// --force is deliberately not consulted: see hardCapMessage.
		return nil, nil, createCapRefusal(hardCapReason(sc.Limit, count, adding))
	}

	// All-exhausted before the soft cap, in that order and not the other way round,
	// because that is the order both TUI paths run and the order the accept paths
	// assume: proceedOverCapMsg does not re-check availability, so a soft-cap refusal
	// reported ahead of an exhausted pool would send a caller back with --force to
	// meet a second refusal it was never told about.
	//
	// Accepting here has to do what accepting the dialog does — pin the soonest-to-
	// reset member. Without the pin, startNewSession fails closed on an unpinned
	// all-limited pool and answers "pick a member explicitly", a flag `atrium new`
	// does not have: --force would be documented as an accept and refused every time.
	var sel *overlay.AccountSelection
	if pool, members, exhausted := m.allExhausted(plan); exhausted {
		if !r.Force {
			return nil, nil, createRefusal(fmt.Sprintf(
				"every account in pool %q is rate-limited — pass --force to create anyway", pool))
		}
		sel = m.pinSoonestMember(pool, members)
	}

	if verdict == capConfirm && !r.Force {
		return nil, nil, createCapRefusal(softCapReason(sc.Limit, count, adding))
	}

	// Never dependency-isolating: that is a form choice, and shared is the default the
	// form itself starts from. sel is nil unless --force just accepted an exhausted
	// pool, which is the only account choice this path can make.
	inst, cmd, err := m.startNewSession(r.Title, path, direct, false, program, r.Branch, r.Prompt, sel, spawnBackground, nil)
	if err != nil {
		// Never an empty reason. "" is this function's success sentinel, so an error
		// whose Error() is empty would be read as a start that worked, and the caller
		// would hold a nil instance under a key no instanceStartedMsg can ever settle —
		// spending the one start budget for the life of the process, silently, with no
		// notice and no receipt. Every error reachable today has a message; the sentinel
		// should not depend on that staying true.
		if reason := err.Error(); reason != "" {
			return nil, nil, createRefusal(reason)
		}
		return nil, nil, createRefusal("the session could not be started")
	}
	return inst, cmd, createVerdict{}
}

// createConflictIn is the conflict gate a spooled request is held to:
// variantTitleConflictIn, except that a request the startup reconcile marked for
// adoption skips the branch half of it.
//
// The branch half is not decoration. git.Worktree.Setup reads a pre-existing branch as
// a resume (session/git/worktree_ops.go), so a creation that reaches it with a taken
// slug adopts that branch's work instead of failing — which is why the form and this
// drain are the two creation paths that consult git.LocalBranchExists at all. Skipping
// it is therefore a hole in a load-bearing guard, and the only thing that may open it
// is evidence a request cannot carry on its own: reconcileCreateClaims sets Adopt only
// for a branch it has proved exists, belongs to no session row, and was not there when
// the interrupted build claimed the request. Under those three, the branch IS the
// session's own half-built work and resuming it is the outcome the caller asked for.
//
// Those three are established at reconcile time and this runs whenever the drain reaches
// the request, so Adopt arriving here is a claim about the past. recheckAdoption is what
// makes it a claim about now: the caller re-checks the branch's commit against the pin the
// reconcile recorded and withdraws Adopt when they differ, so by the time this reads the
// flag the skip has been re-earned rather than inherited (#731).
//
// The title half still applies with Adopt set. A row that owns the title is a live
// session, and nothing about finishing an interrupted build makes it right to mint a
// second session on top of one.
func (m *home) createConflictIn(group string, r outbox.Request, path string, direct bool) string {
	if r.Adopt {
		return m.titleConflictIn(group, r.Title)
	}
	return m.variantTitleConflictIn(group, r.Title, path, direct)
}

// adoptionState is what recheckAdoption found about the branch a request was re-queued to
// take. Three values rather than a bool because the caller does three different things
// with them, and the third is not "refuse": a branch that is gone takes nothing with it,
// so the request becomes an ordinary create AND stops owing a disclosure, while one that
// moved leaves an orphan the caller still has to name.
type adoptionState int

const (
	// adoptionMoved: a branch of that name is there and is not the one that was vetted —
	// or the record carries no pin to compare, which is the same answer for the same
	// reason. The licence is withdrawn; the branch is still a leftover.
	//
	// Iota-0 deliberately. This is the fail-closed answer, and the value that licenses the
	// skip is the one that has to be spelled — so a zero var, an unset struct field or a
	// map miss withdraws the licence rather than granting it. recheckAdoption returns this
	// alongside its error for the same reason, so a caller that ignored the error still
	// refuses.
	adoptionMoved adoptionState = iota
	// adoptionGone: no branch of that name. Nothing to adopt and nothing to disclose.
	adoptionGone
	// adoptionHolds: the branch is at the commit the reconcile pinned. The skip is
	// re-earned.
	adoptionHolds
)

// lookupBranchTip is the seam recheckAdoption reads git through, in the shape tmuxAvailable
// uses and for the same kind of reason: it is the only way to produce the condition this
// function separates out — git failing to RUN at all, for a repo that is definitely there.
//
// Every other way of breaking the probe is a failure git itself reports and exits non-zero
// for, which this treats as a fact about the request rather than about the machine, so a
// fixture cannot reach the hold by deleting a repo or by pointing at a directory that is not
// one. Emptying PATH would reach it and takes the whole process's git with it, including the
// working repo a starvation test needs beside the broken one.
var lookupBranchTip = git.LookupLocalBranchTip

// recheckAdoption re-checks, at execution time, the evidence the startup reconcile gathered
// when it marked this request for adoption: that the session branch it left behind is still
// the same branch.
//
// Adopt licenses a skip of a load-bearing gate (see createConflictIn), and the evidence for
// it is gathered at reconcile time while the skip is taken whenever the drain gets round to
// the request — which can be much later. tmux off PATH holds this drain indefinitely; so
// does createDrainHeld; so does another crash. Delete and recreate the branch in that
// window, by hand or through a fetch or a rebase, and the name still matches while the
// commits are somebody else's: Setup takes its existing-branch arm and the agent silently
// resumes on their work, which is exactly the outcome the branch gate exists to prevent.
//
// The commit is what separates the cases, so a request with no pin fails closed rather than
// being trusted on its name — a hand-written record, or one re-queued by an atrium older
// than Request.AdoptTip. It still costs the probe: "closed" is adoptionMoved rather than
// adoptionGone, and only git can tell those apart. A record naming no branch at all is the
// one case answered without asking.
//
// The error is separate from the answer for LookupLocalBranchTip's reason: the caller holds
// on "git could not be asked" and refuses on "not the branch we vetted", and folding the two
// would make an unreadable repo spend the recovery.
func (m *home) recheckAdoption(r outbox.Request) (adoptionState, error) {
	branch := claimedBranch(r)
	if branch == "" {
		return adoptionGone, nil
	}
	tip, err := lookupBranchTip(m.ctx, r.Path, branch)
	switch {
	case err != nil:
		// "git could not be asked" and "git says there is no repository here" arrive as the
		// same error, and only the first is a fact about the machine. Folded together, a
		// re-queued adoption whose repo has been deleted is held for the whole 24h TTL —
		// no receipt, no gate, `atrium new --wait` timing out with nothing — while the
		// pre-#731 drain answered it in one tick, and each of those ticks pays a
		// for-each-ref fork on the update goroutine. ProbeGitRepo is what separates them:
		// it reports `known` only when git RAN and answered, so a target it says is not a
		// repository has no branch to adopt and belongs to the gates, which have their own
		// wording for a path that is no longer usable.
		if isRepo, known := git.ProbeGitRepo(m.ctx, r.Path); known && !isRepo {
			return adoptionGone, nil
		}
		return adoptionMoved, err
	case tip == "":
		return adoptionGone, nil
	case r.AdoptTip == "" || tip != r.AdoptTip:
		return adoptionMoved, nil
	}
	return adoptionHolds, nil
}

// noteAdoptHold logs the two edges of an adoption hold and nothing in between, which is
// what createAdoptHeld is for: the drain runs twice a second and an unreadable repo stays
// unreadable.
//
// pending is why this is not simply "err != nil". Every skip in the walk — tmux off PATH, a
// spent gate budget, a poisoned path, an unwalked tail — arrives here with err nil while the
// held request is still in the spool, so lifting on err alone announces a resume that did
// not happen and clears the flag that would have logged the next real hold. The lift needs
// both: nothing failed, and nothing was left un-re-checked.
// A permanently-skipped Adopt record keeps pending true for the life of the run — a poisoned
// path, or one already answered by a disclosure, is never re-checked and so is never answered
// for. That owes a resume line the log never prints, which is the cheap direction: the flag's
// only job is to keep one condition from being logged twice a second, and a false "resuming"
// would clear it for the next genuine hold.
func (m *home) noteAdoptHold(err error, pending bool) {
	switch {
	case err != nil && !m.createAdoptHeld:
		m.createAdoptHeld = true
		log.WarningLog.Printf("holding a re-queued create request until its branch can be "+
			"re-checked: %v", err)
	case err == nil && !pending && m.createAdoptHeld:
		m.createAdoptHeld = false
		log.InfoLog.Printf("re-queued create requests can be re-checked again; resuming")
	}
}

// clearMarkOverADroppedRecord offers up the terminal mark whose record this tick has just
// removed, so the same orphan is not reported on two consecutive launches.
//
// The mark is what stopped that record being executed, and the discard above is the unlink
// that had failed when it was written — so its second job is finished the moment that lands.
// Without this the file survives the whole run: loadCreateDisclosures buffered it before the
// event loop, the flush offered it up at the 100ms tick while the record was still on disk,
// and ClearDisclosure declined for exactly the right reason. The drain removes the record
// 400ms later and nothing asks again.
//
// Skipped while an entry for it is still buffered, and that is not an optimisation. The flush
// waits for a frame with no overlay on it — a fresh install sits in the welcome modal until
// somebody answers it — while this walk runs regardless, so clearing here could delete the
// only account of an orphan before anyone had seen it. ClearDisclosure's own guard decides the
// rest: a claim still beside the file keeps it whatever this does.
func (m *home) clearMarkOverADroppedRecord(record string) {
	for _, e := range m.pendingCreateDisclosures {
		if e.Path == record {
			return
		}
	}
	if _, err := outbox.ClearDisclosure(record); err != nil {
		log.ErrorLog.Printf("failed to clear the mark over a create request that has been "+
			"dropped: %v", err)
	}
}

// noteUnreadableMark logs the two edges of a record whose terminal mark could not be looked
// for, and nothing in between — noteAdoptHold's shape, for noteAdoptHold's reason: the drain
// runs twice a second and a spool that cannot be stat'd stays that way.
//
// Worth a line at all because the record is neither executed nor answered while this holds:
// its caller's --wait blocks to its own deadline, and the file sits until the spool horizon
// past which the disposal arms take it — or indefinitely, for the one record that has no
// readable timestamp to be past it (see the probe in the walk). That is the one skip in the
// walk with no other trace. `atrium doctor` reports the data dir; this line says which file.
//
// pending is noteAdoptHold's second argument for noteAdoptHold's reason, and this flag needed
// it as much: the walk's composite break leaves the whole tail of the spool unprobed and a
// poisoned path is never probed at all, so a held record can reach the end of a tick with no
// record name to report. Lifting on that logs a resume nobody earned and clears the flag the
// next genuine hold needed. The lift wants both — nothing failed to be probed, and nothing
// was left unprobed — and an empty spool is where it reliably gets them.
func (m *home) noteUnreadableMark(record string, pending bool) {
	switch {
	case record != "" && !m.createMarkHeld:
		m.createMarkHeld = true
		log.ErrorLog.Printf("holding create request %s: cannot tell whether atrium has already "+
			"given up on it", filepath.Base(record))
	case record == "" && !pending && m.createMarkHeld:
		m.createMarkHeld = false
		log.InfoLog.Printf("create requests can be checked for a terminal mark again; resuming")
	}
}

// holdCreateRequest records that path's session is starting — in memory, keyed by the
// very instance startNewSession built, and on disk, by claiming the spool record.
//
// Keyed by the object rather than looked up by identity, because a (Title, Path)
// search is not the same question the conflict check answered. titleConflictIn scopes
// its "already used" arm to a repo group, so a stored session whose GroupKey has since
// diverged from git.RepoGroupKey for the same directory passes the conflict check and
// is still the first (Title, Path) match in the list — and a map keyed on that older
// row never settles. That leak is not local: settle is a silent miss, the entry stays
// forever, and createStartBudget is seeded from the map's length, so every later
// `atrium new` on the machine is skipped with no notice, no log line and no receipt.
//
// The map still holds the RECORD path, not the claim file's, because that is the path
// the rest of the protocol is keyed on: the receipt a refusal writes, and the file the
// caller's --wait is watching. outbox.ClaimPath derives the other from it.
//
// The claim is what survives this process (#716). Both halves are needed and neither
// subsumes the other: the map settles a start that completes here, and the claim is
// the only thing a start interrupted by a SIGKILL leaves behind for the next launch to
// finish. A claim that cannot be written is logged and the create proceeds unclaimed,
// because refusing a session whose worktree is already going up is worse — but "proceeds
// unclaimed" is not the pre-#716 behaviour and must not be described as it. Pre-#716 the
// createRequestInFlight scan kept the still-named record from being drained twice; that
// scan is gone, so the unclaimed record is poisoned here instead. What degrades to the
// pre-#716 behaviour is only the crash window itself: that one request is again
// unrecoverable if this process dies mid-Start.
//
// It runs before Start does. startNewSession returns a boot command Bubble Tea has not
// executed yet, so the CreateRequest stamp below and the claim's branch probe both
// happen on the update goroutine with nothing else touching the instance.
func (m *home) holdCreateRequest(path string, r outbox.Request, inst *session.Instance) {
	if m.createsInFlight == nil {
		m.createsInFlight = make(map[*session.Instance]string)
	}
	m.createsInFlight[inst] = path
	// Stamped on the instance, persisted with the row, and read only by the next
	// launch's reconcile — see session.InstanceData.CreateRequest for why a
	// (Title, Path) match cannot stand in for it.
	inst.CreateRequest = path

	meta := outbox.ClaimMeta{At: time.Now()}
	if !inst.IsDirect() {
		// The same expression branchSlugConflict evaluates, against the same config
		// this model holds — so the branch recorded here is the one the gate a few
		// lines ago tested, exactly as right and exactly as wrong. (git's own
		// newSessionWorktree derives it identically but from a freshly loaded config,
		// which is the pre-existing seam, not one this adds.)
		//
		// Recorded rather than left to be recomputed later, because BranchPrefix is a
		// config value: one edited between a crash and the next launch would have the
		// reconcile probe for a branch nobody made, read the orphan as "nothing was
		// built", and create a second session beside it.
		meta.SessionBranch = git.BranchNameForSession(m.appConfig.BranchPrefix, inst.Title)
		// Measured here rather than inferred from the gate that ran a moment ago. For
		// an ordinary request the gate has just proved this false and the probe agrees;
		// for a re-queued Adopt it is true by design, and the difference between "true
		// because we are finishing our own orphan" and "true because someone else's
		// branch is in the way" is exactly what the next reconcile has to read off this
		// record. Inferring it from r.Adopt would record a guess in the one field whose
		// job is to be evidence.
		meta.BranchExisted = git.LocalBranchExists(m.ctx, inst.Path, meta.SessionBranch)
	}
	if err := outbox.Claim(path, meta); err != nil {
		// The record is still at its own name, so ListCreates will hand it back on the
		// next tick — and the guard that used to catch that (createRequestInFlight) was
		// deleted on the strength of the rename that just failed. Unguarded, the second
		// pass runs the gates against the row startNewSession has already inserted,
		// refuses the request for its own session's title, and writes that receipt to a
		// --wait that is about to be handed a working session; the expiry arm can also
		// unlink the record out from under a build still running its setup script.
		//
		// Poisoned, the drain skips it for the rest of this run — the same treatment
		// discardSpoolFile gives a record it could not unlink, and for the same reason.
		// settleCreateRequest still clears the file at the end of the build, because
		// DiscardCreate drops the record and the claim without caring which exists.
		if m.outboxPoisoned == nil {
			m.outboxPoisoned = make(map[string]bool)
		}
		m.outboxPoisoned[path] = true
		log.ErrorLog.Printf("failed to claim the create request for %q; it is being built but "+
			"a crash before it is recorded will strand it: %v", r.Title, err)
	}
}

// settleCreateRequest closes out the spool file behind a session that has finished
// starting: removed on success, rejected with the failure otherwise. A session
// that did not come from a request is a map miss and costs nothing.
//
// Holding the file across the whole of Start is what makes `atrium new --wait`
// honest. The prompt drain can unlink as soon as it has queued, because queuing is
// the whole job; here the job is a worktree, a branch and a running agent, none of
// which exist yet when startNewSession returns. Unlinking early would let --wait
// report success for a create that then failed on a dirty repo.
//
// The success call is made *after* persistInstances, for the reason drainOutbox
// persists before it unlinks: the file is the only durable record that the work was
// asked for, so it must not be dropped while the thing it asked for is not yet
// recorded. --wait reads the branch out of state.json the moment the file goes away,
// so unlinking first would also race it into reporting a session with no branch.
//
// The remaining window is a crash between the two, and holdCreateRequest's claim is
// what makes it survivable (#716). The next launch finds a claim rather than a
// re-drainable request, and reconcileCreateClaims judges it on evidence instead of
// re-running the gates. Three of its verdicts answer the three states an interrupted
// build can leave: the row is there and stamped with this record, so the request
// SUCCEEDED and its caller is told so rather than "already used"; nothing was built, so
// it is re-queued; or Worktree.Setup got as far as creating the branch and
// persistInstances never ran, leaving a branch no row owns — which is re-queued to
// adopt that branch rather than refused for it. (Refusal covers what none of those
// describe: an expired or unreadable claim, or a branch that is somebody else's. Two
// more verdicts are about the recovery rather than the build — claimDefer for a git that
// could not be asked, and claimAnswered for a claim an earlier launch already refused.)
// Before the claim existed the third state was a permanent refusal ("branch already
// exists") plus an orphan branch and worktree belonging to no row `atrium ls` could
// show, removable only by hand.
//
// An orderly shutdown does not go through here at all, which is why
// reconcileInFlightStarts settles what it adopts.
func (m *home) settleCreateRequest(inst *session.Instance, startErr error) {
	if startErr != nil {
		// failCreateRequest, not discloseUnrecordedSession: every caller that reaches this
		// arm has just torn the session down, or tried to. reconcileInFlightStarts only
		// logs inst.Kill()'s error and falls through, so a teardown that FAILS reaches here
		// with the artifacts still standing — and while failCreateRequest does leave a
		// terminal mark, the mark carries no inventory, so nothing NAMES them. That is the
		// same class as #732 and named by neither issue. Left as adjacent work rather than
		// fixed here: the fix belongs at the Kill, where the error is, and reporting an
		// inventory from this side would name leftovers for every successful teardown too.
		m.failCreateRequest(inst, fmt.Sprintf("the session could not be started: %v", startErr))
		return
	}
	path, ok := m.createsInFlight[inst]
	if !ok {
		return
	}
	delete(m.createsInFlight, inst)
	// DiscardCreate, not Remove: holdCreateRequest renamed the record, so the file to
	// drop is normally the claim — and "normally" is why this drops both rather than
	// picking one. Success leaves no receipt, so the caller's --wait reads the absence
	// of both — see awaitSpool.
	m.discardSpoolFile(path, func() error { return outbox.DiscardCreate(path) })
}

// failCreateRequest is settleCreateRequest's failure half with the reason written by
// the caller, for the outcomes "could not be started" does not describe.
//
// It answers the caller and drops the request, so on its own it is for a failure whose
// artifacts somebody else is cleaning up, which is what settleCreateRequest's startErr arm
// reaches it for: every path into that arm has torn the session down, or has tried to (see
// that arm for the case where trying is not enough).
//
// It still writes a terminal mark, with nothing in it. This is a giving-up on a CLAIM, and
// outbox.Disclose's ordering exists for exactly the failure available here: DiscardCreate can
// fail, and a claim that outlives it with no mark beside it is re-judged on the next launch
// against live git — into claimRequeue if the teardown worked, into claimAdopt if it did not,
// and either way into a session built for a caller that already read "the session could not
// be started" and exited non-zero. The mark is not conditional on there being an inventory,
// and here there is deliberately none: naming a branch the caller has just deleted would put
// a modal in front of the user for artifacts that are gone.
//
// discloseUnrecordedSession is the sibling this used to be reached through, and no longer is:
// it has an inventory and a live session, so it writes its own disclosure and calls only the
// answer-and-drop half below, because a second write from here would overwrite the full
// account with an empty one. Which leaves this function one caller — settleCreateRequest's
// startErr arm — and the split kept anyway, since what separates them is which disclosure is
// owed rather than how many places ask.
func (m *home) failCreateRequest(inst *session.Instance, reason string) {
	path, ok := m.createsInFlight[inst]
	if !ok {
		return
	}
	m.discloseCreateLeftovers(path, outbox.Disclosure{
		Title:  inst.Title,
		Repo:   inst.Path,
		Reason: reason,
	})
	m.answerAndDropCreate(inst, path, reason)
}

// answerAndDropCreate is the tail both failure paths share: the caller's receipt, then the
// files, then the map entry.
//
// The receipt goes to the record path and the file dropped is the claim. Both halves, and in
// that order: Reject writes before it unlinks so a --wait cannot read the gap as a success,
// and the claim is the half --wait is actually watching by now.
func (m *home) answerAndDropCreate(inst *session.Instance, path, reason string) {
	delete(m.createsInFlight, inst)
	m.discardSpoolFile(path, func() error {
		return errors.Join(outbox.Reject(path, reason), outbox.DiscardCreate(path))
	})
}

// discloseUnrecordedSession answers the caller of a create whose session exists and whose
// row does not, and records what that leaves stranded (#732).
//
// This is the failure the claim was introduced to survive, reached through the one path
// that used to destroy it. The worktree, the branch, the tmux session and the agent are all
// there; what failed is persistInstances, the record of them. Telling the caller is right —
// a --wait must not read the unlink as success — but dropping the claim along with it left
// the next launch nothing to reconcile: no row, because the persist is what failed, and no
// claim either, so the branch and the worktree belonged to nothing and nothing ever
// mentioned them again.
//
// Called instead of failCreateRequest at the two sites where the persist is what failed,
// and chosen AT those sites rather than inferred from inst.Started() here. On the
// start-failure path the artifacts are absent because the caller kills the instance, not
// because the instance says so, and a predicate that reads the same either way would tie
// this decision to a teardown happening somewhere else.
//
// The inventory is free at both sites: the instance is in hand, and Branch, worktree path
// and tmux name are all already resolved on it by the time a persist can fail.
//
// It writes the file and opens no report, which is the difference between this and
// rejectCreateRequest's disclosure and is the reason discloseLiveButUnrecorded exists. A
// refusal's leftovers belong to no session at all, so the only way anyone hears about them
// is a modal. These leftovers belong to a session that is in the list, Running, one row
// away — nothing tore it down — so a modal naming its branch, its worktree and its tmux
// session under "nothing here will clean them up" would be an instruction to destroy a
// live agent. The file is for the case the report cannot cover: this process dying before
// any later persist lands the row, which is the only way these artifacts ever become
// orphans. A persist that succeeds withdraws it (see home.persistInstances).
func (m *home) discloseUnrecordedSession(inst *session.Instance, reason string) {
	path, ok := m.createsInFlight[inst]
	if !ok {
		return
	}
	// The disclosure first and the receipt second, for outbox.Disclose's reason: Reject
	// unlinks the record, so a crash in between would leave the orphan with nothing that
	// mentions it.
	m.discloseLiveButUnrecorded(inst, path, outbox.Disclosure{
		Title:    inst.Title,
		Repo:     inst.Path,
		Branch:   inst.Branch,
		Worktree: inst.GetWorktreePath(),
		TmuxName: inst.TmuxSessionName(),
		Reason:   reason,
	})
	m.answerAndDropCreate(inst, path, reason)
}

// rejectCreateRequest leaves a receipt naming the reason and removes the request,
// so `atrium new --wait` reports the refusal instead of reading the unlink as a
// successful creation.
//
// The gate refusal and both disposals reach this, and none of them has claimed anything,
// because a request is claimed exactly when it is accepted (holdCreateRequest), so there is no
// claim file to release here. A refusal AFTER a claim is failCreateRequest. One arm of the walk
// answers its caller without coming through here at all — the one that finds a terminal mark
// already beside the record — because there the account was written by whoever gave up, and
// re-writing it from a request this drain never executed would replace it with less.
//
// One of those refusals is destructive beyond the record, and it is why this takes the
// request rather than just its path (#731). A request carrying Adopt is the second attempt
// at a build that already made a session branch, and applyCreateClaim removed that branch's
// worktree registration before re-queueing it — deliberately, since a Setup that runs while
// the stale worktree still holds the branch fails with git's "already used by worktree".
// Refuse it here and the branch has no row, no claim, no request and no worktree
// registration: invisible to `atrium ls`, to `atrium reap`, and now to `git worktree list`
// too. The recovery is one-shot, so that state was permanent. The disclosure is what
// survives it.
//
// The branch to disclose is passed in rather than read off r.Adopt, and that is the whole
// of the fix. Adopt is a licence about now and the drain withdraws it whenever the pin no
// longer matches — but withdrawing the licence does not unmake the branch, and every
// refusal that follows a withdrawal is a refusal of a request whose worktree registration
// was already released. Reading the flag left the two withdrawals silent: a branch that moved
// under the same name, and a record from an atrium too old to carry a pin (which
// internal/outbox's createVersion promises is covered). It could not be repaired by reading
// the flag more carefully either, because outbox.WithdrawAdoption persists the withdrawal —
// so a record that comes back round on a later tick, or after a crash, reads Adopt false with
// its branch still standing. The caller derives the branch from the claim's evidence block
// instead, which the withdrawal does not touch, and passes "" for the one case where git has
// said there is genuinely nothing to name.
//
// A disclosure is written even then, with nothing in it, and that is the mark rather than the
// report. discardSpoolFile's unlink can fail, and most of the gates above are facts about the
// FLEET rather than about the request — a rate limit that resets, a cap that is raised, a
// title a kill frees — so a record that survives its own Reject is one a later tick or a later
// launch lets through, building the session for a caller that read the refusal and exited
// non-zero. That is #731's hole 3 through the record-shaped door, and the mark closes it the
// same way it closes the claim-shaped one. flushCreateDisclosures keeps an inventory-less mark
// out of the modal, so this costs the user nothing and the spool one file until the unlink
// lands.
func (m *home) rejectCreateRequest(path string, r outbox.Request, orphanBranch, reason string) {
	m.discloseCreateLeftovers(path, outbox.Disclosure{
		Title:  r.Title,
		Repo:   r.Path,
		Branch: orphanBranch,
		Reason: reason,
	})
	m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
}

// disposeCreateRequest is rejectCreateRequest for the two arms whose answer does not depend
// on anything that can change: a record past the spool horizon, and one nothing can decode.
//
// It differs in exactly one way — the mark is written only when there is something to report
// — and the difference is not a saving, it is what the mark is FOR. rejectCreateRequest marks
// unconditionally because most of the gates above it are facts about the FLEET: a rate limit
// resets, a cap is raised, a title is freed, and a record that survived its own Reject is
// then let through for a caller that already exited non-zero. Neither of these arms has that
// property. Re-drained, an expired record is expired again and an undecodable one is still
// undecodable, so the arm answers the same way for as long as the file exists and a mark
// guards nothing.
//
// What it would cost instead is a file per record per tick. createDisposalBudget allows 50,
// the walk runs twice a second, and a two-day cron backlog cleared while the TUI sits behind
// the welcome modal — where flushCreateDisclosures returns early and clears nothing — mints a
// disclosure and two fsyncs apiece on top of each receipt, all of it on the Bubble Tea update
// goroutine. That is the synchronous freeze createDisposalBudget exists to bound, one layer
// out from where it can see it.
//
// The report survives the mark going, because the two jobs are separate (outbox.Disclose).
// An expired Adopt record still names its branch here: this arm destroys the record
// permanently, so a branch it does not name is named nowhere ever again.
func (m *home) disposeCreateRequest(path string, r outbox.Request, orphanBranch, reason string) {
	if orphanBranch != "" {
		m.discloseCreateLeftovers(path, outbox.Disclosure{
			Title:  r.Title,
			Repo:   r.Path,
			Branch: orphanBranch,
			Reason: reason,
		})
	}
	m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
}
