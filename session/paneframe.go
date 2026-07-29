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

// HarvestPaneFrame returns the frame the metadata poll already captured for this
// instance, so the cache can be warmed without forking a second capture-pane.
//
// Poll captures the pane to classify it and then discards the bytes; this hands
// them over instead. It is what makes selecting a session the user has not looked
// at yet paint its last frame immediately rather than flashing the setup splash
// while a fresh capture is dispatched. Safe on a background goroutine (the read is
// under the monitor's own lock); ok is false for a session that has never polled.
func (i *Instance) HarvestPaneFrame() (raw string, at time.Time, ok bool) {
	ts := i.tmux()
	if ts == nil || i.Paused() {
		return "", time.Time{}, false
	}
	return ts.LastCapture()
}

// SetPaneFrame records a successful capture. Called on the main loop, under the
// lock because parking clears the same fields from whichever goroutine ran the
// pause. An empty capture is recorded too: a live-but-blank pane is a real
// observation, and the preview distinguishes "captured blank" from "never captured".
func (i *Instance) SetPaneFrame(text string, at time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.paneFrame = text
	i.paneFrameAt = at
	i.paneFrameOK = true
}

// NotePaneFrameFailure records a failed capture. It deliberately leaves the last
// good frame and its stamp alone: the frame stays on screen and its age keeps
// growing, which is exactly what the staleness marker reports.
func (i *Instance) NotePaneFrameFailure(err error, at time.Time) {
	if err == nil || !paneFrameLog.ShouldLog() {
		return
	}
	_, frameAt, _ := i.PaneFrame()
	log.WarningLog.Printf("pane capture failed: title=%q status=%d age=%s err=%v",
		i.Title, i.GetStatus(), at.Sub(frameAt), err)
}

// PaneFrame returns the last successfully captured frame, when it was captured,
// and whether any capture has ever succeeded. ok=false is what the preview's
// "still coming up" fallback keys on — the same statement the old main-thread
// TmuxAlive() probe made, without the subprocess.
func (i *Instance) PaneFrame() (text string, at time.Time, ok bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.paneFrame, i.paneFrameAt, i.paneFrameOK
}

// SetPaneLive / PaneLive memo the last observed tmux liveness for this instance,
// fed from the metadata poll's own has-session (its PaneState) so the UI never
// forks a second probe to answer the same question (#380).
//
// PaneLive is optimistically true for a started instance that has not been polled
// yet: the only consumer is the context-bar push, whose cost of being wrong is one
// tmux command that fails and logs, while recoverLostInstances owns real death
// within a couple of ticks. A pessimistic default would instead blank the bar on
// every freshly started session until its first poll.
func (i *Instance) SetPaneLive(live bool) {
	i.paneLive = live
	i.paneLiveKnown = true
}

// PaneLive reports the last observed liveness — see SetPaneLive for why an
// unpolled instance reads as live.
func (i *Instance) PaneLive() bool {
	if !i.paneLiveKnown {
		return true
	}
	return i.paneLive
}

// clearPaneFrameLocked forgets the cached frame. A paused session renders its own
// fallback rather than a frame, so holding one would be pure memory — and a resumed
// pane must not flash the pre-pause picture.
//
// Its one caller is the transition INTO Paused (SetStatus), which already holds the
// write lock — hence the Locked suffix. It is deliberately NOT called at the top of
// pause(): parking commits dirty work and removes a worktree first, and for that
// whole window the session is not yet Paused, so the capture chain is still
// targeting it and a frame landing mid-pause would refill a cache dropped before the
// I/O started. The status edge is the point after which no further capture is armed.
func (i *Instance) clearPaneFrameLocked() {
	i.paneFrame = ""
	i.paneFrameAt = time.Time{}
	i.paneFrameOK = false
}
