package tmux

import (
	"errors"
	"fmt"

	"github.com/ZviBaratz/atrium/log"
)

// DetachReason explains why an Attach loop ended so the caller (app.go's
// attachLoop) can decide whether to return to the list or re-attach a sibling. It
// is set by the stdin interceptor just before Detach and read via AttachExitReason
// after the attach channel closes — the close provides the happens-before, and the
// write+close happen on the same goroutine, so no extra synchronization is needed.
type DetachReason int

const (
	// DetachQuit is the default: a normal Ctrl+Q detach (or any non-nav exit).
	DetachQuit DetachReason = iota
	// DetachNext requests cycling to the next sibling session in the repo group.
	DetachNext
	// DetachPrev requests cycling to the previous sibling session in the repo group.
	DetachPrev
	// DetachExternal means the tmux client exited on its own — the user pressed tmux's
	// native prefix-d, sent Ctrl-D, or the agent process exited — rather than via an
	// in-app key. The stdout pump observes the resulting EOF and tears the attach down
	// so Run() returns and the TUI resumes instead of hanging; see detachOnClientExit.
	DetachExternal
)

// navReason maps a raw stdin chunk to a sibling-navigation detach reason. The keys
// are Ctrl+PageUp (previous) and Ctrl+PageDown (next); their standard xterm
// encodings are matched exactly so pasted content can't trigger an accidental jump.
// Terminals that emit different sequences for these chords simply won't navigate —
// that is the documented terminal-dependency caveat; log buf[:nr] to discover the
// actual bytes before changing this.
func navReason(b []byte) (DetachReason, bool) {
	switch string(b) {
	case "\x1b[5;5~": // Ctrl+PageUp
		return DetachPrev, true
	case "\x1b[6;5~": // Ctrl+PageDown
		return DetachNext, true
	}
	return DetachQuit, false
}

// detachCleanup tears down the goroutines and pty backing the current attach,
// returning any errors instead of panicking. It deliberately does NOT close
// attachCh: the caller closes it last, after writing per-detach state
// (detachReason / detachErr), so the channel close provides the happens-before
// edge that makes that state visible to the reader. Returns nil when there is no
// active attach.
func (t *Session) detachCleanup() []error {
	if t.attachCh == nil {
		return nil // already detached / never attached
	}

	var errs []error

	// Cancel BEFORE closing the pty so the stdout pump, when our ptmx.Close below
	// unblocks its read, observes ctx.Done and treats this as our own teardown (it
	// captured ctx locally) rather than mistaking it for a client-initiated exit and
	// calling detachOnClientExit. That path is reserved for the genuine prefix-d /
	// Ctrl-D / agent-exit case, where the client dies without us cancelling first.
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}

	// Sever the stdout pump in O(1): after this it can never write to the terminal
	// again, so we can hand control back to Bubble Tea without waiting for it to exit.
	// This is the crux of a snappy detach — the pump's pty read is uninterruptible and
	// can stay blocked for seconds (see gatedWriter), so we must NOT wg.Wait on it.
	if t.attachOut != nil {
		t.attachOut.disable()
		t.attachOut = nil
	}

	// Close the attached pty session so the tmux client exits (which eventually
	// unblocks the backgrounded pump's read). Nil the field so a stale pointer is
	// never reused.
	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing attach pty session: %w", err))
		}
		t.ptmx = nil
	}

	// Wait only for the fast goroutines still tracked by t.wg (the window-size
	// monitors, which exit immediately on ctx cancel). The stdout pump is intentionally
	// not in t.wg.
	if t.wg != nil {
		t.wg.Wait()
		t.wg = nil
	}

	t.ctx = nil

	return errs
}

// DetachSafely disconnects from the current tmux session without panicking. It does
// not re-establish the attach pty; the next Attach self-heals a nil ptmx. Used by
// the programmatic lifecycle paths (pause, lost-session recovery).
func (t *Session) DetachSafely() error {
	t.detachMu.Lock()
	defer t.detachMu.Unlock()
	return t.detachSafelyLocked()
}

// detachSafelyLocked is DetachSafely's body; detachMu must be held. It tears the
// attach down without re-Restoring the pty (the next Attach self-heals a nil ptmx),
// and is shared by the programmatic pause/recovery path and the client-exit path.
func (t *Session) detachSafelyLocked() error {
	if t.attachCh == nil {
		return nil // Already detached
	}

	errs := t.detachCleanup()

	// This path nils ptmx without re-Restoring; the next Attach self-heals. Clear
	// the poll guard so the session is polled again while detached.
	t.attached.Store(false)

	// attachCh closed last; nothing reads detachErr on this path, but keep ordering
	// consistent with Detach.
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
	}

	// errors.Join folds the (already-descriptive) detach errors into one unwrappable
	// error, returning nil when there were none.
	return errors.Join(errs...)
}

// Detach disconnects from the current tmux session and re-establishes the attach pty
// for the next Attach. It degrades rather than panicking: a failed pty close or
// Restore is recorded in detachErr (surfaced via AttachExitError) and logged, leaving
// the session recoverable — polling tolerates a nil ptmx, and the next Attach
// re-Restores it. The caller has already set detachReason / killRequested.
func (t *Session) Detach() {
	t.detachMu.Lock()
	defer t.detachMu.Unlock()
	t.detachLocked()
}

// keyDetach is the in-session-keypress teardown (a normal Ctrl+Q, a Ctrl+X kill, or a
// sibling-cycle key). It records the reason/kill and runs the full Detach (Restore
// included) under detachMu so it can't race the stdout pump's client-exit detach:
// whichever caller wins closes attachCh; the loser sees attachCh == nil and returns
// without touching the per-detach state the app reads after the channel closes.
func (t *Session) keyDetach(reason DetachReason, kill bool) {
	t.detachMu.Lock()
	defer t.detachMu.Unlock()
	if t.attachCh == nil {
		return // the client-exit pump already tore this attach down
	}
	t.detachReason = reason
	t.killRequested = kill
	t.detachLocked()
}

// detachOnClientExit tears the attach down when the tmux client exits on its own —
// tmux's native prefix-d, a Ctrl-D, or the agent process exiting — which the stdout
// pump observes as an EOF while our ctx is still live. Before #236 nothing closed
// attachCh on this path, so Run() (and thus the whole TUI) hung; the pump only printed
// a red banner. ch is the attach channel the pump captured at Attach time: a stale
// pump left over from a prior attach must never tear down a newer one, so we act only
// while attachCh is still that channel. It mirrors DetachSafely (no Restore — the next
// Attach self-heals a nil ptmx, and a dead session would fail Restore anyway) and runs
// under detachMu so it can't double-close against a simultaneous keypress detach.
func (t *Session) detachOnClientExit(ch chan struct{}) {
	t.detachMu.Lock()
	defer t.detachMu.Unlock()
	if t.attachCh == nil || t.attachCh != ch {
		return // a keypress detach already won, or a newer attach owns the session
	}
	log.WarningLog.Printf("tmux client for %q exited without an in-app detach "+
		"(prefix-d, Ctrl-D, or the agent exited); returning to the TUI", t.sanitizedName)
	t.detachReason = DetachExternal
	if err := t.detachSafelyLocked(); err != nil {
		log.ErrorLog.Printf("client-exit detach cleanup for %q: %v", t.sanitizedName, err)
	}
}

// detachLocked is Detach's body; detachMu must be held. It re-establishes the attach
// pty for the next Attach and records any teardown error in detachErr.
func (t *Session) detachLocked() {
	if t.attachCh == nil {
		return // already detached / never attached
	}

	errs := t.detachCleanup()

	// Re-establish the attach pty for the next Attach. On failure leave ptmx nil
	// (detachCleanup already did) and record it; Attach/Resume will re-Restore.
	if err := t.Restore(); err != nil {
		errs = append(errs, fmt.Errorf("error restoring attach pty after detach: %w", err))
	}

	if joined := errors.Join(errs...); joined != nil {
		t.detachErr = joined
		log.ErrorLog.Println(t.detachErr)
	} else {
		t.detachErr = nil
	}

	// Clear the poll guard only now, after Restore has reinstalled the detached
	// ptmx/monitor, so no metadata tick observes a half-swapped monitor. Keep
	// this strictly after Restore on any future reorder.
	t.attached.Store(false)

	// attachCh closed LAST, after detachErr is written, so the reader observes it.
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
	}
}
