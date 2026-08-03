package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyEventFor(t *testing.T) {
	cases := []struct {
		name           string
		old, current   session.Status
		unreadAdvanced bool
		asked          bool
		wantEvent      notify.Event
		wantOK         bool
	}{
		{"genuine finish (unread advanced)", session.Running, session.Ready, true, false, notify.EventFinished, true},
		{"suppressed finish (unread not advanced)", session.Running, session.Ready, false, false, 0, false},
		{"into needs-input", session.Running, session.NeedsInput, false, false, notify.EventNeedsInput, true},
		{"gate into needs-input from loading", session.Loading, session.NeedsInput, false, false, notify.EventNeedsInput, true},
		{"still needs-input (no edge)", session.NeedsInput, session.NeedsInput, false, false, 0, false},
		{"finish outranks a coincident needs-input read", session.NeedsInput, session.Ready, true, false, notify.EventFinished, true},
		{"running with nothing new", session.Ready, session.Running, false, false, 0, false},
		{"needs-input cleared to ready without unread", session.NeedsInput, session.Ready, false, false, 0, false},
		// Pending-origin transitions (#290): Pending is the "background sub-agent still
		// in flight" state. A block surfaced from Pending should ring (the user can't
		// auto-continue a blocked pane). A genuine completion from Pending (sub-agent
		// done, unread advanced) should ring. A synthetic Pending hold (no unread change,
		// no NeedsInput) should stay silent.
		{"pending → needs-input rings (sub-agent blocks)", session.Pending, session.NeedsInput, false, false, notify.EventNeedsInput, true},
		{"pending → genuine finish rings (unread advanced)", session.Pending, session.Ready, true, false, notify.EventFinished, true},
		{"pending → suppressed finish (unread not advanced)", session.Pending, session.Ready, false, false, 0, false},
		{"pending still pending (no edge)", session.Pending, session.Pending, false, false, 0, false},
		// #571: a question rides the SAME finish edge — the pane cannot tell one from a
		// plain finish — so asked only renames the event, and everything the finish edge
		// earns (unread stamp, synthetic-transition suppression) is inherited.
		{"a question renames the finish edge", session.Running, session.Ready, true, true, notify.EventAsked, true},
		{"asked without the finish edge is not an event", session.Running, session.Ready, false, true, 0, false},
		{"asked never converts a block edge", session.Running, session.NeedsInput, false, true, notify.EventNeedsInput, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := notifyEventFor(tc.old, tc.current, tc.unreadAdvanced, tc.asked)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantEvent, ev)
			}
		})
	}
}

// TestNotifyRungFor pins the attention ladder: the finished-turn rung is configurable
// and may be quieter than the configured mode, while the block edge always uses the
// configured mode itself. The two rungs are named by event and never compared, so no
// ordering over the modes is involved.
func TestNotifyRungFor(t *testing.T) {
	cases := []struct {
		name           string
		base, finished string
		ev             notify.Event
		want           string
	}{
		{"same follows the base on a finish", config.NotificationsDesktop, config.NotificationsSame, notify.EventFinished, config.NotificationsDesktop},
		{"same follows the base on a block", config.NotificationsDesktop, config.NotificationsSame, notify.EventNeedsInput, config.NotificationsDesktop},
		{"a quieter rung applies to the finish", config.NotificationsDesktop, config.NotificationsBell, notify.EventFinished, config.NotificationsBell},
		{"the block keeps the base even with a quieter finished rung", config.NotificationsDesktop, config.NotificationsBell, notify.EventNeedsInput, config.NotificationsDesktop},
		{"off silences the finish", config.NotificationsOSC, config.NotificationsOff, notify.EventFinished, config.NotificationsOff},
		{"off never silences the block", config.NotificationsOSC, config.NotificationsOff, notify.EventNeedsInput, config.NotificationsOSC},
		{"a bell base can still quiet its finish", config.NotificationsBell, config.NotificationsOff, notify.EventFinished, config.NotificationsOff},
		// #571: a question is the most actionable thing an agent can do, so it uses the
		// base mode like a block — notifications_finished must never reach it. That this
		// needs no branch in notifyRungFor is the design: the ladder defaults to base.
		{"a quieter finished rung never reaches a question", config.NotificationsDesktop, config.NotificationsBell, notify.EventAsked, config.NotificationsDesktop},
		{"off never silences a question", config.NotificationsOSC, config.NotificationsOff, notify.EventAsked, config.NotificationsOSC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, notifyRungFor(tc.base, tc.finished, tc.ev))
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
func newNotifyHome(t *testing.T, buf *bytes.Buffer) (*home, *ui.List) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsBell
	h := &home{
		ctx:        context.Background(),
		state:      stateDefault,
		appConfig:  cfg,
		list:       list,
		notifier:   notify.New(buf, cmd.MakeExecutor()),
		notifySeen: make(map[*session.Instance]*notifyState),
	}
	// applyMetadataResults persists on a status edge, which is the whole point of
	// the edges these tests drive — so a home used with it needs storage.
	withCapturingStore(t, h)
	return h, list
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
		h.maybeNotify(inst, session.Running, time.Time{}, false, config.NotificationsBell)
	})
}

func TestMaybeNotifyFirstObservationIsSilent(t *testing.T) {
	var buf bytes.Buffer
	h, _ := newNotifyHome(t, &buf)
	inst := newNotifyInstance(t)
	inst.SetStatus(session.Ready) // a genuine finish edge is pending...
	// ...but the very first observation of the instance never notifies (startup gate).
	h.maybeNotify(inst, session.Running, time.Time{}, false, config.NotificationsBell)
	require.Empty(t, buf.String(), "first observation must be silent")
	_, seen := h.notifySeen[inst]
	require.True(t, seen, "the instance is recorded as observed")
}

func TestMaybeNotifyEmitsFinishForSeenNonSelected(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
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
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsBell)
	require.Empty(t, buf.String())
	// A genuine finish: SetStatus(Ready) advances unreadAt past the zero snapshot.
	target.SetStatus(session.Ready)
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsBell)
	require.Equal(t, "\a", buf.String(), "a genuine finish on a seen, non-selected session rings once")
}

func TestMaybeNotifySelectedSessionStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	inst := newNotifyInstance(t)
	list.AddInstance(inst)() // the sole instance is the selected one
	require.Same(t, inst, list.GetSelectedInstance())
	// Observe it once (gate), then finish it — still silent because it's selected.
	h.maybeNotify(inst, session.Running, time.Time{}, false, config.NotificationsBell)
	inst.SetStatus(session.Ready)
	h.maybeNotify(inst, session.Running, time.Time{}, false, config.NotificationsBell)
	require.Empty(t, buf.String(), "the selected session the user is watching never notifies")
}

// finishOnce marks the background target observed, then drives a genuine finish edge
// and reports whatever maybeNotify wrote. Shared by the focus/mute gate tests.
func finishOnce(h *home, target *session.Instance) {
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsBell) // observe (gate)
	target.SetStatus(session.Ready)                                                      // genuine finish edge
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsBell)
}

// TestMaybeNotifyFocusedStaysSilent covers AC #1: with the terminal focused, no
// notification fires — even a genuine finish on a background session.
func TestMaybeNotifyFocusedStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.focused = true
	finishOnce(h, notifyTarget(t, list))
	require.Empty(t, buf.String(), "a focused terminal notifies nothing")
}

// TestMaybeNotifyNotifiesAfterBlur covers AC #1's second half: the same edge notifies
// once the terminal is blurred.
func TestMaybeNotifyNotifiesAfterBlur(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := notifyTarget(t, list)

	h.focused = true
	finishOnce(h, target) // suppressed while focused
	require.Empty(t, buf.String())

	// Blur, then re-trigger a finish edge: now it rings.
	h.focused = false
	target.SetStatus(session.Running)
	target.SetStatus(session.Ready)
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsBell)
	require.Equal(t, "\a", buf.String(), "the edge notifies after blur")
}

// TestMaybeNotifyUnknownFocusNotifies covers AC #2: a terminal that never reports
// focus (focused is never set true) behaves exactly like today — it notifies.
func TestMaybeNotifyUnknownFocusNotifies(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	// h.focused is left at its zero value (false) — "no focus event yet" is never
	// treated as focused, so notifications are never permanently silenced.
	finishOnce(h, notifyTarget(t, list))
	require.Equal(t, "\a", buf.String(), "unknown focus notifies, never permanent silence")
}

// TestMaybeNotifyFocusedWithNotifyWhenFocusedNotifies covers the escape hatch: with
// notify_when_focused on, a focused terminal still notifies.
func TestMaybeNotifyFocusedWithNotifyWhenFocusedNotifies(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.focused = true
	h.appConfig.NotifyWhenFocused = true
	finishOnce(h, notifyTarget(t, list))
	require.Equal(t, "\a", buf.String(), "notify_when_focused overrides focus-gating")
}

// TestMaybeNotifyMutedStaysSilent covers AC #5: a muted session never notifies, even
// on a genuine background finish edge.
func TestMaybeNotifyMutedStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := notifyTarget(t, list)
	target.SetMuted(true)
	finishOnce(h, target)
	require.Empty(t, buf.String(), "a muted session never notifies")
}

// blockEdge drives a genuine block edge on target and reports whatever maybeNotify wrote:
// the unread stamp is snapshotted before the status write, exactly as applyMetadataResults
// does, so the transition reads as EventNeedsInput and not as a finish.
func blockEdge(h *home, target *session.Instance, mode string) {
	old := target.GetStatus()
	prevUnreadAt := target.UnreadAt()
	target.SetStatus(session.NeedsInput)
	h.maybeNotify(target, old, prevUnreadAt, false, mode)
}

// TestMaybeNotifyFinishedRungOffStaysSilent covers the quietest rung: with the finished
// rung off, a finished turn is left to the list's own unread marker while a session
// blocking on input still signals at the configured mode.
func TestMaybeNotifyFinishedRungOffStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.appConfig.NotificationsFinished = config.NotificationsOff
	target := notifyTarget(t, list)

	finishOnce(h, target)
	require.Empty(t, buf.String(), "a finished turn on the off rung emits nothing out-of-band")
	require.True(t, target.Unread(), "but the in-app unread marker still flags the row")

	blockEdge(h, target, config.NotificationsBell)
	require.Equal(t, "\a", buf.String(), "a session blocking on input still uses the configured mode")
}

// TestMaybeNotifyFinishedRungQuieterThanBase is the ladder proper: one edge resolves to
// the quieter rung and the other to the configured mode, in the same home, so the rung is
// demonstrably chosen per event rather than per batch.
func TestMaybeNotifyFinishedRungQuieterThanBase(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.appConfig.Notifications = config.NotificationsOSC
	h.appConfig.NotificationsFinished = config.NotificationsBell
	target := notifyTarget(t, list)

	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsOSC) // observe (gate)
	target.SetStatus(session.Ready)
	h.maybeNotify(target, session.Running, time.Time{}, false, config.NotificationsOSC)
	require.Equal(t, "\a", buf.String(), "the finish takes the quieter bell rung, not the configured osc")

	buf.Reset()
	blockEdge(h, target, config.NotificationsOSC)
	require.True(t, strings.HasPrefix(buf.String(), "\x1b]9;"), "the block takes the configured osc rung, got %q", buf.String())
}

// TestMaybeNotifyThrottleStaysKeyedOnEvent guards the throttle's key. Both edges resolve
// to the same rung here (bell), so keying the throttle on the resolved rung instead of the
// event would make them share one budget and swallow the block — which is precisely the
// edge the ladder exists to protect.
func TestMaybeNotifyThrottleStaysKeyedOnEvent(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := notifyTarget(t, list)

	finishOnce(h, target)
	require.Equal(t, "\a", buf.String(), "the finish rings")

	// Well inside notifyThrottle, but a different event, so it keeps its own budget.
	blockEdge(h, target, config.NotificationsBell)
	require.Equal(t, "\a\a", buf.String(), "the block has its own throttle budget")
}

// TestFocusBlurMsgTogglesFocused covers the Update wiring: tea.FocusMsg/BlurMsg set and
// clear m.focused.
func TestFocusBlurMsgTogglesFocused(t *testing.T) {
	var buf bytes.Buffer
	h, _ := newNotifyHome(t, &buf)
	require.False(t, h.focused, "starts unknown (not focused)")

	h.Update(tea.FocusMsg{})
	require.True(t, h.focused, "FocusMsg marks the terminal focused")

	h.Update(tea.BlurMsg{})
	require.False(t, h.focused, "BlurMsg marks it blurred")
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
	h, list := newNotifyHome(t, &buf)
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
	h, list := newNotifyHome(t, &buf)
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
	h, list := newNotifyHome(t, &buf)
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
	h, list := newNotifyHome(t, &buf)
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
	h, list := newNotifyHome(t, &buf)
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
	h, list := newNotifyHome(t, &buf)
	target := notifyTarget(t, list)
	target.QueuePrompt("next step")

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PanePromptManual}}, true)
	require.Equal(t, session.NeedsInput, target.GetStatus())
	require.Equal(t, "\a", buf.String(), "a blocked session rings even with a queued prompt")
}

// TestApplyMetadataResultsFinishedRungOff drives the ladder through the real production
// path: with the finished rung off, a background session that finishes a turn stays silent
// while the same session blocking on input still rings.
func TestApplyMetadataResultsFinishedRungOff(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.appConfig.NotificationsFinished = config.NotificationsOff
	target := notifyTarget(t, list)

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	require.Equal(t, session.Ready, target.GetStatus())
	require.Empty(t, buf.String(), "a finished turn on the off rung emits nothing")

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PanePromptManual}}, true)
	require.Equal(t, session.NeedsInput, target.GetStatus())
	require.Equal(t, "\a", buf.String(), "blocking on a prompt still rings at the configured mode")
}

// TestApplyMetadataResultsMasterOffIgnoresFinishedRung confirms notifications=off remains
// the master switch: applyMetadataResults skips the whole notification path, so a finished
// rung can never revive a disabled feature.
func TestApplyMetadataResultsMasterOffIgnoresFinishedRung(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.appConfig.Notifications = config.NotificationsOff
	h.appConfig.NotificationsFinished = config.NotificationsBell
	target := notifyTarget(t, list)

	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneWorking}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PaneIdle}}, true)
	h.applyMetadataResults([]instanceMetaResult{{instance: target, state: tmux.PanePromptManual}}, true)
	require.Empty(t, buf.String(), "notifications off silences every rung")
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

// finishEdge drives a genuine finish edge on target and reports whatever maybeNotify
// wrote, mirroring blockEdge: the unread stamp is snapshotted before the status write,
// exactly as applyMetadataResults does, so the transition reads as a finish.
func finishEdge(h *home, target *session.Instance, asked bool, mode string) {
	old := target.GetStatus()
	prevUnreadAt := target.UnreadAt()
	target.SetStatus(session.Ready)
	h.maybeNotify(target, old, prevUnreadAt, asked, mode)
}

// TestMaybeNotifyQueuedPromptNeverSilencesAQuestion is the heart of #571's second half.
//
// A finished turn with a queued follow-up is suppressed on the reasoning that the queue
// is about to auto-continue it, so ringing would ping the user for work they queued to
// run unattended. That reasoning is FALSE for a turn that ended by asking: the queue is
// held (promptDeliveryReady), nothing will auto-continue, and the user is the only one who
// can resolve it. Suppressing there silences the one event they must act on — and does it
// *because* they queued work.
func TestMaybeNotifyQueuedPromptNeverSilencesAQuestion(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := newNotifyInstance(t)
	other := newNotifyInstance(t)
	list.AddInstance(target)()
	list.AddInstance(other)()
	list.SetSelectedInstance(1) // the target must not be the selected row

	// Observe once so the first-observation gate is spent.
	finishEdge(h, target, false, config.NotificationsBell)
	buf.Reset()

	target.QueueFollowupPrompt("and then deploy it")

	// A plain finish with a queued follow-up stays silent — the existing behaviour.
	target.SetStatus(session.Running)
	finishEdge(h, target, false, config.NotificationsBell)
	require.Empty(t, buf.String(), "a plain finish with a queued follow-up is still suppressed")

	// The same edge, on a turn that ended asking, must ring.
	target.SetStatus(session.Running)
	finishEdge(h, target, true, config.NotificationsBell)
	require.NotEmpty(t, buf.String(), "a question must ring even with a follow-up queued")
}

// TestMaybeNotifyQuestionIgnoresTheFinishedRung pins the ladder end to end: with
// notifications_finished at off — a setting a user picks to stop finish spam — a plain
// finish is silent and a question still rings at the base mode.
func TestMaybeNotifyQuestionIgnoresTheFinishedRung(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	h.appConfig.NotificationsFinished = config.NotificationsOff
	target := newNotifyInstance(t)
	other := newNotifyInstance(t)
	list.AddInstance(target)()
	list.AddInstance(other)()
	list.SetSelectedInstance(1)

	finishEdge(h, target, false, config.NotificationsBell)
	buf.Reset()

	target.SetStatus(session.Running)
	finishEdge(h, target, false, config.NotificationsBell)
	require.Empty(t, buf.String(), "notifications_finished: off silences a plain finish")

	target.SetStatus(session.Running)
	finishEdge(h, target, true, config.NotificationsBell)
	require.NotEmpty(t, buf.String(), "...but must never reach a question")
}

// TestMaybeNotifyQuestionHasItsOwnThrottleBudget pins that EventAsked throttles against
// its own last-fired stamp, not the finish one.
//
// The edges share a status transition, so a finish moments before a question is the
// common case, not a corner: an agent that finishes a turn, is asked to continue, and
// then stops to ask would have its question swallowed by the finish's budget. The middle
// step is a positive control — without it, the question ringing at the end could just
// mean throttling never applies here.
func TestMaybeNotifyQuestionHasItsOwnThrottleBudget(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := newNotifyInstance(t)
	other := newNotifyInstance(t)
	list.AddInstance(target)()
	list.AddInstance(other)()
	list.SetSelectedInstance(1)

	finishEdge(h, target, false, config.NotificationsBell) // spend the first-observation gate
	buf.Reset()

	target.SetStatus(session.Running)
	finishEdge(h, target, false, config.NotificationsBell)
	require.NotEmpty(t, buf.String(), "the first finish rings and spends the finish budget")
	buf.Reset()

	// Positive control: a second finish inside notifyThrottle is swallowed, so the
	// throttle demonstrably applies on this path.
	target.SetStatus(session.Running)
	finishEdge(h, target, false, config.NotificationsBell)
	require.Empty(t, buf.String(), "a second finish inside the window is throttled")
	buf.Reset()

	// The question rides its own budget and must still ring.
	target.SetStatus(session.Running)
	finishEdge(h, target, true, config.NotificationsBell)
	require.NotEmpty(t, buf.String(),
		"a question must not be swallowed by a finish that spent its budget moments earlier")
}
