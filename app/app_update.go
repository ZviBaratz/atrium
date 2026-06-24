package app

// Top-level event and key dispatch for the home model.

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// wheelScrollLines is how many lines one mouse-wheel notch scrolls the preview
// pane in scroll mode. A notch moves several lines for a fluid feel; the
// keyboard scroll keys move one line for precise positioning.
const wheelScrollLines = 3

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
	case updateFoundMsg:
		// Stage the download as its own command so this notice renders while
		// the transfer runs; the restart hint arrives in updateCheckDoneMsg.
		// The toast is transient; the panel badge persists until restart so
		// the update survives overlays, missed toasts, and hint_bar:false.
		if m.list != nil {
			m.list.SetUpdateBadge(updateBadgeText(msg.release.Version, false))
		}
		return m, tea.Batch(
			m.handleUpdateNotice(fmt.Sprintf("updating to v%s in the background…", msg.release.Version)),
			m.installUpdateCmd(msg.release),
		)
	case updateCheckDoneMsg:
		if m.list != nil {
			m.list.SetUpdateBadge(updateBadgeText(msg.version, msg.installed))
		}
		if msg.installed {
			return m, m.handleUpdateNotice(fmt.Sprintf("updated to v%s — restart %s to apply", msg.version, m.hintBinName()))
		}
		return m, m.handleUpdateNotice(fmt.Sprintf("v%s available — run `%s update`", msg.version, m.hintBinName()))
	case driftFoundMsg:
		// Try to show the hint first. handleInfoNotice returns nil when the hint
		// bar can't render right now (e.g. hint_bar off, or a modal owns the
		// screen); in that case record no ack so the hint re-arms on a later
		// startup instead of being silently consumed. atrium doctor remains the
		// durable surface meanwhile.
		cmd := m.handleInfoNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()))
		if cmd == nil {
			// Toast dropped (hint bar off, or a modal owns the screen). Surface the
			// drift via the persistent panel badge instead — the durable fallback
			// for users who'd otherwise never see it. Don't ack: leave it re-armed.
			if m.list != nil {
				m.list.SetDriftBadge(driftBadgeText())
			}
			return m, nil
		}
		// Shown: record the ack at each agent's current installed version so the
		// hint shows once per version. Batched into a single persist.
		acks := make(map[string]string, len(msg.agents))
		for _, r := range msg.agents {
			acks[string(r.Key)] = r.Installed
		}
		if err := m.appState.SetAckedDrift(acks); err != nil {
			log.WarningLog.Printf("failed to record drift acks: %v", err)
		}
		return m, cmd
	case releaseNotesFetchedMsg:
		// Record the version on the successful fetch so the notes show once and
		// never refetch — even when the body is empty (nothing to show, but no
		// reason to keep polling).
		if err := m.appState.SetLastNotesVersion(msg.version); err != nil {
			log.WarningLog.Printf("failed to record release-notes version: %v", err)
		}
		if strings.TrimSpace(msg.notes) == "" {
			return m, nil
		}
		// Don't clobber an open overlay (e.g. a new-session form): buffer and
		// flush on the next preview tick, like pendingUpdateNotice.
		if m.state != stateDefault {
			buffered := msg
			m.pendingReleaseNotes = &buffered
			return m, nil
		}
		return m, m.showReleaseNotes(msg.version, msg.notes, msg.url)
	case previewTickMsg:
		// The pane owns hint-overlay validity (a selection change or pause
		// drops it there); if it dropped, follow it back to default so keys
		// stop being captured for a vanished overlay.
		if m.state == stateHints && !m.tabbedWindow.InPreviewHintMode() {
			m.exitHintMode()
		}
		m.markSeenAfterDwell(time.Now())
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			// An update notice that arrived while an overlay owned the screen
			// is buffered; deliver it as soon as the hint bar is back.
			m.flushPendingUpdateNotice(),
			// Likewise for "what's new" notes buffered behind another overlay.
			m.flushPendingReleaseNotes(),
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
		m.renameOverlay = overlay.NewRenameOverlay(msg.name, msg.instance.Note(), false)
		m.state = stateRename
		m.recomputeLayout() // the progress bar gave up its row; the overlay self-documents
		return m, nil
	case smartDispatchDoneMsg:
		// Drop a result the user has moved past: the exact form it was launched for is no
		// longer the active overlay (cancelled, submitted, or a different form opened).
		if msg.form == nil || m.textInputOverlay != msg.form {
			return m, nil
		}
		m.textInputOverlay.SetProjectHint("")
		if msg.err != nil {
			return m, nil // routing failed; leave the form as the user left it
		}
		// Upgrade the title independently of routing: a confident match wants only a
		// better title, and even an unrouted result may carry a usable one. Replace the
		// deterministic placeholder only while the user hasn't typed their own. Do this
		// before any re-point so the retarget's async branch check below validates the
		// final title (not the placeholder) against the routed repo.
		if msg.title != "" && m.textInputOverlay.GetTitle() == m.smartDispatchSeededTitle {
			m.textInputOverlay.SetTitleValue(msg.title)
			m.refreshTitleError()
		}
		var cmds []tea.Cmd
		// Re-point the project only when the router found one and the user hasn't moved
		// the picker themselves (still on the contextual default the form opened with).
		// A confident match already sits on its project, so this is a no-op there.
		if msg.project != "" {
			if path := m.candidatePathForBasename(msg.project); path != "" &&
				m.textInputOverlay.GetSelectedPath() == m.newSessionPath && path != m.newSessionPath {
				m.textInputOverlay.SelectPath(path)
				cmds = append(cmds, m.retargetNewSession(path))
			}
		}
		return m, tea.Batch(cmds...)
	case metadataUpdateDoneMsg:
		if recoverLostInstances(msg.results, m.lostStrikes) {
			if err := m.persistInstances(); err != nil {
				log.ErrorLog.Printf("failed to persist recovered sessions: %v", err)
			}
		}
		for _, r := range msg.results {
			// Skip instances that were paused while metadata was being computed, or
			// that were just recovered to Paused above because their session died.
			if r.sessionLost || r.instance.Paused() {
				continue
			}
			r.instance.ApplyPaneState(r.state)
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
			r.instance.SetPRStatus(r.prStatus)
			if r.modelOK {
				r.instance.SetModelMeta(r.model, r.modelStamp)
			}
			if r.modeOK {
				r.instance.SetModeMeta(r.mode)
			}
		}
		// Re-apply the status sort now that pane states are fresh, so urgent sessions
		// keep floating to the top of their group. No-op in creation mode; the
		// selected session stays under the cursor (preserved by identity).
		m.list.ApplySort()
		m.pushSessionContexts()
		cmds := deliverReadyPrompts(msg.results)
		m.metadataTick++
		fullSweep := m.metadataTick%metadataFullSweepEvery == 0
		cmds = append(cmds, tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance(), fullSweep))
		return m, tea.Batch(cmds...)
	case instancePolledMsg:
		// An off-cadence single-instance refresh (selection change / detach). Apply the
		// state but do NOT reschedule the metadata tick — that chain is owned by
		// metadataUpdateDoneMsg above; touching it here would spawn a second tick loop.
		if msg.instance.GetStatus() != session.Paused {
			msg.instance.ApplyPaneState(msg.state)
		}
		return m, nil
	case promptSendErrorMsg:
		// A queued initial prompt that could not be delivered (the session died after
		// the readiness gate passed). Surface it like the manual send path rather than
		// leaving the session Ready-but-idle with no sign the prompt was lost.
		return m, m.handleError(fmt.Errorf("failed to deliver prompt to %q: %w", msg.instance.Title, msg.err))
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		// Modal text overlays (help / info) own the screen: the wheel scrolls
		// an overflowing cheatsheet wherever it hovers, and a left-click
		// outside the box dismisses — mirroring the keyboard semantics
		// (scroll keys scroll, anything else closes). Clicks inside the box
		// are inert so a stray selection click doesn't tear the dialog down.
		if (m.state == stateHelp || m.state == stateInfo) && m.textOverlay != nil {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.textOverlay.ScrollBy(-1)
			case tea.MouseButtonWheelDown:
				m.textOverlay.ScrollBy(1)
			case tea.MouseButtonLeft:
				if !m.textOverlayContains(msg.X, msg.Y) {
					return m.closeTextOverlay()
				}
			}
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
					m.tabbedWindow.ScrollUp(wheelScrollLines)
				} else {
					m.tabbedWindow.ScrollDown(wheelScrollLines)
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
				if m.lastClickInstance == inst && now.Sub(m.lastClickAt) <= doubleClickWindow {
					m.lastClickInstance = nil
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
				m.lastClickInstance = inst
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
			// Re-scope the duplicate-title check to the confirmed target's group and
			// re-run it: the same title may be free in one repo and taken in another.
			if msg.groupKey != "" {
				m.newSessionGroup = msg.groupKey
				m.refreshTitleError()
			}
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
	case titleCheckDebounceMsg:
		// Debounce timer fired — only run the git check if the title and target are
		// still current (the user may have typed on or re-pointed the picker).
		if m.textInputOverlay == nil || !m.textInputOverlay.IsCreateForm() ||
			msg.title != m.textInputOverlay.GetTitle() || msg.path != m.newSessionPath {
			return m, nil
		}
		return m, m.runTitleCheck(msg.title, msg.path)
	case titleCheckResultMsg:
		// Apply only a verdict for the still-current (title, target) pair; a stale
		// one must not flag (or clear) the wrong title.
		if m.textInputOverlay == nil || !m.textInputOverlay.IsCreateForm() ||
			msg.title != m.textInputOverlay.GetTitle() || msg.path != m.newSessionPath {
			return m, nil
		}
		m.titleBranchExists = msg.exists
		m.titleBranchName = msg.branch
		m.refreshTitleError()
		return m, nil
	case projectScanDoneMsg:
		// A background repo scan finished: persist it and refresh an open create
		// form's candidates in place (filter and cursor preserved).
		return m, m.handleProjectScanDone(msg)
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
		// A resize invalidates hint mode's frozen geometry; exit rather than
		// redraw stale coordinates (cheap and correct — scroll-mode pragmatism).
		if m.state == stateHints {
			m.exitHintMode()
		}
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
	case batchResumeDoneMsg:
		// A confirmed "resume all" finished. All-success gets a transient notice;
		// any failures go to a persistent modal the user must read (it names which
		// sessions didn't come back and why). Either way, refresh the list so the
		// now-Running rows reflect the restore.
		if len(msg.failures) == 0 {
			return m, tea.Batch(
				m.handleInfoNotice(fmt.Sprintf("resumed %d session%s", msg.resumed, plural(msg.resumed))),
				m.instanceChanged(),
			)
		}
		return m, tea.Batch(m.showInfo(msg.summary()), m.instanceChanged())
	case batchPauseDoneMsg:
		// A confirmed "pause all" finished. Tear down each parked session's preview
		// terminal on the main loop (single-session pause does the same after Pause).
		// All-success gets a transient notice; any failures go to a persistent modal
		// naming which sessions didn't park and why. Either way, refresh the list so
		// the now-Paused rows reflect the park.
		for _, inst := range msg.pausedInstances {
			m.tabbedWindow.CleanupTerminalForInstance(inst)
		}
		if len(msg.failures) == 0 {
			return m, tea.Batch(
				m.handleInfoNotice(fmt.Sprintf("paused %d session%s", msg.paused, plural(msg.paused))),
				m.instanceChanged(),
			)
		}
		return m, tea.Batch(m.showInfo(msg.summary()), m.instanceChanged())
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
			// A detach that hit a pty close/restore error can't ride msg.err (that
			// comes from Run(), nil on a normal detach). Surface it via the persistent
			// modal — it's actionable — and short-circuit the kill/cycle so we don't
			// hop siblings while this session is half-broken. Keep tea.WindowSize() so
			// the modal and layout redraw at the correct dimensions after the
			// full-screen attach, matching the other detach returns below. (The
			// terminal tab, killTarget nil, has no such teardown to report.)
			if derr := msg.killTarget.AttachExitError(); derr != nil {
				m.showInfo(fmt.Sprintf(
					"Session detach hit an error and may need re-attaching "+
						"(pause then resume to recover):\n%v", derr))
				return m, tea.WindowSize()
			}
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
		// completed, so the write is race-free. ApplyPaneState refines it to
		// Ready/NeedsInput on later ticks.
		msg.instance.SetStatus(session.Running)

		// Save after successful start
		if err := m.persistInstances(); err != nil {
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
	if err := m.persistInstances(); err != nil {
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
		return m.handlePromptState(msg)
	}

	if m.state == stateConfirm {
		return m.handleConfirmState(msg)
	}

	// Rename must run before the global q/ctrl+c quit handling below so those keys
	// edit (or cancel) the label instead of quitting the app.
	if m.state == stateRename {
		return m.handleRenameState(msg)
	}

	// Settings, like the other overlay states, must run before the global quit
	// handling so q/esc and printable keys reach the panel.
	if m.state == stateSettings {
		return m.handleSettingsState(msg)
	}

	// Filter must run before the global quit handling so that printable keys and Esc
	// update the filter instead of quitting.
	if m.state == stateFilter {
		return m.handleFilterState(msg)
	}

	// Hint (fingers) mode: every key is either a hint character or an exit.
	// Must run before the global esc/quit handling below so hint letters like
	// q never quit the app.
	if m.state == stateHints {
		return m.handleHintsState(msg)
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
	case keys.KeySmartDispatch:
		// Smart dispatch: one free-form line routed to a project and a pre-filled form.
		m.state = statePrompt
		m.textInputOverlay = overlay.NewSmartDispatchOverlay("Describe the session")
		return m, tea.WindowSize()
	case keys.KeyQuickSend:
		return m.openQuickSend()
	case keys.KeyApprove:
		return m.approveSelected()
	case keys.KeyCopyBranch:
		return m.copySelectedBranch()
	case keys.KeyHints:
		// Freeze the preview and overlay copy/open hints on its matches.
		return m.enterHintMode()
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyNextUnread:
		if m.list.NextUnread() {
			return m, m.instanceChanged()
		}
		return m, m.handleInfoNotice("no more unread sessions")
	case keys.KeyNextNeedsInput:
		if m.list.NextNeedsInput() {
			return m, m.instanceChanged()
		}
		return m, m.handleInfoNotice("no more blocked sessions")
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp(1)
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown(1)
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
		return m.openRenameSelected()
	case keys.KeyAutoName:
		return m.startAutoNameSelected()
	case keys.KeySubmit:
		return m.pushSelected()
	case keys.KeyMerge:
		return m.mergeSelected()
	case keys.KeyCreate:
		return m.createPRForSelected()
	case keys.KeyOpenPR:
		return m.openPRForSelected()
	case keys.KeyPause:
		return m.pauseSelected()
	case keys.KeyMoveUp, keys.KeyMoveDown:
		if !m.list.ManualReorderEnabled() {
			return m, m.handleInfoNotice("manual reorder only in creation sort")
		}
		if name == keys.KeyMoveUp {
			return m.moveAndPersist(m.list.MoveUp)
		}
		return m.moveAndPersist(m.list.MoveDown)
	case keys.KeyMoveGroupUp:
		return m.moveAndPersist(m.list.MoveGroupUp)
	case keys.KeyMoveGroupDown:
		return m.moveAndPersist(m.list.MoveGroupDown)
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
		return m.resumeSelectedKey()
	case keys.KeyResumeAll:
		return m, m.resumeAll()
	case keys.KeyPauseAll:
		return m, m.pauseAll()
	case keys.KeyEnter, keys.KeyAttachToggle:
		return m.attachSelected()
	default:
		return m, nil
	}
}

// handleInfoState dismisses the info modal on any key press (scroll keys
// scroll first while the text overflows, exactly like the help state).
func (m *home) handleInfoState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.textOverlay.HandleKeyPress(msg) {
		return m.closeTextOverlay()
	}
	return m, nil
}
