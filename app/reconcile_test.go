package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingStore is a config.InstanceStorage that counts SaveInstances calls and
// keeps the last payload, so a reconcile test can assert whether (and what) a
// session was persisted.
type capturingStore struct {
	saves int
	last  json.RawMessage
	// saveErr, when set, makes every SaveInstances fail — the "session is live but
	// unrecorded" case an `atrium new --wait` must be told about rather than left to
	// read the request's disappearance as success.
	saveErr error
}

func (c *capturingStore) SaveInstances(d json.RawMessage) error {
	c.saves++
	if c.saveErr != nil {
		return c.saveErr
	}
	c.last = d
	return nil
}
func (c *capturingStore) GetInstances() json.RawMessage { return c.last }
func (c *capturingStore) DeleteAllInstances() error     { c.last = nil; return nil }

// withCapturingStore swaps h's storage for a capturingStore and returns it.
func withCapturingStore(t *testing.T, h *home) *capturingStore {
	t.Helper()
	cs := &capturingStore{}
	st, err := session.NewStorage(cs)
	require.NoError(t, err)
	h.storage = st
	return cs
}

// #282: with nothing Loading, reconcile is a no-op — it must not persist or tear
// anything down. This guards the graceful #268/#281 quit path, which exits with
// nothing Loading and must remain untouched.
func TestReconcileInFlightStarts_NoLoadingIsNoop(t *testing.T) {
	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even a signal shutdown must no-op when nothing is Loading
	h.reconcileInFlightStarts(ctx)

	assert.Equal(t, 0, cs.saves, "no Loading session -> nothing to persist")
}

// #282 force-quit abandon: a never-completed Loading session torn down on a live
// context (the user chose to abandon it) must be cleaned up (Kill is a safe no-op
// on an unstarted instance) and never persisted — no orphan, no state entry.
func TestReconcileInFlightStarts_ForceQuitTearsDownUnstarted(t *testing.T) {
	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)
	inst := addLoadingInstance(t, h, "abandon-me")
	require.False(t, inst.Started())

	// Force-quit exits via a normal QuitMsg, so the context is still live.
	h.reconcileInFlightStarts(context.Background())

	assert.Equal(t, 0, cs.saves, "an abandoned unstarted session must not be persisted")
}

// #282 signal shutdown, partial start: a Loading session that never finished must
// be torn down under a rebound (WithoutCancel) context. On an unstarted instance
// the rebind + Kill is a safe no-op; the point is it must not panic and must not
// persist a half-created session.
func TestReconcileInFlightStarts_SignalTearsDownUnstartedPartial(t *testing.T) {
	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)
	inst := addLoadingInstance(t, h, "partial-one")
	require.False(t, inst.Started())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal shutdown
	h.reconcileInFlightStarts(ctx)

	assert.Equal(t, 0, cs.saves, "a partial/failed start must not be persisted")
}

// #282: the drain must be bounded. If a Start goroutine never signals Done (a
// wedged start, or a queued command Bubble Tea dropped on ctx-cancel), reconcile
// must return within the timeout instead of hanging on the WaitGroup — and leave
// the still-Loading session untouched.
func TestReconcileInFlightStarts_BoundedDrainDoesNotHang(t *testing.T) {
	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)
	inst := addLoadingInstance(t, h, "wedged-one")

	// Simulate an in-flight Start that never completes.
	h.startWG.Add(1)
	defer h.startWG.Done() // release the drain goroutine after the test

	orig := drainTimeout
	drainTimeout = 50 * time.Millisecond
	defer func() { drainTimeout = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		h.reconcileInFlightStarts(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcileInFlightStarts hung on an unsettled Start")
	}

	assert.Equal(t, session.Loading, inst.GetStatus(), "a wedged start is left as-is")
	assert.Equal(t, 0, cs.saves, "a wedged start is not persisted")
}

// #282 signal shutdown, completed start: when Start actually finished but its
// completion message was dropped (event loop bypassed Update), the session must be
// adopted — flipped to Running and persisted — so the daemon handoff / next launch
// keeps it rather than orphaning it. Needs a real tmux session to reach
// Started()==true.
func TestReconcileInFlightStarts_SignalAdoptsCompletedStart(t *testing.T) {
	testutil.RequireTmux(t)

	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)

	// A direct session skips git worktree setup; a real Start still creates a live
	// tmux session and flips Started() true. "sleep 300" keeps the pane alive.
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "adopt-me", Path: t.TempDir(), Program: "sleep 300", Direct: true,
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	require.True(t, inst.Started())
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})

	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading) // simulate the dropped instanceStartedMsg

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal shutdown
	h.reconcileInFlightStarts(ctx)

	assert.Equal(t, session.Running, inst.GetStatus(), "a completed start is adopted as Running")
	require.Equal(t, 1, cs.saves, "the adopted session is persisted exactly once")
	assert.Contains(t, string(cs.last), "adopt-me", "the persisted payload includes the adopted session")
}

// #282 end-to-end teardown: a real session (git worktree + branch + tmux) that is
// force-quit while Loading must be fully torn down — no orphaned branch (the
// "branch exists on title reuse" symptom) and no worktree directory left behind.
// This is the completed-start + force-quit case (Start finished but its message
// wasn't processed before the abandon), which routes through Kill on a live ctx.
func TestReconcileInFlightStarts_ForceQuitTearsDownRealSession(t *testing.T) {
	testutil.RequireTmux(t)

	h := newQuitTestHome(t)
	cs := withCapturingStore(t, h)

	repo := gitInitRepo(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "abandon-real", Path: repo, Program: "sleep 300",
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	require.True(t, inst.Started())
	t.Cleanup(func() { inst.RebindBaseContext(context.Background()); _ = inst.Kill() })

	branch, wtPath := inst.Branch, inst.WorkingDir()
	require.True(t, git.LocalBranchExists(context.Background(), repo, branch), "branch exists after Start")

	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading) // start finished, completion message not yet processed

	// Force-quit abandon: the lifecycle context is still live.
	h.reconcileInFlightStarts(context.Background())

	assert.False(t, git.LocalBranchExists(context.Background(), repo, branch), "abandoned session's branch is removed")
	_, statErr := os.Stat(wtPath)
	assert.True(t, os.IsNotExist(statErr), "abandoned session's worktree directory is removed")
	assert.Equal(t, 0, cs.saves, "an abandoned session is not persisted")
}
