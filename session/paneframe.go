package session

import (
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// The last-known pane frame: captured off the main thread (CapturePaneFrame),
// applied on it (SetPaneFrame / NotePaneFrameFailure), read by the View
// (PaneFrame). It follows the same single-writer rule as diffStats and prStatus
// in metadata.go — the fields are deliberately unguarded because the only writer
// is the main event loop.
//
// This cache is what lets the preview render without shelling out to tmux on the
// Bubble Tea update thread (#380). Every tick used to run `has-session` +
// `capture-pane` inline, so a contended tmux server added its latency to every
// repaint — and to every keypress, since instanceChanged() runs from ~60 sites.
// A wedged server could freeze the whole TUI for tmuxOpTimeout. Now a background
// capture fills this cache and the pane paints from it: stale-but-marked beats
// fresh-but-frozen.

// paneFrameLog throttles the capture-failure trail to one line per window. A
// failing capture repeats at the frame cadence, so an unthrottled log would bury
// everything else — and unlike the old path, a failure no longer reaches the
// error box at all (it surfaces as the pane's staleness marker), which makes
// this log the only evidence trail.
var paneFrameLog = log.NewEvery(5 * time.Second)

// CapturePaneFrame captures the instance's agent pane for the preview.
//
// Safe on a background goroutine: tmux() reads under the lock and returns, and
// no lock is held across the subprocess. It touches none of the unguarded
// main-thread fields.
//
// Unlike Preview it does NOT probe has-session first. A capture failure IS the
// liveness signal here — the probe only ever cost a second subprocess to predict
// what the capture was about to tell us — and Poll's PaneDead remains the
// authoritative death verdict that parks the session. Returns empty content (not
// an error) for a paused, unstarted, or attached session, so the caller stores
// nothing and the pane keeps its last frame.
func (i *Instance) CapturePaneFrame() (string, error) {
	if i.Paused() {
		return "", nil
	}
	ts := i.tmux()
	if ts == nil {
		return "", nil
	}
	// While the user is attached, the live tmux client owns the pane; capturing
	// would contend the socket for a frame nobody is looking at. This mirrors
	// tmux.Session.Poll's own attach skip.
	if ts.Attached() {
		return "", nil
	}
	return ts.CapturePaneContent()
}

// SetPaneFrame records a successful capture. Main-loop only, like SetDiffStats.
// An empty capture is recorded too: a live-but-blank pane is a real observation,
// and the preview distinguishes "captured blank" from "never captured".
func (i *Instance) SetPaneFrame(text string, at time.Time) {
	i.paneFrame = text
	i.paneFrameAt = at
	i.paneFrameOK = true
}

// NotePaneFrameFailure records a failed capture. Main-loop only. It deliberately
// leaves the last good frame and its stamp alone: the frame stays on screen and
// its age keeps growing, which is exactly what the staleness marker reports.
func (i *Instance) NotePaneFrameFailure(err error, at time.Time) {
	if err == nil || !paneFrameLog.ShouldLog() {
		return
	}
	log.WarningLog.Printf("pane capture failed: title=%q status=%d age=%s err=%v",
		i.Title, i.GetStatus(), at.Sub(i.paneFrameAt), err)
}

// PaneFrame returns the last successfully captured frame, when it was captured,
// and whether any capture has ever succeeded. ok=false is what the preview's
// "still coming up" fallback keys on — the same statement the old main-thread
// TmuxAlive() probe made, without the subprocess.
func (i *Instance) PaneFrame() (text string, at time.Time, ok bool) {
	return i.paneFrame, i.paneFrameAt, i.paneFrameOK
}

// dropPaneFrame forgets the cached frame. Called from pause(), which runs on the
// main event loop, so it shares the "main loop only" contract above. A paused
// session renders its own fallback rather than a frame, so holding one would be
// pure memory — and a resumed pane must not flash the pre-pause picture.
func (i *Instance) dropPaneFrame() {
	i.paneFrame = ""
	i.paneFrameAt = time.Time{}
	i.paneFrameOK = false
}
