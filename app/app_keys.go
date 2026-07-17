package app

// Per-state overlay key handlers and per-action key handlers extracted from
// handleKeyPress (app_update.go). handleKeyPress stays a thin dispatcher: the
// overlay-state prelude delegates to the handleXState methods here, and the
// substantial key-action cases delegate to the verb-named methods here. Trivial
// one-line cases (navigation, tab switching) remain inline in the switch.

import (
	"fmt"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/actions"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
)

// stillStartingNotice is shown when a per-session action is pressed while the
// session is still in the Loading phase. It is a single source so every guard
// surfaces identical wording.
const stillStartingNotice = "session is still starting — try again in a moment"

// quitAfterStartupNotice is shown when the user asks to quit while a session is
// still Loading. Rather than drop the half-created session, handleQuit waits for
// its Start to finish and then exits (issue #268). The parenthetical advertises
// the force-quit escape (a second quit) for a Start that never finishes.
const quitAfterStartupNotice = "finishing session startup before quitting… (q again to abandon)"

// selectedActionable returns the selected instance when a per-session action may
// run against it. The bool is false (with the command to return) when there is no
// selection or it is still starting — the two guards almost every session action
// shares. Handlers with extra guards (paused-only, attach liveness, …) keep their
// own checks and only reuse stillStartingNotice.
func (m *home) selectedActionable() (*session.Instance, tea.Cmd, bool) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return nil, nil, false
	}
	if selected.GetStatus() == session.Loading {
		return nil, m.handleInfoNotice(stillStartingNotice), false
	}
	return selected, nil, true
}

// --- Overlay-state key handlers (delegated from handleKeyPress's prelude) ------

// handlePromptState routes a key to the text-input overlay (new-session form or
// quick-send compose box) and handles submit/cancel/retarget/debounce.
func (m *home) handlePromptState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle cancel via ctrl+c before delegating to the overlay
	if msg.String() == "ctrl+c" {
		return m, m.cancelPromptOverlay()
	}

	// #388: up-arrow on an empty prompt field opens the prompt-history reuse
	// picker. ctrl+r — the issue's first suggestion — already arms the create
	// form's clear gesture, so up-on-empty is used instead: it is free in both the
	// create form and quick-send (an empty field has nothing to move up to).
	if msg.String() == "up" && m.textInputOverlay.PromptFocusedAndEmpty() {
		if texts := promptHistoryTexts(m.appState.GetPromptHistory()); len(texts) > 0 {
			m.promptHistoryOverlay = overlay.NewPromptHistoryOverlay(texts)
			m.promptHistoryOverlay.SetWidth(historyOverlayWidth(m.windowWidth))
			m.state = stateHistory
			return m, nil
		}
	}

	// Snapshot the title so a keystroke that edits it can refresh the inline
	// duplicate verdict (and schedule the async branch-existence check) below.
	prevTitle := ""
	if m.textInputOverlay.IsCreateForm() {
		prevTitle = m.textInputOverlay.GetTitle()
	}

	// Use the new TextInputOverlay component to handle all key events
	shouldClose, branchFilterChanged := m.textInputOverlay.HandleKeyPress(msg)

	// Check if the form was submitted or canceled
	if shouldClose {
		if m.textInputOverlay.IsCanceled() {
			return m, m.cancelPromptOverlay()
		}

		if !m.textInputOverlay.IsSubmitted() {
			m.textInputOverlay = nil
			m.state = stateDefault
			return m, nil
		}

		prompt := m.textInputOverlay.GetValue()

		// Smart dispatch: route the single line to a project and open the pre-filled
		// form (or, opt-in, create directly) instead of sending it anywhere.
		if m.textInputOverlay.IsSmartDispatch() {
			return m, m.handleSmartDispatchSubmit(prompt)
		}

		// The new-session form creates the instance only now, on submit, so no row
		// appears in the list while the user is still filling it in.
		if m.textInputOverlay.IsCreateForm() {
			return m, m.createSessionFromForm(prompt)
		}

		// Quick-send overlay: queue the message for the selected running session and drop
		// straight back to the list (no new-session help — the session is already up).
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			m.textInputOverlay = nil
			m.state = stateDefault
			return m, nil
		}
		// Route the message through the same tick-driven, verified delivery as the
		// new-session prompt rather than sending it inline. SendPrompt now polls to confirm
		// the text landed and submitted, which must not block the UI thread, and its soft
		// "pane not ready yet" outcomes must defer to a retry rather than surface as user
		// errors; queuing reuses that closed-loop machinery (await readiness, paste
		// multi-line, confirm, idempotent retry, persist) instead of duplicating it here.
		// A quick-send appends a follow-up (zero-clock: delivered when the agent next
		// idles, never force-injected mid-turn) rather than overwriting the slot, so a
		// message queued behind a booting or busy agent is never silently lost. Flash a
		// "queued" acknowledgment so the submit isn't silent.
		selected.QueueFollowupPrompt(prompt)
		m.recordPrompt(prompt)
		if err := m.persistInstances(); err != nil {
			log.ErrorLog.Printf("failed to persist queued quick-send prompt: %v", err)
		}
		m.textInputOverlay = nil
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)
		notice := m.handleInfoNotice(fmt.Sprintf("queued for %q", selected.DisplayName()))
		return m, tea.Sequence(tea.WindowSize(), m.instanceChanged(), notice)
	}

	// A confirmed double-tap Ctrl+R rebuilds the form fresh and drops any draft.
	if m.textInputOverlay.ClearRequested() {
		m.stashedDraft = nil
		m.clearPersistedDraft()
		return m, m.openCreateForm(true)
	}

	// If the target directory changed in the picker, re-scope the form to the
	// new repo (branch search + async target-state re-check; see
	// retargetNewSession for why the check is debounced and async).
	if newPath := m.textInputOverlay.GetSelectedPath(); newPath != "" && newPath != m.newSessionPath {
		return m, m.retargetNewSession(newPath)
	}

	// Schedule a debounced branch search if the filter changed
	if branchFilterChanged {
		filter := m.textInputOverlay.BranchFilter()
		version := m.textInputOverlay.BranchFilterVersion()
		return m, m.scheduleBranchSearch(filter, version)
	}

	// A keystroke that edited the title refreshes the inline duplicate verdict
	// (in-memory, instant) and schedules the async branch-existence check.
	if m.textInputOverlay.IsCreateForm() {
		if title := m.textInputOverlay.GetTitle(); title != prevTitle {
			m.titleBranchExists = false // the old verdict was for the old title
			m.refreshTitleError()
			return m, m.scheduleTitleCheck(title, m.newSessionPath)
		}
	}

	return m, nil
}

// handleConfirmState routes a key to the confirmation overlay and, on a confirmed
// close, runs the pending action. The action runs one of two ways depending on how
// it was armed:
//   - confirmAsyncAction set a busy label → run it off the UI thread via
//     beginAsyncAction (a real tea.Cmd goroutine) behind a "busy" progress row.
//     These closures are UI-thread-safe (they touch only their captured
//     instance/worktree); their model mutation happens in the completion handler.
//   - confirmAction (no label) → run it inline on the main loop, as before. Kill and
//     the other list/terminal-mutating confirms take this path deliberately: a
//     goroutine would race Update on shared model state.
//
// Either way only the resulting message flows back through the runtime, so a
// returned error reaches the error box.
func (m *home) handleConfirmState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
	if shouldClose {
		confirmed := m.confirmationOverlay.Confirmed
		action := m.pendingConfirmAction
		busyLabel := m.pendingConfirmBusyLabel
		m.state = stateDefault
		m.confirmationOverlay = nil
		m.pendingConfirmAction = nil
		m.pendingConfirmBusyLabel = ""
		if confirmed && action != nil {
			if busyLabel != "" {
				return m, m.beginAsyncAction(busyLabel, action)
			}
			resultMsg := action()
			return m, func() tea.Msg { return resultMsg }
		}
		return m, nil
	}
	return m, nil
}

// handleRenameState routes a key to the rename overlay and, on submit, applies the
// new display name / note to the instance the overlay was opened for.
func (m *home) handleRenameState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.renameOverlay.HandleKeyPress(msg)
	if !shouldClose {
		return m, nil
	}

	submitted := m.renameOverlay.IsSubmitted()
	value := m.renameOverlay.Value()
	note := m.renameOverlay.NoteValue()
	deep := m.renameOverlay.IsDeep()
	// Apply to the instance the overlay was opened for, not the currently
	// selected one — they can differ if the selection moved while the overlay
	// was open (notably during async auto-naming).
	target := m.renameTarget
	m.renameOverlay = nil
	m.renameTarget = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)

	if submitted && target != nil {
		if deep {
			if err := m.deepRename(target, value); err != nil {
				// The rename was rejected before anything changed (e.g. a name
				// collision). Reopen the dialog pre-filled with the attempted name
				// and note so neither is lost — nothing is persisted until a rename
				// succeeds.
				m.renameTarget = target
				m.renameOverlay = overlay.NewRenameOverlay(value, note, false)
				m.state = stateRename
				return m, m.handleError(err)
			}
		} else {
			target.SetDisplayName(value)
		}
		target.SetNote(note)
		if err := m.persistInstances(); err != nil {
			return m, m.handleError(err)
		}
	}
	return m, m.instanceChanged()
}

// handleQueueState routes a key to the queue overlay: cursor moves and esc are
// handled inside the overlay; a cancel (d/x) is performed here against the target
// instance the overlay was opened for (queueTarget), not the live selection —
// which can move while the overlay is open. A successful cancel persists and
// refreshes the list; cancelling the last entry closes the overlay and flashes on
// the now-visible hint bar; a refusal explains itself in-overlay (since the hint
// bar is hidden behind the box), distinguishing the in-flight head from a queue
// that shifted under the snapshot.
func (m *home) handleQueueState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.queueOverlay.HandleKeyPress(msg)

	if m.queueOverlay.RemoveRequested() && m.queueTarget != nil {
		// Capture what the user was acting on *before* the refresh so a refusal can
		// name the right reason: cancelling the head they could see was in flight
		// (marked with the ⟳ glyph) versus a stale index whose queue shifted.
		refusingInFlightHead := m.queueOverlay.SelectedIndex() == 0 && m.queueOverlay.HeadInFlight()
		removed := m.queueTarget.CancelQueuedPrompt(m.queueOverlay.SelectedIndex(), m.queueOverlay.SelectedText())
		if removed {
			if err := m.persistInstances(); err != nil {
				log.ErrorLog.Printf("failed to persist after cancelling queued prompt: %v", err)
			}
		}
		texts, headInFlight := m.queueTarget.QueueView()
		if len(texts) == 0 {
			// The queue drained — close and flash on the (now visible) hint bar.
			m.dismissQueueOverlay()
			return m, tea.Batch(m.handleInfoNotice("queue cleared"), m.instanceChanged())
		}
		m.queueOverlay.SetQueue(texts, headInFlight)
		if !removed {
			if refusingInFlightHead {
				m.queueOverlay.SetMessage("can't cancel — prompt is being delivered")
			} else {
				m.queueOverlay.SetMessage("can't cancel — the queue just changed")
			}
		}
		return m, m.instanceChanged()
	}

	if shouldClose {
		m.dismissQueueOverlay()
		return m, m.instanceChanged()
	}
	return m, nil
}

// dismissQueueOverlay tears down the queue overlay and returns to the list.
func (m *home) dismissQueueOverlay() {
	m.queueOverlay = nil
	m.queueTarget = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
}

// handleCmdLogState routes a key to the command-log overlay. All navigation,
// filter cycling and failure expansion live inside the overlay, which reads the
// log ring live; only esc/ctrl+c closes.
func (m *home) handleCmdLogState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cmdLogOverlay.HandleKeyPress(msg) {
		m.dismissCmdLogOverlay()
	}
	return m, nil
}

// dismissCmdLogOverlay tears down the command-log overlay and returns to the list.
func (m *home) dismissCmdLogOverlay() {
	m.cmdLogOverlay = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
}

// handleSettingsState routes a key to the settings overlay, live-applies any
// changed row, and reclaims the menu row when the panel closes.
func (m *home) handleSettingsState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	closed, changedKey := m.settingsOverlay.HandleKeyPress(msg)
	var cmds []tea.Cmd
	if changedKey != "" {
		if cmd := m.applySettingChange(changedKey); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if closed {
		m.settingsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row
		cmds = append(cmds, tea.WindowSize())
	}
	return m, tea.Batch(cmds...)
}

// handleFilterState routes a key while the list filter is being edited. The list
// holds the query (single source of truth); printable keys extend it, so j/k
// cannot be reserved as commit keys here.
func (m *home) handleFilterState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Esc clears the filter and returns to default.
		m.list.ClearFilter()
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)
		m.recomputeLayout() // the hint bar gave up its row; panes reclaim it
		return m, m.instanceChanged()
	case "enter", "down":
		// Accept the current query and move focus to the filtered list.
		m.list.SetFilterActive(false)
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)
		m.recomputeLayout() // the hint bar gave up its row; panes reclaim it
		return m, m.instanceChanged()
	case "backspace", "ctrl+h":
		if q := m.list.FilterQuery(); q != "" {
			// Remove the last rune (handles multi-byte correctly).
			runes := []rune(q)
			m.list.SetFilter(string(runes[:len(runes)-1]))
		}
		return m, m.instanceChanged()
	default:
		// Append printable characters to the filter query.
		if len(msg.Runes) > 0 {
			m.list.SetFilter(m.list.FilterQuery() + string(msg.Runes))
		}
		return m, m.instanceChanged()
	}
}

// enterMultiSelect enters multi-select ("visual") mode from the list. It is a
// no-op (with an explanation) when there are no sessions, so the mode never opens
// on an empty list.
func (m *home) enterMultiSelect() (tea.Model, tea.Cmd) {
	if m.list.NumInstances() == 0 {
		return m, m.handleInfoNotice("no sessions to select")
	}
	m.enterVisualMode()
	return m, m.instanceChanged()
}

// enterVisualMode flips into multi-select mode and gives the hint bar its row
// (even when the always-on bar is off, so the gestures are always taught).
func (m *home) enterVisualMode() {
	m.state = stateVisual
	m.menu.SetState(ui.StateVisual)
	m.recomputeLayout()
}

// exitVisualMode leaves multi-select mode: it clears the marks and restores the
// default state/menu, reclaiming the hint-bar row. The marked runners call this
// before opening their confirmation; confirmAction then overrides the state to
// stateConfirm, and the layout is already correct for the post-confirm default.
func (m *home) exitVisualMode() {
	m.list.ClearMarks()
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.recomputeLayout()
}

// handleMultiSelectState routes a key while multi-select ("visual") mode is
// active: space marks/unmarks the highlighted session, the lifecycle keys act on
// the marked set (each opens its own count confirmation over the eligible
// subset), navigation still moves the cursor, and esc clears the marks and exits.
func (m *home) handleMultiSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.exitVisualMode()
		return m, m.instanceChanged()
	case "x":
		// Plain x kills the marked set (the bar advertises "x"); ctrl+x (the global
		// kill chord, KeyKill below) does the same.
		return m, m.killMarked()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}
	switch name {
	case keys.KeyMultiSelect:
		// v toggles the mode: pressing it again exits, mirroring esc.
		m.exitVisualMode()
		return m, m.instanceChanged()
	case keys.KeyToggleMark:
		m.list.ToggleMark(m.list.GetSelectedInstance())
		return m, m.instanceChanged()
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyPause:
		return m, m.pauseMarked()
	case keys.KeyResume:
		return m, m.resumeMarked()
	case keys.KeyKill:
		return m, m.killMarked()
	default:
		return m, nil
	}
}

// --- Per-action key handlers (delegated from handleKeyPress's switch) ----------

// openQuickSend opens a compose box to fire an ad-hoc message at the selected
// running session without attaching. Only meaningful when the agent is up and
// accepting input; other states explain the guard instead of swallowing the key.
func (m *home) openQuickSend() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Paused() {
		return m, m.handleInfoNotice("session is paused — press r to resume before sending")
	}
	if !selected.Started() || selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}
	m.state = statePrompt
	m.textInputOverlay = overlay.NewQuickSendOverlay("Send to " + selected.DisplayName())
	return m, tea.WindowSize()
}

// approveSelected answers the selected session's visible prompt with a single
// Enter, or accepts claude's ghost-text suggestion with Right+Enter — both without
// attaching. Strictly gated so a stray 'a' can't poke an agent that isn't asking.
func (m *home) approveSelected() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.GetStatus() == session.NeedsInput {
		if err := selected.ApprovePrompt(); err != nil {
			return m, m.handleError(fmt.Errorf("approve: %w", err))
		}
		// Optimistic flip: updates the row glyph immediately and turns a
		// double-press into the guard notice instead of a second Enter.
		// Self-correcting — the next poll tick reclassifies the pane.
		selected.SetStatus(session.Running)
		return m, m.handleInfoNotice(fmt.Sprintf("approved — enter sent to '%s'", selected.DisplayName()))
	}
	// A suggestion only renders on an idle input box, so Ready is the cheap
	// pre-filter (never capture a busy pane); Started keeps an instance
	// with no live pane on the guarded-notice path rather than surfacing
	// AcceptSuggestion's "not running" error for a no-op keypress.
	if selected.GetStatus() == session.Ready && selected.Started() {
		accepted, err := selected.AcceptSuggestion()
		if err != nil {
			return m, m.handleError(fmt.Errorf("accept suggestion: %w", err))
		}
		if accepted {
			// Same optimistic flip as approve, for the same reasons.
			selected.SetStatus(session.Running)
			return m, m.handleInfoNotice(fmt.Sprintf("accepted suggestion — sent to '%s'", selected.DisplayName()))
		}
	}
	return m, m.handleInfoNotice("agent isn't waiting on a prompt — nothing to approve or accept")
}

// copySelectedBranch yanks the selected session's branch name to the system
// clipboard. Both outcomes are acknowledged on the hint row.
func (m *home) copySelectedBranch() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Branch == "" {
		return m, m.handleInfoNotice("no branch to copy yet")
	}
	if err := actions.CopyToClipboard(selected.Branch); err != nil {
		return m, m.handleError(fmt.Errorf("copy branch: %w", err))
	}
	return m, m.handleInfoNotice(fmt.Sprintf("branch '%s' copied", selected.Branch))
}

// openRenameSelected opens the rename overlay for the selected session.
func (m *home) openRenameSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	m.renameTarget = selected
	m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName(), selected.Note(), false)
	m.state = stateRename
	return m, nil
}

// openQueue opens the pending-prompt management overlay for the selected session,
// listing its queued prompts so the user can cancel one before delivery. Unlike
// openQuickSend it needs no live pane (management is a pure in-memory read +
// cancel + persist), so paused and loading sessions are fair game; only an empty
// queue is a dead end worth refusing. The overlay acts on this instance even if
// the selection later moves (queueTarget), mirroring the rename flow.
func (m *home) openQueue() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if !selected.HasQueuedPrompt() {
		return m, m.handleInfoNotice(fmt.Sprintf("nothing queued for %q", selected.DisplayName()))
	}
	m.queueTarget = selected
	m.queueOverlay = overlay.NewQueueOverlay(selected.DisplayName())
	texts, headInFlight := selected.QueueView()
	m.queueOverlay.SetQueue(texts, headInFlight)
	m.state = stateQueue
	// tea.WindowSize re-runs layout so the overlay gets its responsive width.
	return m, tea.WindowSize()
}

// openCmdLog opens the command-log overlay: the tmux/git/gh subprocesses Atrium
// has run (#372). It is a global inspection surface, so it opens with or without a
// selection; the selected session's Title (the same label git records against) is
// passed so the overlay's per-session filter has a target.
func (m *home) openCmdLog() (tea.Model, tea.Cmd) {
	session := ""
	if sel := m.list.GetSelectedInstance(); sel != nil {
		session = sel.Title
	}
	m.cmdLogOverlay = overlay.NewCmdLogOverlay(session)
	m.state = stateCmdLog
	// tea.WindowSize re-runs layout so the overlay gets its responsive size.
	return m, tea.WindowSize()
}

// startAutoNameSelected kicks off background model-driven naming for the selected
// session. The model call and the diff it needs run in the Cmd so the UI stays
// responsive; only the instance and prompt are captured here.
func (m *home) startAutoNameSelected() (tea.Model, tea.Cmd) {
	// Guard order matters here: an in-flight generation is reported before a
	// still-loading session, so this handler can't fold the nil+Loading check
	// into selectedActionable() without changing which notice the user sees.
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if m.generatingName {
		return m, m.handleInfoNotice("already generating a name")
	}
	if selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}
	m.generatingName = true
	m.menu.SetState(ui.StateGeneratingName)
	m.recomputeLayout() // the progress bar now claims a row; shrink the panes to fit
	return m, runAutoNameCmd(m.ctx, selected, selected.Prompt())
}

// worktreeAction builds a deferred command that resolves selected's git worktree
// and runs fn against it, surfacing a lookup failure as an error message. It
// hoists the GetGitWorktree + error preamble the push/merge/create/open PR
// actions share. The read-only openPR runs it directly as a tea.Cmd (off the UI
// thread); the mutating PR actions run it via confirmWorktreeAction, which — once
// confirmed — dispatches it through beginAsyncAction, likewise off the UI thread.
func worktreeAction(selected *session.Instance, fn func(worktree *git.Worktree) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		worktree, err := selected.GetGitWorktree()
		if err != nil {
			return err
		}
		return fn(worktree)
	}
}

// confirmWorktreeAction shows a confirmation modal whose accepted action runs fn
// against selected's git worktree off the UI thread, behind the busyLabel progress
// row. The mutating PR actions (push, merge, create) share this confirm -> worktree
// -> message shape; the read-only openPR uses worktreeAction directly without a
// prompt. fn must be UI-thread-safe (touch only worktree/selected, return a
// message) since it runs in a goroutine.
func (m *home) confirmWorktreeAction(message, busyLabel string, selected *session.Instance, fn func(worktree *git.Worktree) tea.Msg) tea.Cmd {
	return m.confirmAsyncAction(message, busyLabel, worktreeAction(selected, fn))
}

// pushSelected confirms and pushes the selected session's branch.
func (m *home) pushSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	// A direct (non-git) session has nothing to push. Fail fast rather than prompting
	// for confirmation and only then erroring. (The menu also hides this action.)
	if selected.IsDirect() {
		return m, m.handleError(fmt.Errorf("push is not available for a direct (non-git) session"))
	}

	// Show confirmation modal; the push runs off the UI thread only on confirm.
	message := fmt.Sprintf("Push changes from session '%s'?", selected.DisplayName())
	return m, m.confirmWorktreeAction(message, "pushing…", selected, func(worktree *git.Worktree) tea.Msg {
		// Default commit message with timestamp.
		commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s", selected.DisplayName(), time.Now().Format(time.RFC822))
		if err := worktree.PushChanges(commitMsg, true); err != nil {
			return err
		}
		return pushedMsg{}
	})
}

// mergeSelected confirms and squash-merges the selected session's open PR, gated
// on the poll-maintained PR snapshot (no I/O on the UI thread).
func (m *home) mergeSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	// A direct (non-git) session has no branch and therefore no PR to merge.
	if selected.IsDirect() {
		return m, m.handleError(fmt.Errorf("merge is not available for a direct (non-git) session"))
	}
	// Decide from the poll-maintained PR snapshot (no I/O) — the same read-model
	// the list badges use. Never call PRStatus() here: it can block on a network
	// fetch, and this runs on the UI thread. A nil snapshot (never fetched / no
	// PR) reads as the zero value, whose MergeBlockedReason is "no open PR".
	var status git.PRStatus
	if pr := selected.GetPRStatus(); pr != nil {
		status = *pr
	}
	if reason := status.MergeBlockedReason(); reason != "" {
		return m, m.handleInfoNotice(reason)
	}
	number := status.Number
	message := fmt.Sprintf("Merge PR #%d from '%s' (squash)?", number, selected.DisplayName())
	if status.CI == git.CIPending {
		message += " CI is still running."
	}
	// Defer the worktree lookup and network merge into the confirm action, run off
	// the UI thread only if the user confirms.
	return m, m.confirmWorktreeAction(message, fmt.Sprintf("merging PR #%d…", number), selected, func(worktree *git.Worktree) tea.Msg {
		if err := worktree.MergePR(); err != nil {
			return err
		}
		return prMergedMsg{number: number, instance: selected}
	})
}

// createPRForSelected confirms and opens a PR for the selected session, gated on
// the poll-maintained PR snapshot.
func (m *home) createPRForSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	// A paused session has had its worktree freed, but CreatePR runs gh from that
	// worktree path (where --fill reads the branch's commits). Merge can act on a
	// paused session because it runs gh from the always-present repo root; create
	// cannot, so block it with a notice rather than letting the deferred action
	// fail with a raw chdir error. Resume rebuilds the worktree.
	if selected.Paused() {
		return m, m.handleInfoNotice("resume the session first — pausing freed its worktree, so there's nothing to create a PR from")
	}
	// A direct (non-git) session has no branch and therefore no PR to open.
	if selected.IsDirect() {
		return m, m.handleError(fmt.Errorf("create PR is not available for a direct (non-git) session"))
	}
	// Decide from the poll-maintained PR snapshot (no I/O) — the same read-model
	// the list badges and hint bar use. Never call PRStatus() here: it can block
	// on a network fetch, and this runs on the UI thread. A nil snapshot reads as
	// the zero value, whose CreateBlockedReason is "not pushed yet".
	var status git.PRStatus
	if pr := selected.GetPRStatus(); pr != nil {
		status = *pr
	}
	if reason := status.CreateBlockedReason(); reason != "" {
		return m, m.handleInfoNotice(reason)
	}
	// The draft default is configurable; capture it for the deferred action.
	draft := m.appConfig.GetPRCreateDraft()
	adjective := "ready-for-review"
	if draft {
		adjective = "draft"
	}
	message := fmt.Sprintf("Create %s PR from '%s'?", adjective, selected.DisplayName())
	// Defer the worktree lookup and network create into the confirm action, run off
	// the UI thread only if the user confirms.
	return m, m.confirmWorktreeAction(message, "creating PR…", selected, func(worktree *git.Worktree) tea.Msg {
		number, err := worktree.CreatePR(draft)
		if err != nil {
			return err
		}
		return prCreatedMsg{number: number}
	})
}

// openPRForSelected launches the browser at the selected session's PR. Viewing is
// permissive where merging is strict: any existing PR opens.
func (m *home) openPRForSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	// A direct (non-git) session has no branch and therefore no PR.
	if selected.IsDirect() {
		return m, m.handleInfoNotice("no PR for a direct (non-git) session")
	}
	// Read the poll-maintained snapshot (no I/O), like the merge handler. The
	// guard is the looser HasPR rather than MergeBlockedReason: viewing is
	// permissive where merging is strict, so drafts, CI-pending, conflicting and
	// already-merged PRs all open.
	var status git.PRStatus
	if pr := selected.GetPRStatus(); pr != nil {
		status = *pr
	}
	if !status.HasPR {
		return m, m.handleInfoNotice("no PR for this session yet")
	}
	number := status.Number
	// Defer the worktree lookup + browser launch into a tea.Cmd so a slow gh
	// never blocks the UI thread. No confirmation: opening a browser is read-only.
	return m, worktreeAction(selected, func(worktree *git.Worktree) tea.Msg {
		if err := worktree.OpenPRURL(); err != nil {
			return err
		}
		return prOpenedMsg{number: number}
	})
}

// pauseSelected commits the selected session's changes, frees its worktree, and
// then offers the rename overlay focused on the note field so "park this, jot why"
// is one motion. Pause (git commit + worktree removal + tmux detach) runs off the
// UI thread behind a "pausing…" progress row; the rename overlay opens when
// pauseDoneMsg lands.
func (m *home) pauseSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}

	// A direct (non-git) session has no worktree to free and runs in the user's
	// real directory, so pausing it would only detach a still-running agent.
	// Warn instead of pausing. (The menu also hides this action for direct sessions.)
	if selected.IsDirect() {
		return m, m.handleError(fmt.Errorf("pause is not available for a direct (non-git) session; it runs in place with no worktree to free"))
	}

	// Pause off the UI thread: Pause() commits any dirty work and removes the
	// worktree, keeping the branch to check out elsewhere. The completion handler
	// (pauseDoneMsg) tears down the terminal, persists, and opens the rename overlay.
	return m, m.beginAsyncAction("pausing…", func() tea.Msg {
		if err := selected.Pause(); err != nil {
			return pauseDoneMsg{instance: selected, err: err}
		}
		return pauseDoneMsg{instance: selected}
	})
}

// resumeSelectedKey resumes the selected paused session (rebuilding its worktree).
func (m *home) resumeSelectedKey() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	if !selected.Paused() {
		return m, m.handleInfoNotice("session is already running — only paused sessions resume")
	}
	return m, m.resumeSelected(selected)
}

// attachSelected attaches to the selected session (or its terminal tab) via
// tea.Exec. KeyAttachToggle (ctrl+q) mirrors the in-session detach key, making it
// a symmetric attach/detach toggle that funnels through the same guards as enter.
func (m *home) attachSelected() (tea.Model, tea.Cmd) {
	if m.list.NumInstances() == 0 {
		return m, nil
	}
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Paused() {
		return m, m.handleInfoNotice("session is paused — press r to resume")
	}
	if selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}
	if !selected.TmuxAlive() {
		// Don't say "resume it": r refuses a non-paused session (resumeSelectedKey).
		// A dead terminal is parked as paused within a couple of poll ticks (the
		// lost-session recovery), after which r works; until then kill is the action.
		return m, m.handleInfoNotice("session terminal has exited — it will be parked as paused shortly (then press r), or press k to kill")
	}
	// Attach to the session (or its terminal tab) via tea.Exec, which hands the
	// terminal to tmux and repaints on detach; the hint bar carries the ctrl-q
	// detach reminder. Post-detach handling lands in the attachFinishedMsg handler.
	if m.tabbedWindow.IsInTerminalTab() {
		// The terminal tab has no in-session kill key, so no kill target.
		return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
	}
	// Attach the captured selection directly (not m.list.Attach, which re-reads the
	// selected index when the deferred command runs) so the attach target and the
	// killTarget can't diverge. Matches the double-click and sibling/auto-open paths.
	return m, m.attachExec(selected.Attach, selected)
}

// promptHistoryTexts projects the persisted history entries to their reuse texts,
// most-recent-first (the order they are stored in).
func promptHistoryTexts(entries []config.PromptHistoryEntry) []string {
	texts := make([]string, len(entries))
	for i, e := range entries {
		texts[i] = e.Text
	}
	return texts
}

// historyOverlayWidth sizes the prompt-history picker to ~60% of the terminal,
// capped at 80 — the same responsive box the queue overlay uses.
func historyOverlayWidth(termWidth int) int {
	w := int(float32(termWidth) * 0.6)
	if w > 80 {
		w = 80
	}
	return w
}

// recordPrompt appends a submitted prompt to the reuse history when recording is
// enabled (config), swallowing a persist error into the log — history is a
// convenience and must never fail a submit.
func (m *home) recordPrompt(text string) {
	if !m.appConfig.GetRecordPromptHistory() {
		return
	}
	if err := m.appState.AddPromptHistory(text); err != nil {
		log.ErrorLog.Printf("failed to record prompt history: %v", err)
	}
}

// handleHistoryState routes keys to the prompt-history picker. Selecting a row
// inserts its text into the prompt field being composed (never submits) and
// returns to the compose overlay; x empties the history in place; esc cancels
// back to the compose overlay.
func (m *home) handleHistoryState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.promptHistoryOverlay.HandleKeyPress(msg)
	if m.promptHistoryOverlay.ClearRequested() {
		if err := m.appState.ClearPromptHistory(); err != nil {
			log.ErrorLog.Printf("failed to clear prompt history: %v", err)
		}
		m.promptHistoryOverlay.SetItems(nil)
		return m, nil
	}
	if !shouldClose {
		return m, nil
	}
	if m.promptHistoryOverlay.Selected() && m.textInputOverlay != nil {
		m.textInputOverlay.SetPrompt(m.promptHistoryOverlay.SelectedText())
	}
	m.promptHistoryOverlay = nil
	m.state = statePrompt
	return m, nil
}
