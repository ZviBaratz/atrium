package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// claudePendingInstance builds a started claude Instance whose tmux session is alive and
// captures *content, so a test can drive the pending/watchdog flow end to end (Poll →
// ApplyPaneState → SetStatus, plus the real ClearInflight file write). HOME is a temp dir
// so the hook state file lands under the sandbox, never the real data dir.
func claudePendingInstance(t *testing.T, content *string) *Instance {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil }, // has-session succeeds → alive
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(*content), nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", tmux.MakePtyFactory(), aliveExec)
	return &Instance{ident: identity{title: "sess"}, status: Running, started: true, tmuxSession: ts}
}

// seedInflight writes a hook record for inst's session: the working/ready latch (stateEvent
// is tmux.HookEventWorking / HookEventReady) plus one SubagentStart per id, through the same
// locked update path the real hooks use.
func seedInflight(t *testing.T, inst *Instance, stateEvent string, ids ...string) {
	t.Helper()
	path, err := inst.tmux().HookStateFile()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, tmux.UpdateHookState(path, stateEvent, tmux.HookPayload{}, ""))
	for _, id := range ids {
		require.NoError(t, tmux.UpdateHookState(path, tmux.HookEventSubagentStart, tmux.HookPayload{AgentID: id}, ""))
	}
}

// TestPendingWatchdogCap walks the whole three-rung ladder pendingWatchdogCap resolves
// (#799): the user's configured cap outranks an agent adapter's override, which outranks
// DefaultPendingWatchdog. All three rungs are exercised against the SAME instance, so what
// the assertions distinguish is precedence and not two unrelated defaults.
//
// The adapter rung has to be installed by the test: the Adapter field exists but no entry
// in the registry declares one today, so a fixture that waited for a real override would
// assert nothing at all. Resolve returns the shared adapter, so the write is undone in a
// cleanup.
func TestPendingWatchdogCap(t *testing.T) {
	t.Cleanup(func() { SetPendingWatchdog(0) })
	inst := &Instance{Program: "claude"}

	const configured = 1234 * time.Millisecond
	const adapterCap = 7 * time.Minute
	require.NotEqual(t, configured, adapterCap, "the two overrides must be distinguishable")
	require.NotEqual(t, DefaultPendingWatchdog, adapterCap, "as must the adapter's and the default")

	t.Run("default", func(t *testing.T) {
		require.Zero(t, agent.Resolve(inst.Program).PendingWatchdog,
			"the default rung needs an agent carrying no override")
		require.Equal(t, DefaultPendingWatchdog, inst.pendingWatchdogCap())
	})

	t.Run("configured beats the default", func(t *testing.T) {
		SetPendingWatchdog(configured)
		require.Equal(t, configured, inst.pendingWatchdogCap())

		SetPendingWatchdog(0)
		require.Equal(t, DefaultPendingWatchdog, inst.pendingWatchdogCap(),
			"clearing falls back, so the setter is not one-way")
	})

	t.Run("adapter beats the default", func(t *testing.T) {
		a := agent.Resolve(inst.Program)
		t.Cleanup(func() { a.PendingWatchdog = 0 })
		a.PendingWatchdog = adapterCap
		require.Equal(t, adapterCap, inst.pendingWatchdogCap())
	})

	t.Run("configured beats the adapter", func(t *testing.T) {
		a := agent.Resolve(inst.Program)
		t.Cleanup(func() { a.PendingWatchdog = 0 })
		a.PendingWatchdog = adapterCap
		SetPendingWatchdog(configured)
		require.Equal(t, configured, inst.pendingWatchdogCap(),
			"the user's cap is the top rung, so a per-agent default can never make it inert")
	})
}

// TestPending_UnreadSemantics is the #289 freebie: routing a Stop-with-sub-agent to
// Pending (not Ready) means the finished-turn unread edge — which drives the "dinged done
// while still working" notification — does NOT fire on entry to Pending, only on the real
// Pending→Ready once the sub-agent completes.
func TestPending_UnreadSemantics(t *testing.T) {
	inst := &Instance{ident: identity{title: "s"}, status: Running}

	inst.SetStatus(Pending) // Running → Pending: the false end-of-turn
	require.False(t, inst.Unread(), "entering Pending must not flag unread (no false 'finished')")

	inst.SetStatus(Ready) // Pending → Ready: the genuine completion
	require.True(t, inst.Unread(), "the real completion flags unread as usual")
}

// TestApplyPending_EntersPending: the #290 classification, end to end. A hook latched
// "ready" with a sub-agent still in flight enters Pending, NOT Ready — so the row is never
// mislabeled done while background work continues.
func TestApplyPending_EntersPending(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady, "aa")

	st := inst.Poll()
	require.Equal(t, tmux.PanePending, st, "ready + in-flight polls as pending")
	inst.ApplyPaneState(st)
	require.Equal(t, Pending, inst.GetStatus(), "enters Pending, not Ready")

	// Before the cap the watchdog must not fire: a subsequent pending poll holds Pending.
	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus(), "held pending within the cap")
}

// TestApplyPending_WatchdogReconciles is the alive-but-stuck acceptance case: a session
// whose SubagentStop never fired (the set never drains) sits Pending. Past the wall-clock
// cap the watchdog force-reconciles it to done EVEN THOUGH the pane is alive, and clears
// the stuck set deterministically so it commits to idle and does not oscillate (#46).
func TestApplyPending_WatchdogReconciles(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady, "aa")

	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus())

	// Pretend we entered Pending longer ago than the cap (SubagentStop never fired). The
	// watchdog measures pendingSince under the pendingInflight producer, not
	// statusChangedAt: Pending has two producers now and only this one is capped (see the
	// field's doc).
	inst.mu.Lock()
	require.Equal(t, pendingInflight, inst.pendingSource, "the set is what is holding this row")
	inst.pendingSince = time.Now().Add(-2 * DefaultPendingWatchdog)
	inst.mu.Unlock()

	st := inst.Poll()
	require.Equal(t, tmux.PanePending, st, "still polling pending — the set is stuck non-empty")
	inst.ApplyPaneState(st)
	require.Equal(t, Ready, inst.GetStatus(), "held past the cap → watchdog reconciles to done")

	// Deterministic latch-clear: the set is now empty, so the next poll is plain idle, not
	// pending again — no pending/ready flapping.
	require.Equal(t, tmux.PaneIdle, inst.Poll(), "the stuck in-flight set was cleared")
	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Ready, inst.GetStatus(), "stays done — no oscillation")
}

// backgroundFooter is a live claude idle pane whose footer still chips a background shell:
// the turn has ended (hook ready, empty set) but the work it started is running.
const backgroundFooter = "───────────────\n❯ \n───────────────\n  ⏵⏵ auto mode on · 2 shells · ← for agents"

// A pane held Pending by the FOOTER CHIP is exempt from the watchdog. The cap backstops a
// SubagentStop that never fired — a latch that can leak — and a chip cannot leak: it is
// re-scraped every poll and gone when the work exits. Expiring it would re-commit the exact
// "done while still working" bug once the cap elapsed, and a persistent Monitor legitimately
// outlives any cap.
func TestApplyBackground_IsNeverReconciledByTheWatchdog(t *testing.T) {
	c := backgroundFooter
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady) // ready latch, EMPTY set

	st := inst.Poll()
	require.Equal(t, tmux.PaneBackground, st, "ready+empty with a chip is background work, not idle")
	inst.ApplyPaneState(st)
	require.Equal(t, Pending, inst.GetStatus(), "background work reads as Pending")

	// Age it far past the cap. A set-driven Pending would reconcile here; this must not.
	inst.mu.Lock()
	inst.statusChangedAt = time.Now().Add(-4 * DefaultPendingWatchdog)
	inst.mu.Unlock()

	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus(), "a visible chip is live proof — it never times out")

	// And it still clears the moment the work does.
	c = "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Ready, inst.GetStatus(), "chip gone → the turn is genuinely done")
}

// The cross-talk regression. Both producers write Pending, and recordStatusChange does not
// re-stamp statusChangedAt when from == to — so a watchdog reading that shared stamp would
// see a long background hold as a long IN-FLIGHT hold and fire on the first tick of a
// legitimate sub-agent run, clearing a live set and force-committing a false Ready.
func TestApplyPending_LongBackgroundHoldDoesNotExpireTheNextSubagentRun(t *testing.T) {
	c := backgroundFooter
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady)

	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus())

	// Held by the chip for far longer than the watchdog cap. Age BOTH stamps: the shared one
	// the watchdog used to read, and the producer clock — a hold this long is exactly what
	// the handover below has to reset, and leaving pendingSince fresh here would let the
	// test pass on a shared clock too.
	inst.mu.Lock()
	aged := time.Now().Add(-4 * DefaultPendingWatchdog)
	inst.statusChangedAt, inst.pendingSince = aged, aged
	inst.mu.Unlock()
	inst.ApplyPaneState(inst.Poll())

	// Now a REAL sub-agent starts, on a pane with no chip left.
	c = "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	seedInflight(t, inst, tmux.HookEventReady, "aa")

	st := inst.Poll()
	require.Equal(t, tmux.PanePending, st, "a non-empty in-flight set is set-driven pending")
	inst.ApplyPaneState(st)
	require.Equal(t, Pending, inst.GetStatus(),
		"the handover restamps the producer clock, so a prior background hold cannot expire this run")
	// The two stamps have now demonstrably diverged, which is the whole reason the list's
	// elapsed cue reads PendingSince: rendering StatusChangedAt here would tell the user
	// this sub-agent had been running for four watchdog caps.
	require.WithinDuration(t, time.Now(), inst.PendingSince(), time.Minute,
		"the cue dates the sub-agent run, not the chip hold it replaced")
	require.True(t, inst.StatusChangedAt().Before(time.Now().Add(-DefaultPendingWatchdog)),
		"while the shared stamp still carries the background hold's age")
}

// A row restored from state.json as Pending keeps its elapsed cue: the clock is seeded from
// the persisted statusChangedAt rather than starting blank until the first poll. The
// producer stays unattributed, because state.json does not record which one was holding and
// crediting the set would hand a restored row to a watchdog clock it never earned.
func TestFromInstanceData_SeedsThePendingClock(t *testing.T) {
	held := time.Now().Add(-12 * time.Minute)

	inst := &Instance{}
	inst.pendingSince = pendingSinceOnRestore(Pending, held)
	require.Equal(t, held, inst.PendingSince(), "a restored Pending row keeps its age")
	require.Equal(t, pendingNone, inst.pendingSource, "but not a producer it cannot know")
	require.False(t, inst.pendingExpired(), "and an unattributed clock is not watchdog fuel")

	require.Zero(t, pendingSinceOnRestore(Ready, held), "any other status is simply not held")
}

// Findings 1-3 of the #682 review, which share one root cause: every "the agent produced
// something" consumer keys off the unread bit, and the bit was only ever raised on entry to
// Ready. A chip-held row never reaches Ready, so a turn that ended while a background shell
// ran went silent — no finish/asked ding, no unread glyph, skipped by NextUnread, and #571's
// question hold inert because it RELEASES on Unread(), meaning a queued follow-up could be
// delivered as the answer to a question the user never saw. With a session-length Monitor
// that silence covered every later turn too, not just the one that launched the work.
func TestApplyBackground_TurnEndRingsOnceAndStaysPending(t *testing.T) {
	c := backgroundFooter
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady)
	inst.SetStatus(Running) // a turn is under way
	inst.MarkSeen()

	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus(), "the work is still running, so the row is not done")
	require.True(t, inst.Unread(), "but the turn ENDED — that is a turn-end the user must see")
	first := inst.UnreadAt()

	// Every later poll is the same hold, not a new turn: re-ringing it would ding on every
	// tick for as long as the work runs.
	inst.MarkSeen()
	inst.ApplyPaneState(inst.Poll())
	require.False(t, inst.Unread(), "a continuing hold is not a new turn-end")
	require.Equal(t, first, inst.UnreadAt(), "and does not re-stamp the unread clock")
}

// The handover case the status alone cannot express: the turn ends with sub-agents in
// flight (Pending, correctly silent — the turn is not over), they drain, and only a
// background shell is left. That is the moment the agent became done-and-waiting, and the
// status never changes across it, so an edge keyed on the status would miss it entirely.
func TestApplyBackground_SetHandingOverToChipIsATurnEnd(t *testing.T) {
	c := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	inst := claudePendingInstance(t, &c)
	seedInflight(t, inst, tmux.HookEventReady, "aa")

	inst.ApplyPaneState(inst.Poll())
	require.Equal(t, Pending, inst.GetStatus())
	require.False(t, inst.Unread(), "sub-agents still in flight: the turn is not over yet")

	// The set drains (seedInflight only ever adds, so stop the id explicitly); the footer
	// still chips a background shell.
	c = backgroundFooter
	path, err := inst.tmux().HookStateFile()
	require.NoError(t, err)
	require.NoError(t, tmux.UpdateHookState(path, tmux.HookEventSubagentStop, tmux.HookPayload{AgentID: "aa"}, ""))

	st := inst.Poll()
	require.Equal(t, tmux.PaneBackground, st)
	inst.ApplyPaneState(st)
	require.Equal(t, Pending, inst.GetStatus(), "still not done — the shell is running")
	require.True(t, inst.Unread(), "but the turn is over now, and the status never moved to say so")
}
