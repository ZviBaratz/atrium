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

// createStartBudget caps how many session starts may be in flight at once, counting
// the ones still running from earlier ticks — not how many this tick may begin. One,
// where the prompt drain's budget is 50: queuing a prompt is a map write, while
// creating a session builds a git worktree, runs the repo's setup script and launches
// a program in a pty. A script that spooled twenty would otherwise have twenty
// worktree setups running at once, contending on the same index.lock and coming back
// to their callers as arbitrary-looking failures.
//
// Counting in-flight starts is the whole point. A per-tick budget looks identical in a
// single-tick test and delivers only a ~500ms stagger in production, because tick N+1
// skips the still-running request without spending anything and starts the next one
// anyway.
const createStartBudget = 1

// createDisposalBudget caps how many requests one tick may refuse or discard. Larger
// than the start budget because a refusal is a receipt and an unlink, not a worktree —
// but not unbounded: a two-day-old cron backlog would otherwise unlink thousands of
// expired requests inside one Update, freezing the UI. Bounded, a backlog clears at 50
// a tick while the loop stays responsive.
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
// than let through. And startNewSession no longer moves the cursor, opens the fold set
// or repoints the preview for a spawnBackground create, so there is nothing left for a
// state to be surprised by.
//
// The one thing a state does gate is an *accepted* confirm, which is why a staged
// spawn plan holds the drain — see stagedSpawnPlan.
func (m *home) drainCreateRequests() tea.Cmd {
	if m.stagedSpawnPlan() {
		return nil
	}

	entries, err := outbox.ListCreates()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool: %v", err)
		return nil
	}

	now := time.Now()
	// Seeded with the starts still running from earlier ticks: the budget is on
	// concurrency, not on arrivals.
	started := len(m.createsInFlight)
	var disposed int
	var cmds []tea.Cmd
	var refused int

	for _, e := range entries {
		if started >= createStartBudget && disposed >= createDisposalBudget {
			break
		}
		// An in-flight request still has its file, on purpose (see
		// settleCreateRequest), so it has to be skipped rather than re-executed.
		if m.outboxPoisoned[e.Path] || m.createRequestInFlight(e.Path) {
			continue
		}

		// Disposals and starts draw on separate budgets, so a backlog of expired
		// requests cannot spend the one start this tick was allowed to make.
		dispose := func(reason string) {
			m.rejectCreateRequest(e.Path, reason)
			disposed++
			refused++
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
			dispose("the request could not be read")

		case e.Request.Expired(now):
			if disposed >= createDisposalBudget {
				continue
			}
			age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a create request for %q: spooled %s ago, past the %s horizon",
				e.Request.Title, age, outbox.TTL)
			dispose(fmt.Sprintf("the request was spooled %s ago, past the %s horizon", age, outbox.TTL))

		default:
			if started >= createStartBudget {
				continue
			}
			cmd, reason := m.executeCreateRequest(e.Request)
			if reason != "" {
				if disposed >= createDisposalBudget {
					continue
				}
				log.WarningLog.Printf("refusing a create request for %q: %s", e.Request.Title, reason)
				dispose(reason)
				continue
			}
			// The file deliberately stays until the start lands and the row is
			// persisted, so `atrium new --wait` reading its absence means "created and
			// recorded" rather than "consumed".
			m.holdCreateRequest(e.Path, e.Request)
			started++
			cmds = append(cmds, cmd)
		}
	}

	// No title in either notice: a request's title has no length ceiling this side of
	// the wire format, and the notice row truncates its tail. Prose says why; the log
	// (and the caller's rejection receipt) says which.
	switch {
	case len(cmds) > 0:
		// A tick that both created and refused reports both: flashNotice writes one
		// notice, so naming only the create would silently drop the half the person at
		// the TUI can actually act on.
		text := "created a session from atrium new"
		if refused > 0 {
			text = fmt.Sprintf("%s (%d other request%s refused)", text, refused, plural(refused))
		}
		cmds = append(cmds, m.flashNotice(text, ui.NoticeInfo))
	case refused > 0:
		// A refusal reaches the person who ran `atrium new` as a receipt, but the
		// person at the TUI is the one who can fix a cap or a taken title, and to them
		// a silent tick is indistinguishable from no request at all.
		return m.flashNotice(fmt.Sprintf("refused %d create request%s from atrium new", refused, plural(refused)),
			ui.NoticeError)
	default:
		return nil
	}
	return tea.Batch(cmds...)
}

// stagedSpawnPlan reports whether a confirmation dialog is holding a spawn plan that
// has already passed its title and cap checks.
//
// This is the one place a background create genuinely can corrupt another surface.
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
func (m *home) executeCreateRequest(r outbox.Request) (tea.Cmd, string) {
	if err := tmuxAvailable(); err != nil {
		return nil, err.Error()
	}

	// The CLI bounds the title too, and that is the check a caller normally meets.
	// This one is for what the CLI cannot speak for: a hand-written spool file, or one
	// written by a build of atrium whose limit differs from this one's. Unbounded, an
	// over-long title reaches the list as a row nothing can render sensibly.
	if n := len([]rune(r.Title)); n > session.MaxTitleLen {
		return nil, fmt.Sprintf("the title is %d characters; the limit is %d", n, session.MaxTitleLen)
	}

	// Absolute, because session.NewInstance stores the absolute path and
	// findInstanceByIdentity below matches on it. The CLI already sends one; doing
	// it again here means a hand-written spool file cannot desynchronise the two.
	path, err := filepath.Abs(r.Path)
	if err != nil {
		return nil, fmt.Sprintf("%q is not a usable path: %v", r.Path, err)
	}

	// A non-git directory is not an error: it becomes a direct session, exactly as
	// it does in the form. It simply has no worktree and no branch.
	valid, direct, _ := targetValidity(m.ctx, path)
	if !valid {
		return nil, fmt.Sprintf("%q is not a directory", path)
	}

	// The group is derived here rather than read from m.newSessionGroup, which is
	// create-form state this path must not touch — see titleConflictIn.
	group := git.RepoGroupKey(m.ctx, path)
	if conflict := m.variantTitleConflictIn(group, r.Title, path, direct); conflict != "" {
		return nil, fmt.Sprintf("%s (%q in %s)", conflict, r.Title, path)
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
		return nil, hardCapMessage(sc.Limit)
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
			return nil, fmt.Sprintf(
				"every account in pool %q is rate-limited — pass --force to create anyway", pool)
		}
		sel = m.pinSoonestMember(pool, members)
	}

	if verdict == capConfirm && !r.Force {
		return nil, fmt.Sprintf("%s — pass --force to create anyway", hostCapacityLine(sc.Limit, count))
	}

	// Never dependency-isolating: that is a form choice, and shared is the default the
	// form itself starts from. sel is nil unless --force just accepted an exhausted
	// pool, which is the only account choice this path can make.
	cmd, err := m.startNewSession(r.Title, path, direct, false, program, r.Branch, r.Prompt, sel, spawnBackground, nil)
	if err != nil {
		return nil, err.Error()
	}
	return cmd, ""
}

// holdCreateRequest records that path's session is starting, keying it by the
// instance startNewSession just added to the list.
//
// The instance is recovered by identity rather than returned by startNewSession
// because (Title, Path) is unique by construction here — variantTitleConflictIn
// has just proved no other session holds either derived name — and threading a
// second return value through the form and smart-dispatch callers would buy
// nothing else.
func (m *home) holdCreateRequest(path string, r outbox.Request) {
	abs, err := filepath.Abs(r.Path)
	if err != nil {
		abs = r.Path
	}
	inst := m.findInstanceByIdentity(r.Title, abs)
	if inst == nil {
		// Unreachable: startNewSession added it a moment ago. Clearing the file is
		// still better than leaving one that no settle will ever reach, which would
		// be re-executed on the next tick and create a duplicate session.
		//
		// Rejected rather than removed, like every other failure here: the session's
		// real start outcome can no longer reach anyone, and unlinking silently would
		// tell a waiting --wait that a create it can say nothing about succeeded.
		log.ErrorLog.Printf("created %q but could not find it to track its request", r.Title)
		m.rejectCreateRequest(path,
			"the session was created but atrium lost track of it before it finished starting")
		return
	}
	if m.createsInFlight == nil {
		m.createsInFlight = make(map[*session.Instance]string)
	}
	m.createsInFlight[inst] = path
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
// The remaining window is a crash between the two, which self-corrects rather than
// duplicating: the next launch re-reads the file, and either the session persisted —
// so the title collides and the request is refused — or it did not, and re-creating it
// is exactly right. An orderly shutdown does not go through here at all, which is why
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
	m.discardSpoolFile(path, func() error { return outbox.Remove(path) })
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
	m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
}

// createRequestInFlight reports whether path's session is already starting. A
// linear scan because the map is bounded by the number of creates started but not
// yet settled, which createDrainBudget holds to one per tick.
func (m *home) createRequestInFlight(path string) bool {
	for _, p := range m.createsInFlight {
		if p == path {
			return true
		}
	}
	return false
}

// rejectCreateRequest leaves a receipt naming the reason and removes the request,
// so `atrium new --wait` reports the refusal instead of reading the unlink as a
// successful creation.
func (m *home) rejectCreateRequest(path, reason string) {
	m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
}
