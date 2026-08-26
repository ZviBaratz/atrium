package app

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolRetire writes a retirement into the spool and returns its record path.
func spoolRetire(t *testing.T, r outbox.Retire) string {
	t.Helper()
	record, err := outbox.WriteRetire(r)
	require.NoError(t, err)
	return record
}

func killRecord(t *testing.T, title, path string) string {
	t.Helper()
	return spoolRetire(t, outbox.Retire{Title: title, Path: path, Mode: outbox.ModeKill})
}

// retireRecords is what the spool still holds, so a test can say "the record was
// answered" and "the record is still queued" as different things.
func retireRecords(t *testing.T) []outbox.RetireEntry {
	t.Helper()
	entries, err := outbox.ListRetires()
	require.NoError(t, err)
	return entries
}

// rejectionFor is the reason a producer blocked in --wait would read back.
func rejectionFor(t *testing.T, record string) string {
	t.Helper()
	reason, ok := outbox.Rejection(record)
	require.True(t, ok, "the record must leave a receipt, or --wait reads the unlink as success")
	return reason
}

// retirable makes inst look like the one shape a kill clears: idle, and carrying
// computed stats that show nothing at risk.
//
// measuredClean rather than a bare &git.DiffStats{}, because the gate now separates
// "measured and clean" from "never measured" and a zero DiffStats is the second one.
func retirable(inst *session.Instance) {
	inst.SetStatus(session.Ready)
	inst.SetDiffStats(measuredClean())
}

// measuredClean is what a successful RepoStats returns for a tree with nothing at
// risk. BranchStatsMeasured is the field that says the two git commands behind Dirty
// and Unpushed actually ran, so leaving it off asserts about the unestablished path
// while looking like it asserts about the clean one.
func measuredClean() *git.DiffStats {
	return &git.DiffStats{BranchStatsMeasured: true}
}

// TestRetireDrainDispatchesAKillForACleanIdleSession is the happy path, observed
// where it can be observed without a real teardown: the record is consumed and the
// instance is marked retiring, which is what armTeardown does and what keeps the
// poll loop from reading the dying pane as a lost session.
func TestRetireDrainDispatchesAKillForACleanIdleSession(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	killRecord(t, "fix-auth", "/repo/web")

	cmd := h.drainRetireRequests()

	require.NotNil(t, cmd, "a dispatched teardown returns a command to run it")
	assert.True(t, h.retiring[inst], "the row must be marked retiring for the length of the teardown")
	// Claimed rather than consumed: the record is what --wait watches, and the teardown
	// has not happened yet. See TestRetireDrainKeepsTheRecordUntilTheTeardownReportsBack.
	require.NotNil(t, h.pendingRetirement)
	assert.Equal(t, inst, h.pendingRetirement.inst)
}

// TestRetireDrainDispatchesAPauseWithoutGating: pause is the escape valve, so the
// drain must not re-gate it either. A session with uncommitted work and a working
// agent is exactly what an orchestrator reaches for pause to reclaim.
func TestRetireDrainDispatchesAPauseWithoutGating(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	inst.SetStatus(session.Running)
	inst.SetDiffStats(&git.DiffStats{Dirty: true, Unpushed: 4, BranchStatsMeasured: true})
	spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModePause})

	cmd := h.drainRetireRequests()

	require.NotNil(t, cmd)
	assert.True(t, h.actionInFlight,
		"a pause must run behind the busy gate: its pane dies seconds before the status flips")
	require.NotNil(t, h.pendingRetirement, "and its record is claimed until the park reports")
	assert.Equal(t, outbox.ModePause, h.pendingRetirement.mode)
}

// TestRetireDrainRegatesBeforeTearingDown is the TOCTOU half of the gate, and the
// reason the producer's check is not enough on its own. At least a poll tick passes
// between the spool and this walk, and the target agent keeps working through it — so
// a session that was clean when `atrium kill` looked can be dirty by now.
//
// These are the refusals that ANSWER the request: each names something a person clears
// (push the branch, wait for the turn), so the record is spent and its producer is owed
// the reason. The conditions that clear themselves are the next test's.
func TestRetireDrainRegatesBeforeTearingDown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status session.Status
		stats  *git.DiffStats
		says   string
	}{
		{"went dirty", session.Ready, &git.DiffStats{Dirty: true, BranchStatsMeasured: true}, "uncommitted"},
		{"has unpushed commits", session.Ready, &git.DiffStats{Unpushed: 2, BranchStatsMeasured: true}, "2 unpushed"},
		{"started working again", session.Running, measuredClean(), "still working"},
		{"has background work outstanding", session.Pending, measuredClean(), "still working"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			inst.SetStatus(tc.status)
			inst.SetDiffStats(tc.stats)
			record := killRecord(t, "fix-auth", "/repo/web")

			cmd := h.drainRetireRequests()

			assert.False(t, h.retiring[inst], "a refused kill must leave the session alone")
			assert.Contains(t, rejectionFor(t, record), tc.says)
			assert.Empty(t, retireRecords(t))
			assert.NotNil(t, cmd, "a refusal has to surface, not just leave a receipt for a --wait nobody ran")
		})
	}
}

// TestRetireDrainHoldsTheRefusalsThatClearThemselves is the other polarity of the
// re-gate, and the one a durable request depends on. A session still starting finishes
// starting; a tree whose numbers could not be taken is re-measured by the next poll.
// Nobody has to do anything for either, so answering them refuses a request for a
// condition that is false a tick later — and it is not a rare tick: every row is
// Loading while it comes online, and stats a state.json never carried read as
// unmeasured until the first poll lands.
//
// The record has to STAY for that to be a hold rather than a slow refusal, and no
// receipt may be written: a receipt is terminal, so one written here would be re-read
// as an answer even after the condition cleared.
func TestRetireDrainHoldsTheRefusalsThatClearThemselves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status session.Status
		stats  *git.DiffStats
	}{
		{"is still starting up", session.Loading, measuredClean()},
		{"has no computed stats at all", session.Ready, nil},
		{"was measured but not by anything that succeeded", session.Ready, &git.DiffStats{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			inst.SetStatus(tc.status)
			inst.SetDiffStats(tc.stats)
			record := killRecord(t, "fix-auth", "/repo/web")
			_ = record

			cmd := h.drainRetireRequests()

			assert.Nil(t, cmd, "a hold surfaces nothing: nothing happened and nothing was decided")
			assert.False(t, h.retiring[inst], "and it must not touch the session")
			_, receipted := outbox.Rejection(record)
			assert.False(t, receipted, "a receipt would answer a request that is still pending")
			require.Len(t, retireRecords(t), 1, "the record stays queued for the next tick")
		})
	}
}

// TestRetireDrainActsOnceTheTransientConditionClears is the other end of that hold: a
// held record is not a lost one. This is the ordinary launch sequence — the first tick
// finds the row Loading, a later one finds it ready — and the kill an agent spooled
// while no TUI was up has to survive it.
func TestRetireDrainActsOnceTheTransientConditionClears(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	inst.SetStatus(session.Loading)
	inst.SetDiffStats(measuredClean())
	record := killRecord(t, "fix-auth", "/repo/web")

	require.Nil(t, h.drainRetireRequests(), "the launch tick holds")
	require.Len(t, retireRecords(t), 1)

	retirable(inst)
	assert.NotNil(t, h.drainRetireRequests(), "the next tick dispatches it")
	assert.True(t, h.retiring[inst])
	_ = record
}

// TestRetireDrainRefusesASessionItCannotFind: a session killed between the spool and
// this tick is the realistic case, and the receipt has to say so — otherwise
// `atrium kill --wait` reads the unlink as a successful teardown of a session that
// something else retired.
func TestRetireDrainRefusesASessionItCannotFind(t *testing.T) {
	h := drainHome(t)
	record := killRecord(t, "gone", "/repo/web")

	h.drainRetireRequests()

	assert.Contains(t, rejectionFor(t, record), "no session")
	assert.Empty(t, retireRecords(t))
}

// TestRetireDrainMatchesOnTheIdentityPairNotTheTitle: titles are unique only within a
// repo group, so a record naming one repo must never retire the same-titled session
// in another. This is the assertion that fails if the walk ever matches on Title.
func TestRetireDrainMatchesOnTheIdentityPairNotTheTitle(t *testing.T) {
	h := drainHome(t)
	other := addInstance(t, h, "fix-auth", "/repo/other")
	retirable(other)
	record := killRecord(t, "fix-auth", "/repo/web")

	h.drainRetireRequests()

	assert.False(t, h.retiring[other], "a session in a different repo is not the target")
	assert.Contains(t, rejectionFor(t, record), "no session")
}

// TestRetireDrainDiscardsAnExpiredRecord: a retirement spooled a day ago describes a
// session that has moved on, so acting on it is worse than dropping it.
func TestRetireDrainDiscardsAnExpiredRecord(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := spoolRetire(t, outbox.Retire{
		Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModeKill,
		CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})

	h.drainRetireRequests()

	assert.False(t, h.retiring[inst], "an expired record must not tear anything down")
	assert.Contains(t, rejectionFor(t, record), "horizon")
}

// TestRetireDrainHoldsWhileATeardownIsInFlight: one teardown at a time. A kill is
// several subprocesses plus a recursive worktree delete, and a second dispatched
// underneath it would race the first for the same list and storage.
func TestRetireDrainHoldsWhileATeardownIsInFlight(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	h.retiring = map[*session.Instance]bool{addInstance(t, h, "other", "/repo/web"): true}
	record := killRecord(t, "fix-auth", "/repo/web")

	assert.Nil(t, h.drainRetireRequests(), "the tick holds rather than acting")
	assert.False(t, h.retiring[inst])
	entries := retireRecords(t)
	require.Len(t, entries, 1, "a held record stays queued")
	assert.Equal(t, record, entries[0].Path)
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "holding is not refusing — no receipt")
}

// TestRetireDrainHoldsWhileAConfirmationIsOpen closes the gap none of the four
// inherited holds cover. confirmKill captures its instance when the dialog is staged
// and nothing marks that instance retiring until the dialog is accepted, so a
// teardown dispatched underneath an open kill dialog leaves its accept to act on a
// session that is already gone.
func TestRetireDrainHoldsWhileAConfirmationIsOpen(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	// The real dialog rather than the state it sets, because the state is no longer
	// what the drain reads: confirmKill is what stashes the action naming this row.
	h.confirmKill(inst)
	require.NotNil(t, h.pendingConfirmAction, "precondition: the dialog captured an action")
	killRecord(t, "fix-auth", "/repo/web")

	assert.Nil(t, h.drainRetireRequests())
	assert.False(t, h.retiring[inst])
	assert.Len(t, retireRecords(t), 1, "the record waits for the dialog")
}

// TestRetireDrainDispatchesOneTeardownPerTick: refusals are cheap and can all be
// answered at once, but a teardown is seconds of I/O. The second record must be left
// queued rather than dispatched alongside the first.
func TestRetireDrainDispatchesOneTeardownPerTick(t *testing.T) {
	h := drainHome(t)
	first := addInstance(t, h, "fix-auth", "/repo/web")
	second := addInstance(t, h, "add-cache", "/repo/web")
	retirable(first)
	retirable(second)
	killRecord(t, "fix-auth", "/repo/web")
	killRecord(t, "add-cache", "/repo/web")

	require.NotNil(t, h.drainRetireRequests())

	assert.True(t, h.retiring[first], "the older record goes first")
	assert.False(t, h.retiring[second])
	require.NotNil(t, h.pendingRetirement)
	assert.Equal(t, first, h.pendingRetirement.inst)
	// Two records still on disk, and they mean different things: the first is claimed
	// pending its outcome, the second was never picked up. Neither has a receipt.
	require.Len(t, retireRecords(t), 2)
	for _, e := range retireRecords(t) {
		_, answered := outbox.Rejection(e.Path)
		assert.False(t, answered, "neither a claim nor a hold is a refusal")
	}
}

// TestRetireDrainAnswersEveryRefusalOnOneTick is the other side of that budget: a
// backlog of records the drain will never act on must not take one tick each, or a
// spool full of stale requests starves the one good record behind them.
func TestRetireDrainAnswersEveryRefusalOnOneTick(t *testing.T) {
	h := drainHome(t)
	first := killRecord(t, "gone-a", "/repo/web")
	second := killRecord(t, "gone-b", "/repo/web")
	third := killRecord(t, "gone-c", "/repo/web")

	h.drainRetireRequests()

	assert.Empty(t, retireRecords(t))
	for _, record := range []string{first, second, third} {
		assert.Contains(t, rejectionFor(t, record), "no session")
	}
}

// TestRetireDrainDiscardsAnUnreadableRecord: a file nobody can decode and nobody
// deletes is re-read on every tick forever. ListRetires only ever surfaces files
// matching the spool's own name format, so this can only discard our own.
func TestRetireDrainDiscardsAnUnreadableRecord(t *testing.T) {
	h := drainHome(t)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NoError(t, os.WriteFile(record, []byte(`{not json`), 0o644))

	h.drainRetireRequests()

	assert.Empty(t, retireRecords(t))
	assert.Contains(t, rejectionFor(t, record), "could not be read")
}

// directInstance registers a direct (non-git) session, the one shape neither verb can
// act on: no worktree to free, no branch to delete.
func directInstance(t *testing.T, h *home, title, path string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: path, Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	return inst
}

// TestRetireDrainRechecksTheStatesTheCommandRefuses is the other half of the TOCTOU
// story, and the half the first implementation left out. `atrium kill` screens a
// direct, parked or starting session before it spools, but the drain re-ran only the
// tree gate — so a session that entered one of those states in the window was torn
// down by the same code path whose command refuses it outright.
//
// A pause is not exempt. It is ungated on the TREE, which is a statement about work at
// risk; it was never meant to be able to park a session whose Start is still building
// the worktree it would remove — which is covered by the hold test rather than here,
// because a Start finishes on its own and so must not spend the record.
func TestRetireDrainRechecksTheStatesTheCommandRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode outbox.Mode
		make func(*testing.T, *home) *session.Instance
		says string
	}{
		{"a kill of a session parked since the spool", outbox.ModeKill,
			func(t *testing.T, h *home) *session.Instance {
				inst := addInstance(t, h, "fix-auth", "/repo/web")
				retirable(inst)
				inst.SetStatus(session.Paused)
				return inst
			}, "cannot be established"},
		{"a pause of a session parked since the spool", outbox.ModePause,
			func(t *testing.T, h *home) *session.Instance {
				inst := addInstance(t, h, "fix-auth", "/repo/web")
				inst.SetStatus(session.Paused)
				return inst
			}, "already paused"},

		{"a kill of a direct session", outbox.ModeKill,
			func(t *testing.T, h *home) *session.Instance {
				inst := directInstance(t, h, "fix-auth", "/repo/web")
				retirable(inst)
				return inst
			}, "direct"},
		{"a pause of a direct session", outbox.ModePause,
			func(t *testing.T, h *home) *session.Instance {
				return directInstance(t, h, "fix-auth", "/repo/web")
			}, "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			inst := tc.make(t, h)
			record := spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: tc.mode})

			h.drainRetireRequests()

			assert.False(t, h.retiring[inst], "a refused retirement must leave the session alone")
			assert.Contains(t, rejectionFor(t, record), tc.says)
			assert.Nil(t, h.pendingRetirement, "and must not claim a record it did not act on")
		})
	}
}

// TestRetireDrainKeepsTheRecordUntilTheTeardownReportsBack is the delivery contract,
// and it was inverted.
//
// awaitSpool reads "record gone, no receipt" as a successful retirement, and the drain
// used to unlink at DISPATCH — synchronously, before the returned tea.Cmd had run a
// line. So `--wait` reported success the instant the drain decided to try, which is
// not the same event: killIOCmd still refuses a branch checked out in the base repo,
// and every Instance.Pause failure lands in batchPauseDoneMsg.failures.
func TestRetireDrainKeepsTheRecordUntilTheTeardownReportsBack(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")

	require.NotNil(t, h.drainRetireRequests())

	require.NotNil(t, h.pendingRetirement, "the dispatch claims the record rather than answering it")
	assert.Equal(t, record, h.pendingRetirement.record)
	entries := retireRecords(t)
	require.Len(t, entries, 1, "the record is still on disk: nothing has been retired yet")
	assert.Equal(t, record, entries[0].Path)
	_, answered := outbox.Rejection(record)
	assert.False(t, answered, "and no receipt, because there is no outcome to report")
}

// TestRetireDrainReportsATeardownThatSucceeded: the record goes when the teardown
// lands, which is the event `--wait` is actually waiting for.
func TestRetireDrainReportsATeardownThatSucceeded(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())

	h.settleRetirement(inst, nil)

	assert.Nil(t, h.pendingRetirement)
	assert.Empty(t, retireRecords(t), "the record is answered by the outcome, not the attempt")
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a teardown that happened is not a refusal")
}

// TestRetireDrainReportsATeardownThatFailed is the case the old ordering could not
// report at all: `atrium kill x --wait 60s` exited 0 while x sat untouched, because
// the only signal was an unlink that had already happened.
func TestRetireDrainReportsATeardownThatFailed(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())

	h.settleRetirement(inst, errors.New("branch for fix-auth is checked out in the main repo"))

	assert.Nil(t, h.pendingRetirement)
	assert.Empty(t, retireRecords(t))
	assert.Contains(t, rejectionFor(t, record), "checked out in the main repo",
		"the producer has to read back the reason its session is still there")
}

// TestRetireDrainIgnoresAnOutcomeForADifferentSession: the done handlers fire for
// every kill and pause, TUI-initiated ones included, so the claim has to be keyed to
// the instance the drain dispatched or a keypress-driven pause would answer a spooled
// record it had nothing to do with.
func TestRetireDrainIgnoresAnOutcomeForADifferentSession(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	other := addInstance(t, h, "add-cache", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())

	h.settleRetirement(other, nil)

	require.NotNil(t, h.pendingRetirement, "somebody else's teardown does not settle ours")
	assert.Len(t, retireRecords(t), 1)
	assert.Equal(t, record, h.pendingRetirement.record)
}

// TestRetireDrainHoldsWhileARetirementIsUnsettled closes the window a drained PAUSE
// leaves open. A kill is covered by the retiring mark for its whole async window; a
// pause sets only actionInFlight, and the asyncActionDoneMsg handler clears that
// before batchPauseDoneMsg is processed — so between those two messages neither
// inherited hold is true and a second teardown could be dispatched into a model half
// that has not run.
func TestRetireDrainHoldsWhileARetirementIsUnsettled(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModePause})
	require.NotNil(t, h.drainRetireRequests())
	require.NotNil(t, h.pendingRetirement)
	// What the real sequence does between the two messages, and the reason the
	// inherited holds are not enough here.
	h.actionInFlight = false
	second := addInstance(t, h, "add-cache", "/repo/web")
	retirable(second)
	killRecord(t, "add-cache", "/repo/web")

	assert.Nil(t, h.drainRetireRequests(), "the tick holds until the first retirement reports")
	assert.False(t, h.retiring[second])
}

// TestRetireDrainAbandonsARetirementThatNeverReported is the backstop the claim needs.
// Holding on an unsettled retirement means a claim that is never answered would wedge
// the spool for good, so the drain gives up on one past its grace and says so, rather
// than blocking every later record behind it until the TTL.
func TestRetireDrainAbandonsARetirementThatNeverReported(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())
	require.NotNil(t, h.pendingRetirement)
	h.pendingRetirement.at = time.Now().Add(-retireSettleGrace - time.Minute)
	// Deliberately NOT clearing actionInFlight or the retiring mark by hand. The lost
	// message is exactly what would have cleared them, so a test that clears them itself
	// is testing a state the scenario never reaches — and it hid the fact that the
	// abandon released only the claim.
	require.True(t, h.actionInFlight, "precondition: the dispatch armed the busy gate")
	require.True(t, h.retiring[inst], "precondition: and the retiring mark")

	h.drainRetireRequests()

	assert.Nil(t, h.pendingRetirement, "an unanswerable claim is released, not held forever")
	assert.Contains(t, rejectionFor(t, record), "never reported",
		"and the producer is told rather than left to time out")
	assert.False(t, h.actionInFlight, "and the busy gate the dispatch armed")
	assert.False(t, h.retiring[inst], "and the retiring mark, which holds both drains")
	assert.False(t, h.retireDrainHeld(), "so the spool is actually drainable again")
	assert.False(t, h.createDrainHeld(), "and `atrium new` is not wedged behind it either")
}

// TestRetireDrainDrainsAgainAfterAnAbandonment is the abandonment's whole point stated as
// behaviour rather than as fields: the next record has to get through. Releasing the claim
// but leaving actionInFlight and the retiring mark set traded a wedged spool for a wedged
// key gate, which is the same outage with a different cause.
func TestRetireDrainDrainsAgainAfterAnAbandonment(t *testing.T) {
	h := drainHome(t)
	lost := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(lost)
	killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())
	require.NotNil(t, h.pendingRetirement)
	h.pendingRetirement.at = time.Now().Add(-retireSettleGrace - time.Minute)

	next := addInstance(t, h, "add-cache", "/repo/web")
	retirable(next)
	killRecord(t, "add-cache", "/repo/web")

	// One tick does both, which is why the abandonment runs before the hold rather than
	// after it: the release and the walk are the same tick, so nothing waits an extra
	// interval to discover the spool is usable again.
	assert.NotNil(t, h.drainRetireRequests(), "the spool is live again")
	assert.True(t, h.retiring[next], "and the record behind the lost one is acted on")
	assert.Equal(t, next, h.pendingRetirement.inst, "claimed by the new dispatch, not the lost one")
}

// TestRetireDrainHoldsWhileAnyOverlayHoldsAnInstance: a confirmation was not the only
// overlay that captures one. handlePauseDone ends every successful single-session pause
// in stateRename with that instance in renameTarget, and the queue overlay's
// queueTarget mirrors it — so a teardown dispatched underneath either leaves an
// overlay to act on a session that is already gone. With the deep-rename toggle on
// that is a tmux rename, a `git branch -m` and a worktree move against a deleted
// session.
//
// Each capture is set here rather than the UI state it comes with, because the capture
// is what the drain reads. The companion test below is why.
func TestRetireDrainHoldsWhileAnyOverlayHoldsAnInstance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capture func(*home, *session.Instance)
	}{
		{"a confirmation stashed a teardown", func(h *home, inst *session.Instance) {
			h.confirmKill(inst)
		}},
		{"a rename captured the row", func(h *home, inst *session.Instance) {
			h.state, h.renameTarget = stateRename, inst
		}},
		{"the queue overlay captured the row", func(h *home, inst *session.Instance) {
			h.state, h.queueTarget = stateQueue, inst
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			retirable(inst)
			tc.capture(h, inst)
			killRecord(t, "fix-auth", "/repo/web")

			assert.Nil(t, h.drainRetireRequests())
			assert.False(t, h.retiring[inst])
			assert.Len(t, retireRecords(t), 1, "the record waits for the overlay to close")
		})
	}
}

// TestRetireDrainActsUnderAFrameThatHoldsNoInstance is the half a blanket "anything but
// the default frame" gate got wrong, and it is not a corner: two of these frames never
// end on their own.
//
// A fresh install sits in stateWelcome until somebody answers the modal, and nothing
// answers it on a machine driven only by `atrium new` — markWelcomeSeen is deliberately
// skipped for a background spawn. stateInfo is the same trap arriving later: a drained
// retirement that fails used to raise one, so the drain would hold on the modal its own
// failure put up. Under either, a blanket gate means `atrium kill --wait` always times
// out and every record expires at the TTL, on exactly the headless machine these verbs
// exist for.
//
// The rest are here because they are one keypress away and hold nothing: none captures
// a row to act on when it closes.
func TestRetireDrainActsUnderAFrameThatHoldsNoInstance(t *testing.T) {
	for _, st := range []state{stateWelcome, stateInfo, stateHelp, statePrompt, stateFilter, stateSettings} {
		t.Run(fmt.Sprintf("state %d", st), func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			retirable(inst)
			h.state = st
			killRecord(t, "fix-auth", "/repo/web")

			assert.NotNil(t, h.drainRetireRequests(), "the frame captures no row, so nothing is at risk")
			assert.True(t, h.retiring[inst], "and the retirement an agent asked for goes through")
		})
	}
}

// TestRetireDrainHoldsWhileTmuxIsUnusable mirrors the hold drainCreateRequests has
// had, for the reason that one documents: the window of a `brew upgrade tmux` must not
// destroy what is queued in it.
//
// A kill in that window is worse than a lost create. Instance.Kill wraps the
// kill-session failure and proceeds unconditionally to the worktree cleanup, and
// applyKillDone removes the row and the storage entry regardless — so the branch is
// deleted, the row is gone, and the agent is still running on the socket with nothing
// that owns it.
func TestRetireDrainHoldsWhileTmuxIsUnusable(t *testing.T) {
	orig := tmuxAvailable
	tmuxAvailable = func() error { return errors.New("tmux is not on PATH") }
	t.Cleanup(func() { tmuxAvailable = orig })

	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")

	assert.Nil(t, h.drainRetireRequests())
	assert.False(t, h.retiring[inst])
	assert.Len(t, retireRecords(t), 1, "held, not refused")
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a machine with no tmux is not a reason to destroy the request")
}

// TestRetireDrainStillDisposesWhileTmuxIsUnusable: the hold is on ACTING, not on
// answering. An expired or undecodable record needs no tmux to discard, and its
// producer is owed the receipt whatever the machine is doing — the same exemption the
// create drain makes.
func TestRetireDrainStillDisposesWhileTmuxIsUnusable(t *testing.T) {
	orig := tmuxAvailable
	tmuxAvailable = func() error { return errors.New("tmux is not on PATH") }
	t.Cleanup(func() { tmuxAvailable = orig })

	h := drainHome(t)
	record := spoolRetire(t, outbox.Retire{
		Title: "old", Path: "/repo/web", Mode: outbox.ModeKill,
		CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})

	h.drainRetireRequests()

	assert.Empty(t, retireRecords(t))
	assert.Contains(t, rejectionFor(t, record), "horizon")
}

// TestRetireDrainAnswersStaleRecordsBehindADispatch: the walk used to break out of the
// loop the moment its one teardown was spent, so every refusable record after that
// point went unanswered — and with the drain now holding until the teardown reports,
// "next tick" could be a long way off. Disposals cost no teardown, so they are
// answered whatever the dispatch budget has done.
func TestRetireDrainAnswersStaleRecordsBehindADispatch(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	dispatched := killRecord(t, "fix-auth", "/repo/web")
	stale := spoolRetire(t, outbox.Retire{
		Title: "old", Path: "/repo/web", Mode: outbox.ModeKill,
		CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})

	require.NotNil(t, h.drainRetireRequests())

	assert.Contains(t, rejectionFor(t, stale), "horizon", "a record behind the dispatch is still answered")
	entries := retireRecords(t)
	require.Len(t, entries, 1, "and the dispatched one is still claimed, not answered")
	assert.Equal(t, dispatched, entries[0].Path)
}

// TestRetireDrainBoundsHowManyRecordsOneTickDisposesOf: each disposal is an fsync-ing
// atomic write plus an unlink, on the Bubble Tea update goroutine. The drain is
// suspended for the whole of an attach, so an orchestrator retrying every few seconds
// through a long one leaves hundreds of records to answer on the first tick after
// detach — which is the synchronous freeze createDisposalBudget exists to bound, and
// this had no bound at all.
func TestRetireDrainBoundsHowManyRecordsOneTickDisposesOf(t *testing.T) {
	h := drainHome(t)
	var records []string
	for i := 0; i < retireDisposalBudget+5; i++ {
		records = append(records, killRecord(t, fmt.Sprintf("gone-%02d", i), "/repo/web"))
	}

	h.drainRetireRequests()

	assert.Len(t, retireRecords(t), 5, "the overflow waits for the next tick rather than freezing this one")
	var answered int
	for _, record := range records {
		if _, ok := outbox.Rejection(record); ok {
			answered++
		}
	}
	assert.Equal(t, retireDisposalBudget, answered)
}

// TestRetireDrainRunsOnTheMetadataTick is the wiring guard. Every other test here calls
// drainRetireRequests directly, so all of them would pass with the call missing from
// Update — both verbs would be registered, documented and dead.
func TestRetireDrainRunsOnTheMetadataTick(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	killRecord(t, "fix-auth", "/repo/web")

	// Stats in the result, as a real tick carries: the drain judges what this tick
	// applied, and a result with none leaves the row unmeasured and the record held.
	h.Update(metadataUpdateDoneMsg{results: []instanceMetaResult{{
		instance: inst, state: tmux.PaneIdle, diffStats: measuredClean(),
	}}})

	assert.True(t, h.retiring[inst], "the tick must reach the retire drain")
}

// TestRetireDrainSkipsAStaleAttachTick is where this drain parts company with the other
// two, and the reason is what it judges on. The prompt and create drains sit OUTSIDE the
// attachGen guard on purpose: neither reads a pane observation, so a tick captured before
// an attach is as good as any other, and running anyway is what bounds `atrium new`'s wait
// to one attach.
//
// A retirement is not like that. It re-judges the target's tree and reads that judgement
// off the model, and a stale tick's results are DISCARDED rather than applied — so the
// model still holds whatever preceded the attach. A session clean and idle when an agent
// spooled the kill, dirty by the time somebody detaches an hour later, would clear the
// gate on the hour-old numbers and take the work with it. One skipped tick costs nothing:
// the record is durable and the next tick judges fresh numbers.
func TestRetireDrainSkipsAStaleAttachTick(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")

	h.attachGen = 7
	h.Update(metadataUpdateDoneMsg{attachGen: 3}) // stale: captured before the attach

	assert.False(t, h.retiring[inst], "a stale tick's numbers are not the numbers to judge on")
	require.Len(t, retireRecords(t), 1, "and the record waits for a tick that has fresh ones")
	_, receipted := outbox.Rejection(record)
	assert.False(t, receipted, "waiting is not refusing")
}

// TestRetireDrainJudgesTheNumbersThisTickApplied is the ordering inside the guard, which
// the guard alone does not give. applyMetadataResults is what moves this tick's freshly
// measured stats onto the instance, and the drain reads them off the instance — so a drain
// that ran first would re-gate on the PREVIOUS tick's numbers even on a live tick.
//
// Here the row still carries clean stats and the tick brings dirty ones. Judged after,
// the kill is refused; judged before, it would tear down a dirty tree.
func TestRetireDrainJudgesTheNumbersThisTickApplied(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst) // clean and idle, as of the last tick
	record := killRecord(t, "fix-auth", "/repo/web")

	h.Update(metadataUpdateDoneMsg{results: []instanceMetaResult{{
		instance:  inst,
		state:     tmux.PaneIdle,
		diffStats: &git.DiffStats{Dirty: true, BranchStatsMeasured: true},
	}}})

	assert.False(t, h.retiring[inst], "the tree went dirty in this very tick's results")
	assert.Contains(t, rejectionFor(t, record), "uncommitted")
}

// TestRetireDrainSkipsARecordThatAlreadyCarriesAReceipt is the durable half of "answered".
// outboxPoisoned covers a record whose clearing failed for the rest of THIS run and dies
// with the process, which is not long enough: Reject writes the receipt before the unlink,
// so a refusal whose unlink failed leaves both files on disk for the next TUI to read. By
// then the condition that justified the refusal may have cleared, and re-judging it would
// tear down a session whose producer was told — up to a TTL earlier — that the request was
// refused.
func TestRetireDrainSkipsARecordThatAlreadyCarriesAReceipt(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	// The state a receipt-then-failed-unlink leaves: answered, but still listed.
	require.NoError(t, os.WriteFile(record+".rejected", []byte("it has uncommitted changes"), 0o644))
	require.True(t, outbox.Receipted(record), "precondition: the record carries its answer")

	assert.Nil(t, h.drainRetireRequests(), "an answered record is not judged again")
	assert.False(t, h.retiring[inst], "and emphatically not acted on")
}

// TestDrainedKillRefusalDoesNotRaiseAModal is the surface rule stated where it is
// actually decided. The drain avoids handleError for its own refusals — a modal covers
// the frame to report an operation nobody at the keyboard asked for, and the producer
// already has the reason in a receipt — but the OUTCOME handlers it dispatches into did
// not honour that: applyKillDone routed a refusal through handleError, which for a
// message this long is showInfo, which is a persistent modal.
//
// That was also a wedge for as long as the drain held on any non-default frame: one
// failed background retirement stopped the spool until somebody pressed a key. The frame
// no longer holds it, so this is now about the surface alone — which is reason enough.
func TestDrainedKillRefusalDoesNotRaiseAModal(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NotNil(t, h.drainRetireRequests())
	require.NotNil(t, h.pendingRetirement)

	// What killIOCmd sends when it declines before touching anything: the branch is
	// checked out in the base repo.
	refused := errors.New("cannot delete the branch: it is checked out in the main repository")
	cmd := h.applyKillDone(killDoneMsg{outcome: killOutcome{inst: inst}, refused: refused})

	assert.NotEqual(t, stateInfo, h.state, "a background refusal must not park the app in a modal")
	assert.Equal(t, stateDefault, h.state)
	assert.NotNil(t, cmd, "it still has to surface — as the transient notice the drain uses")
	assert.Contains(t, rejectionFor(t, record), "checked out",
		"and the producer is told through its receipt, which is the surface that asked")
	// The claim is released, so nothing of this retirement holds the spool. actionInFlight
	// is still set here and cleared one message later by asyncActionDoneMsg, which is the
	// ordinary async window rather than anything this refusal left behind.
	assert.Nil(t, h.pendingRetirement)
}

// TestPressedKillRefusalStillRaisesAModal is the other half, and the reason the check is
// on whose retirement it was rather than on the error. A person who pressed `x` and got a
// refusal IS the audience for a persistent modal: nothing else records what happened, and
// they are looking at the screen.
func TestPressedKillRefusalStillRaisesAModal(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	require.Nil(t, h.pendingRetirement, "precondition: no drained retirement in flight")
	// A sized error box, so "does it fit" is a real question — that predicate is what
	// handleError routes on.
	h.errBox.SetSize(80, 1)

	// Long enough not to fit it, which is the routing this test is about.
	refused := errors.New("cannot delete the branch 'zvi/fix-auth': it is checked out in the " +
		"main repository at /repo/web, so deleting it would leave that checkout on a branch " +
		"that no longer exists — switch it away first, then kill the session again")
	h.applyKillDone(killDoneMsg{outcome: killOutcome{inst: inst}, refused: refused})

	assert.Equal(t, stateInfo, h.state, "a refusal somebody asked for is theirs to read and dismiss")
}

// TestDrainedPauseThatParkedIsReportedAsDone is pause()'s own instruction taken
// seriously: its returned error discriminates none of its outcomes, so a caller that
// needs one must MEASURE it. Most of its failing arms reach SetStatus(Paused) — they park
// the session and report what they could not tidy — so reading the error as "still
// running" told `atrium pause --wait` that a session whose agent was already dead had
// been refused, and an orchestrator kept polling a dead pane. applyKillDone reasons the
// same way about an incomplete kill.
func TestDrainedPauseThatParkedIsReportedAsDone(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModePause})
	require.NotNil(t, h.drainRetireRequests())
	require.NotNil(t, h.pendingRetirement)

	h.Update(batchPauseDoneMsg{failures: []pauseFailure{{
		inst: inst, title: inst.Title(), parked: true,
		err: errors.New("parked, but the worktree could not be removed"),
	}}})

	assert.Nil(t, h.pendingRetirement, "the claim is settled either way")
	_, receipted := outbox.Rejection(record)
	assert.False(t, receipted, "the session IS parked, so its producer must not read a refusal")
	assert.Empty(t, retireRecords(t), "and the record goes, which is what --wait reads as done")
	assert.NotEqual(t, stateInfo, h.state, "nor is a modal raised for a background park")
}

// TestDrainedPauseThatNeverParkedIsReportedAsRefused is the polarity guard: pause's two
// guard returns (not started, already paused) touch nothing, so the session really is
// still there and its producer really was refused.
func TestDrainedPauseThatNeverParkedIsReportedAsRefused(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModePause})
	require.NotNil(t, h.drainRetireRequests())

	h.Update(batchPauseDoneMsg{failures: []pauseFailure{{
		inst: inst, title: inst.Title(), parked: false,
		err: errors.New("cannot pause instance that has not been started"),
	}}})

	assert.Contains(t, rejectionFor(t, record), "has not been started",
		"nothing happened, so the producer hears why")
}
