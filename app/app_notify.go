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
	lastAsked      time.Time
}

// notifyEventFor maps a status transition to the notification event it warrants, if
// any. The finish edge reuses the existing unread machinery: unreadAdvanced is true
// exactly when SetStatus flagged a genuine, non-suppressed non-Ready→Ready
// transition this tick (its unreadAt stamp moved), so ArmReadySuppression's silencing
// of synthetic restore/resume/recover transitions is inherited for free. The
// NeedsInput edge is a plain status diff — synthetic lifecycle writes only ever go to
// Running, never NeedsInput, so no suppression is needed there.
//
// asked splits that finish edge in two (#571). It is the same transition on the same
// tick — a question does not have an edge of its own, because the pane cannot tell one
// from a plain finish — so it is tested FIRST and simply renames the event. Everything
// the finish edge already earns (the unread stamp, the synthetic-transition
// suppression) is inherited unchanged; only the routing downstream differs.
func notifyEventFor(old, current session.Status, unreadAdvanced, asked bool) (notify.Event, bool) {
	switch {
	case unreadAdvanced && asked:
		return notify.EventAsked, true
	case unreadAdvanced:
		return notify.EventFinished, true
	case old != session.NeedsInput && current == session.NeedsInput:
		return notify.EventNeedsInput, true
	default:
		return 0, false
	}
}

// notifyRungFor resolves which notification mode delivers ev — the attention ladder.
//
// Only ONE event has a rung of its own: a plain finished turn, which may use a quieter
// mode when the user configured one. Everything else — a session blocking on input, and a
// turn that ended by asking a question (#571) — uses base, so neither can be out-shouted
// by an agent that merely finished, and neither can be silenced by a setting chosen for an
// unrelated reason. That EventAsked needs no branch here is the point: the ladder is keyed
// by event and defaults to base, so a new event is loud until something opts it out.
//
// The rungs are named by *event* and never compared with each other, so the ladder needs
// no ordering over the modes — which is what lets it exist at all, since "one rung quieter
// than bell" would be silence and desktop and osc are peers. finished is the normalized
// selector from config.GetNotificationsFinished, whose restricted vocabulary is what keeps
// the finished rung from outranking base.
func notifyRungFor(base, finished string, ev notify.Event) string {
	if ev != notify.EventFinished || finished == config.NotificationsSame {
		return base
	}
	return finished
}

// maybeNotify emits a notification for one instance's status transition, applying the
// suppression rules in order: the startup replay, a focused terminal (unless
// notify_when_focused), a muted session, and the selected/attached session all stay
// silent; a finished turn stays silent when a follow-up prompt is queued or when the
// attention ladder puts its rung at off; and repeats of the same edge are throttled.
// old/prevUnreadAt are snapshots taken immediately before ApplyPaneState in
// applyMetadataResults; asked is the #571 question verdict resolved for this same tick,
// which splits the finish edge into "finished" and "asked"; mode is the live configured
// mode (never off — the caller gates on that), from which the ladder derives the rung
// actually emitted. Runs on the main Update thread, so it never fires while attached (the
// event loop is suspended) and never races the bell write with the renderer beyond the
// documented single-BEL window.
func (m *home) maybeNotify(inst *session.Instance, old session.Status, prevUnreadAt time.Time, asked bool, mode string) {
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
	ev, ok := notifyEventFor(old, inst.GetStatus(), inst.UnreadAt().After(prevUnreadAt), asked)
	if !ok {
		return
	}
	// A finished turn on a session with a queued or in-flight follow-up prompt is about
	// to be auto-continued by deliverReadyPrompts in this same applyMetadataResults pass,
	// so ringing "finished" would ping the user for work they explicitly queued to run
	// unattended. Two edges are exempt, for the same underlying reason — the queue is not
	// about to consume them:
	//   - NeedsInput: a blocked pane can't consume its queue (delivery needs an idle input
	//     box), so it stays genuinely actionable.
	//   - Asked (#571): questionHoldsPrompt now HOLDS the queue on an unanswered question,
	//     applied by deliverReadyPrompts later in this same pass (the attach keeper hands
	//     the same predicate to promptDeliveryReady instead — same hold, different
	//     dispatcher), so the premise of this suppression is false for it. This comment
	//     used to concede that such a turn "can't be told apart from a real finish that
	//     awaits them" — that was true when it was written and is not any more, which is
	//     exactly why the suppression had to stop covering it. Suppressing here would
	//     silence the one event the user must act on, and would do it *because* they
	//     queued work.
	// The final finish, once the queue has drained, still rings.
	if ev == notify.EventFinished && (inst.Prompt() != "" || inst.PromptSending()) {
		return
	}
	// The attention ladder picks WHICH signal fires, strictly downstream of every gate
	// above, which decide WHETHER one does — a muted or focused session stays silent no
	// matter how loud its rung. It sits ahead of the throttle so a rung the user silenced
	// doesn't consume the edge's budget (same reasoning as the focus gate above), and the
	// throttle stays keyed on the event rather than the resolved rung, so a block still
	// rings when a finish moments earlier resolved to the same mode. An off rung is not
	// silence: the list's unread marker still flags the row.
	rung := notifyRungFor(mode, m.appConfig.GetNotificationsFinished(), ev)
	if rung == config.NotificationsOff {
		return
	}
	if st.throttled(ev) {
		return
	}
	m.notifier.Emit(rung, m.appConfig.GetNotifyCommand(), inst.DisplayName(), ev)
}

// throttled reports whether an edge of type ev fired too recently to signal again,
// stamping the current time when it permits the notification. Each event carries its own
// budget — a question must not be swallowed because a finish moments earlier spent one.
func (st *notifyState) throttled(ev notify.Event) bool {
	now := time.Now()
	last := &st.lastFinished
	switch ev {
	case notify.EventNeedsInput:
		last = &st.lastNeedsInput
	case notify.EventAsked:
		last = &st.lastAsked
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
