package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/stretchr/testify/require"
)

// seedHookRecord writes a specific hook record to a session's state file and cleans it up
// at test end (the sandbox HOME is shared across a -count=N run, so a leftover would leak).
func seedHookRecord(t *testing.T, s *Session, rec hookRecord) {
	t.Helper()
	path, err := s.HookStateFile()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, writeHookRecordAtomic(path, rec))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
}

// TestApplyHookEvent pins the record transitions each hook event drives, including the
// set semantics that make a SET correct where a ++/-- counter is not: idempotent adds,
// unmatched stops as no-ops, and an empty agent_id skipped rather than stranding a
// phantom member.
func TestApplyHookEvent(t *testing.T) {
	var rec hookRecord
	const t0, t1 int64 = 1_700_000_000, 1_700_000_050

	applyHookEvent(&rec, HookEventWorking, "", "", t0)
	require.Equal(t, hookStateWorking, rec.State)
	require.Equal(t, t0, rec.LastHeartbeat, "a working edge bumps the heartbeat (#311)")

	applyHookEvent(&rec, HookEventSubagentStart, "aa", "", t1)
	applyHookEvent(&rec, HookEventSubagentStart, "aa", "", t1) // duplicate start is idempotent
	applyHookEvent(&rec, HookEventSubagentStart, "bb", "", t1)
	require.ElementsMatch(t, []string{"aa", "bb"}, rec.Inflight)
	require.Equal(t, t0, rec.LastHeartbeat, "sub-agent edges do not bump the main heartbeat")

	applyHookEvent(&rec, HookEventReady, "", "", t1)
	require.Equal(t, hookStateReady, rec.State)
	require.Len(t, rec.Inflight, 2, "the ready latch never touches the in-flight set")
	require.Equal(t, t0, rec.LastHeartbeat, "ready does not bump the heartbeat (else it would mask a clean turn-end)")

	applyHookEvent(&rec, HookEventSubagentStop, "zz", "", t1) // unmatched stop → no-op
	require.ElementsMatch(t, []string{"aa", "bb"}, rec.Inflight)

	applyHookEvent(&rec, HookEventSubagentStart, "", "", t1) // empty id can't be tracked → skipped
	require.ElementsMatch(t, []string{"aa", "bb"}, rec.Inflight)

	applyHookEvent(&rec, HookEventSubagentStop, "aa", "", t1)
	require.ElementsMatch(t, []string{"bb"}, rec.Inflight)

	applyHookEvent(&rec, HookEventResetInflight, "", "", t1)
	require.Empty(t, rec.Inflight)
	require.Equal(t, hookStateReady, rec.State, "reset clears only the set, not the latch")

	// A later working edge advances the heartbeat.
	applyHookEvent(&rec, HookEventWorking, "", "", t1)
	require.Equal(t, t1, rec.LastHeartbeat, "the newest working edge wins")
}

// TestApplyHookEventEffort pins the effort write rule. Each clause guards a distinct real
// bug, so each gets its own case.
func TestApplyHookEventEffort(t *testing.T) {
	const now int64 = 1_700_000_000

	t.Run("recorded on a working edge with an empty set", func(t *testing.T) {
		var rec hookRecord
		applyHookEvent(&rec, HookEventWorking, "", "max", now)
		require.Equal(t, "max", rec.Effort)
	})

	t.Run("recorded on a ready edge with an empty set", func(t *testing.T) {
		var rec hookRecord
		applyHookEvent(&rec, HookEventReady, "", "xhigh", now)
		require.Equal(t, "xhigh", rec.Effort)
	})

	// The spike's headline finding: UserPromptSubmit's $CLAUDE_EFFORT is a STALE
	// pre-resolution value (a --effort low session reports the model default "high"
	// there). It is present-and-wrong, so an `effort != ""` guard does not catch it —
	// only routing it to its own event does. This is the clobber regression guard.
	t.Run("prompt-submit latches working and bumps the heartbeat but never records effort", func(t *testing.T) {
		rec := hookRecord{Effort: "low"}
		applyHookEvent(&rec, HookEventPromptSubmit, "", "high", now)
		require.Equal(t, "low", rec.Effort, "the stale prompt-submit value must not clobber the resolved truth")
		require.Equal(t, hookStateWorking, rec.State, "prompt-submit still latches working")
		require.Equal(t, now, rec.LastHeartbeat, "prompt-submit still bumps the heartbeat (#311)")
	})

	// A model without effort support reports nothing; an empty read must not clear the
	// last known truth (the same reasoning as SetModelMeta). A backstop, not the
	// UserPromptSubmit defense.
	t.Run("empty effort does not clear a known one", func(t *testing.T) {
		rec := hookRecord{Effort: "max"}
		applyHookEvent(&rec, HookEventWorking, "", "", now)
		require.Equal(t, "max", rec.Effort)
	})

	// Per #290 a background sub-agent's PreToolUse fires in the MAIN session, and skill
	// frontmatter can set its own effort. While the set is non-empty those events are
	// gated out, so the chip can't flicker to the sub-agent's level.
	t.Run("not recorded while a sub-agent is in flight", func(t *testing.T) {
		rec := hookRecord{Effort: "max", Inflight: []string{"aa"}}
		applyHookEvent(&rec, HookEventWorking, "", "low", now)
		require.Equal(t, "max", rec.Effort, "a sub-agent's effort must not contaminate the main session's")
		require.Equal(t, hookStateWorking, rec.State, "the latch still applies — only the effort write is gated")
	})

	// subagent-stop's effort IS the sub-agent's, and it removes the last id from the set,
	// leaving it empty — which would sneak past the in-flight gate if it recorded.
	t.Run("not recorded on sub-agent lifecycle edges", func(t *testing.T) {
		rec := hookRecord{Effort: "max", Inflight: []string{"aa"}}
		applyHookEvent(&rec, HookEventSubagentStop, "aa", "low", now)
		require.Empty(t, rec.Inflight, "the stop still drains the set")
		require.Equal(t, "max", rec.Effort, "subagent-stop empties the set but must never record its own effort")

		applyHookEvent(&rec, HookEventSubagentStart, "bb", "low", now)
		require.Equal(t, "max", rec.Effort, "subagent-start must never record either")
	})

	t.Run("a later working edge advances a known effort", func(t *testing.T) {
		rec := hookRecord{Effort: "max"}
		applyHookEvent(&rec, HookEventWorking, "", "low", now)
		require.Equal(t, "low", rec.Effort, "an in-session /effort switch is the whole point of the feature")
	})

	t.Run("unknown event is still a no-op", func(t *testing.T) {
		rec := hookRecord{Effort: "max", State: hookStateReady}
		applyHookEvent(&rec, "some-future-event", "", "low", now)
		require.Equal(t, hookRecord{Effort: "max", State: hookStateReady}, rec)
	})
}

// TestRuntimeEffort covers the poll-side half of the carrier: Poll lifts the record's
// effort onto the monitor, where the Instance reads it on the metadata tick. Sticky like
// monitor.mode — a record with no effort (a pre-first-turn session, a model without effort
// support) must leave the last known level alone rather than blank the chip.
func TestRuntimeEffort(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	require.Empty(t, s.RuntimeEffort(), "nothing detected before the first poll")

	seedHookRecord(t, s, hookRecord{State: hookStateReady, Effort: "max"})
	s.Poll()
	require.Equal(t, "max", s.RuntimeEffort())

	// An in-session /effort switch: the next turn's hooks rewrite the record.
	seedHookRecord(t, s, hookRecord{State: hookStateReady, Effort: "low"})
	s.Poll()
	require.Equal(t, "low", s.RuntimeEffort(), "the newest reported level wins")

	seedHookRecord(t, s, hookRecord{State: hookStateReady})
	s.Poll()
	require.Equal(t, "low", s.RuntimeEffort(), "an effort-less record must not blank the chip")
}

// PollNow (the detach/switch face-value refresh) stashes effort too — that is the tick
// right after a user detaches from an in-session /effort switch, so it is exactly when a
// changed level most needs to land.
func TestRuntimeEffortPollNow(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	seedHookRecord(t, s, hookRecord{State: hookStateReady, Effort: "xhigh"})
	s.PollNow()
	require.Equal(t, "xhigh", s.RuntimeEffort())
}

// TestParseHookRecord covers the JSON record, the Phase 1 bare-word compat fallback (a
// session running across an atrium upgrade still writes bare words until relaunched), and
// the no-signal cases that must fall back to the scrape classifier.
func TestParseHookRecord(t *testing.T) {
	rec, ok := parseHookRecord([]byte(`{"state":"ready","inflight":["aa","bb"]}`))
	require.True(t, ok)
	require.Equal(t, hookStateReady, rec.State)
	require.ElementsMatch(t, []string{"aa", "bb"}, rec.Inflight)
	require.Empty(t, rec.Effort, "a record predating the effort field decodes with an unknown effort")

	rec, ok = parseHookRecord([]byte(`{"state":"working","effort":"xhigh"}`))
	require.True(t, ok)
	require.Equal(t, "xhigh", rec.Effort)

	rec, ok = parseHookRecord([]byte("  working\n")) // bare word, whitespace tolerated
	require.True(t, ok)
	require.Equal(t, hookStateWorking, rec.State)
	require.Empty(t, rec.Inflight)
	require.Empty(t, rec.Effort, "a Phase-1 bare-word file carries no effort")

	for _, bad := range []string{"garbage", "   ", "", "{bad json"} {
		_, ok := parseHookRecord([]byte(bad))
		require.False(t, ok, "%q is no usable signal", bad)
	}
}

// TestUpdateHookStateRoundTrip drives the events through the real locked update path and
// reads the record back, confirming the on-disk format matches what Poll consumes.
func TestUpdateHookStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	require.NoError(t, UpdateHookState(path, HookEventWorking, "", ""))
	require.NoError(t, UpdateHookState(path, HookEventSubagentStart, "aa", ""))
	require.NoError(t, UpdateHookState(path, HookEventReady, "", ""))

	rec, ok := readHookRecordFile(path)
	require.True(t, ok)
	require.Equal(t, hookStateReady, rec.State)
	require.ElementsMatch(t, []string{"aa"}, rec.Inflight)

	require.NoError(t, UpdateHookState(path, HookEventSubagentStop, "aa", ""))
	rec, ok = readHookRecordFile(path)
	require.True(t, ok)
	require.Equal(t, hookStateReady, rec.State)
	require.Empty(t, rec.Inflight, "the matched stop drains the set")
}

// TestUpdateHookStateConcurrentAdds is the lost-update guard: N goroutines each add a
// distinct agent_id through the locked read-modify-write. Without the cross-process flock,
// concurrent RMWs would clobber each other (last-writer-wins) and the final set would be
// short; the lock guarantees every add survives. Run under -race, it also proves the path
// has no data races.
func TestUpdateHookStateConcurrentAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			require.NoError(t, UpdateHookState(path, HookEventSubagentStart, id, ""))
		}("agent-" + itoa(i))
	}
	wg.Wait()

	rec, ok := readHookRecordFile(path)
	require.True(t, ok)
	require.Len(t, rec.Inflight, n, "no lost updates: every concurrent add is present")
}

// TestUpdateHookStateConcurrentRemoves mirrors the above for removes: seed N ids, then
// discard them all concurrently. Each remove must apply under the lock, leaving the set
// empty — a lost remove would strand a stale id and (in production) a false-pending row.
func TestUpdateHookStateConcurrentRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	const n = 40

	ids := make([]string, n)
	for i := range ids {
		ids[i] = "agent-" + itoa(i)
		require.NoError(t, UpdateHookState(path, HookEventSubagentStart, ids[i], ""))
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			require.NoError(t, UpdateHookState(path, HookEventSubagentStop, id, ""))
		}(id)
	}
	wg.Wait()

	rec, ok := readHookRecordFile(path)
	require.True(t, ok)
	require.Empty(t, rec.Inflight, "all concurrent removes applied → empty set")
}

// TestClearInflight confirms the watchdog's deterministic latch-clear: it empties the set
// while preserving the ready latch, so the next poll reads ready+empty → idle (not
// ready+non-empty → pending), which is what stops the #46 oscillation.
func TestClearInflight(t *testing.T) {
	c := "x"
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa", "bb"}})

	require.NoError(t, s.ClearInflight())

	rec, ok := s.readHookRecord()
	require.True(t, ok)
	require.Empty(t, rec.Inflight, "set cleared")
	require.Equal(t, hookStateReady, rec.State, "state latch preserved")
}

// TestPollPendingWhenSubagentInFlight is the core #290 classification: with the marker
// absent and the hook latched ready, a non-empty in-flight set reads as PanePending (not
// PaneIdle), so the row isn't mislabeled done while a background sub-agent finishes. Once
// the set drains, it commits idle.
func TestPollPendingWhenSubagentInFlight(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})
	require.Equal(t, PanePending, s.Poll(), "hook ready + a sub-agent in flight → pending")

	seedHookRecord(t, s, hookRecord{State: hookStateReady})
	require.Equal(t, PaneIdle, s.Poll(), "ready + empty set → idle (done)")
}

// TestPollPendingWorkingLatchWithInflight is the #290 follow-up regression: a background
// sub-agent's OWN PreToolUse re-latches "working" on the parent's state file while it runs,
// so the record reads {working, inflight>0}, not {ready, inflight>0}. The session is still
// busy — the SET, not the latch, decides — so it must read PanePending rather than fall
// through to the marker-absent grace and commit a false idle (the observed regression: a
// session showing "Waiting for N background agents to finish" rendered as done).
func TestPollPendingWorkingLatchWithInflight(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	seedHookRecord(t, s, hookRecord{State: hookStateWorking, Inflight: []string{"aa", "bb"}})
	require.Equal(t, PanePending, s.Poll(), "working latch + sub-agents in flight → pending, not idle")
	require.Equal(t, PanePending, s.PollNow(), "PollNow agrees: the set outranks the latch")

	// Once the set drains, a bare "working" latch is NOT trusted to hold working — it falls to
	// the bounded marker-absent grace (unchanged #46 behavior), not a fresh pending.
	seedHookRecord(t, s, hookRecord{State: hookStateWorking})
	require.Equal(t, PaneWorking, s.Poll(), "empty set + working latch → the bounded grace, not pending")
}

// TestPollPendingResumeHoldsWorking guards the sub-agent resume boundary: when a pending
// session resumes, a working hook latches before the busy marker repaints. The marker-absent
// grace must hold working across that gap (rather than dropping to idle → a false "done"), so
// the row goes pending → working, never pending → a spurious ready blip.
func TestPollPendingResumeHoldsWorking(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})
	require.Equal(t, PanePending, s.Poll(), "waiting on a background sub-agent")

	// The agent resumes: a working hook fires (state=working, set drains) but the marker
	// hasn't repainted yet. The grace holds working instead of committing a false idle.
	seedHookRecord(t, s, hookRecord{State: hookStateWorking})
	require.Equal(t, PaneWorking, s.Poll(), "marker-absent grace holds working across resume")

	// The marker repaints → unambiguously working.
	c = "✻ Cogitating… (1s · esc to interrupt)"
	require.Equal(t, PaneWorking, s.Poll())
}

// A live busy marker positively proves foreground work, so it outranks a pending record:
// the agent is actively working even if a sub-agent is also in flight.
func TestPollMarkerOverridesPending(t *testing.T) {
	c := "✻ Cogitating… (5s · esc to interrupt)"
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})
	require.Equal(t, PaneWorking, s.Poll(), "a live marker outranks a pending record")
}

// PollNow (the detach/switch face-value refresh) classifies pending too.
func TestPollNowPending(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)

	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})
	require.Equal(t, PanePending, s.PollNow(), "ready + in-flight → pending at face value")

	seedHookRecord(t, s, hookRecord{State: hookStateReady})
	require.Equal(t, PaneIdle, s.PollNow(), "ready + empty set → idle")
}

// TestPollDeadSessionOutranksPending is the #290 dead-pane-mid-sub-agent case: a session
// that dies while a sub-agent is still recorded in flight (ready + non-empty set) must
// still report PaneDead, because liveness is probed BEFORE the hook record is read. So the
// stuck set can never mask the death — the metadata loop flags it lost and recovers it to
// Paused, rather than stranding a permanently-pending row. Reconciliation here is liveness,
// not the watchdog.
func TestPollDeadSessionOutranksPending(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	deadExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("can't find session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(idle), nil },
	}
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), deadExec)
	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})

	require.Equal(t, PaneDead, s.Poll(), "a dead pane mid-sub-agent is dead, not pending")
	require.Equal(t, PaneDead, s.PollNow(), "PollNow agrees: liveness outranks the in-flight set")
}
