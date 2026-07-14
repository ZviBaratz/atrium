package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
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
	return &Instance{Title: "sess", status: Running, started: true, tmuxSession: ts}
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

// TestPendingWatchdogCap: claude has no adapter override, so it uses the package default;
// the default is a tunable var.
func TestPendingWatchdogCap(t *testing.T) {
	inst := &Instance{Program: "claude"}
	require.Equal(t, defaultPendingWatchdog, inst.pendingWatchdogCap(),
		"claude resolves the package default (no adapter override)")

	prev := defaultPendingWatchdog
	defaultPendingWatchdog = 1234 * time.Millisecond
	t.Cleanup(func() { defaultPendingWatchdog = prev })
	require.Equal(t, 1234*time.Millisecond, inst.pendingWatchdogCap())
}

// TestPending_UnreadSemantics is the #289 freebie: routing a Stop-with-sub-agent to
// Pending (not Ready) means the finished-turn unread edge — which drives the "dinged done
// while still working" notification — does NOT fire on entry to Pending, only on the real
// Pending→Ready once the sub-agent completes.
func TestPending_UnreadSemantics(t *testing.T) {
	inst := &Instance{Title: "s", status: Running}

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

	// Pretend we entered Pending longer ago than the cap (SubagentStop never fired).
	inst.mu.Lock()
	inst.statusChangedAt = time.Now().Add(-2 * defaultPendingWatchdog)
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
