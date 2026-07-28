package app

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
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
// throttles non-selected sessions to a ~2s sweep, while the watched pane needs
// ~10fps.

// paneFrameInterval is the pane-capture cadence. It matches the preview tick's
// 100ms, but runs on its own chain: the tick must keep firing — dwell marking,
// splash arming, notice flushing — even while a capture is parked on an
// unresponsive tmux server for the full operation timeout.
const paneFrameInterval = 100 * time.Millisecond

// frameTarget names what a capture was launched for. It is resolved on the main
// thread at arm time so the background goroutine never reads live selection
// state. The zero target means "no I/O this round"; the chain still ticks, so it
// self-heals the moment something worth capturing is selected.
type frameTarget struct {
	instance *session.Instance
}

// empty reports a target with nothing to capture.
func (t frameTarget) empty() bool { return t.instance == nil }

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
// It returns the zero target — costing no subprocess at all — whenever a capture
// would be pointless or actively unwanted: nothing selected, a paused session
// (the pane renders its own fallback), or a tab that does not show pane content.
// The attached case is skipped inside CapturePaneFrame rather than here, because
// an attach can begin after the target is resolved.
func (m *home) resolveFrameTarget() frameTarget {
	if !m.tabbedWindow.IsInPreviewTab() {
		return frameTarget{}
	}
	selected := m.list.GetSelectedInstance()
	if selected == nil || selected.Paused() {
		return frameTarget{}
	}
	return frameTarget{instance: selected}
}

// captureFrameCmd sleeps for delay, then captures off the update thread.
//
// Sanitization runs here too (theme.SanitizeWidth is pure), so the main thread
// only ever assigns a finished string. An empty target still returns a message
// after the sleep, without touching tmux: that is what keeps the chain alive
// across a paused selection or a stint on the diff tab.
func captureFrameCmd(ctx context.Context, target frameTarget, delay time.Duration) tea.Cmd {
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
		text, err := target.instance.CapturePaneFrame()
		return paneFrameMsg{
			target: target,
			text:   theme.SanitizeWidth(text),
			err:    err,
			at:     time.Now(),
		}
	}
}

// armFrameCapture dispatches the next capture.
//
// The in-flight flag is the whole no-overlap guarantee: arming sets it, the
// message clears it, and the message handler is the only place the chain re-arms.
// So the loop can neither fork (a second arm is a no-op) nor die (every dispatch
// returns a message, even the ones that do no I/O), and a wedged tmux parks one
// goroutine rather than a growing pile of them.
func (m *home) armFrameCapture(delay time.Duration) tea.Cmd {
	if m.frameInFlight || m.ctx.Err() != nil {
		return nil
	}
	m.frameInFlight = true
	return captureFrameCmd(m.ctx, m.resolveFrameTarget(), delay)
}

// handlePaneFrame applies a captured frame and re-arms the chain.
//
// The frame is stored against the instance it was captured from, never against
// whatever is selected now. Storing a frame for a session the user has already
// moved on from is not waste: it is what makes switching back to that session
// paint immediately instead of flashing the setup splash.
func (m *home) handlePaneFrame(msg paneFrameMsg) (tea.Model, tea.Cmd) {
	m.frameInFlight = false

	if inst := msg.target.instance; inst != nil {
		if msg.err != nil {
			// A failed capture deliberately does not reach the error box. It used to,
			// ten times a second, through instanceChanged's handleError — while the
			// pane it was describing kept rendering fine. The frame and its stamp stay
			// put; the pane reports the growing age itself.
			inst.NotePaneFrameFailure(msg.err, msg.at)
		} else {
			inst.SetPaneFrame(msg.text, msg.at)
		}
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
	return nil
}

// noteFrameTargetChange restamps the preview's freshness because the user pointed
// it somewhere new. Without it, returning from a tab that captures nothing would
// briefly show the age of the frame the pane had left behind — a real number
// answering the wrong question.
func (m *home) noteFrameTargetChange() {
	m.tabbedWindow.NoteFrameTargetChange(time.Now())
}

// tabChanged is the shared tail of every tab switch: sync the hint bar's tab
// state, restamp preview freshness (only the preview tab captures, so arriving
// there is arriving at a new frame source), and re-render the panes.
func (m *home) tabChanged() tea.Cmd {
	m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
	m.noteFrameTargetChange()
	return m.instanceChanged()
}
