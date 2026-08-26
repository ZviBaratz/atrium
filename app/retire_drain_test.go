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
		{"is still starting up", session.Loading, measuredClean(), "starting up"},
		{"has no computed stats at all", session.Ready, nil, "could not be established"},
		{"was measured but not by anything that succeeded", session.Ready, &git.DiffStats{}, "could not be established"},
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
	h.state = stateConfirm
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
// the worktree it would remove.
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
		{"a pause of a session whose Start is still in flight", outbox.ModePause,
			func(t *testing.T, h *home) *session.Instance {
				inst := addInstance(t, h, "fix-auth", "/repo/web")
				inst.SetStatus(session.Loading)
				return inst
			}, "starting up"},
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
	h.actionInFlight = false
	h.retiring = map[*session.Instance]bool{}

	h.drainRetireRequests()

	assert.Nil(t, h.pendingRetirement, "an unanswerable claim is released, not held forever")
	assert.Contains(t, rejectionFor(t, record), "never reported",
		"and the producer is told rather than left to time out")
}

// TestRetireDrainHoldsWhileAnyOverlayHoldsAnInstance: stateConfirm was not the only
// state that captures one. handlePauseDone ends every successful single-session pause
// in stateRename with that instance in renameTarget, and the queue overlay's
// queueTarget mirrors it — so a teardown dispatched underneath either leaves an
// overlay to act on a session that is already gone. With the deep-rename toggle on
// that is a tmux rename, a `git branch -m` and a worktree move against a deleted
// session.
//
// The rule is the state rather than a list of the dangerous ones, because a list of
// which overlays capture an instance is a list that falls behind.
func TestRetireDrainHoldsWhileAnyOverlayHoldsAnInstance(t *testing.T) {
	for _, st := range []state{stateConfirm, stateRename, stateQueue, statePrompt, stateHelp} {
		t.Run(fmt.Sprintf("state %d", st), func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			retirable(inst)
			h.state = st
			killRecord(t, "fix-auth", "/repo/web")

			assert.Nil(t, h.drainRetireRequests())
			assert.False(t, h.retiring[inst])
			assert.Len(t, retireRecords(t), 1, "the record waits for the overlay to close")
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
