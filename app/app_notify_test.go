package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyEventFor(t *testing.T) {
	cases := []struct {
		name           string
		old, current   session.Status
		unreadAdvanced bool
		wantEvent      notify.Event
		wantOK         bool
	}{
		{"genuine finish (unread advanced)", session.Running, session.Ready, true, notify.EventFinished, true},
		{"suppressed finish (unread not advanced)", session.Running, session.Ready, false, 0, false},
		{"into needs-input", session.Running, session.NeedsInput, false, notify.EventNeedsInput, true},
		{"gate into needs-input from loading", session.Loading, session.NeedsInput, false, notify.EventNeedsInput, true},
		{"still needs-input (no edge)", session.NeedsInput, session.NeedsInput, false, 0, false},
		{"finish outranks a coincident needs-input read", session.NeedsInput, session.Ready, true, notify.EventFinished, true},
		{"running with nothing new", session.Ready, session.Running, false, 0, false},
		{"needs-input cleared to ready without unread", session.NeedsInput, session.Ready, false, 0, false},
		// Pending-origin transitions (#290): Pending is the "background sub-agent still
		// in flight" state. A block surfaced from Pending should ring (the user can't
		// auto-continue a blocked pane). A genuine completion from Pending (sub-agent
		// done, unread advanced) should ring. A synthetic Pending hold (no unread change,
		// no NeedsInput) should stay silent.
		{"pending → needs-input rings (sub-agent blocks)", session.Pending, session.NeedsInput, false, notify.EventNeedsInput, true},
		{"pending → genuine finish rings (unread advanced)", session.Pending, session.Ready, true, notify.EventFinished, true},
		{"pending → suppressed finish (unread not advanced)", session.Pending, session.Ready, false, 0, false},
		{"pending still pending (no edge)", session.Pending, session.Pending, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := notifyEventFor(tc.old, tc.current, tc.unreadAdvanced)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantEvent, ev)
			}
		})
	}
}

func TestNotifyStateThrottle(t *testing.T) {
	st := &notifyState{}
	// First of each edge passes; an immediate repeat of the same edge is throttled;
	// the other edge is tracked independently.
	require.False(t, st.throttled(notify.EventFinished), "first finish passes")
	require.True(t, st.throttled(notify.EventFinished), "immediate second finish is throttled")
	require.False(t, st.throttled(notify.EventNeedsInput), "needs-input has its own budget")
	require.True(t, st.throttled(notify.EventNeedsInput), "immediate second needs-input is throttled")

	// After the throttle window elapses, the edge fires again.
	st.lastFinished = time.Now().Add(-2 * notifyThrottle)
	require.False(t, st.throttled(notify.EventFinished), "finish passes again once the window elapsed")
}

// newNotifyHome builds a home with a bell notifier writing to buf, a real list, and an
// empty seen map. Bell mode never touches the executor, so the real one is safe here.
func newNotifyHome(buf *bytes.Buffer) (*home, *ui.List) {
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsBell
	return &home{
		ctx:        context.Background(),
		state:      stateDefault,
		appConfig:  cfg,
		list:       list,
		notifier:   notify.New(buf, cmd.MakeExecutor()),
		notifySeen: make(map[*session.Instance]*notifyState),
	}, list
}

func newNotifyInstance(t *testing.T) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	return inst
}

func TestMaybeNotifyNilNotifierIsNoOp(t *testing.T) {
	h := &home{notifySeen: make(map[*session.Instance]*notifyState)}
	inst := newNotifyInstance(t)
	// No notifier and no list — must not panic or touch anything.
	require.NotPanics(t, func() {
		h.maybeNotify(inst, session.Running, time.Time{}, config.NotificationsBell)
	})
}

func TestMaybeNotifyFirstObservationIsSilent(t *testing.T) {
	var buf bytes.Buffer
	h, _ := newNotifyHome(&buf)
	inst := newNotifyInstance(t)
	inst.SetStatus(session.Ready) // a genuine finish edge is pending...
	// ...but the very first observation of the instance never notifies (startup gate).
	h.maybeNotify(inst, session.Running, time.Time{}, config.NotificationsBell)
	require.Empty(t, buf.String(), "first observation must be silent")
	_, seen := h.notifySeen[inst]
	require.True(t, seen, "the instance is recorded as observed")
}

func TestMaybeNotifyEmitsFinishForSeenNonSelected(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	a := newNotifyInstance(t)
	b := newNotifyInstance(t)
	list.AddInstance(a)()
	list.AddInstance(b)()
	// Work on whichever instance is NOT selected, so selection-suppression doesn't apply.
	sel := list.GetSelectedInstance()
	target := a
	if sel == a {
		target = b
	}
	// First call marks it observed (silent gate).
	h.maybeNotify(target, session.Running, time.Time{}, config.NotificationsBell)
	require.Empty(t, buf.String())
	// A genuine finish: SetStatus(Ready) advances unreadAt past the zero snapshot.
	target.SetStatus(session.Ready)
	h.maybeNotify(target, session.Running, time.Time{}, config.NotificationsBell)
	require.Equal(t, "\a", buf.String(), "a genuine finish on a seen, non-selected session rings once")
}

func TestMaybeNotifySelectedSessionStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	inst := newNotifyInstance(t)
	list.AddInstance(inst)() // the sole instance is the selected one
	require.Same(t, inst, list.GetSelectedInstance())
	// Observe it once (gate), then finish it — still silent because it's selected.
	h.maybeNotify(inst, session.Running, time.Time{}, config.NotificationsBell)
	inst.SetStatus(session.Ready)
	h.maybeNotify(inst, session.Running, time.Time{}, config.NotificationsBell)
	require.Empty(t, buf.String(), "the selected session the user is watching never notifies")
}

// notifyTarget adds two instances and returns the one that is NOT selected, plus the
// list, so tests can drive edges on a genuinely-background session.
func notifyTarget(t *testing.T, list *ui.List) *session.Instance {
	t.Helper()
	a := newNotifyInstance(t)
	b := newNotifyInstance(t)
	list.AddInstance(a)()
	list.AddInstance(b)()
	if list.GetSelectedInstance() == a {
		return b
	}
	return a
}

// TestApplyMetadataResultsEmitsBellOnFinish drives the real production insertion
// point: applyMetadataResults snapshots the status around ApplyPaneState and notifies
// on the resulting edge. It exercises the whole in-process chain (mode gate → snapshot
// → ApplyPaneState/SetStatus → maybeNotify → notifier bell).
func TestApplyMetadataResultsEmitsBellOnFinish(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	target := notifyTarget(t, list)

	// Tick 1: the session is working — first observation, silent (startup gate).
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	require.Empty(t, buf.String())
	require.Equal(t, session.Running, target.GetStatus())

	// Tick 2: the session finishes its turn (PaneIdle → Ready) → the bell rings once.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Equal(t, session.Ready, target.GetStatus())
	require.Equal(t, "\a", buf.String(), "finishing a turn on a background session rings the bell")

	// Tick 3: still idle — no new edge, so no second bell.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Equal(t, "\a", buf.String(), "a steady Ready state does not re-ring")
}

// TestApplyMetadataResultsEmitsBellOnNeedsInput covers the block edge.
func TestApplyMetadataResultsEmitsBellOnNeedsInput(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	target := notifyTarget(t, list)

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	require.Empty(t, buf.String())
	// A manual prompt (never auto-tapped) blocks the session → bell.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PanePromptManual}}, true)
	require.Equal(t, session.NeedsInput, target.GetStatus())
	require.Equal(t, "\a", buf.String(), "blocking on a prompt rings the bell")
}

// TestApplyMetadataResultsSweepDoesNotEmit confirms the post-detach sweep (emit=false)
// applies state but never notifies, so returning to the list replays no burst.
func TestApplyMetadataResultsSweepDoesNotEmit(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	target := notifyTarget(t, list)

	// Seed the instance as observed via a normal emit=true working tick.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	// A finish arriving through the detach sweep stays silent, but is still applied.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, false)
	require.Empty(t, buf.String(), "the post-detach sweep never notifies")
	require.Equal(t, session.Ready, target.GetStatus(), "but the sweep still applies the state")
}

// TestApplyMetadataResultsOffIsSilent confirms the default (off) never emits, even on
// a real finish edge.
func TestApplyMetadataResultsOffIsSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	h.appConfig.Notifications = config.NotificationsOff
	target := notifyTarget(t, list)

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Empty(t, buf.String(), "notifications off emits nothing")
}

// TestApplyMetadataResultsFinishSuppressedWithQueuedPrompt covers the queued-follow-up
// case: a background session that finishes a turn while a prompt is queued is about to be
// auto-continued by deliverReadyPrompts, so the finish must not ring. Once the queue
// drains, the next finishing turn does ring.
func TestApplyMetadataResultsFinishSuppressedWithQueuedPrompt(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	target := notifyTarget(t, list)
	target.QueuePrompt("next step")

	// Observe it working (gate), then finish the turn: normally a bell, but a queued
	// prompt is about to be delivered, so it stays silent.
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Equal(t, session.Ready, target.GetStatus())
	require.Empty(t, buf.String(), "a finish with a queued follow-up must not ring")

	// Drain the queue and finish again: now genuinely idle-awaiting-the-user, so it rings.
	target.ClearPrompt("next step")
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Equal(t, "\a", buf.String(), "once the queue drains, the finishing turn rings")
}

// TestApplyMetadataResultsBlockNotSuppressedWithQueuedPrompt confirms the queued-prompt
// exemption is finish-only: a blocked pane can't consume its queue, so a session that
// blocks on a prompt still rings even with a follow-up queued.
func TestApplyMetadataResultsBlockNotSuppressedWithQueuedPrompt(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(&buf)
	target := notifyTarget(t, list)
	target.QueuePrompt("next step")

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PanePromptManual}}, true)
	require.Equal(t, session.NeedsInput, target.GetStatus())
	require.Equal(t, "\a", buf.String(), "a blocked session rings even with a queued prompt")
}

// TestForgetInstanceDropsBookkeeping confirms a killed session's per-instance maps are
// pruned, so its *session.Instance is not pinned in memory for the process lifetime.
func TestForgetInstanceDropsBookkeeping(t *testing.T) {
	inst := newNotifyInstance(t)
	h := &home{
		notifySeen:  map[*session.Instance]*notifyState{inst: {}},
		lostStrikes: map[*session.Instance]int{inst: 2},
	}
	h.forgetInstance(inst)
	_, seen := h.notifySeen[inst]
	require.False(t, seen, "notifySeen entry is dropped so the killed instance can be GC'd")
	_, striking := h.lostStrikes[inst]
	require.False(t, striking, "lostStrikes entry is dropped too")
}
