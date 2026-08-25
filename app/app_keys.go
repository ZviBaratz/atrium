package app

// Per-state overlay key handlers and per-action key handlers extracted from
// handleKeyPress (app_update.go). handleKeyPress stays a thin dispatcher: the
// surfaceSpecs keys entries route each overlay state to its handleXState
// method here, and the substantial key-action cases delegate to the verb-named
// methods here. Trivial one-line cases (navigation, tab switching) remain
// inline in the switch.

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
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

// filterFoldNotice explains a fold refused because a filter is live. Shared by the
// fold keys and the header click so the two cannot drift apart — they are the same
// gesture, and a filter silences both. Phrased as the standing rule it is, naming its
// ladder ("folding") and the key that lifts it, like the sibling reorder refusals (#346).
const filterFoldNotice = "folding is off while a filter is live (esc to clear)"

// pressToResume is the "how do I get this back" clause every paused refusal ends
// with, naming the resume key through the registry rather than as a literal.
//
// It is a function and not a const for that reason: a const would be correct
// only until someone rebinds resume, and then a dozen notices would be telling
// them to press a key that does something else. That is not hypothetical — two
// of these notices shipped saying "press k to kill", where k moves the
// selection; the kill key is ctrl-x.
func pressToResume() string {
	return "press " + keys.LabelOf(keys.KeyResume) + " to resume"
}

// pausedResumeNotice is the standing refusal for a per-session action pressed on
// a paused session. tail, when set, says what the resume would unblock ("before
// sending"); the bare form is the general case.
func pausedResumeNotice(tail string) string {
	notice := "session is paused — " + pressToResume()
	if tail != "" {
		notice += " " + tail
	}
	return notice
}

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

// --- Overlay-state key handlers (routed from surfaceSpecs' keys entries) ------

// handlePromptState routes a key to the text-input overlay (new-session form or
// quick-send compose box) and handles submit/cancel/retarget/debounce.
func (m *home) handlePromptState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// #383 diff comment: this composer was opened from the diff line cursor. Route
	// it to its own submit/cancel so a queued comment (or a cancel) returns to
	// comment mode rather than the list, and never runs the quick-send/create paths.
	if m.composingDiffComment {
		return m.handleDiffCommentComposer(msg)
	}
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
		return m, tea.Sequence(tea.RequestWindowSize, m.instanceChanged(), notice)
	}

	// A confirmed double-tap Ctrl+R rebuilds the form fresh and drops any draft.
	if m.textInputOverlay.ClearRequested() {
		m.stashedDraft = nil
		m.clearPersistedDraft()
		return m, m.openCreateForm(true)
	}

	return m.afterPromptEdit(prevTitle, branchFilterChanged)
}

// afterPromptEdit runs the follow-up work an edit to the create form implies:
// re-scoping to a newly picked repo, the debounced branch search, and the title's
// duplicate verdict. It is shared by the key and paste paths because the follow-up
// is owed to the *edit*, not to how the characters arrived — a pasted title needs
// its duplicate check as much as a typed one does.
func (m *home) afterPromptEdit(prevTitle string, branchFilterChanged bool) (tea.Model, tea.Cmd) {
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

	// An edit to the title refreshes the inline duplicate verdict (in-memory,
	// instant) and schedules the async branch-existence check.
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
//   - confirmAction with a busy label → run it off the UI thread via beginAsyncAction
//     (a real tea.Cmd goroutine) behind a "busy" progress row. These closures are
//     UI-thread-safe (they touch only their captured instance/worktree); their model
//     mutation happens in the completion handler.
//   - confirmAction with instantAction → run it inline on the main loop. The
//     remaining users return a message and nothing else; anything that touches the
//     list or storage from a goroutine would race Update.
//
// Either way only the resulting message flows back through the runtime, so a
// returned error reaches the error box.
func (m *home) handleConfirmState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// A dialog whose copy promises the settings key opens the setting it named. It is a
	// cancel: nothing staged is run, and the stashed create form stays restorable exactly as
	// declining leaves it. Armed per-dialog (confirmOverCap), so the key stays inert in every
	// other confirmation.
	//
	// Matched through the registry rather than as ',' so that the key the dialog's copy
	// promises and the key it honors are the same one — the copy is generated from the same
	// binding (settingNotice), so a literal here would let the two drift apart.
	//
	// The dialog's own keys win the tie. Reading the settings key from the registry means
	// an override can land it on y or n, which nothing reserves, and this branch runs
	// before the overlay sees the press: rebinding settings onto y made the advertised
	// "Create it anyway? y/n" open the settings panel and silently discard the staged
	// session. Deferring to Answers keeps the dialog answering for the keys it prints.
	settingsKey := keys.PrimaryKey(keys.KeySettings)
	if key := m.pendingConfirmSettingKey; key != "" && settingsKey != "" &&
		msg.String() == settingsKey && !m.confirmationOverlay.Answers(settingsKey) {
		m.pendingConfirmSettingKey = ""
		m.pendingConfirmAction = nil
		m.pendingConfirmBusyLabel = ""
		m.pendingConfirmArm = nil
		m.pendingConfirmDecline = nil
		m.confirmationOverlay = nil
		return m, m.openSettingsAt(key)
	}
	shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
	if shouldClose {
		confirmed := m.confirmationOverlay.Confirmed
		action := m.pendingConfirmAction
		busyLabel := m.pendingConfirmBusyLabel
		arm := m.pendingConfirmArm
		decline := m.pendingConfirmDecline
		m.state = stateDefault
		m.confirmationOverlay = nil
		m.pendingConfirmAction = nil
		m.pendingConfirmBusyLabel = ""
		m.pendingConfirmArm = nil
		m.pendingConfirmDecline = nil
		m.pendingConfirmSettingKey = ""
		if confirmed && action != nil {
			// Here, on the update thread, is the only place a staged action's
			// bookkeeping may be applied: a declined dialog returns below having
			// touched nothing — unless it armed a decline action of its own
			// (armOnDecline), which for every ordinary dialog is nil.
			if arm != nil {
				arm()
			}
			if busyLabel != "" {
				return m, m.beginAsyncAction(busyLabel, action)
			}
			resultMsg := action()
			return m, func() tea.Msg { return resultMsg }
		}
		if !confirmed && decline != nil {
			resultMsg := decline()
			return m, func() tea.Msg { return resultMsg }
		}
		return m, nil
	}
	return m, nil
}

// handleRenameState routes a key to the rename overlay and, on submit, applies the
// new display name / note to the instance the overlay was opened for.
func (m *home) handleRenameState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
			// Validate here, on the main thread — the collision check reads the whole
			// instance list. Only the I/O half (tmux rename, git branch rename, and a
			// worktree directory move) goes to the goroutine, behind its label.
			if err := m.validateDeepRename(target, value); err != nil {
				// The rename was rejected before anything changed (e.g. a name
				// collision). Reopen the dialog pre-filled with the attempted name
				// and note so neither is lost — nothing is persisted until a rename
				// succeeds.
				m.renameTarget = target
				m.renameOverlay = overlay.NewRenameOverlay(value, note, false)
				m.state = stateRename
				return m, m.handleError(err)
			}
			// The note lands with the rename, in renameDoneMsg — the deep path
			// persists once, after the I/O succeeds.
			return m, m.beginAsyncAction(fmt.Sprintf("renaming to '%s'…", value),
				renameIOCmd(target, value, note))
		}
		target.SetDisplayName(value)
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
func (m *home) handleQueueState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
func (m *home) handleCmdLogState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
func (m *home) handleSettingsState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	closed, changedKey := m.settingsOverlay.HandleKeyPress(msg)
	var cmds []tea.Cmd
	if changedKey != "" {
		if cmd := m.applySettingChange(changedKey); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.settingsOverlay.TakeProfileDetect() {
		// Agent detection spawns a login shell (config.GetClaudeCommand), so it runs as a
		// command rather than inline: a synchronous probe would freeze the update loop — and
		// with it every session's poll — for up to its ten-second timeout.
		cmds = append(cmds, m.detectProfilesCmd())
	}
	if closed {
		// Read the handoff BEFORE dropping the overlay: a rail entry that owns no rows can ask
		// home to open a sibling surface in the panel's place, and the request lives on the
		// overlay we are about to discard.
		handoff := m.settingsOverlay.Handoff()
		rail := m.settingsOverlay.RailIndex()
		m.settingsRail = &rail
		m.settingsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row
		if handoff == overlay.HandoffAccounts {
			// openAccounts sets the state and returns its own WindowSize, so the default one
			// below is not also needed.
			return m, tea.Batch(append(cmds, m.openAccounts())...)
		}
		cmds = append(cmds, tea.RequestWindowSize)
	}
	return m, tea.Batch(cmds...)
}

// handleFilterState routes a key while the list filter is being edited. The list
// holds the query (single source of truth); printable keys extend it, so j/k
// cannot be reserved as commit keys here.
func (m *home) handleFilterState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		if msg.Text != "" {
			m.list.SetFilter(m.list.FilterQuery() + msg.Text)
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
func (m *home) handleMultiSelectState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.exitVisualMode()
		return m, m.instanceChanged()
	case "x":
		// Plain x kills the marked set (the bar advertises "x"); ctrl+x (the global
		// kill chord, KeyKill below) does the same. Each hands its own key to the
		// confirmation so the double-tap is the key the user actually pressed: the
		// literal here because no registry Entry owns this "x", msg.String() below
		// because the arm holds the keystroke and its siblings all forward theirs.
		//
		// Below it can only ever BE the kill key: "kill" is in attachedLayerActions,
		// which keys.Apply refuses to bind to more than one key, because the attach
		// layer forwards a single raw byte and a second alias would type itself into
		// the agent's pane. So msg.String() and keys.KillKey() cannot diverge there —
		// it is uniformity with the siblings, not a fix. Don't write the multi-key
		// drift test for it; the binding it would need is rejected before it applies
		// (keys/override.go, and the guard over that refusal in override_test.go).
		return m, m.killMarked("x")
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
		return m, m.pauseMarked(msg.String())
	case keys.KeyResume:
		return m, m.resumeMarked(msg.String())
	case keys.KeyKill:
		return m, m.killMarked(msg.String())
	default:
		return m, nil
	}
}

// --- Per-action key handlers (delegated from handleKeyPress's switch) ----------

// foldKey runs the three repo-fold keys (← / → / Z) behind one contract: fold, persist
// the set, or explain why the screen did not change (#399). The two guards below run
// before the fold because both describe the *list*, not the key, so they answer all
// three keys identically — and both outrank the per-key refusal, which would otherwise
// promise a state change that the guard has already ruled out (#346).
func (m *home) foldKey(name keys.KeyName) (tea.Model, tea.Cmd) {
	// A lone repo group renders no fold marker, so folding is not a thing the user can
	// see happen or undo. Silent on purpose: a notice here would name a state no key
	// reaches, and unlike the filter below, nothing the user can clear lifts it.
	if !m.list.HasMultipleGroups() {
		return m, nil
	}
	// While a filter is live it is the sole visibility gate and overrides the fold in
	// the render (see ui.List.isHidden), so a folded group is on screen *expanded*.
	// Folding under it would rewrite the persisted set with the list standing still
	// (#339), and "already collapsed" would describe something the user cannot see —
	// the case hiddenNeighborNotice names the filter for. Refuse and name it here too.
	if m.list.Filtering() {
		return m, m.handleInfoNotice(filterFoldNotice)
	}
	var acted bool
	var already string
	switch name {
	case keys.KeyCollapse:
		acted, already = m.list.Collapse(), "repo group is already collapsed"
	case keys.KeyExpand:
		acted, already = m.list.Expand(), "repo group is already expanded"
	case keys.KeyCollapseAll:
		// Z always flips (fold every group unless all are folded, then unfold), so past
		// the guards above it has no already-in-that-state refusal to report.
		acted = m.list.ToggleCollapseAll()
	}
	if !acted {
		if already == "" {
			return m, nil
		}
		// The scope noun separates this from Z, which shares the fold vocabulary and is
		// one keypress away — "already collapsed" alone reads as a claim about the list.
		return m, m.handleInfoNotice(already)
	}
	if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
		return m, m.handleError(err)
	}
	return m, m.instanceChanged()
}

// openQuickSend opens a compose box to fire an ad-hoc message at the selected
// running session without attaching. Only meaningful when the agent is up and
// accepting input; other states explain the guard instead of swallowing the key.
func (m *home) openQuickSend() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Paused() {
		return m, m.handleInfoNotice(pausedResumeNotice("before sending"))
	}
	if !selected.Started() || selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}
	m.state = statePrompt
	m.textInputOverlay = overlay.NewQuickSendOverlay("Send to " + selected.DisplayName())
	return m, tea.RequestWindowSize
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
		// The one branch here that must leave the update thread. AcceptSuggestion
		// captures the pane, sends Right, then POLLS the pane every 20ms for up to a
		// second waiting for the ghost text to commit — up to ~52 tmux subprocesses
		// and a full second of frozen UI, from a bare keypress with no dialog in
		// front of it. It was the worst per-keystroke stall in the app (#380).
		// The ApprovePrompt branch above stays inline: it is one send-keys.
		inst := selected
		return m, m.beginAsyncAction("accepting suggestion…", func() tea.Msg {
			accepted, err := inst.AcceptSuggestion()
			return suggestionAcceptedMsg{instance: inst, accepted: accepted, err: err}
		})
	}
	return m, m.handleInfoNotice("agent isn't waiting on a prompt — nothing to approve or accept")
}

// copySelectedBranch yanks the selected session's branch name to the system
// clipboard. A copy has no failure outcome — the OSC 52 leg is dispatched
// unconditionally (see copyToClipboard) — so the hint row carries either the
// confirmation or the "no branch yet" guard, never an error.
func (m *home) copySelectedBranch() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Branch() == "" {
		return m, m.handleInfoNotice("no branch to copy yet")
	}
	return m, tea.Batch(
		m.copyToClipboard(selected.Branch()),
		m.handleInfoNotice(fmt.Sprintf("branch '%s' copied", selected.Branch())))
}

// copyPaneContent copies the active tab's content to the clipboard, unstyled.
//
// This is the answer to the standing complaint about full-screen TUIs: dragging a
// mouse selection across the alt-screen takes the borders, the gutter and the
// neighbouring pane's text with it, so nothing copied that way is worth pasting.
// The clipboard transport is already SSH-safe (OSC 52, #395); what was missing was
// content clean enough to send through it.
func (m *home) copyPaneContent() (tea.Model, tea.Cmd) {
	text, what, ok := m.tabbedWindow.CopyableContent(m.list.GetSelectedInstance())
	if !ok {
		return m, m.handleInfoNotice("nothing to copy from this tab yet")
	}
	// The line count is the receipt: a clipboard write is otherwise invisible, and
	// "copied" alone cannot distinguish the whole diff from one selected hunk.
	lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	return m, tea.Batch(
		m.copyToClipboard(text),
		m.handleInfoNotice(fmt.Sprintf("%s copied (%d line%s)", what, lines, plural(lines))))
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

// toggleMuteSelected flips the selected session's notification mute and persists it.
// A muted session never notifies (see maybeNotify), and the mute survives a restart.
// It uses the raw selection (not selectedActionable): muting is a lightweight state
// toggle with no readiness dependency, so it works even on a still-starting session.
// The row glyph is subtle, so a transient toast confirms which way it flipped.
func (m *home) toggleMuteSelected() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	muted := !selected.Muted()
	selected.SetMuted(muted)
	if err := m.persistInstances(); err != nil {
		return m, m.handleError(err)
	}
	verb := "unmuted"
	if muted {
		verb = "muted"
	}
	return m, m.handleInfoNotice(fmt.Sprintf("%s %s", verb, selected.DisplayName()))
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
	return m, tea.RequestWindowSize
}

// openCmdLog opens the command-log overlay: the tmux/git/gh subprocesses Atrium
// has run (#372). It is a global inspection surface, so it opens with or without a
// selection; the selected session's Title (the same label git records against) is
// passed so the overlay's per-session filter has a target.
func (m *home) openCmdLog() (tea.Model, tea.Cmd) {
	session := ""
	if sel := m.list.GetSelectedInstance(); sel != nil {
		session = sel.Title()
	}
	m.cmdLogOverlay = overlay.NewCmdLogOverlay(session)
	m.state = stateCmdLog
	// tea.WindowSize re-runs layout so the overlay gets its responsive size.
	return m, tea.RequestWindowSize
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
	// Background, not modal: an LLM name generation takes seconds, and freezing
	// every mutating key for that long would be a far worse trade than the double-A
	// press m.generatingName already refuses. The row still names it (#380).
	return m, m.beginBackgroundAction("generating name…", runAutoNameCmd(m.ctx, selected, selected.Prompt()))
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
func (m *home) confirmWorktreeAction(message string, label busyLabel, selected *session.Instance, fn func(worktree *git.Worktree) tea.Msg) tea.Cmd {
	return m.confirmAction(message, label, worktreeAction(selected, fn))
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
	// A paused session has had its worktree freed, and PushChanges runs git from
	// that path (commit first, then push). Same reasoning — and the same refusal —
	// as createPRForSelected: merge and open-PR survive a pause because gh runs
	// from the repo root, push and create do not. Without this the user confirms a
	// dialog and gets a raw "cannot change to directory" from git.
	//
	// pausedHintKeys drops P from a paused session's cues, which is why this went
	// unnoticed: a hidden hint is not a guard, and the command palette (#374)
	// reaches every action regardless of which cues are showing.
	if selected.Paused() {
		return m, m.handleInfoNotice("resume the session first — pausing freed its worktree, so there's nothing to push from")
	}

	// Show confirmation modal; the push runs off the UI thread only on confirm.
	//
	// The clause names the two things confirming does that the question does not, and
	// that no row on screen shows: the commit (only when there is one to make) and the
	// browser tab. Read off the cached diff stats, never a fresh git call — this is the
	// UI thread, and GetDiffStats is the same poll-maintained snapshot killDataWarning
	// keys off. That snapshot lags: its dirty flag is cached for dirtyCacheTTL on top
	// of the poll tick, so an edit made in the pane a moment before P can still read
	// clean, exactly as a nil snapshot (never polled) does. Both err the same way, and
	// that direction is the deliberate one: a clause promising a commit that does not
	// happen is worse than a missing one, and the commit itself is unconditional in
	// PushChanges either way. What the user loses to a stale read is the warning, not
	// the work.
	dirty := false
	if stats := selected.GetDiffStats(); stats != nil {
		dirty = stats.Dirty
	}
	message := fmt.Sprintf("Push changes from session '%s'?%s",
		selected.DisplayName(), pushConsequenceClause(dirty))
	confirm := m.confirmWorktreeAction(message, "pushing…", selected, func(worktree *git.Worktree) tea.Msg {
		// Default commit message with timestamp.
		commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s", selected.DisplayName(), time.Now().Format(time.RFC822))
		if err := worktree.PushChanges(commitMsg, true); err != nil {
			return err
		}
		return pushedMsg{}
	})
	m.confirmationOverlay.SetConfirmLabel("push")
	m.armDoubleTap(keys.PrimaryKey(keys.KeySubmit))
	return m, confirm
}

// pushConsequenceClause is the parenthetical on the push confirmation: what
// confirming does beyond pushing, and that the session row cannot show (#469).
//
// Two effects, one of them conditional. PushChanges commits any uncommitted work
// first, under a generated message the user never sees and cannot edit — a no-op on a
// clean worktree, so the surprise lands exactly on the user who had work in progress,
// and the clause appears exactly there. Then it opens the branch in a browser, which
// happens every time and is named every time.
//
// It borrows killDataWarning's data and its question-first-with-a-parenthetical shape
// — the confirmation-voice rule in app_feedback.go, and #469's preferred option of the
// three it offered — but not its risk-only rule: killDataWarning's whole clause
// vanishes when nothing is at risk, and this one never can, because the browser tab
// opens on every push. Only the commit half is conditional.
//
// It says "your uncommitted work" rather than naming the commit message: the message
// is `[atrium] update from '<name>' on <time>`, and quoting it would cost two rendered
// lines to tell the user something they cannot act on from inside the dialog.
func pushConsequenceClause(dirty bool) string {
	if dirty {
		return " (commits your uncommitted work first, then opens the branch in your browser)"
	}
	return " (opens the branch in your browser)"
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
	// The squash is stated in the question itself, freeing the parenthetical for the
	// caveat that trailed it as a second sentence: unmerged CI is exactly the thing
	// the user cannot see from here, so it belongs in the kill dialog's shape
	// (see the confirmation-voice rule in app_feedback.go).
	message := fmt.Sprintf("Merge PR #%d from '%s' as a squash merge?", number, selected.DisplayName())
	if status.CI == git.CIPending {
		message += " (CI is still running)"
	}
	// Defer the worktree lookup and network merge into the confirm action, run off
	// the UI thread only if the user confirms.
	confirm := m.confirmWorktreeAction(message, busyLabel(fmt.Sprintf("merging PR #%d…", number)), selected, func(worktree *git.Worktree) tea.Msg {
		if err := worktree.MergePR(); err != nil {
			return err
		}
		return prMergedMsg{number: number, instance: selected}
	})
	m.confirmationOverlay.SetConfirmLabel(fmt.Sprintf("merge PR #%d", number))
	m.armDoubleTap(keys.PrimaryKey(keys.KeyMerge))
	return m, confirm
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
	confirm := m.confirmWorktreeAction(message, "creating PR…", selected, func(worktree *git.Worktree) tea.Msg {
		number, err := worktree.CreatePR(draft)
		if err != nil {
			return err
		}
		return prCreatedMsg{number: number}
	})
	// The label names only what this action does: CreateBlockedReason above already
	// refused an unpushed branch, so the PR is all that gets created here (#399).
	m.confirmationOverlay.SetConfirmLabel("create the PR")
	m.armDoubleTap(keys.PrimaryKey(keys.KeyCreate))
	return m, confirm
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
	// Background rather than modal for the same reason — `gh pr view --web` is a
	// network round trip that can stall on auth, so it earns a name on the row, but
	// freezing every mutating key for a read-only browser launch would be a heavier
	// promise than the work deserves.
	return m, m.beginBackgroundAction(fmt.Sprintf("opening PR #%d…", number),
		worktreeAction(selected, func(worktree *git.Worktree) tea.Msg {
			if err := worktree.OpenPRURL(); err != nil {
				return err
			}
			return prOpenedMsg{number: number}
		}))
}

// pauseSelected stops the selected session's agent, commits its changes, frees its
// worktree, and then offers the rename overlay focused on the note field so "park
// this, jot why" is one motion. Pause (git commit + tmux close + worktree removal)
// runs off the UI thread behind a "pausing…" progress row; the rename overlay opens
// when pauseDoneMsg lands.
func (m *home) pauseSelected() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}

	// A direct (non-git) session has no worktree to free and runs in the user's
	// real directory, so pausing it would stop the agent and reclaim nothing.
	// Warn instead of pausing. (The menu also hides this action for direct sessions.)
	if selected.IsDirect() {
		return m, m.handleError(fmt.Errorf("pause is not available for a direct (non-git) session; it runs in place with no worktree to free"))
	}

	// Pausing what is already paused re-runs commit + worktree removal against a
	// worktree the last pause already freed. The hint bar drops p from a paused
	// session's cues, which is why this went unnoticed — a hidden hint is not a
	// guard, and the command palette (#374) reaches every action regardless of
	// which cues are showing. Mirrors resume's "already running" refusal.
	if selected.Paused() {
		return m, m.handleInfoNotice("session is already paused — " + pressToResume())
	}

	// Pause off the UI thread: Pause() commits any dirty work and removes the
	// worktree, keeping the branch to check out elsewhere. The completion handler
	// (pauseDoneMsg) tears down the terminal, persists, and opens the rename overlay.
	return m, m.beginAsyncAction("pausing…", func() tea.Msg {
		err := selected.Pause()
		// Sampled here, not in the handler: the shell to reap is the one that was
		// running in the directory this call removed, and re-deriving it later can
		// read a directory some other action has since put back. asyncActionDoneMsg
		// clears actionInFlight one message BEFORE pauseDoneMsg reaches the handler,
		// and messages are processed in between — the same gap #701 needed the
		// retiring mark for — so a resume can land in it and re-create this path
		// around a shell that still holds the old inode (#707).
		return pauseDoneMsg{instance: selected, err: err, worktreeGone: selected.WorkingDirGone()}
	})
}

// resumeSelectedKey resumes the selected paused session, rebuilding its worktree if
// the pause removed one — a parked direct session never had one and a commit-failure
// pause kept its own, and this key reaches both (it gates on Paused() alone, which is
// also why the batch scope can keep them: `r` is not the only way back). Same
// qualification as resumeConfirmMessage's copy, for the same reason.
//
// One resume away from the host budget it asks first (resumeSelected); every resume
// that fits is immediate, as it always was.
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
		return m, m.handleInfoNotice(pausedResumeNotice(""))
	}
	if selected.GetStatus() == session.Loading {
		return m, m.handleInfoNotice(stillStartingNotice)
	}
	if !selected.TmuxAlive() {
		// Don't say "resume it": r refuses a non-paused session (resumeSelectedKey).
		// A dead terminal is parked as paused within a couple of poll ticks (the
		// lost-session recovery), after which r works; until then kill is the action.
		return m, m.handleInfoNotice(fmt.Sprintf(
			"session terminal has exited — it will be parked as paused shortly (then press %s), or press %s to kill",
			keys.LabelOf(keys.KeyResume), keys.LabelOf(keys.KeyKill)))
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

// historyOverlayWidth sizes the prompt-history picker — and the queue overlay,
// whose size closure in surfaceSpecs shares the box — to ~60% of the terminal,
// capped at 80. The picker's opener and both resize closures all call it, so
// the widths cannot drift apart.
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
func (m *home) handleHistoryState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
