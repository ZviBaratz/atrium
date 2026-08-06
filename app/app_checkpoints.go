package app

// The checkpoint timeline (#385): the native file-history checkpoints Claude Code
// records before each prompt, read off its own transcript and listed per session.
//
// Read-only on purpose. Claude can be driven to restore files headlessly, but a
// checkpoint covers every file the session ever touched wherever it lives — its
// own plan files, a /tmp scratch dir, sometimes another checkout — and restoring
// deletes files created since, with no dry run. So the one action here is attach,
// handing the user to Claude's own Esc-Esc, which shows the changes first and can
// rewind the conversation along with the code. The findings are on the issue.

import (
	"fmt"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
)

// checkpointsLoadedMsg carries one enumeration back to the update thread. target
// is what makes it safe: the read is a whole-transcript scan, so a slow one can
// land after the user has moved on, and the handler drops any result whose target
// is not the session the open timeline belongs to.
type checkpointsLoadedMsg struct {
	target *session.Instance
	result transcript.Checkpoints
	err    error
}

// openCheckpoints opens the timeline for the selected session and starts the read.
func (m *home) openCheckpoints() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if !selected.SupportsCheckpoints() {
		// Say so rather than opening an empty box or leaving the key dead: only
		// Claude Code keeps checkpoints, and nothing on screen tells the user which
		// agent a row is running.
		return m, m.handleInfoNotice("checkpoints are a Claude Code feature — this session runs a different agent")
	}
	if selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}

	m.checkpointTarget = selected
	m.checkpointRows = nil
	m.checkpointOverlay = overlay.NewCheckpointOverlay(selected.DisplayName())
	m.state = stateCheckpoints
	// Sized here rather than by returning tea.RequestWindowSize, for the reason
	// openCustomCommands spells out: reached from the command palette, both states
	// hide the hint bar, so Update's before/after comparison fires no recompute and
	// an overlay left at its constructor size can be wider than the terminal.
	m.recomputeLayout()
	return m, m.loadCheckpointsCmd(selected)
}

// loadCheckpointsCmd reads the target's checkpoints off the UI thread. Everything
// the closure needs is captured here, on the update thread — it never reaches back
// into m or into unguarded Instance fields.
func (m *home) loadCheckpointsCmd(target *session.Instance) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		result, err := target.LoadCheckpoints(ctx)
		return checkpointsLoadedMsg{target: target, result: result, err: err}
	}
}

// handleCheckpointsLoaded applies an enumeration, or explains why there is none.
func (m *home) handleCheckpointsLoaded(msg checkpointsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.checkpointOverlay == nil || msg.target == nil || msg.target != m.checkpointTarget {
		return m, nil // the timeline closed, or moved to another session, while this read ran
	}
	if msg.err != nil {
		// A missing transcript is the common case here (a session that has not had a
		// turn yet), not a fault worth an error line — the box states it instead.
		log.WarningLog.Printf("checkpoints for %q: %v", msg.target.DisplayName(), msg.err)
		m.checkpointRows = nil
		m.checkpointOverlay.SetUnavailable("no transcript for this session yet")
		return m, nil
	}
	if len(msg.result.List) == 0 {
		m.checkpointRows = nil
		// Also how an older Claude, or one with checkpointing switched off, reads:
		// the records simply are not in the transcript, and there is nothing to
		// probe for. An empty list is a legitimate answer, not a degraded one.
		m.checkpointOverlay.SetUnavailable("no checkpoints recorded for this session")
		return m, nil
	}

	// Newest first — the checkpoint a user wants is nearly always a recent one — and
	// the app's row table is stored in the same order, so SelectedIndex maps through.
	m.checkpointRows = reverseCheckpoints(msg.result.List)
	rows := make([]overlay.CheckpointRow, 0, len(m.checkpointRows))
	for _, cp := range m.checkpointRows {
		rows = append(rows, overlay.CheckpointRow{
			When:    cp.At,
			Label:   cp.Label,
			Files:   cp.Files,
			Outside: cp.Outside,
		})
	}
	m.checkpointOverlay.SetRows(rows)
	m.checkpointOverlay.SetNote(checkpointNote(msg.result))
	return m, nil
}

// checkpointNote is the standing caveat for an enumeration, or "" when there is
// none. Claude sweeps a session's file backups on its own retention schedule while
// leaving the transcript records in place, so a listed checkpoint can outlive the
// copies it would restore from — which the list must not imply away.
func checkpointNote(result transcript.Checkpoints) string {
	if !result.Blobs {
		return "claude has already swept this session's file backups — these are a record, not a restore point"
	}
	return ""
}

// reverseCheckpoints returns the list newest-first. The reversal happens exactly
// once, here, so the overlay's indices and m.checkpointRows can never disagree.
func reverseCheckpoints(list []transcript.Checkpoint) []transcript.Checkpoint {
	out := make([]transcript.Checkpoint, len(list))
	for i, cp := range list {
		out[len(list)-1-i] = cp
	}
	return out
}

// handleCheckpointsState routes a key to the timeline and acts on what it armed.
func (m *home) handleCheckpointsState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.checkpointOverlay.HandleKeyPress(msg)

	if m.checkpointOverlay.RefreshRequested() && m.checkpointTarget != nil {
		m.checkpointOverlay.SetLoading()
		return m, m.loadCheckpointsCmd(m.checkpointTarget)
	}

	if m.checkpointOverlay.AttachRequested() {
		target := m.checkpointTarget
		if target == nil {
			return m, nil
		}
		selected := m.checkpointSelection()
		// Dismiss before attaching: attachExec suspends the event loop, and a box
		// still on screen when the terminal is handed to tmux would be repainted
		// over an attached session.
		m.dismissCheckpointOverlay()
		if target.Paused() {
			return m, m.handleInfoNotice("session is paused — press r to resume, then Esc Esc in the agent")
		}
		notice := "attached — press Esc Esc in claude to rewind"
		if selected != nil {
			notice = fmt.Sprintf("attached — press Esc Esc in claude and pick %s", checkpointStamp(*selected))
		}
		return m, tea.Sequence(m.handleInfoNotice(notice), m.attachExec(target.Attach, target))
	}

	if shouldClose {
		m.dismissCheckpointOverlay()
		return m, m.instanceChanged()
	}
	return m, nil
}

// checkpointSelection is the checkpoint under the cursor, or nil when the list is
// empty (loading, unavailable, or nothing recorded).
func (m *home) checkpointSelection() *transcript.Checkpoint {
	idx := m.checkpointOverlay.SelectedIndex()
	if idx < 0 || idx >= len(m.checkpointRows) {
		return nil
	}
	cp := m.checkpointRows[idx]
	return &cp
}

// checkpointStamp names a checkpoint the way the user will have to recognize it in
// Claude's own rewind list — by its time, which is all both surfaces show.
func checkpointStamp(cp transcript.Checkpoint) string {
	if cp.At.IsZero() {
		return "the checkpoint you were looking at"
	}
	return "the one from " + cp.At.Format("15:04")
}

// dismissCheckpointOverlay tears the timeline down and returns to the list. The row
// table goes with it, so a stale index can never resolve to a checkpoint.
func (m *home) dismissCheckpointOverlay() {
	m.checkpointOverlay = nil
	m.checkpointTarget = nil
	m.checkpointRows = nil
	m.state = stateDefault
}
