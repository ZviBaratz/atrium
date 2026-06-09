package app

// Top-level event and key dispatch for the home model.

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// prMergedMsg is returned by a confirmed merge action to report success back
// through the runtime, carrying the merged PR number for the acknowledgment.
type prMergedMsg struct{ number int }

// prCreatedMsg is returned by a confirmed create action to report success back
// through the runtime, carrying the new PR number (0 if gh's output had none).
type prCreatedMsg struct{ number int }

// prOpenedMsg is returned by the open-PR action once gh has launched the browser,
// carrying the PR number for the acknowledgment. Unlike a merge it changes no
// state, so its handler only shows a notice.
type prOpenedMsg struct{ number int }

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		if msg.gen == m.noticeGen {
			if m.menu != nil {
				m.menu.ClearNotice()
			}
			m.errBox.Clear()
			m.recomputeLayout() // reclaim the error row; panes grow back by one
		}
	case previewTickMsg:
		m.markSeenAfterDwell(time.Now())
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
	case autoNameDoneMsg:
		m.generatingName = false
		if msg.err != nil {
			// The progress row goes away and we return to plain navigation; surface the
			// failure and leave the name untouched rather than applying a junk fallback.
			m.menu.SetState(ui.StateDefault)
			m.recomputeLayout() // the progress bar gave up its row; panes reclaim it
			return m, m.handleError(msg.err)
		}
		// Offer the generated name through the existing rename overlay so the user
		// can confirm or edit it before it commits.
		m.renameTarget = msg.instance
		m.renameOverlay = overlay.NewRenameOverlay(msg.name)
		m.state = stateRename
		m.recomputeLayout() // the progress bar gave up its row; the overlay self-documents
		return m, nil
	case metadataUpdateDoneMsg:
		if recoverLostInstances(msg.results, m.lostStrikes) {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				log.ErrorLog.Printf("failed to persist recovered sessions: %v", err)
			}
		}
		for _, r := range msg.results {
			// Skip instances that were paused while metadata was being computed, or
			// that were just recovered to Paused above because their session died.
			if r.sessionLost || r.instance.Paused() {
				continue
			}
			applyPaneState(r.instance, r.state)
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
			r.instance.SetPRStatus(r.prStatus)
		}
		m.pushSessionContexts()
		cmds := deliverReadyPrompts(msg.results)
		cmds = append(cmds, tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()))
		return m, tea.Batch(cmds...)
	case instancePolledMsg:
		// An off-cadence single-instance refresh (selection change / detach). Apply the
		// state but do NOT reschedule the metadata tick — that chain is owned by
		// metadataUpdateDoneMsg above; touching it here would spawn a second tick loop.
		if msg.instance.GetStatus() != session.Paused {
			applyPaneState(msg.instance, msg.state)
		}
		return m, nil
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		// Mouse wheel is routed by what it hovers, only in the default state
		// (overlays own the screen otherwise, mirroring the left-click gate
		// below). Over the session list it moves the selection like ↑/↓; over
		// the right tabbed pane it scrolls the active tab; anywhere else (menu /
		// hint bar / error rows) it is ignored. Zones are resolved against the
		// frame scanned in View(); before the first scan both InBounds checks
		// return false, so the wheel does nothing.
		if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
			if m.state != stateDefault {
				return m, nil
			}
			// Over the list: move the selection, regardless of the selected
			// instance's state (paused / nil), exactly like the keyboard paths.
			if m.list.InPanelBounds(msg) {
				if msg.Button == tea.MouseButtonWheelUp {
					m.list.Up()
				} else {
					m.list.Down()
				}
				return m, m.instanceChanged()
			}
			// Over the right tabbed pane: scroll the active tab. A nil or
			// paused selection has nothing to scroll.
			if m.tabbedWindow.InBounds(msg) {
				selected := m.list.GetSelectedInstance()
				if selected == nil || selected.Paused() {
					return m, nil
				}
				if msg.Button == tea.MouseButtonWheelUp {
					m.tabbedWindow.ScrollUp()
				} else {
					m.tabbedWindow.ScrollDown()
				}
				return m, nil
			}
			return m, nil
		}
		// Left-click selects a session row, switches the active tab, or (on a quick
		// second click of the same row) attaches. Only in the default state — when
		// an overlay is up the rows behind it still have recorded bounds, so a click
		// there must be ignored. Click regions are resolved against the frame
		// scanned in View().
		if msg.Button == tea.MouseButtonLeft && m.state == stateDefault {
			if inst := m.list.InstanceAtZone(msg); inst != nil {
				m.list.SelectInstance(inst)
				// A second click on the same row within doubleClickWindow attaches,
				// mirroring Enter, via the tea.Exec attach path (attachExec). The first
				// click already selected the row, so it is the current selection.
				now := time.Now()
				if m.lastClickTitle == inst.Title && now.Sub(m.lastClickAt) <= doubleClickWindow {
					m.lastClickTitle = ""
					if inst.Paused() || inst.GetStatus() == session.Loading || !inst.TmuxAlive() {
						return m, m.instanceChanged()
					}
					if m.tabbedWindow.IsInTerminalTab() {
						return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
					}
					// inst is the current selection, so list.Attach targets it;
					// killTarget carries it for the ctrl-x in-session kill flow.
					return m, m.attachExec(m.list.Attach, inst)
				}
				m.lastClickTitle = inst.Title
				m.lastClickAt = now
				return m, m.instanceChanged()
			}
			// A click on a repo-group header toggles its fold, mirroring ←/→.
			// Persist the new collapsed set exactly like the keyboard paths do.
			if key, ok := m.list.HeaderAtZone(msg); ok {
				if m.list.ClickHeader(key) {
					if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
						return m, m.handleError(err)
					}
					return m, m.instanceChanged()
				}
				return m, nil
			}
			if idx, ok := m.tabbedWindow.TabAtZone(msg); ok {
				m.tabbedWindow.SetActiveTab(idx)
				return m, m.instanceChanged()
			}
		}
		return m, nil
	case branchSearchDebounceMsg:
		// Debounce timer fired — check if this is still the current filter version
		if m.textInputOverlay == nil {
			return m, nil
		}
		if msg.version != m.textInputOverlay.BranchFilterVersion() {
			return m, nil // stale, a newer debounce is pending
		}
		return m, m.runBranchSearch(msg.filter, msg.version)
	case branchSearchResultMsg:
		if m.textInputOverlay != nil {
			if msg.err {
				m.textInputOverlay.SetBranchSearchError(msg.version)
			} else {
				m.textInputOverlay.SetBranchResults(msg.branches, msg.version)
			}
		}
		return m, nil
	case targetValidityDebounceMsg:
		// Debounce timer fired — only check if this is still the current target.
		if m.textInputOverlay == nil || msg.path != m.newSessionPath {
			return m, nil
		}
		return m, m.runValidityCheck(msg.path)
	case targetValidityResultMsg:
		// Apply only if the result is for the still-current target, so a stale check
		// (the user has navigated on) can't clobber the indicator.
		if m.textInputOverlay != nil && msg.path == m.newSessionPath {
			m.textInputOverlay.SetTargetValidity(msg.valid, msg.direct, msg.headBranch)
			// Re-point the account picker at the new project's auto-routed account so the
			// displayed selection tracks the target. No-op once the user has overridden it.
			m.textInputOverlay.PreselectAccount(msg.accountName)
			// A confirmed git target gets one background fetch per form-session, so its
			// branch list reflects current remote refs. The verdict (not the path change)
			// is the trigger: filesystem browsing through non-repos never fetches.
			if msg.valid && !msg.direct && !m.fetchedPaths[msg.path] {
				if m.fetchedPaths == nil {
					m.fetchedPaths = make(map[string]bool)
				}
				m.fetchedPaths[msg.path] = true
				return m, m.runBranchFetch(msg.path)
			}
		}
		return m, nil
	case branchFetchDoneMsg:
		// A background fetch finished. If its path is still the current target, re-run
		// the branch search so newly-fetched refs appear without retyping the filter; a
		// completion for an abandoned path is dropped. (SetResults' version check still
		// guards against the user having typed during the search itself.)
		if m.textInputOverlay == nil || msg.path != m.newSessionPath {
			return m, nil
		}
		return m, m.runBranchSearch(m.textInputOverlay.BranchFilter(), m.textInputOverlay.BranchFilterVersion())
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		// First launch ever: show the one-time welcome once the size is known.
		m.maybeShowWelcome()
		return m, nil
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action
		return m, m.instanceChanged()
	case prMergedMsg:
		// A confirmed merge succeeded: acknowledge it and refresh so the PR badge
		// reflects the now-merged state on the next poll.
		return m, tea.Batch(
			m.handleInfoNotice(fmt.Sprintf("merged PR #%d", msg.number)),
			m.instanceChanged(),
		)
	case prCreatedMsg:
		// A confirmed create succeeded: acknowledge it and refresh so the PR badge
		// reflects the new PR on the next poll (flipping the hint toward merge).
		notice := "created PR"
		if msg.number > 0 {
			notice = fmt.Sprintf("created PR #%d", msg.number)
		}
		return m, tea.Batch(
			m.handleInfoNotice(notice),
			m.instanceChanged(),
		)
	case prOpenedMsg:
		// The browser was launched (nothing to refresh): just acknowledge it.
		if msg.number > 0 {
			return m, m.handleInfoNotice(fmt.Sprintf("opened PR #%d in browser", msg.number))
		}
		return m, m.handleInfoNotice("opened PR in browser")
	case attachFinishedMsg:
		// A tea.Exec terminal attach returned (the user detached, or it failed to
		// start). tea.Exec's RestoreTerminal has already repainted the frame; refine
		// the layout and selection-derived panes from here.
		m.state = stateDefault
		if msg.err != nil {
			return m, m.handleError(msg.err)
		}
		// The user was watching this session until a moment ago, so if the agent
		// finished while attached, the poll below settles a stale Running to Ready —
		// a synthetic transition that must not flag unread. An agent still working
		// at detach is observed as Running first, which clears the suppression, so a
		// later genuine completion flags normally. Armed before BOTH detach paths:
		// the sibling-cycle early return below and the normal fresh poll.
		if msg.killTarget != nil {
			msg.killTarget.ArmReadySuppression()
		}
		// Honor an in-session kill (Ctrl+X) requested before detach. killTarget is the
		// attached instance (nil for the terminal tab, which has no kill key); keep
		// tea.WindowSize() so the confirmation overlay redraws at the correct
		// dimensions after the full-screen attach (confirmKill only mutates state).
		if msg.killTarget != nil && msg.killTarget.AttachKillRequested() {
			return m, tea.Batch(tea.WindowSize(), m.confirmKill(msg.killTarget))
		}
		// A sibling-cycle key (Ctrl+PgUp/PgDn) detaches with a direction; re-attach the
		// neighbouring session in the repo group, keeping cycling inside Atrium's model.
		// killTarget is the session just detached (nil for the terminal tab, which has
		// no cycle keys).
		if msg.killTarget != nil {
			if next := m.cycleTarget(msg.killTarget); next != nil {
				m.list.SelectInstance(next)
				m.pushOneContext(next)
				return m, m.attachExec(next.Attach, next)
			}
		}
		// Polling stalled while attached, so the smoothing state is stale: refresh the
		// selected session at face value (fresh) rather than letting a stale "running" on a
		// now-idle agent linger — and re-run through the hysteresis — until it settles. Pin
		// the poll tracker to the current selection first so instanceChanged's own
		// (hysteresis) poll doesn't also fire for the same instance.
		selected := m.list.GetSelectedInstance()
		m.lastStatusPollSelection = selected
		return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), pollSelectedCmd(selected, true))
	case infoMsg:
		// An action requested a dismissible info modal (e.g. an actionable resume
		// error). Unlike handleError's transient box, this persists until dismissed.
		return m, m.showInfo(string(msg))
	case instanceStartedMsg:
		// Select the instance that just started (or failed)
		m.list.SelectInstance(msg.instance)

		if msg.err != nil {
			m.list.Kill()
			return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
		}

		// Own the Loading -> Running transition here, on the main thread. Start()
		// deliberately no longer sets Running from its background goroutine (that
		// raced the UI/poll readers and could leave the session stuck on the
		// "Setting up workspace..." splash); this message arrives after Start()
		// completed, so the write is race-free. applyPaneState refines it to
		// Ready/NeedsInput on later ticks.
		msg.instance.SetStatus(session.Running)

		// Save after successful start
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			return m, m.handleError(err)
		}
		m.recordRecentPath(msg.instance.Path)
		// First successful session start retires the one-time welcome. This is the single
		// chokepoint every start (inline `n` and the `N` form) funnels through, so the
		// welcome re-shows on every launch until the user has actually created a session —
		// a dismissal alone no longer burns it (see showHelpScreen). Best-effort persist.
		if seen := m.appState.GetHelpScreensSeen(); seen&(helpTypeWelcome{}.mask()) == 0 {
			if err := m.appState.SetHelpScreensSeen(seen | helpTypeWelcome{}.mask()); err != nil {
				log.WarningLog.Printf("failed to persist welcome-seen state: %v", err)
			}
		}
		if m.autoYes {
			msg.instance.AutoYes = true
		}

		// A prompt from the N form is delivered later by the metadata tick loop,
		// once the agent is past its startup/trust screen and ready for input
		// (see deliverReadyPrompts). Sending here races the agent's boot and lands
		// keystrokes in the trust dialog instead of the input box.
		m.menu.SetState(ui.StateDefault)

		if m.shouldAutoOpen(msg.instance) {
			// Drop straight into the new session, mirroring the KeyEnter attach path.
			// Attach msg.instance directly rather than via m.list.Attach(): a background
			// instanceStartedMsg from another freshly-created session could have moved
			// the list selection by now. The attach runs through tea.Exec, which hands
			// the terminal to tmux and repaints on detach; post-detach handling — an
			// in-session Ctrl+X kill request, keyed on msg.instance since the selection
			// may have drifted, or a sibling-cycle request — lands in the
			// attachFinishedMsg handler.
			return m, m.attachExec(msg.instance.Attach, msg.instance)
		}

		return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
		return m, m.handleError(err)
	}
	return m, tea.Quit
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	// Ctrl+L forces a full repaint. The alt-screen renderer updates incrementally and
	// never erases lines, so it desyncs (leaving accumulating ghost rows) if the terminal
	// ever renders a line wider than measured — e.g. a font lacking a combined emoji glyph.
	// theme.SanitizeWidth prevents the known cases; this is the universal manual-redraw
	// escape hatch for any residual artifact, in any state.
	if msg.String() == "ctrl+l" {
		return m, tea.ClearScreen
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateInfo {
		return m.handleInfoState(msg)
	}

	if m.state == statePrompt {
		// Handle cancel via ctrl+c before delegating to the overlay
		if msg.String() == "ctrl+c" {
			return m, m.cancelPromptOverlay()
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

			// The new-session form creates the instance only now, on submit, so no row
			// appears in the list while the user is still filling it in.
			if m.textInputOverlay.IsCreateForm() {
				return m, m.createSessionFromForm(prompt)
			}

			// Quick-send overlay: fire the message at the selected running session and drop
			// straight back to the list (no new-session help — the session is already up).
			selected := m.list.GetSelectedInstance()
			if selected == nil {
				m.textInputOverlay = nil
				m.state = stateDefault
				return m, nil
			}
			if err := selected.SendPrompt(prompt); err != nil {
				return m, m.handleError(err)
			}
			m.textInputOverlay = nil
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)
			return m, tea.Sequence(tea.WindowSize(), m.instanceChanged())
		}

		// If the target directory changed in the picker, re-scope the branch search to
		// the new repo: invalidate in-flight results for the old repo, then schedule a
		// fresh (debounced) search with the current branch filter.
		if newPath := m.textInputOverlay.GetSelectedPath(); newPath != "" && newPath != m.newSessionPath {
			m.newSessionPath = newPath
			// Re-scope the branch search and (debounced, off the hot path) re-check the
			// new target's state (directory? git repo or direct session?). The check is
			// async because filesystem browsing changes the selected path almost every
			// keystroke, and a synchronous git subprocess per keystroke would stutter the
			// UI. Reset the indicator to "unknown" up front so the previous path's verdict
			// isn't asserted for the new path during the debounce window; the async
			// result re-sets it.
			m.textInputOverlay.ClearTargetValidity()
			version := m.textInputOverlay.InvalidateBranchSearch()
			return m, tea.Batch(
				m.scheduleBranchSearch(m.textInputOverlay.BranchFilter(), version),
				m.scheduleValidityCheck(newPath),
			)
		}

		// Schedule a debounced branch search if the filter changed
		if branchFilterChanged {
			filter := m.textInputOverlay.BranchFilter()
			version := m.textInputOverlay.BranchFilterVersion()
			return m, m.scheduleBranchSearch(filter, version)
		}

		return m, nil
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
		if shouldClose {
			confirmed := m.confirmationOverlay.Confirmed
			action := m.pendingConfirmAction
			m.state = stateDefault
			m.confirmationOverlay = nil
			m.pendingConfirmAction = nil
			if confirmed && action != nil {
				// Run the action here, on the main loop, because it mutates shared
				// model state (list, terminals); a tea.Cmd would run it in a
				// goroutine and race Update. Feed only the resulting message back
				// through the runtime so a returned error reaches the error box.
				resultMsg := action()
				return m, func() tea.Msg { return resultMsg }
			}
			return m, nil
		}
		return m, nil
	}

	// Handle rename state. This must run before the global q/ctrl+c quit handling below so
	// those keys edit (or cancel) the label instead of quitting the app.
	if m.state == stateRename {
		shouldClose := m.renameOverlay.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}

		submitted := m.renameOverlay.IsSubmitted()
		value := m.renameOverlay.Value()
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
					return m, m.handleError(err)
				}
			} else {
				target.SetDisplayName(value)
				if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
					return m, m.handleError(err)
				}
			}
		}
		return m, m.instanceChanged()
	}

	// Handle settings state. Like the other overlay states, this must run before
	// the global quit handling so q/esc and printable keys reach the panel. The
	// overlay mutates appConfig in place and reports which row changed; persisting
	// and live-applying that change is applySettingChange's job.
	if m.state == stateSettings {
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

	// Handle filter state. This must run before the global quit handling so that printable keys
	// and Esc update the filter instead of quitting. The list holds the query (single source of
	// truth); note that letter keys must reach the default case, so we cannot reserve "j"/"k"
	// (vim navigation elsewhere) as commit keys — they have to be typeable into the query.
	if m.state == stateFilter {
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

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
		// If in preview tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode() {
			// Use the selected instance from the list
			selected := m.list.GetSelectedInstance()
			err := m.tabbedWindow.ResetPreviewToNormalMode(selected)
			if err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		// If in terminal tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode() {
			m.tabbedWindow.ResetTerminalToNormalMode()
			return m, m.instanceChanged()
		}
		// A committed filter (typed with /, accepted with Enter) is still
		// narrowing the list; Esc clears it, the expected escape hatch.
		if m.list.FilterQuery() != "" {
			m.list.ClearFilter()
			return m, m.instanceChanged()
		}
	}

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeySettings:
		m.state = stateSettings
		m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
		m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
		return m, tea.WindowSize()
	case keys.KeyPrompt:
		// The full entry point: focus starts on the project picker.
		return m, m.openCreateForm(false)
	case keys.KeyNew:
		// The quick entry point: the same form, focused on the title, so
		// "n → type a name → ⌃S" creates a session in the contextual repo.
		return m, m.openCreateForm(true)
	case keys.KeyQuickSend:
		// Open a compose box to fire an ad-hoc message at the selected running session
		// without attaching. Only meaningful when the agent is up and accepting input;
		// other states explain the guard instead of silently swallowing the key.
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.Paused() {
			return m, m.handleInfoNotice("session is paused — press r to resume before sending")
		}
		if !selected.Started() || selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		m.state = statePrompt
		m.textInputOverlay = overlay.NewQuickSendOverlay("Send to " + selected.DisplayName())
		return m, tea.WindowSize()
	case keys.KeyCopyBranch:
		// Yank the selected session's branch name to the system clipboard for handoff
		// to a PR, a teammate, or a git command. Both outcomes are acknowledged on the
		// hint row: without a toast, success and failure were indistinguishable from
		// the keyboard.
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.Branch == "" {
			return m, m.handleInfoNotice("no branch to copy yet")
		}
		if err := copyToClipboard(selected.Branch); err != nil {
			return m, m.handleError(fmt.Errorf("copy branch: %w", err))
		}
		return m, m.handleInfoNotice(fmt.Sprintf("branch '%s' copied", selected.Branch))
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp()
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown()
		return m, m.instanceChanged()
	case keys.KeyShrinkList:
		return m, m.adjustListRatio(-listRatioStep)
	case keys.KeyGrowList:
		return m, m.adjustListRatio(+listRatioStep)
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyShiftTab:
		m.tabbedWindow.ToggleReverse()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyTabPreview, keys.KeyTabDiff, keys.KeyTabTerminal:
		// Direct tab jump by number, complementing Tab/Shift+Tab cycling. The
		// three KeyNames are consecutive, so the offset from KeyTabPreview is the
		// tab index (PreviewTab/DiffTab/TerminalTab are likewise 0/1/2).
		m.tabbedWindow.SetActiveTab(int(name - keys.KeyTabPreview))
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		return m, m.confirmKill(m.list.GetSelectedInstance())
	case keys.KeyFilter:
		// Resume editing a committed query rather than resetting it — re-pressing
		// / to refine a filter should not force retyping it. Esc still clears.
		m.list.SetFilterActive(true)
		m.state = stateFilter
		m.menu.SetState(ui.StateFilter)
		m.recomputeLayout() // the hint bar now claims a row; shrink the panes to fit
		return m, m.instanceChanged()
	case keys.KeyRename:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		m.renameTarget = selected
		m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName())
		m.state = stateRename
		return m, nil
	case keys.KeyAutoName:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if m.generatingName {
			return m, m.handleInfoNotice("already generating a name")
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		// The model call (and the full diff it needs) happen in the background Cmd so
		// the UI stays responsive; only the instance and prompt are captured here.
		m.generatingName = true
		m.menu.SetState(ui.StateGeneratingName)
		m.recomputeLayout() // the progress bar now claims a row; shrink the panes to fit
		return m, runAutoNameCmd(m.ctx, selected, selected.Prompt)
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		// A direct (non-git) session has nothing to push. Fail fast rather than prompting
		// for confirmation and only then erroring. (The menu also hides this action.)
		if selected.IsDirect() {
			return m, m.handleError(fmt.Errorf("push is not available for a direct (non-git) session"))
		}

		// Create the push action as a tea.Cmd
		pushAction := func() tea.Msg {
			// Default commit message with timestamp
			commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s", selected.DisplayName(), time.Now().Format(time.RFC822))
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err = worktree.PushChanges(commitMsg, true); err != nil {
				return err
			}
			return nil
		}

		// Show confirmation modal
		message := fmt.Sprintf("Push changes from session '%s'?", selected.DisplayName())
		return m, m.confirmAction(message, pushAction)
	case keys.KeyMerge:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
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
		// Defer the worktree lookup and network merge into the confirm action, which
		// the runtime runs only if the user confirms.
		mergeAction := func() tea.Msg {
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err := worktree.MergePR(); err != nil {
				return err
			}
			return prMergedMsg{number: number}
		}
		message := fmt.Sprintf("Merge PR #%d from '%s' (squash)?", number, selected.DisplayName())
		if status.CI == git.CIPending {
			message += " CI is still running."
		}
		return m, m.confirmAction(message, mergeAction)
	case keys.KeyCreate:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
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
		// Defer the worktree lookup and network create into the confirm action, which
		// the runtime runs only if the user confirms.
		createAction := func() tea.Msg {
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			number, err := worktree.CreatePR(draft)
			if err != nil {
				return err
			}
			return prCreatedMsg{number: number}
		}
		adjective := "ready-for-review"
		if draft {
			adjective = "draft"
		}
		message := fmt.Sprintf("Create %s PR from '%s'?", adjective, selected.DisplayName())
		return m, m.confirmAction(message, createAction)
	case keys.KeyOpenPR:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
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
		openAction := func() tea.Msg {
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err := worktree.OpenPRURL(); err != nil {
				return err
			}
			return prOpenedMsg{number: number}
		}
		return m, openAction
	case keys.KeyPause:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}

		// A direct (non-git) session has no worktree to free and runs in the user's
		// real directory, so pausing it would only detach a still-running agent.
		// Warn instead of pausing. (The menu also hides this action for direct sessions.)
		if selected.IsDirect() {
			return m, m.handleError(fmt.Errorf("pause is not available for a direct (non-git) session; it runs in place with no worktree to free"))
		}

		// Pause: commit changes and free the worktree. The branch name is copied to
		// the clipboard inside Pause(); the always-on hint bar carries the reminder.
		if err := selected.Pause(); err != nil {
			return m, m.handleError(err)
		}
		m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
		return m, m.instanceChanged()
	case keys.KeyMoveUp:
		if m.list.MoveUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveDown:
		if m.list.MoveDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupUp:
		if m.list.MoveGroupUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupDown:
		if m.list.MoveGroupDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapse:
		if m.list.Collapse() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyExpand:
		if m.list.Expand() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapseAll:
		if m.list.ToggleCollapseAll() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if selected.GetStatus() == session.Loading {
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		if !selected.Paused() {
			return m, m.handleInfoNotice("session is already running — only paused sessions resume")
		}
		return m, m.resumeSelected(selected)
	case keys.KeyEnter, keys.KeyAttachToggle:
		// KeyAttachToggle (ctrl+q) mirrors the in-session detach key
		// (session/tmux/tmux.go): on the list it attaches the selected session,
		// making ctrl+q a symmetric attach/detach toggle. It funnels through the
		// same guards as enter.
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
			return m, m.handleInfoNotice("session is still starting — try again in a moment")
		}
		if !selected.TmuxAlive() {
			return m, m.handleInfoNotice("session has no live terminal — resume it or kill it")
		}
		// Attach to the session (or its terminal tab) via tea.Exec, which hands the
		// terminal to tmux and repaints on detach; the hint bar carries the ctrl-q
		// detach reminder. Post-detach handling lands in the attachFinishedMsg handler.
		if m.tabbedWindow.IsInTerminalTab() {
			// The terminal tab has no in-session kill key, so no kill target.
			return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
		}
		return m, m.attachExec(m.list.Attach, selected)
	default:
		return m, nil
	}
}

// handleInfoState dismisses the info modal on any key press.
func (m *home) handleInfoState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.textOverlay.HandleKeyPress(msg) {
		m.state = stateDefault
		return m, tea.Sequence(
			tea.WindowSize(),
			func() tea.Msg {
				m.menu.SetState(ui.StateDefault)
				return nil
			},
		)
	}
	return m, nil
}
