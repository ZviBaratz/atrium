package app

import (
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
)

// notifyThrottle is the minimum spacing between two notifications of the same edge
// for the same session. Edges already fire only on status transitions, so this only
// guards a markerless agent's classifier flapping (e.g. prompt detection flipping
// NeedsInput↔Running); it is deliberately coarse.
const notifyThrottle = 3 * time.Second

// notifyState tracks a single instance's notification bookkeeping: the last time
// each edge was signalled, for throttling. Its mere presence in home.notifySeen also
// means "this instance has been observed at least once" — the first-observation gate.
type notifyState struct {
	lastFinished   time.Time
	lastNeedsInput time.Time
}

// notifyEventFor maps a status transition to the notification event it warrants, if
// any. The finish edge reuses the existing unread machinery: unreadAdvanced is true
// exactly when SetStatus flagged a genuine, non-suppressed non-Ready→Ready
// transition this tick (its unreadAt stamp moved), so ArmReadySuppression's silencing
// of synthetic restore/resume/recover transitions is inherited for free. The
// NeedsInput edge is a plain status diff — synthetic lifecycle writes only ever go to
// Running, never NeedsInput, so no suppression is needed there.
func notifyEventFor(old, current session.Status, unreadAdvanced bool) (notify.Event, bool) {
	switch {
	case unreadAdvanced:
		return notify.EventFinished, true
	case old != session.NeedsInput && current == session.NeedsInput:
		return notify.EventNeedsInput, true
	default:
		return 0, false
	}
}

// maybeNotify emits a notification for one instance's status transition, applying the
// suppression rules: the selected/attached session and the startup replay stay silent,
// and repeats of the same edge are throttled. old/prevUnreadAt are snapshots taken
// immediately before ApplyPaneState in applyMetadataResults; mode is the live config
// value (never off — the caller gates on that). Runs on the main Update thread, so it
// never fires while attached (the event loop is suspended) and never races the bell
// write with the renderer beyond the documented single-BEL window.
func (m *home) maybeNotify(inst *session.Instance, old session.Status, prevUnreadAt time.Time, mode string) {
	if m.notifier == nil {
		return // hand-built test home without a notifier
	}
	st, seen := m.notifySeen[inst]
	if !seen {
		// First time we've observed this instance (startup restore, or a freshly
		// created session): record it but never notify on the initial status, so a
		// batch of restored NeedsInput/Ready sessions can't ring on launch.
		m.notifySeen[inst] = &notifyState{}
		return
	}
	// While Atrium's terminal is focused the user is watching the fleet, so nothing
	// notifies (unless notify_when_focused overrides it). This sits after the
	// first-observation gate so notifySeen stays maintained while focused — the first
	// edge after a blur isn't mistaken for a first observation — and before the
	// throttle so a suppressed edge doesn't consume its budget. A terminal that never
	// reports focus leaves m.focused false, so this never fires (AC #2).
	if m.focused && !m.appConfig.GetNotifyWhenFocused() {
		return
	}
	if inst.Muted() {
		return // the user has silenced this session (M)
	}
	if inst == m.list.GetSelectedInstance() {
		return // the user is already looking at this row
	}
	ev, ok := notifyEventFor(old, inst.GetStatus(), inst.UnreadAt().After(prevUnreadAt))
	if !ok {
		return
	}
	// A finished turn on a session with a queued or in-flight follow-up prompt is about
	// to be auto-continued by deliverReadyPrompts in this same applyMetadataResults pass,
	// so ringing "finished" would ping the user for work they explicitly queued to run
	// unattended (and can't be told apart from a real finish that awaits them). The block
	// edge is exempt: a blocked pane can't consume its queue — delivery needs an idle
	// input box — so a NeedsInput session stays genuinely actionable. The final finish,
	// once the queue has drained, still rings.
	if ev == notify.EventFinished && (inst.Prompt() != "" || inst.PromptSending()) {
		return
	}
	if st.throttled(ev) {
		return
	}
	m.notifier.Emit(mode, m.appConfig.GetNotifyCommand(), inst.DisplayName(), ev)
}

// throttled reports whether an edge of type ev fired too recently to signal again,
// stamping the current time when it permits the notification.
func (st *notifyState) throttled(ev notify.Event) bool {
	now := time.Now()
	last := &st.lastFinished
	if ev == notify.EventNeedsInput {
		last = &st.lastNeedsInput
	}
	if now.Sub(*last) < notifyThrottle {
		return true
	}
	*last = now
	return false
}

// notificationsMode returns the live notification mode, or off when notifications are
// disabled or the notifier is absent — the single gate applyMetadataResults consults
// so a disabled feature (the default) costs nothing per tick.
func (m *home) notificationsMode() string {
	if m.notifier == nil {
		return config.NotificationsOff
	}
	return m.appConfig.GetNotifications()
}
