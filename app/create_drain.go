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
)

// createDrainBudget caps how many requests one tick executes. One, where the
// prompt drain's budget is 50: queuing a prompt is a map write, while creating a
// session builds a git worktree, runs the repo's setup script and launches a
// program in a pty. A script that spooled twenty would otherwise start twenty
// worktree setups inside one tick, all racing the same git index.
//
// Spreading them costs a tick (~500ms) each, which is nothing against the seconds
// a single Start already takes.
const createDrainBudget = 1

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
// Nothing needed the gate. The one mutation that could reach another state is the
// selection, which startNewSession moves to the row it creates — and that is
// restored below rather than deferred around. Confirmations do not re-read it:
// confirmKill and confirmWorktreeAction capture their instance when the dialog is
// staged, so an accepted dialog acts on the session it named whatever the cursor
// did. The create form is the one surface with live per-keystroke verdicts about a
// title and a cap, and it re-runs both at submit (createSessionFromForm), so a
// session appearing underneath is refused there rather than let through.
func (m *home) drainCreateRequests() tea.Cmd {
	entries, err := outbox.ListCreates()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool: %v", err)
		return nil
	}

	now := time.Now()
	var spent int
	var cmds []tea.Cmd

	// startNewSession selects the row it creates, which is right for a keypress and
	// wrong for this: the request came from another terminal, and #439 settled that
	// a background event does not move a cursor a human placed. Captured here and
	// restored after, so the new session announces itself with its notice and its
	// spinner rather than by stealing the highlight.
	selected := m.list.GetSelectedInstance()

	for _, e := range entries {
		if spent >= createDrainBudget {
			break
		}
		// An in-flight request still has its file, on purpose (see
		// settleCreateRequest), so it has to be skipped rather than re-executed.
		if m.outboxPoisoned[e.Path] || m.createRequestInFlight(e.Path) {
			continue
		}
		spent++

		switch {
		case e.Err != nil:
			// Unreadable, or from a newer atrium. Discarding is the only way out: a
			// file nobody can decode and nobody deletes would be re-read on every
			// tick forever. ListCreates only ever surfaces files matching the
			// spool's own name format, so this can only discard our own.
			log.ErrorLog.Printf("discarding an unreadable create request: %v", e.Err)
			m.rejectCreateRequest(e.Path, "the request could not be read")

		case e.Request.Expired(now):
			age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding a create request for %q: spooled %s ago, past the %s horizon",
				e.Request.Title, age, outbox.TTL)
			m.rejectCreateRequest(e.Path,
				fmt.Sprintf("the request was spooled %s ago, past the %s horizon", age, outbox.TTL))

		default:
			cmd, reason := m.executeCreateRequest(e.Request)
			if reason != "" {
				log.WarningLog.Printf("refusing a create request for %q: %s", e.Request.Title, reason)
				m.rejectCreateRequest(e.Path, reason)
				continue
			}
			// The file deliberately stays until the start lands, so `atrium new
			// --wait` reading its absence means "created" rather than "consumed".
			m.holdCreateRequest(e.Path, e.Request)
			cmds = append(cmds, cmd)
		}
	}

	if len(cmds) == 0 {
		return nil
	}
	// A nil previous selection means the list was empty, and SelectInstance would
	// be a no-op anyway; leaving the new row selected is right there.
	if selected != nil {
		m.list.SelectInstance(selected)
	}
	// No title in the notice: a request's title has no length ceiling this side of
	// the wire format, and the notice row truncates its tail. The new row is
	// selected and spinning, which says which session far better than a name that
	// might be cut. Prose says why; the row says what.
	cmds = append(cmds, m.flashNotice("created a session from atrium new", ui.NoticeInfo))
	return tea.Batch(cmds...)
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
	switch capVerdict(sc, count, 1) {
	case capBlock:
		// --force is deliberately not consulted: see hardCapMessage.
		return nil, hardCapMessage(sc.Limit)
	case capConfirm:
		if !r.Force {
			return nil, fmt.Sprintf("%s — pass --force to create anyway", hostCapacityLine(sc.Limit, count))
		}
	case capAllow:
	}

	if pool, _, exhausted := m.allExhausted(plan); exhausted && !r.Force {
		return nil, fmt.Sprintf(
			"every account in pool %q is rate-limited — pass --force to create anyway", pool)
	}

	// Never dependency-isolating and never account-pinned: both are form choices,
	// and shared is the default the form itself starts from.
	cmd, err := m.startNewSession(r.Title, path, direct, false, program, r.Branch, r.Prompt, nil, false, nil)
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
		log.ErrorLog.Printf("created %q but could not find it to track its request", r.Title)
		m.discardSpoolFile(path, func() error { return outbox.Remove(path) })
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
// The cost is a crash window: a TUI that dies between starting a session and this
// call leaves the file, and the next launch re-reads it. That self-corrects rather
// than duplicating — if the session did start and persist, the title now collides
// and the request is refused; if it did not, re-creating it is exactly right.
func (m *home) settleCreateRequest(inst *session.Instance, startErr error) {
	path, ok := m.createsInFlight[inst]
	if !ok {
		return
	}
	delete(m.createsInFlight, inst)
	if startErr != nil {
		m.discardSpoolFile(path, func() error {
			return outbox.Reject(path, fmt.Sprintf("the session could not be started: %v", startErr))
		})
		return
	}
	m.discardSpoolFile(path, func() error { return outbox.Remove(path) })
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
