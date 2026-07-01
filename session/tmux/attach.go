package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// attachInputAction is the outcome of classifying a chunk of attach stdin: keep
// forwarding it to the pty, detach, detach-and-request-kill, or detach to cycle to
// the next/previous sibling session.
type attachInputAction int

const (
	attachForward attachInputAction = iota
	attachDetach
	attachKill
	attachNext
	attachPrev
)

// Control bytes intercepted while attached.
const (
	ctrlQ = 17 // detach
	ctrlX = 24 // kill (detach + request teardown)
)

// classifyAttachInput decides what a single stdin read means while attached.
// Ctrl+Q detaches; Ctrl+X requests a kill but only when allowKill is set (agent
// sessions, not the Terminal-tab shell, where Ctrl+X is a normal editing key).
// A control byte is only honored when it arrives alone (a single-byte read) so
// it isn't mistaken for part of a longer escape sequence or paste. Everything
// else is forwarded to the pty unchanged.
func classifyAttachInput(in []byte, allowKill bool) attachInputAction {
	if len(in) == 1 {
		switch in[0] {
		case ctrlQ:
			return attachDetach
		case ctrlX:
			if allowKill {
				return attachKill
			}
		}
	}
	// Sibling navigation (Ctrl+PageUp/PageDown) arrives as a multi-byte escape
	// sequence, so it is matched separately from the single-byte control keys.
	switch reason, ok := navReason(in); {
	case ok && reason == DetachNext:
		return attachNext
	case ok && reason == DetachPrev:
		return attachPrev
	}
	return attachForward
}

// gatedWriter wraps the attach's stdout pump so detach can sever it in O(1). The pty
// master that the io.Copy goroutine reads is opened in blocking mode by creack/pty
// (its open() ioctls the master through os.File.Fd(), which drops the fd from Go's
// runtime poller), so neither Close nor SetReadDeadline can interrupt that read — it
// returns only once the tmux client process exits, which a busy single-threaded server
// can delay by seconds. Rather than block detach on it, we disable the writer (so any
// late bytes are discarded instead of corrupting the reclaimed terminal) and let the
// goroutine drain on its own.
type gatedWriter struct {
	w        io.Writer
	disabled atomic.Bool
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	if g.disabled.Load() {
		return len(p), nil // detached: swallow late output instead of writing the TUI's screen
	}
	return g.w.Write(p)
}

func (g *gatedWriter) disable() { g.disabled.Store(true) }

// stdinNukeWindow is how long after an attach the stdin reader treats incoming
// bytes as the terminal's control-sequence burst (e.g. ?[?62c, ]10;rgb:...) and
// drops them so they never reach tmux. A var, not a const, so tests can shorten it.
var stdinNukeWindow = 50 * time.Millisecond

// Attach connects the terminal to the tmux session and blocks (via the returned
// channel) until the user detaches. When allowKill is true, the in-session kill
// key (Ctrl+X) detaches and sets KillRequested so the caller can tear the session
// down; the Terminal-tab shell passes false so Ctrl+X stays a normal shell key.
func (t *Session) Attach(allowKill bool) (chan struct{}, error) {
	// A prior Detach whose Restore failed leaves ptmx nil rather than panicking;
	// re-establish the attach pty here so the degraded session heals transparently
	// on the next attach. If Restore fails again, propagate it to the caller.
	if t.ptmx == nil {
		if err := t.Restore(); err != nil {
			return nil, fmt.Errorf("cannot attach: pty unavailable and restore failed: %w", err)
		}
	}

	t.attachCh = make(chan struct{})
	t.killRequested = false
	t.detachReason = DetachQuit
	t.detachErr = nil

	t.wg = &sync.WaitGroup{}
	t.ctx, t.cancel = context.WithCancel(context.Background())

	// The stdout pump copies the pty master to the terminal until the master closes.
	// It is deliberately NOT tracked by t.wg: creack/pty opens the master in blocking
	// mode, so its Read cannot be interrupted by Close — it returns only once the tmux
	// client exits, which a busy server can delay by seconds. Blocking detach on that
	// froze the whole UI (the wg.Wait in detachCleanup). Instead, detach disables the
	// gated writer — so this goroutine can no longer touch the terminal Bubble Tea has
	// reclaimed — and lets it drain in the background. ctx and ptmx are captured locally
	// because detachCleanup nils both without waiting for this goroutine. The pump's
	// captures are taken here; the goroutine itself is spawned last (see the end of Attach).
	out := &gatedWriter{w: os.Stdout}
	t.attachOut = out
	pumpPtmx := t.ptmx
	pumpCtx := t.ctx
	pumpCh := t.attachCh

	// Snapshot ptmx before the loop so the goroutine writes through a local copy instead
	// of re-reading the shared t.ptmx field on every keypress. DetachSafely (called by
	// lost-session recovery) can set t.ptmx = nil from another goroutine while this one is
	// blocked on os.Stdin.Read; reading the field in the loop would be a data race on that
	// pointer. (os.File.Write is nil-safe, so the original code raced rather than panicked.)
	attachedPtmx := t.ptmx
	// Capture ctx locally for the same reason: detachCleanup nils t.ctx without waiting
	// for this goroutine, which blocks on the uninterruptible os.Stdin.Read (like the
	// stdout pump it is deliberately NOT in t.wg — wg.Wait would freeze detach). The
	// local ctx lets a reader left over from a prior attach notice the detach after its
	// next read and exit WITHOUT touching the detach state or pty of a newer attach.
	readCtx := t.ctx
	// nukeUntil is evaluated here, at the spawn, rather than inside the goroutine so the
	// window is testable: readAttachStdin takes the deadline as a parameter.
	go t.readAttachStdin(readCtx, os.Stdin, attachedPtmx, allowKill, time.Now().Add(stdinNukeWindow))

	t.monitorWindowSize()
	// Mark attached last, once attachCh + goroutines + the captured ptmx are all
	// live. From here the metadata tick skips this session until teardown clears
	// the flag (after Restore reinstalls the detached ptmx/monitor).
	t.attached.Store(true)

	// Spawn the stdout pump LAST — only now are wg (monitorWindowSize's Adds) and the
	// attached flag fully established. The pump is the one teardown trigger with no
	// startup delay (the stdin reader is gated by stdinNukeWindow), so if it fired
	// detachOnClientExit mid-setup it could nil t.wg under monitorWindowSize's Add or
	// have its attached.Store(false) clobbered by the Store(true) above. Starting it here
	// closes that window; the pty buffers output until the copy begins microseconds later.
	// Return a captured channel so a near-instant client exit (pump nils t.attachCh) still
	// hands Run the now-closed channel instead of a nil one.
	ret := t.attachCh
	go func() {
		_, _ = io.Copy(out, pumpPtmx)
		// io.Copy returned: the pty closed. If our own detach cancelled ctx, the teardown
		// already owns this — do nothing. Otherwise the tmux client exited on its own
		// (prefix-d, Ctrl-D, or the agent process exited); tear the attach down here so
		// attachCh closes and Run() returns, instead of leaving the TUI hung (#236). The
		// old code only printed a red banner here, which both failed to unblock Run and —
		// now that we hand the terminal back — would corrupt the reclaimed TUI screen.
		select {
		case <-pumpCtx.Done():
			// Normal detach, do nothing
		default:
			t.detachOnClientExit(pumpCh)
		}
	}()

	return ret, nil
}

// readAttachStdin reads the attached terminal's stdin and acts on each chunk until
// the user detaches (Ctrl+Q), requests a kill (Ctrl+X when allowKill), cycles to a
// sibling (Ctrl+PageUp/Down), the reader goes stale, or in reaches EOF. Bytes read
// before nukeUntil are the terminal's post-attach control-sequence burst (device
// attributes, color queries) and are dropped so they never reach tmux; everything
// after is classified and either acted on or forwarded to ptmx.
//
// Attach spawns it with os.Stdin and the pty master. It is deliberately NOT tracked
// by t.wg: in.Read is uninterruptible, so a reader left over from a prior attach
// exits via the ctx check after its next read rather than being waited on by detach.
// ctx and ptmx are captured by Attach before the goroutine starts so a concurrent
// detachCleanup that nils t.ctx / t.ptmx can't race them.
func (t *Session) readAttachStdin(ctx context.Context, in io.Reader, ptmx io.Writer, allowKill bool, nukeUntil time.Time) {
	stdinReadErr := log.NewEvery(60 * time.Second)
	// Read input from stdin and check for Ctrl+q
	buf := make([]byte, 32)
	for {
		nr, err := in.Read(buf)
		if err != nil {
			if err == io.EOF {
				return
			}
			// Throttle: a persistently failing stdin (e.g. a closed fd) must not spin
			// the log; the loop still retries on the next read.
			if stdinReadErr.ShouldLog() {
				log.ErrorLog.Printf("attach stdin read error (throttled): %v", err)
			}
			continue
		}

		// A detach (user keystroke, pause, or app shutdown) cancels ctx. Once cancelled
		// this goroutine is stale — stop reading and never act on the shared detach state
		// or pty, which may already belong to a newer attach.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Drop the initial control-sequence burst (see nukeUntil).
		if time.Now().Before(nukeUntil) {
			log.InfoLog.Printf("nuked first stdin: %s", buf[:nr])
			continue
		}

		switch classifyAttachInput(buf[:nr], allowKill) {
		case attachDetach:
			t.keyDetach(DetachQuit, false)
			return
		case attachKill:
			// Detach and request a kill; the caller reads KillRequested after
			// the attach returns and runs the teardown confirmation.
			t.keyDetach(DetachQuit, true)
			return
		case attachNext:
			t.keyDetach(DetachNext, false)
			return
		case attachPrev:
			t.keyDetach(DetachPrev, false)
			return
		default:
			// Forward other input to tmux. If DetachSafely closed the pty, this write
			// returns a "file already closed" error (discarded) rather than racing on
			// t.ptmx. ptmx is captured live at Attach time, so it is never nil.
			_, _ = ptmx.Write(buf[:nr])
		}
	}
}

// KillRequested reports whether the most recent attach ended with the user
// pressing the in-session kill key (Ctrl+X). It is reset at the start of Attach.
func (t *Session) KillRequested() bool {
	return t.killRequested
}

// AttachExitReason reports why the most recent attach ended. It is meaningful only
// after the attach channel returned by Attach has closed.
func (t *Session) AttachExitReason() DetachReason {
	return t.detachReason
}

// AttachExitError reports any error encountered while tearing down the most recent
// attach (a failed pty close or Restore). It is meaningful only after the attach
// channel returned by Attach has closed, and is nil for a clean detach.
func (t *Session) AttachExitError() error {
	return t.detachErr
}
