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
	"context"
	"errors"

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
	if m.checkpointSourceIsAmbiguous(selected) {
		return m, m.handleInfoNotice("another session shares this one's claude project directory — its checkpoints cannot be told apart")
	}

	m.checkpointTarget = selected
	m.checkpointOverlay = overlay.NewCheckpointOverlay(selected.DisplayName())
	m.state = stateCheckpoints
	// Sized here rather than by returning tea.RequestWindowSize, for the reason
	// openCustomCommands spells out: reached from the command palette, both states
	// hide the hint bar, so Update's before/after comparison fires no recompute and
	// an overlay left at its constructor size can be wider than the terminal.
	m.recomputeLayout()
	return m, m.loadCheckpointsCmd(selected)
}

// checkpointSourceIsAmbiguous reports whether another session in the fleet reads
// the same Claude project directory as this one.
//
// The enumeration resolves its transcript the way every reader here does — newest
// mtime in <root>/projects/<sanitized-cwd> — and that mapping is many-to-one:
// two direct sessions on one directory, or two paths sanitizeCWD flattens together,
// share the whole set of files, so newest-mtime picks arbitrarily between their
// conversations (transcript.ProjectDir, #596). The context indicator answers that
// by declining the reading (usagePolicy: absent rather than wrong), and a timeline
// has to decline harder — it is headed with the session's own name, and its one
// action walks the user into that session to rewind files off the list.
//
// Deliberately not usagePolicy itself: that one is gated on the context-indicator
// setting, and turning an indicator off must not make the timeline start lying.
// The whole fleet is scanned, like newUsagePolicy, because a collision is a fact
// about what is on disk — a filtered or scrolled-out session writes just the same.
func (m *home) checkpointSourceIsAmbiguous(target *session.Instance) bool {
	key := target.ContextSourceKey()
	if key == "" {
		return false // nothing resolved, so nothing to collide on
	}
	for _, inst := range m.list.GetInstances() {
		if inst != target && inst.ContextSourceKey() == key {
			return true
		}
	}
	return false
}

// loadCheckpointsCmd reads the target's checkpoints off the UI thread. Everything
// the closure needs is captured here, on the update thread — it never reaches back
// into m or into unguarded Instance fields.
//
// The context is per-read, not the app's: a whole-transcript scan is unbounded, so
// closing the box or reloading over it cancels the scan it supersedes rather than
// stacking another one behind it. Cancelling the previous read here is what makes
// repeated `r` cost one scan rather than one per press.
func (m *home) loadCheckpointsCmd(target *session.Instance) tea.Cmd {
	m.cancelCheckpointRead()
	ctx, cancel := context.WithCancel(m.ctx)
	m.checkpointCancel = cancel
	return func() tea.Msg {
		result, err := target.LoadCheckpoints(ctx)
		return checkpointsLoadedMsg{target: target, result: result, err: err}
	}
}

// cancelCheckpointRead stops whatever enumeration is in flight, if any.
func (m *home) cancelCheckpointRead() {
	if m.checkpointCancel != nil {
		m.checkpointCancel()
		m.checkpointCancel = nil
	}
}

// handleCheckpointsLoaded applies an enumeration, or explains why there is none.
func (m *home) handleCheckpointsLoaded(msg checkpointsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.checkpointOverlay == nil || msg.target == nil || msg.target != m.checkpointTarget {
		return m, nil // the timeline closed, or moved to another session, while this read ran
	}
	if msg.err != nil {
		log.WarningLog.Printf("checkpoints for %q: %v", msg.target.DisplayName(), msg.err)
		// A session that has not had a turn has no transcript, which is the common
		// case here and not a fault worth an error line — the box states it. Anything
		// else did fail (a permission error, an I/O error, a transcript line past the
		// scanner's ceiling) and must not be described as an absent transcript: the
		// user would be told the file does not exist while it plainly does, and `r`
		// would repeat the same wrong sentence.
		if errors.Is(msg.err, transcript.ErrNoTranscript) {
			m.checkpointOverlay.SetUnavailable("no transcript for this session yet")
		} else {
			m.checkpointOverlay.SetUnavailable("could not read this session's transcript — see the log")
		}
		return m, nil
	}
	if len(msg.result.List) == 0 {
		// Also how an older Claude, or one with checkpointing switched off, reads:
		// the records simply are not in the transcript, and there is nothing to
		// probe for. An empty list is a legitimate answer, not a degraded one.
		m.checkpointOverlay.SetUnavailable("no checkpoints recorded for this session")
		return m, nil
	}

	// Newest first — the checkpoint a user wants is nearly always a recent one, and
	// the cursor opens on it. The reader returns file order, so reverse here, once.
	//
	// No parallel row table is kept, unlike the palette's: v1's only action is
	// "attach to this session", which the cursor position does not affect. When a
	// per-checkpoint action arrives it will need one, and the read-once intent flags
	// are the seam for it.
	rows := make([]overlay.CheckpointRow, 0, len(msg.result.List))
	for i := len(msg.result.List) - 1; i >= 0; i-- {
		cp := msg.result.List[i]
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
//
// Blobs is the existence of the file-history directory, and Claude creates that
// directory on the first file it backs up: a session that has touched no files has
// no directory and never had one, so its absence is not a sweep. Saying so anyway
// would put a standing data-loss warning on a session whose checkpointing is
// working normally. The last row's Files is the cumulative tracked count, so
// Files == 0 there is exactly "there was never anything to sweep".
func checkpointNote(result transcript.Checkpoints) string {
	if result.Blobs || len(result.List) == 0 {
		return ""
	}
	if result.List[len(result.List)-1].Files == 0 {
		return ""
	}
	return "claude has already swept this session's file backups — these are a record, not a restore point"
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
		// Every precondition first, exactly as attachSelected orders them: a refused
		// attach must leave the timeline standing. Tearing it down here and then
		// declining would cost the user the list they were reading — and a second
		// whole-transcript scan to get it back — for an action that never happened.
		//
		// Refusals speak, a successful attach does not — the same split
		// attachSelected makes, and for a sharper reason here: the next thing that
		// happens is tmux taking the terminal, so a notice would be painted for one
		// frame and then buried. The footer carries the Esc-Esc reminder instead,
		// where it is read before the keypress rather than after it. The notice rides
		// the error row, which the centred box does not cover.
		if target.Paused() {
			// Through pausedResumeNotice so the resume key is read from the registry
			// rather than spelled: an override can move it, and a notice telling the
			// user to press a key that now does something else is worse than silence.
			// "Esc Esc" stays a literal — it is Claude's chord inside the agent's pane,
			// not an Atrium binding, so no override reaches it.
			return m, m.handleInfoNotice(pausedResumeNotice("before Esc Esc in the agent"))
		}
		if target.GetStatus() == session.Loading {
			return m, m.handleInfoNotice(stillStartingNotice)
		}
		if !target.TmuxAlive() {
			return m, m.handleInfoNotice("session terminal has exited — nothing to attach to")
		}
		// Dismiss before attaching: attachExec suspends the event loop, and a box
		// still on screen when the terminal is handed to tmux would be repainted
		// over an attached session.
		m.dismissCheckpointOverlay()
		return m, m.attachExec(target.Attach, target)
	}

	if shouldClose {
		m.dismissCheckpointOverlay()
		return m, m.instanceChanged()
	}
	return m, nil
}

// dismissCheckpointOverlay tears the timeline down and returns to the list.
func (m *home) dismissCheckpointOverlay() {
	m.cancelCheckpointRead()
	m.checkpointOverlay = nil
	m.checkpointTarget = nil
	m.state = stateDefault
}
