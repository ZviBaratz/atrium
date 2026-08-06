package app

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// capOutcome is what a creation attempt should do under the effective session cap.
type capOutcome int

const (
	// capAllow: the creation fits (or the cap is unlimited) — proceed.
	capAllow capOutcome = iota
	// capConfirm: the creation exceeds the host-derived soft cap — ask once, then
	// allow on confirmation.
	capConfirm
	// capBlock: the creation exceeds an explicit hard cap — refuse.
	capBlock
)

// capVerdict decides what a creation that would bring the session count to
// count+adding should do under sc. adding is the batch size (≥1); count is the
// current population (live sessions for a soft cap, all sessions for a hard cap).
// An unlimited cap (Limit ≤ 0) always allows; within Limit always allows; over a
// soft cap confirms; over a hard cap blocks.
func capVerdict(sc config.SessionCap, count, adding int) capOutcome {
	if sc.Limit <= 0 || count+adding <= sc.Limit {
		return capAllow
	}
	if sc.Soft {
		return capConfirm
	}
	return capBlock
}

// sessionCap resolves the effective cap for the enforcement sites. An unset
// max_sessions yields the host-derived soft cap (Limit from m.hostCap, injectable
// in tests); an explicit value delegates to config (a positive hard cap, or an
// explicit "unlimited" for ≤ 0).
func (m *home) sessionCap() config.SessionCap {
	if m.appConfig == nil || m.appConfig.MaxSessions == nil {
		return config.SessionCap{Limit: m.hostCap, Soft: true}
	}
	return m.appConfig.SessionCap()
}

// capCount returns the population to compare against sc: live (non-Paused)
// sessions for the host-derived soft cap — a paused session imposes no load — and
// all sessions for an explicit hard cap, whose contract is a total-session limit.
// This is the creation count, and a creation is the one action that grows both
// populations at once; a resume grows only the live one, which is why it measures
// itself (see resumeCapConfirm).
func (m *home) capCount(sc config.SessionCap) int {
	if sc.Soft {
		return m.list.NumActiveInstances()
	}
	return m.list.NumInstances()
}

// hostCapacityLine states the capacity and how much of it is spoken for — the one
// fact both the create confirmation (overCapMessage) and the resume clause
// (resumeCapClause) open with, written once so they cannot drift.
//
// It carries no noun on the count because the call sites reach it with 0, 1 and many
// live sessions: the earlier phrasing ("%d sessions are already running") printed "1
// sessions are already running" for a fan-out batch of 3 over a capacity of 2 with a
// single session live.
func hostCapacityLine(limit, live int) string {
	return fmt.Sprintf("Host capacity is %d, with %d already running", limit, live)
}

// overCapMessage is the host-capacity confirmation text: it names the derived cap
// and the live count so the tradeoff — more sessions queue rather than
// parallelize — is explicit at the moment the user crosses it, and it names the key
// that changes the limit.
//
// That tail used to read "(set max_sessions in config.json to change this)", which
// sent the user to a text editor for a setting the configuration panel owns. ','
// now opens the panel straight onto the Session limit row (confirmOverCap arms it,
// handleConfirmState consumes it). The dialog wraps at 46 cells, so the replacement
// is priced in rendered lines: the old tail took two and the new one takes one,
// making the dialog shorter as well as more useful.
func overCapMessage(limit, active, adding int) string {
	if adding > 1 {
		return fmt.Sprintf(
			"%s.\nSpawning %d more will queue, not parallelize, and drive up load.\n"+
				"Create them anyway? (%s to change the limit)",
			hostCapacityLine(limit, active), adding, keys.LabelOf(keys.KeySettings))
	}
	return fmt.Sprintf(
		"%s.\nAnother will queue, not parallelize, and drive up load.\n"+
			"Create it anyway? (%s to change the limit)",
		hostCapacityLine(limit, active), keys.LabelOf(keys.KeySettings))
}

// resumeCapConfirm reports whether resuming n paused sessions must ask the user
// first: it is capVerdict read from the resume side, and it differs from a creation
// in both of its inputs (#463).
//
// The count is the *live* population, because that is the only one a resume changes —
// the sessions themselves already exist. And only a soft cap is consulted, because a
// hard cap cannot be crossed by resuming: capCount measures it against
// NumInstances(), paused sessions included, and every creation gate holds
// total + adding ≤ Limit, so live + n ≤ total ≤ Limit for any set of paused sessions.
// The one state where live + n could pass an explicit Limit is a total that already
// does — max_sessions lowered under an existing fleet — where creation is refused
// outright and refusing to restore parked work as well would strand it.
//
// So resume confirms or proceeds; it never blocks. Nothing it starts is a session the
// user does not already have.
func resumeCapConfirm(sc config.SessionCap, live, n int) bool {
	return sc.Soft && capVerdict(sc, live, n) != capAllow
}

// resumeCapClause is the host-capacity paragraph appended to a resume confirmation
// when the batch would cross the soft cap. It states the capacity and then what the
// extra sessions cost, in fewer words than overCapMessage spends: the resume question
// already owns the dialog's first two rendered lines (it wraps at 46 cells), and the
// create dialog is where the max_sessions escape hatch is taught.
//
// n is the batch the user was asked about, and is ≥ 1 the way capVerdict's adding is:
// resumeAll and resumeMarked both refuse an empty set before they ask, so the singular
// branch is exactly one session and never none. A resume that then fails — a branch
// checked out elsewhere — leaves the live count lower than this predicted, and the
// batch summary reports it; a confirmation does not enumerate error paths the run
// itself surfaces (the same call pauseConfirmMessage makes).
func resumeCapClause(limit, live, n int) string {
	if n > 1 {
		return fmt.Sprintf("%s — %d more will queue rather than parallelize.",
			hostCapacityLine(limit, live), n)
	}
	return fmt.Sprintf("%s — another will queue rather than parallelize.",
		hostCapacityLine(limit, live))
}

// resumeCapNotice returns the host-capacity clause for a resume of n paused sessions,
// or "" when it fits the budget (or no soft cap applies) — the model-reading half of
// resumeCapConfirm. It reads NumActiveInstances() rather than capCount(sc) because the
// live population is the only one a resume changes; whether that reading decides
// anything is resumeCapConfirm's call.
func (m *home) resumeCapNotice(n int) string {
	sc := m.sessionCap()
	live := m.list.NumActiveInstances()
	if !resumeCapConfirm(sc, live, n) {
		return ""
	}
	return resumeCapClause(sc.Limit, live, n)
}

// startupParkNotice is the toast for sessions a startup recovery left paused because
// relaunching their agents would have exceeded the host budget (#474). "" when
// nothing was deferred.
//
// A silent Running→Paused is indistinguishable from a user pause — the finding #270
// made and lostRecovery exists for — so this park owes the same batched toast, in the
// same voice as surfaceLostRecoveries: state what happened, then the key that undoes
// it. Not a modal — showInfo is for what the user must read and act on, and nothing
// here blocks them; the parked rows are visible and r brings any of them back whenever
// they get to it. What the rows cannot say is *why* they are paused, which is the one
// thing this line exists to carry (and the atrium log keeps, per session, for a user
// who missed the toast).
//
// It states the capacity rather than reusing hostCapacityLine, and it names no
// session. Both are width: the hint-bar notice TRUNCATES ITS TAIL at width-2
// (ui.Menu.String), so on an 80-column terminal a longer line would drop the very key
// it is here to teach. hostCapacityLine's live count and a session title (unbounded
// user input) are the two halves that had to go; the capacity is the number the user
// acts on, while what is running is on the rows in front of them. Both spellings
// below are asserted against a width bound rather than eyeballed.
//
// It deliberately does not teach ',': the create dialog owns the max_sessions escape
// hatch (see resumeCapClause), and this line has room for exactly one key.
//
// That key is ctrl+r even for a single session, where r looks like the friendlier
// advice. r acts on the SELECTED row (resumeSelectedKey) and a parked session is by
// construction the one that came after the budget ran out in stored order, so it is
// never the startup selection — following "press r" would just earn the "only paused
// sessions resume" refusal. Naming the row instead is not open either: a title is
// unbounded input on a row that truncates. So the advice is the key that needs no
// selection, and it says "resumes paused" rather than "resumes them" because resumeAll
// takes every paused row in view, which may include sessions the user parked
// deliberately; its confirmation states the real count.
//
// The tail is that terse because the width bound is real and both interpolated numbers
// can reach three digits: the count is bounded only by the fleet, and the capacity is
// DefaultSessionCap(), which is half the host's threads — 128 on a 256-thread machine.
// The fuller "(ctrl+r resumes paused sessions)" measured 82 cells at that worst case and
// would have had "paused sessions" truncated off an 80-column bar. The bound is asserted
// at three digits for both, which is why the overflow was caught rather than shipped.
func startupParkNotice(d session.DeferredRecovery) string {
	n := len(d.Sessions)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return fmt.Sprintf("1 session stayed paused — host capacity is %d (ctrl+r resumes paused)", d.Limit)
	}
	return fmt.Sprintf("%d sessions stayed paused — host capacity is %d (ctrl+r resumes paused)", n, d.Limit)
}

// earlierParkNotice is startupParkNotice for a park a DIFFERENT process made while the
// user was away — the autoyes daemon's, arriving through the spool (#622). "" when
// nothing survived reconciliation.
//
// Only the tense differs, and it has to. "stayed paused" is present-tense about the
// load the user just triggered; a spooled report describes a decision taken hours ago,
// by a process they never saw, so the same words would misdate it. Everything else is
// deliberately identical — the capacity rather than a live count, no session title, and
// ctrl+r rather than r — for the reasons startupParkNotice argues above.
//
// "earlier" is also what the width budget affords. It costs one cell more than "stayed
// paused" (74 against 73 at the worst case both interpolations can reach), where
// "parked while away" would leave a single cell of headroom on an 80-column bar; both
// spellings are held to startupParkNoticeMaxWidth by
// TestStartupParkNoticeFitsNarrowBar rather than by this arithmetic.
func earlierParkNotice(d session.DeferredRecovery) string {
	n := len(d.Sessions)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return fmt.Sprintf("1 session parked earlier — host capacity is %d (ctrl+r resumes paused)", d.Limit)
	}
	return fmt.Sprintf("%d sessions parked earlier — host capacity is %d (ctrl+r resumes paused)", n, d.Limit)
}

// startupParkNoticeMaxWidth is the widest any park-notice spelling may render, in
// cells — both startupParkNotice's and earlierParkNotice's — and it is a *bound to
// keep* rather than a measurement: the notice row truncates its tail, so a spelling
// that outgrows the narrowest supported terminal loses the key it advertises. 78 is the
// room an 80-column terminal leaves (ui.Menu.String truncates at width-2). Pinned by
// TestStartupParkNoticeFitsNarrowBar.
const startupParkNoticeMaxWidth = 78

// spawnPlan is a fully-validated, ready-to-spawn creation: title conflicts,
// program flags, cap, and target have all passed. It is captured before a
// host-capacity confirmation so the post-confirm path spawns exactly what the
// user submitted, with no overlay to re-read.
type spawnPlan struct {
	titles   []string
	path     string
	direct   bool
	programs []string
	branch   string
	prompt   string
	account  *overlay.AccountSelection
}

// proceedOverCapMsg is emitted when the user confirms the host-capacity prompt; its
// Update handler spawns the staged pendingOverCap plan.
type proceedOverCapMsg struct{}

// proceedExhaustedMsg is emitted when the user accepts creating a session even
// though every member of the routed pool is rate-limited (see confirmAllExhausted).
type proceedExhaustedMsg struct{}

// confirmOverCap stages plan behind a host-capacity confirmation, dismisses the
// create form, and returns the confirm command. The dismissed form is stashed as a
// restorable draft first, so declining is non-destructive (the accept path clears it
// via closeCreateForm on commit). On acceptance the staged action emits
// proceedOverCapMsg (handled in Update → spawnVariants); on decline nothing is
// spawned and the stale plan is inert (overwritten by the next stage).
func (m *home) confirmOverCap(plan spawnPlan, limit, active int) tea.Cmd {
	m.pendingOverCap = &plan
	// The message teaches ','; handleConfirmState turns that into a deep link onto the
	// row it names. Armed here rather than in confirmAction, because it is this dialog's
	// copy that promises it — every other confirmation leaves ',' inert.
	m.pendingConfirmSettingKey = "max_sessions"
	m.stashDirtyCreateForm()
	m.textInputOverlay = nil
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
	return m.confirmAction(
		overCapMessage(limit, active, len(plan.programs)),
		// The spawn this unblocks happens later, in the proceedOverCapMsg handler,
		// and each new row announces itself with its own Loading spinner. A label
		// here would name an operation that is not running yet.
		instantAction,
		func() tea.Msg { return proceedOverCapMsg{} })
}

// spawnVariants boots every variant in plan, which has passed all pre-flight
// validation (titles, programs, cap). It is shared by the direct form submit
// (allow) and the post-confirm resume; the resume path runs with the form already
// closed, so every form touch is nil-guarded. On a first-variant failure the batch
// has spawned nothing, so the form (if still open) stays open to retry; a
// later-variant failure has committed live sessions, so the form closes and the
// error surfaces alongside them.
func (m *home) spawnVariants(plan spawnPlan) tea.Cmd {
	total := len(plan.programs)
	cmds := make([]tea.Cmd, 0, total)
	for i := range plan.programs {
		created, err := m.startNewSession(
			plan.titles[i], plan.path, plan.direct, plan.programs[i],
			plan.branch, plan.prompt, plan.account, total > 1)
		if err != nil {
			if i == 0 {
				if m.textInputOverlay != nil {
					m.textInputOverlay.Submitted = false // keep the form open to correct + retry
				}
				return m.handleError(err)
			}
			// Some variants are already live; a resubmit would double-spawn them.
			m.recordPrompt(plan.prompt)
			m.closeCreateForm()
			return tea.Batch(append(cmds, m.handleError(err))...)
		}
		cmds = append(cmds, created)
	}
	m.recordPrompt(plan.prompt)
	m.closeCreateForm()
	return tea.Batch(cmds...)
}

// closeCreateForm tears down the create form after a committed submit: it drops the
// overlay and any stashed/persisted draft, and returns the UI to the default state.
func (m *home) closeCreateForm() {
	m.textInputOverlay = nil
	m.stashedDraft = nil
	m.clearPersistedDraft()
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
}
