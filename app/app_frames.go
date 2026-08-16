package app

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
)

// The pane-capture loop: the one place tmux content is read for the preview.
//
// It exists because the read used to happen inside Update. instanceChanged()
// runs on the 100ms preview tick AND from ~60 key/mouse handlers, and it called
// instance.Preview() — has-session plus capture-pane — inline. Every repaint and
// every keypress therefore waited on a subprocess against a single-threaded,
// shared tmux server, and a wedged server froze the whole TUI for the full
// operation timeout with no way to quit (#380).
//
// The shape is the one the 500ms metadata pass already uses (tickUpdateMetadataCmd
// → collectMetadata → applyMetadataResults): sleep and do I/O in a tea.Cmd, apply
// the result on the main thread. It is a separate chain rather than another job in
// that pass because the cadences are irreconcilable — metadata runs at 500ms and
// throttles non-selected sessions to a ~2s sweep, while a watched pane that is
// moving needs ~10fps.
//
// A pane that is NOT moving needs none of that, which is the second thing this
// file does: the chain ends whenever there is nothing worth capturing, including a
// preview pane that keeps returning byte-identical frames, and the metadata pass's
// free harvest carries it at 2Hz until it moves again (framegate.go, #546).

// paneFrameInterval is the pane-capture cadence. It matches the preview tick's
// 100ms, but runs on its own chain: the tick must keep firing — dwell marking,
// splash arming, notice flushing — even while a capture is parked on an
// unresponsive tmux server for the full operation timeout.
const paneFrameInterval = 100 * time.Millisecond

// frameTarget names what a capture was launched for. It is resolved on the main
// thread at arm time so the background goroutine never reads live selection
// state. The zero target means "nothing to capture", and the chain does not run
// on one at all — armFrameCapture declines to arm and the loop ends, to be
// revived by the 100ms preview tick as soon as there is something to capture
// again.
//
// It is compared with == (handlePaneFrame's re-arm decision, and framegate's quietRun),
// so every field is part of the target's identity and adding one narrows what counts as
// "the same target". See termTitle for what that cost on the field added for #718.
type frameTarget struct {
	// instance is the session whose agent pane to capture (preview tab).
	instance *session.Instance
	// terminal is the session whose shell pane to capture (terminal tab), with
	// termKey naming its cache slot. A nil terminal with a non-empty termKey means
	// "the shell has to be created first" — deliberately part of the background
	// work, since creating one runs tmux new-session.
	terminal *tmux.Session
	termKey  string
	// termInstance is the instance the shell belongs to, needed to create it.
	termInstance *session.Instance
	// termTitle is termInstance's Title, snapshotted HERE on the update thread for the
	// same reason termKey is computed here: Instance.Title is a plain exported field with
	// no mutex, AdoptRename writes it on this thread, and EnsureSession reads it on the
	// capture goroutine — an unsynchronised pair, and a torn string header rather than
	// merely a stale value (#718).
	//
	// It buys correctness at the cost of freshness: a rename landing between this
	// resolution and the create makes the shell's tmux window name, and the legacy reap
	// name, use the OLD title. TerminalPane.EnsureSession's doc decides that per use — for
	// the legacy reap the older title is the more correct one, and the window name is
	// frozen at creation anyway.
	//
	// Because frameTarget is compared by value, this field also joins the target's
	// identity, which has one deliberate consequence: a rename landing while a terminal
	// capture is in flight now makes handlePaneFrame's re-arm see a changed target and
	// re-capture with no delay. That is one capture ~100ms early, once per rename — the
	// next round's own target carries the new title and matches again. The quiet-run
	// comparisons in framegate.go are unaffected: both writers build preview-tab targets
	// (frameTarget{instance: …}), where termTitle is always "".
	termTitle string
}

// empty reports a target with nothing to capture.
func (t frameTarget) empty() bool {
	return t.instance == nil && t.termInstance == nil
}

// paneFrameMsg carries one background capture back to the main thread. It names
// the target it was captured for, and the handler stores it under that identity
// — which is why a frame captured for one session can never be painted into
// another's slot, however fast the selection moves.
type paneFrameMsg struct {
	target frameTarget
	text   string
	err    error
	at     time.Time
}

// resolveFrameTarget picks what to capture, on the main thread.
//
// It returns the zero target — costing no subprocess and, since armFrameCapture
// declines to arm on one, no message either — whenever a capture would be
// pointless or actively unwanted: nothing selected, a paused session (the pane
// renders its own fallback), the screensaver (which replaces the whole frame, so
// every capture under it is discarded on arrival), a tab that does not show pane
// content, or a preview pane that has stopped moving. The attached case is
// skipped inside CapturePaneFrame rather than here, because an attach can begin
// after the target is resolved.
func (m *home) resolveFrameTarget() frameTarget {
	selected := m.list.GetSelectedInstance()
	if selected == nil || selected.Paused() || !selected.Started() {
		return frameTarget{}
	}
	// The screensaver draws the full-window splash and returns from viewContent
	// before the panes are reached, so every capture taken under it is discarded on
	// arrival. It is also the one such state that lasts: hint and scroll mode
	// discard the frame too, but both are transient and holding a capture back
	// through them would freeze frameAt and flash a false staleness marker on exit
	// for no idle saving at all. dismissScreensaver restamps freshness on the way
	// out, and the preview tick revives the chain on the frame after wake-up.
	if m.state == stateScreensaver {
		return frameTarget{}
	}
	switch {
	case m.tabbedWindow.IsInPreviewTab():
		target := frameTarget{instance: selected}
		// A pane that has returned frameQuietRuns identical captures in a row has
		// stopped moving, so stop paying for it. Nothing is lost: the 500ms metadata
		// sweep polls the selected session on every tick and hands over the bytes it
		// already captured (applyHarvestedFrame), so the preview keeps updating at
		// 2Hz — comfortably inside previewStaleAfter's 1.2s, which is why the marker
		// stays silent and why this gate skips the capture rather than slowing it.
		// That harvest is also what re-opens the gate: the first frame that differs
		// resets the run, so the very next tick resolves a real target again.
		//
		// Preview tab only. The terminal tab has no harvest to fall back on, so a
		// gate there would freeze its pane rather than slow it down.
		if m.frameQuiet.settled(target, frameQuietRuns) {
			return frameTarget{}
		}
		return target
	case m.tabbedWindow.IsInTerminalTab():
		// A missing shell is not a reason to skip: creating one is tmux work too,
		// and doing it here on the main thread is what this change removes.
		sess, key, _ := m.tabbedWindow.TerminalCaptureTarget(selected)
		// A shell that does not exist yet has to be CREATED, which is the one thing
		// a teardown in flight must not race (#701). An existing one keeps being
		// captured — this gate withholds a shell, it does not freeze the tab.
		if sess == nil && m.shellStartRefused(selected) {
			return frameTarget{}
		}
		return frameTarget{terminal: sess, termKey: key, termInstance: selected, termTitle: selected.Title}
	default:
		// The diff tab renders from cached git metadata and captures nothing.
		return frameTarget{}
	}
}

// shellStartRefused reports whether the terminal tab must not start a shell for
// inst right now. Creating one runs tmux new-session with the user's $SHELL in
// inst.WorkingDir(), and a pause or a kill is in the middle of deleting that
// directory — on a kill, its branch and its instance record too, so the shell it
// leaves behind has nothing left to reap it but `atrium reset` (#701).
//
// It gates the creation SURFACE rather than the keys, because two of the three
// routes there involve no keypress at all: an already-active terminal tab starts
// its shell from the capture chain on every tick, and the tab bar is clickable
// (TabAtZone → SetActiveTab in app_msgs.go), which never reaches the busy gate.
// Dropping the tab keys from keyAllowedWhileBusy would close neither, and would
// make the view unnavigable during every async action to stop a shell the next
// frame starts anyway.
//
// Both conditions are read on the update thread, which is the only place
// actionInFlight may be read at all (see its declaration). retiring is not
// redundant with it, and the pairing is what covers a kill: asyncActionDoneMsg
// clears actionInFlight one message BEFORE killDoneMsg reaches the reap in
// applyKillDone, and messages are processed in between.
//
// It deliberately does not ask which action is in flight. A rename or a
// run-command start withholds a first shell for its duration too — self-correcting
// on the next tick, and cheaper than an enumeration of the destructive actions
// that would rot the first time one is added.
//
// #522 (--readonly) adds its condition HERE. One predicate, not a second gate:
// a read-only session must refuse a shell in the repo by the same routes.
func (m *home) shellStartRefused(inst *session.Instance) bool {
	return m.actionInFlight || m.retiring[inst]
}

// captureFrameCmd sleeps for delay, then captures off the update thread.
//
// Sanitization runs here too (theme.SanitizeWidth is pure), so the main thread
// only ever assigns a finished string. The empty-target branch is unreachable
// from armFrameCapture, which declines to arm on one; it stays because every
// path here must return a message rather than nothing — a dispatched command
// that produced no paneFrameMsg would leave frameInFlight set forever, and the
// chain would be wedged rather than merely dead.
func captureFrameCmd(ctx context.Context, target frameTarget, delay time.Duration, ensure terminalEnsurer) tea.Cmd {
	return func() tea.Msg {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return paneFrameMsg{target: target, at: time.Now()}
			case <-time.After(delay):
			}
		}
		if ctx.Err() != nil || target.empty() {
			return paneFrameMsg{target: target, at: time.Now()}
		}
		if target.instance != nil {
			text, err := target.instance.CapturePaneFrame()
			return paneFrameMsg{
				target: target,
				text:   theme.SanitizeWidth(text),
				err:    err,
				at:     time.Now(),
			}
		}
		return captureTerminalFrame(target, ensure)
	}
}

// captureTerminalFrame captures the terminal tab's shell pane, creating the shell
// first when it does not exist yet. Both halves run here, on the capture
// goroutine — including tmux new-session, which the pane used to run inline on a
// 100ms tick the moment the user opened the tab.
func captureTerminalFrame(target frameTarget, ensure terminalEnsurer) paneFrameMsg {
	sess := target.terminal
	if sess == nil {
		created, err := ensure(target.termInstance, target.termTitle)
		if err != nil {
			return paneFrameMsg{target: target, err: err, at: time.Now()}
		}
		if created == "" {
			return paneFrameMsg{target: target, at: time.Now()}
		}
		// The shell exists now, but this round has no frame for it. Report the
		// creation with NO cache slot named (termKey empty), so the handler applies
		// nothing and the pane keeps showing "Opening terminal…": applying a blank
		// frame would stamp frameAt, and a non-zero stamp is exactly what
		// TerminalPane.UpdateContent reads as "a frame has arrived", painting an
		// empty pane instead of the fallback. The next round captures it — the
		// target has changed, so it is armed with no delay.
		//
		// termTitle rides along because this target is still the target: it is the snapshot
		// this call was given, carried forward rather than re-read (this runs on the capture
		// goroutine, where reading it would be the #718 defect). Nothing reads the VALUE —
		// the handler applies nothing on this target, since instance is nil and termKey is
		// empty. It does take part in the struct comparison there, like every other field,
		// but cannot change its outcome: an empty termKey already makes this target unequal
		// to any freshly resolved one.
		return paneFrameMsg{
			target: frameTarget{termInstance: target.termInstance, termTitle: target.termTitle},
			at:     time.Now(),
		}
	}
	text, err := sess.CapturePaneContent()
	return paneFrameMsg{
		target: target,
		text:   theme.SanitizeWidth(text),
		err:    err,
		at:     time.Now(),
	}
}

// terminalEnsurer creates the shell session for an instance and returns its cache
// key. It is a function rather than a direct call so captureFrameCmd stays free of
// the ui type — and so a test can drive the create path without a pane.
//
// The title is a parameter rather than something the implementation reads off the
// instance, because it runs on the capture goroutine and Title is unguarded (#718). See
// frameTarget.termTitle.
type terminalEnsurer func(inst *session.Instance, title string) (string, error)

// armFrameCapture dispatches the next capture, or lets the chain end when there
// is nothing to capture.
//
// The in-flight flag is the no-overlap guarantee: arming sets it, the message
// clears it, and every arm goes through here — handlePaneFrame continues a live
// chain, the preview tick revives a dead one (below). So the loop cannot fork —
// a second arm is a no-op — and a wedged tmux parks one goroutine rather than a
// growing pile of them.
//
// It CAN die, deliberately. A zero target used to be dispatched anyway, so a
// paused selection, the diff tab, the screensaver and now a still pane each cost
// ten messages a second that captured nothing and applied nothing — and Bubble
// Tea rebuilds the whole frame after every message (Program.eventLoop's render
// call), measured at 6-9ms and 1.2-2.3MB a rebuild. Declining to arm is what
// turns that into zero. The unconditional 100ms preview tick is the revive
// (handlePreviewTick), in the shape armSplashTick and armSpinnerTick already use:
// a general self-heal rather than an enumeration of the ways a target can become
// interesting again, so a future zero-target case gets its revive for free.
func (m *home) armFrameCapture(delay time.Duration) tea.Cmd {
	if m.frameInFlight || m.ctx.Err() != nil {
		return nil
	}
	target := m.resolveFrameTarget()
	if target.empty() {
		return nil
	}
	m.frameInFlight = true
	return captureFrameCmd(m.ctx, target, delay, m.tabbedWindow.EnsureTerminalSession)
}

// handlePaneFrame applies a captured frame and re-arms the chain.
//
// The frame is stored against the instance it was captured from, never against
// whatever is selected now. Storing a frame for a session the user has already
// moved on from is not waste: it is what makes switching back to that session
// paint immediately instead of flashing the setup splash.
func (m *home) handlePaneFrame(msg paneFrameMsg) (tea.Model, tea.Cmd) {
	m.frameInFlight = false

	switch {
	case msg.target.instance != nil:
		if msg.err != nil {
			// A failed capture deliberately does not reach the error box. It used to,
			// ten times a second, through instanceChanged's handleError — while the
			// pane it was describing kept rendering fine. The frame and its stamp stay
			// put; the pane reports the growing age itself.
			// A failure deliberately notes nothing into the quiet run either: it is
			// not evidence that the pane stopped moving, only that we could not see
			// it. Leaving the run where it is means a run of failures neither closes
			// the gate nor re-opens it.
			msg.target.instance.NotePaneFrameFailure(msg.err, msg.at)
		} else {
			msg.target.instance.SetPaneFrame(msg.text, msg.at)
			m.noteFrameSeen(msg.target, msg.text)
		}
	case msg.target.termKey != "":
		m.tabbedWindow.ApplyTerminalFrame(msg.target.termKey, msg.text, msg.err, msg.at)
	}

	// A capture that came back for a target the user has since moved off leaves the
	// new one with no fresh frame, so re-arm immediately instead of sleeping out
	// another interval first. This is what makes a selection change paint quickly
	// without a second "kick" path: the chain always has one capture in flight, so an
	// extra arm at selection time would be a no-op anyway.
	delay := paneFrameInterval
	if m.resolveFrameTarget() != msg.target {
		delay = 0
	}

	// Repaint from the cache — cheap, and a no-op when the frame belongs to a
	// session that is no longer selected.
	return m, tea.Batch(m.refreshPanes(), m.armFrameCapture(delay))
}

// refreshPanes re-renders the content panes from cached state. It performs no
// I/O: that is the invariant this whole file exists to establish, and the tick-path
// guard in app/frames_test.go is what holds it.
func (m *home) refreshPanes() tea.Cmd {
	selected := m.list.GetSelectedInstance()
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}
	return nil
}

// noteFrameTargetChange restamps the preview's freshness because the user pointed
// it somewhere new. Without it, returning from a tab that captures nothing would
// briefly show the age of the frame the pane had left behind — a real number
// answering the wrong question.
//
// It drops the quiet run for the same reason, and it lives here rather than at
// each caller because this is already the shared tail of every deliberate
// re-point: a selection change (instanceChanged), a tab switch (tabChanged), a
// screensaver wake (dismissScreensaver) and a resume (handleResumeDone). A run
// describing the pane the user just looked away from must not decide whether the
// new one is captured.
//
// The resume is the one that does not announce itself. The other three change
// what the preview points AT; a resume leaves the same *Instance selected and
// swaps the pane underneath it, so instanceChanged's pointer comparison sees
// nothing and only an explicit call gets the run dropped.
func (m *home) noteFrameTargetChange() {
	m.tabbedWindow.NoteFrameTargetChange(time.Now())
	m.frameQuiet = quietRun{}
}

// tabChanged is the shared tail of every tab switch: sync the hint bar's tab
// state, restamp preview freshness (a tab switch changes which pane is being
// captured — only the diff tab captures nothing — so arriving is arriving at a
// new frame source), and re-render the panes.
func (m *home) tabChanged() tea.Cmd {
	m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
	m.noteFrameTargetChange()
	return m.instanceChanged()
}

// dismissScreensaver is the shared tail of every screensaver exit, for the same
// reason tabChanged is one for tab switches: waking is arriving at a new frame
// source. resolveFrameTarget zero-targets under the splash, so no capture ran
// while it owned the window and the cached frame is as old as the screensaver
// was; without the restamp the first frame back reports that age, which is a real
// number about the wrong question.
//
// It is a helper rather than a line repeated at each exit because the exits live
// in different files (a key and a resize in app_update.go, a click in
// app_msgs.go) and a bare `m.state = stateDefault` reads as complete on its own —
// so the restamp is the half that gets forgotten. Waking anywhere means going
// through here; grep stateScreensaver before adding a fourth way out.
func (m *home) dismissScreensaver() {
	m.state = stateDefault
	m.noteFrameTargetChange()
}
