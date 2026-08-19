package main

import (
	"fmt"

	"github.com/ZviBaratz/atrium/internal/handover"
)

// Who, if anyone, is draining the outbox spools.
//
// `atrium send` and `atrium new` are producers: they spool a file and the TUI's
// metadata tick drains it. Neither can do anything about a tick that is not running,
// so this exists only to phrase a message — never to decide whether to spool, which
// has already happened by the time anything here is asked. Both probes are advisory
// and inherently racy in the way tuiRunning's doc comment describes.
//
// It reads two locks, because one does not answer the question. tui.lock says a TUI
// process is alive; handover.lock says that TUI has handed its terminal to a child,
// which blocks the Bubble Tea event loop and with it the tick. A TUI that is alive and
// parked drains nothing until the user detaches — the case #760 is about, and the case
// `atrium new` is most often in, since an agent handing off runs inside a session and
// somebody is usually watching that session.
type drainVerdict int

const (
	// drainUnknown: nothing can be asserted. Either probe may say so, and a caller
	// must then say only what is true of the spool file.
	drainUnknown drainVerdict = iota
	// drainNoTUI: no TUI holds tui.lock, so nothing is draining until one starts.
	drainNoTUI
	// drainParked: a TUI is alive but its terminal is handed to a child, so its tick
	// is blocked and the spool drains on the return rather than on a relaunch.
	drainParked
	// drainLive: a TUI is alive with its loop running, so the spool is being polled.
	drainLive
)

// drainState reports who is draining the spools and, for drainParked, a noun phrase
// naming what the terminal was handed to ("" when there is nothing trustworthy to say).
func drainState() (drainVerdict, string) {
	running, known := tuiRunning()
	switch {
	case !known:
		return drainUnknown, ""
	case !running:
		return drainNoTUI, ""
	}
	held, payload, known := handover.Held()
	switch {
	case !known:
		// A live TUI that cannot be asked whether it is parked is the one combination
		// with two plausible readings and no way to pick, so it asserts neither.
		return drainUnknown, ""
	case held:
		return drainParked, payload.Describe()
	default:
		return drainLive, ""
	}
}

// spoolWarningFor renders the stderr warning for a verdict drainState already
// returned, or "" when there is nothing worth saying. gerund is the caller's verb for
// what is not happening ("creating", "delivering").
//
// Takes the verdict rather than re-deriving it so one command run makes one pair of
// lock probes, and so the warning and any later --wait message cannot disagree about
// what was seen.
//
// The two cases it does speak to are not equally urgent, and only one is addressed to
// the caller. "No TUI is running" is for whoever ran the command: the request is
// durable and lands on the next launch. "Atrium is parked" is for the person at the
// keyboard, who is the only party that can unblock it — and, since an agent's `atrium
// new` runs inside a session, the pane this lands in is usually the one they are
// looking at.
func spoolWarningFor(verdict drainVerdict, what, gerund string) string {
	switch verdict {
	case drainNoTUI:
		return fmt.Sprintf("warning: no atrium TUI is running, so nothing is %s this yet; "+
			"it stays queued and is picked up the next time one starts\n", gerund)
	case drainParked:
		return fmt.Sprintf("warning: atrium is running but %s, so its poll loop is parked and "+
			"nothing is %s this yet; it stays queued and is picked up when you detach\n",
			handedOverPhrase(what), gerund)
	default:
		return ""
	}
}

// drainerClause names who is draining, for the tail of a --wait timeout message, or ""
// when nothing can be asserted and the caller should fall back to enumerating.
//
// Evaluated at the deadline rather than when the wait began: a user who detached
// during the wait has changed the answer, and reporting the state at the start would
// name a condition that has since cleared.
func drainerClause() string {
	verdict, what := drainState()
	switch verdict {
	case drainNoTUI:
		return "No atrium TUI is running, so nothing is draining the outbox; " +
			"the next one to start picks it up"
	case drainParked:
		return fmt.Sprintf("Atrium is running but %s, so its poll loop is parked: "+
			"the outbox drains when it returns, not on a relaunch", handedOverPhrase(what))
	case drainLive:
		return "An atrium TUI is running with its poll loop live, so the outbox is being " +
			"read — this is queued behind something rather than unattended"
	default:
		return ""
	}
}

// handedOverPhrase renders what the terminal went to, falling back to the fact itself
// when the payload named nothing. The fallback is not a cosmetic default: the lock is
// what proves the loop is parked, and the label is decoration written beside it, so a
// missing label must not cost the caller the finding.
func handedOverPhrase(what string) string {
	if what == "" {
		return "has handed its terminal to a session"
	}
	return what
}
