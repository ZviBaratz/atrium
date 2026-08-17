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
// alone writes, so it does not see the TUI's own starts. A variant fan-out spawns up to
// maxVariantBatch sessions in one keypress, all in one repo, and this constant neither
// counts nor limits them — a human asking for twenty is not the accident this guards
// against, and refusing a spooled create because a fan-out is mid-flight would make
// `atrium new` fail for a reason its caller cannot see. The number to hold in mind is
// therefore "one from the spool, plus whatever the keyboard started".
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
// re-queued to finish an interrupted build — spends four: a git target's five minus the
// LocalBranchExists it skips deliberately rather than for want of a branch (see
// createConflictIn).
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
// unreadable files and expired ones, which cost a receipt and an unlink apiece. Larger
// than the budgets above because there is no git in that path — but not unbounded: a
// two-day-old cron backlog would otherwise unlink thousands of expired requests inside
// one Update, freezing the UI. Bounded, a backlog clears at 50 a tick.
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
// Three things do hold it, none of them a state: a staged spawn plan, a pending quit
// and an in-flight async action. See createDrainHeld.
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
		log.WarningLog.Printf("holding %d create request(s) until tmux is usable again: %v",
			len(entries), tmuxDown)
	case tmuxDown == nil && m.createTmuxHeld:
		m.createTmuxHeld = false
		log.InfoLog.Printf("tmux is usable again; resuming create requests")
	}

	now := time.Now()
	// Seeded with the starts still running from earlier ticks: the budget is on
	// concurrency, not on arrivals.
	started := len(m.createsInFlight)
	var gated, disposed int
	var cmds []tea.Cmd
	// Gate refusals only. A disposal is charged to disposed and stays off this counter
	// deliberately — see the notice below for why an expired file must not raise one.
	var refused int

	for _, e := range entries {
		// Nothing more can be gated (so nothing more can be started or refused) and
		// nothing more can be discarded: the rest of the spool is next tick's.
		//
		// Only a spool with 50+ disposable records reaches this; a healthy backlog
		// leaves disposed at 0 and is bounded by the per-arm `continue`s below instead,
		// walking the remaining entries to a map lookup apiece. Cheap, and not worth a
		// cleverer condition while ListCreates has already read them all anyway.
		if (started >= createStartBudget || gated >= createGateBudget) && disposed >= createDisposalBudget {
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
			continue
		}

		// Discards, gate evaluations and starts draw on separate budgets, so a backlog
		// of expired requests cannot spend the one start this tick was allowed to make —
		// and cannot buy itself unbounded git either. Each arm charges its own budget,
		// so which one paid is read at the call site rather than through a parameter.
		reject := func(reason string) {
			m.rejectCreateRequest(e.Path, reason)
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
			log.ErrorLog.Printf("discarding an unreadable create request: %v", e.Err)
			reject("the request could not be read")
			disposed++

		case e.Request.Expired(now):
			if disposed >= createDisposalBudget {
				continue
			}
			age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a create request for %q: spooled %s ago, past the %s horizon",
				e.Request.Title, age, outbox.TTL)
			reject(fmt.Sprintf("the request was spooled %s ago, past the %s horizon", age, outbox.TTL))
			disposed++

		default:
			// Held, not refused — see the probe above.
			if tmuxDown != nil {
				continue
			}
			// Both budgets, before the gates rather than after: whether this request
			// ends in a session or a receipt, finding out costs the same git either
			// way (see createGateBudget).
			if started >= createStartBudget || gated >= createGateBudget {
				continue
			}
			gated++
			inst, cmd, reason := m.executeCreateRequest(e.Request)
			if reason != "" {
				log.WarningLog.Printf("refusing a create request for %q: %s", e.Request.Title, reason)
				reject(reason)
				refused++
				continue
			}
			// The request deliberately stays on disk — as a claim — until the start
			// lands and the row is persisted, so `atrium new --wait` reading the
			// absence of both means "created and recorded" rather than "consumed".
			m.holdCreateRequest(e.Path, e.Request, inst)
			started++
			cmds = append(cmds, cmd)
		}
	}

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
		// tick, so refused is necessarily 0 here. A disposal can share the tick, but that
		// is deliberately not an event this notice reports (see below).
		cmds = append(cmds, m.showMenuNotice("created a session from atrium new", ui.NoticeInfo))
	case refused > 0:
		// A refusal reaches the person who ran `atrium new` as a receipt, but the
		// person at the TUI is the one who can fix a cap or a taken title, and to them
		// a silent tick is indistinguishable from no request at all.
		//
		// Singular, with no count, because refused can only ever be 1: the default arm
		// spends a gate to reach a refusal and createGateBudget allows one per tick — the
		// same fact the create branch above uses to know refused is 0 there. A `%d` and a
		// plural() here would be printing a number that cannot vary and pluralising a
		// word that cannot become plural, which reads to a later editor as evidence that
		// a tick can refuse several. A backlog is refused one per tick instead, each
		// replacing this toast rather than adding to it.
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

// createDrainHeld reports whether this tick must not create. All three cases are keyed
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
// Of the three holds this is the one about corrupting another surface rather than about
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
func (m *home) executeCreateRequest(r outbox.Request) (*session.Instance, tea.Cmd, string) {
	// The CLI bounds the title too, and that is the check a caller normally meets.
	// This one is for what the CLI cannot speak for: a spool file written by a build
	// of atrium whose limit differs from this one's. Unbounded, an over-long title
	// reaches the list as a row nothing can render sensibly. A blank title and a blank
	// path are refused by readCreate before they get here, mirroring WriteCreate.
	if n := len([]rune(r.Title)); n > session.MaxTitleLen {
		return nil, nil, fmt.Sprintf("the title is %d characters; the limit is %d", n, session.MaxTitleLen)
	}

	// Absolute, because session.NewInstance stores the absolute path. The CLI already
	// sends one; doing it again here means a spool file written elsewhere cannot
	// desynchronise the two.
	path, err := filepath.Abs(r.Path)
	if err != nil {
		return nil, nil, fmt.Sprintf("%q is not a usable path: %v", r.Path, err)
	}

	// A non-git directory is not an error: it becomes a direct session, exactly as
	// it does in the form. It simply has no worktree and no branch.
	valid, direct, _ := targetValidity(m.ctx, path)
	if !valid {
		return nil, nil, fmt.Sprintf("%q is not a directory", path)
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
			return nil, nil, fmt.Sprintf(
				"could not determine whether %q is a git repository, and creating a session there "+
					"would have run the agent in it directly rather than in a worktree; nothing was "+
					"created, so retry", path)
		}
	}

	// A base branch for a target with no branches is a contradiction, and a silent one:
	// startNewSession would drop it, Start would base on nothing, and --wait would print
	// `created "x"` with no branch clause — which reads exactly like a legitimate direct
	// session. Refusing gives the caller the one signal that separates the two.
	if direct && r.Branch != "" {
		return nil, nil, fmt.Sprintf(
			"%q has no git repository to take branch %q from", path, r.Branch)
	}

	// The group is derived here rather than read from m.newSessionGroup, which is
	// create-form state this path must not touch — see titleConflictIn.
	group := git.RepoGroupKey(m.ctx, path)
	if conflict := m.createConflictIn(group, r, path, direct); conflict != "" {
		return nil, nil, fmt.Sprintf("%s (%q in %s)", conflict, r.Title, path)
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
	verdict := capVerdict(sc, count, 1)
	if verdict == capBlock {
		// --force is deliberately not consulted: see hardCapMessage.
		return nil, nil, hardCapMessage(sc.Limit)
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
			return nil, nil, fmt.Sprintf(
				"every account in pool %q is rate-limited — pass --force to create anyway", pool)
		}
		sel = m.pinSoonestMember(pool, members)
	}

	if verdict == capConfirm && !r.Force {
		return nil, nil, fmt.Sprintf("%s — pass --force to create anyway", hostCapacityLine(sc.Limit, count))
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
			return nil, nil, reason
		}
		return nil, nil, "the session could not be started"
	}
	return inst, cmd, ""
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
// The title half still applies with Adopt set. A row that owns the title is a live
// session, and nothing about finishing an interrupted build makes it right to mint a
// second session on top of one.
func (m *home) createConflictIn(group string, r outbox.Request, path string, direct bool) string {
	if r.Adopt {
		return m.titleConflictIn(group, r.Title)
	}
	return m.variantTitleConflictIn(group, r.Title, path, direct)
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
// adopt that branch rather than refused for it. (A fourth, refusal, covers what none of
// those describe: an expired or unreadable claim, or a branch that is somebody else's.)
// Before the claim existed the third state was a permanent refusal ("branch already
// exists") plus an orphan branch and worktree belonging to no row `atrium ls` could
// show, removable only by hand.
//
// An orderly shutdown does not go through here at all, which is why
// reconcileInFlightStarts settles what it adopts.
func (m *home) settleCreateRequest(inst *session.Instance, startErr error) {
	if startErr != nil {
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
// the caller, for the outcomes "could not be started" does not describe. The persist
// failure is the one that matters: the session started fine — worktree, branch and
// agent all exist — and what went wrong is that no record of it did, which is a
// different thing for a caller to be told and a different thing to clean up after.
func (m *home) failCreateRequest(inst *session.Instance, reason string) {
	path, ok := m.createsInFlight[inst]
	if !ok {
		return
	}
	delete(m.createsInFlight, inst)
	// The receipt goes to the record path and the file dropped is the claim. Both
	// halves, and in that order: Reject writes before it unlinks so a --wait cannot
	// read the gap as a success, and the claim is the half --wait is actually
	// watching by now.
	m.discardSpoolFile(path, func() error {
		return errors.Join(outbox.Reject(path, reason), outbox.DiscardCreate(path))
	})
}

// rejectCreateRequest leaves a receipt naming the reason and removes the request,
// so `atrium new --wait` reports the refusal instead of reading the unlink as a
// successful creation.
//
// Only the gate arms of drainCreateRequests reach this, and none of them has claimed
// anything — a request is claimed exactly when it is accepted (holdCreateRequest), so
// there is no claim file to release here. A refusal AFTER a claim is failCreateRequest.
func (m *home) rejectCreateRequest(path, reason string) {
	m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
}
