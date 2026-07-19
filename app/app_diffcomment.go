package app

// Diff-tab comments (#383): a line cursor on the diff tab whose comment queues to
// the selected session's agent as a follow-up prompt. Entered with C, the pane
// freezes on a row snapshot (see ui.DiffPane), j/k move the cursor over code lines,
// enter opens a composer anchored to that line, and the submitted note becomes a
// "Re: file:line …" queued prompt reusing the verified-delivery queue.

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
)

// enterDiffComment focuses the diff tab, renders its diff, and drops the line
// cursor. It declines with a notice when there is no session or no code line to
// anchor a comment to (an empty or still-loading diff).
func (m *home) enterDiffComment() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, m.handleInfoNotice("no session selected")
	}
	// Show the diff (freshly rendered) before freezing it for comments.
	if !m.tabbedWindow.IsInDiffTab() {
		m.tabbedWindow.SetActiveTab(ui.DiffTab)
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
	}
	m.tabbedWindow.UpdateDiff(selected)
	if !m.tabbedWindow.EnterDiffComment() {
		return m, m.handleInfoNotice("no diff lines to comment on")
	}
	m.state = stateDiffComment
	m.menu.SetState(ui.StateDiffComment)
	m.recomputeLayout() // the hint bar claims a row; shrink the panes to fit
	return m, nil
}

// handleDiffCommentState routes a key while the line cursor is active: j/k move it
// over code lines, enter opens the composer for the current line, esc (or C again)
// leaves comment mode.
func (m *home) handleDiffCommentState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, m.exitDiffComment()
	case "enter":
		return m.openDiffCommentComposer()
	}
	// Movement (and the C toggle) go through the registry so a future rebind only
	// touches the keymap. The ok check matters: KeyUp is the zero value, so a missing
	// key must not fall through to it.
	if name, ok := keys.GlobalKeyStringsMap[msg.String()]; ok {
		switch name {
		case keys.KeyUp:
			m.tabbedWindow.DiffCursorUp()
		case keys.KeyDown:
			m.tabbedWindow.DiffCursorDown()
		case keys.KeyMoveUp, keys.KeyShiftUp: // K or shift+↑ — extend the selection up
			m.tabbedWindow.DiffExtendUp()
		case keys.KeyMoveDown, keys.KeyShiftDown: // J or shift+↓ — extend the selection down
			m.tabbedWindow.DiffExtendDown()
		case keys.KeyDiffComment:
			return m, m.exitDiffComment()
		}
	}
	return m, nil
}

// exitDiffComment leaves comment mode and lets the live diff resume (cursor gone).
func (m *home) exitDiffComment() tea.Cmd {
	m.tabbedWindow.ExitDiffComment()
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.recomputeLayout()
	return m.instanceChanged()
}

// openDiffCommentComposer opens a compose box titled with the cursor's file:line.
// The diff pane stays frozen underneath, so the anchor read at submit is the one
// the user is looking at now.
func (m *home) openDiffCommentComposer() (tea.Model, tea.Cmd) {
	loc, ok := m.tabbedWindow.DiffCommentLocation()
	if !ok {
		return m, m.handleInfoNotice("no line under the cursor")
	}
	m.composingDiffComment = true
	m.state = statePrompt
	m.textInputOverlay = overlay.NewQuickSendOverlay("Comment on " + loc)
	m.recomputeLayout() // the hint bar hides behind the composer; panes reclaim its row
	return m, tea.WindowSize()
}

// handleDiffCommentComposer routes keys while the diff-comment composer is up. On
// submit it queues the composed comment; on cancel (esc/ctrl+c) it returns to the
// line cursor rather than the list.
func (m *home) handleDiffCommentComposer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, m.cancelDiffComment()
	}
	shouldClose, _ := m.textInputOverlay.HandleKeyPress(msg)
	if !shouldClose {
		return m, nil
	}
	if m.textInputOverlay.IsCanceled() || !m.textInputOverlay.IsSubmitted() {
		return m, m.cancelDiffComment()
	}
	return m, m.submitDiffComment(m.textInputOverlay.GetValue())
}

// cancelDiffComment closes the composer without queuing and returns to the cursor.
func (m *home) cancelDiffComment() tea.Cmd {
	m.textInputOverlay = nil
	m.composingDiffComment = false
	m.state = stateDiffComment
	m.menu.SetState(ui.StateDiffComment)
	m.recomputeLayout()
	return tea.WindowSize()
}

// submitDiffComment composes the anchored comment and queues it to the selected
// session's agent (reusing the verified-delivery queue), then returns to the line
// cursor so the reviewer can annotate the next line. An empty note or a lost anchor
// queues nothing and says so.
func (m *home) submitDiffComment(note string) tea.Cmd {
	m.textInputOverlay = nil
	m.composingDiffComment = false
	m.state = stateDiffComment
	m.menu.SetState(ui.StateDiffComment)
	m.recomputeLayout()

	selected := m.list.GetSelectedInstance()
	msg, ok := m.tabbedWindow.DiffCommentMessage(note)
	if selected == nil || !ok || strings.TrimSpace(note) == "" {
		return tea.Sequence(tea.WindowSize(), m.handleInfoNotice("empty comment — nothing queued"))
	}
	selected.QueueFollowupPrompt(msg)
	if err := m.persistInstances(); err != nil {
		log.ErrorLog.Printf("failed to persist diff comment: %v", err)
	}
	return tea.Sequence(tea.WindowSize(), m.handleInfoNotice(fmt.Sprintf("comment queued for %q", selected.DisplayName())))
}
