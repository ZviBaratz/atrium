package app

// Top-level event and key dispatch for the home model.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// wheelScrollLines is how many lines one mouse-wheel notch scrolls the preview
// pane in scroll mode. A notch moves several lines for a fluid feel; the
// keyboard scroll keys move one line for precise positioning.
//
// Aliased from the tmux package rather than spelled again: the same notch distance is
// rendered into the managed tmux config's wheel bindings, so a pane of an attached
// session and the preview pane of the same session scroll the same amount. Two
// independent literals (which is what this was) drift with nothing to catch it.
const wheelScrollLines = tmux.WheelScrollLines

// cleanupTerminalForInstance tears down an instance's cached preview terminal.
// It is a package var (method expression) so batch-outcome tests can swap in a
// capturing fake and pin which instances a batch tears down — resume must tear
// down none. Same seam idiom as releaseResolved / actions.CopyToClipboard.
var cleanupTerminalForInstance = (*ui.TabbedWindow).CleanupTerminalForInstance

// prMergedMsg carries the merged session so the handler can offer to clean it up
// (#384) — the selection may have moved by the time the async merge lands.
//
// prMergedMsg is returned by a confirmed merge action to report success back
// through the runtime, carrying the merged PR number for the acknowledgment.
type prMergedMsg struct {
	number   int
	instance *session.Instance
}

// pushedMsg is returned by a confirmed push action to acknowledge success. Push
// used to return nil (no notice at all); this lets its handler flash a "pushed"
// notice like merge/create do.
type pushedMsg struct{}

// prCreatedMsg is returned by a confirmed create action to report success back
// through the runtime, carrying the new PR number (0 if gh's output had none).
type prCreatedMsg struct{ number int }

// suggestionAcceptedMsg carries the result of accepting claude's ghost-text
// suggestion. The accept runs off the update thread (it polls the pane for up to a
// second), so the optimistic status flip and the acknowledgment land here, on the
// main loop, rather than in the goroutine that did the tmux work.
type suggestionAcceptedMsg struct {
	instance *session.Instance
	accepted bool
	err      error
}

// prOpenedMsg is returned by the open-PR action once gh has launched the browser,
// carrying the PR number for the acknowledgment. Unlike a merge it changes no
// state, so its handler only shows a notice.
type prOpenedMsg struct{ number int }

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Whatever this message does, if it flips menuVisible the vertical budget just
	// changed and the panes are sized against the old one. Opening an overlay is
	// fine either way — the openers size themselves — but closing one returns to
	// the list with the panes still holding the row the hint bar is about to take
	// back, so the frame renders one line taller than the terminal. The alt-screen
	// renderer never erases lines: that row scrolls the frame's top border away for
	// good, and no later tick recomputes the layout to bring it back.
	//
	// Guarded here rather than in each dismiss* helper because it is the property
	// that matters (the budget changed), not the site: five helpers were missing it
	// (palette, command log, queue, rename, confirm) and a sixth would be one
	// forgotten line away. The handlers that already recompute stay as they are —
	// recomputeLayout is a resize replay, so running it twice costs nothing. It is
	// also why this sits on Update rather than handleKeyPress: a state can be
	// entered or left by a message (an async action completing), not only by a key.
	barBefore := m.menuVisible()
	defer func() {
		if m.menuVisible() != barBefore {
			m.recomputeLayout()
		}
	}()

	switch msg := msg.(type) {
	case hideErrMsg:
		if msg.gen == m.noticeGen {
			if m.menu != nil {
				m.menu.ClearNotice()
			}
			m.errBox.Clear()
			m.noticeSettingKey = "" // the advice is off screen; ',' goes back to the rail
			m.recomputeLayout()     // reclaim the error row; panes grow back by one
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
	case agentCheckDoneMsg:
		names := strings.Join(msg.newAgents, ", ")
		var text string
		if len(msg.newAgents) == 1 {
			text = fmt.Sprintf("New agent `%s` detected. Run `atrium profiles detect` to add it.", names)
		} else {
			text = fmt.Sprintf("New agents `%s` detected. Run `atrium profiles detect` to add them.", names)
		}
		return m, m.handleAgentNotice(text)
	case profilesDetectedMsg:
		// The merge is unconditional. The user asked for it, and the probe takes long enough
		// that gating it on the panel still being open made one set of keystrokes produce three
		// different outcomes — dropped if they closed the panel, merged if they reopened it (a
		// DIFFERENT overlay instance), merged-but-silent if they only moved the rail. What
		// varies is where the outcome is reported, never whether it happens.
		added := m.appConfig.MergeDetectedProfiles(msg.detected)
		text := profilesDetectedText(added)
		shown := m.settingsOverlay != nil && m.settingsOverlay.NoteProfilesDetected(added, text)
		var cmds []tea.Cmd
		if len(added) > 0 {
			// Nothing added means nothing to persist, mirroring the CLI's early return.
			if cmd := m.applySettingChange("profiles"); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if !shown {
			// handleAgentNotice is the held-over path the startup agent check already uses: it
			// shows now if the hint bar is free, and otherwise waits — which is exactly right
			// while a panel is covering the row.
			if cmd := m.handleAgentNotice(text); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	case updateCheckDoneMsg:
		if m.list != nil {
			m.list.SetUpdateBadge(updateBadgeText(msg.version, msg.installed))
		}
		if msg.installed {
			return m, m.handleUpdateNotice(fmt.Sprintf("updated to v%s — restart %s to apply", msg.version, m.hintBinName()))
		}
		return m, m.handleUpdateNotice(fmt.Sprintf("v%s available — run `%s update`", msg.version, m.hintBinName()))
	case driftFoundMsg:
		return m.handleDriftFound(msg)
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
		return m.handlePreviewTick(msg)
	case paneFrameMsg:
		return m.handlePaneFrame(msg)
	case contextPushFailedMsg:
		// Un-arm the optimistic cache for the pushes that didn't land, so the next
		// metadata tick tries again instead of believing a value tmux never got.
		for _, inst := range msg.instances {
			inst.ClearContextCache()
		}
		return m, nil
	case splashTickMsg:
		return m.handleSplashTick()
	case autoNameDoneMsg:
		m.generatingName = false
		if msg.err != nil {
			// The progress row goes away and we return to plain navigation; surface the
			// failure and leave the name untouched rather than applying a junk fallback.
			// A concurrent modal action keeps the row — that is the owner ranking's job
			// now, not a hand-rolled actionInFlight check here.
			m.menu.ClearBusy(ui.BusyBackground)
			m.recomputeLayout() // the progress bar gave up its row; panes reclaim it
			return m, m.handleError(msg.err)
		}
		m.menu.ClearBusy(ui.BusyBackground)
		// Offer the generated name through the existing rename overlay so the user
		// can confirm or edit it before it commits.
		m.renameTarget = msg.instance
		m.renameOverlay = overlay.NewRenameOverlay(msg.name, msg.instance.Note(), false)
		m.state = stateRename
		m.recomputeLayout() // the progress bar gave up its row; the overlay self-documents
		return m, nil
	case smartDispatchDoneMsg:
		return m.handleSmartDispatchDone(msg)
	case proceedOverCapMsg:
		// The user accepted the host-capacity confirmation: spawn the staged plan on
		// the UI thread (AddInstance mutates shared model state).
		if m.pendingOverCap == nil {
			return m, nil
		}
		plan := *m.pendingOverCap
		m.pendingOverCap = nil
		return m, m.spawnVariants(plan)
	case proceedExhaustedMsg:
		// The user accepted spawning on a fully-rate-limited pool: spawn the staged
		// plan (already pinned to the soonest-to-reset member) on the UI thread.
		if m.pendingExhausted == nil {
			return m, nil
		}
		plan := *m.pendingExhausted
		m.pendingExhausted = nil
		return m, m.spawnVariants(plan)
	case metadataUpdateDoneMsg:
		// Drop results captured before a terminal attach ran (see home.attachGen):
		// the keeper may have advanced those panes mid-attach, so replaying a stale
		// PanePrompt would tap whatever dialog is up now. The post-detach sweep
		// re-polls everything, so nothing is lost — but the tick must still re-arm.
		var cmds []tea.Cmd
		// Deliver anything `atrium send` spooled since the last tick. Outside the
		// attachGen guard on purpose: a spooled prompt is not a pane observation,
		// so an attach having happened gives no reason to drop it.
		if cmd := m.drainOutbox(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if msg.attachGen == m.attachGen {
			recoveries := recoverLostInstances(msg.results, m.lostStrikes, m.retiring)
			if len(recoveries) > 0 {
				// Every recovery ends the instance Paused (even a failed one), so its
				// status genuinely changed — persist. Then make the transition visible
				// rather than a silent Running→Paused (#270).
				if err := m.persistInstances(); err != nil {
					log.ErrorLog.Printf("failed to persist recovered sessions: %v", err)
				}
				cmds = append(cmds, m.surfaceLostRecoveries(recoveries))
			}
			cmds = append(cmds, m.applyMetadataResults(msg.results, true)...)
			// Surface the fleet in the OS chrome once per tick; a session death this
			// tick (a recovery) shows the taskbar error state, cleared next tick.
			m.refreshOSChrome(len(recoveries) > 0)
		}
		m.metadataTick++
		fullSweep := m.metadataTick%metadataFullSweepEvery == 0
		// Stop the self-chaining tick once the app context is cancelled (shutdown):
		// re-arming would only spawn a Cmd that immediately returns on ctx.Done().
		if m.ctx.Err() == nil {
			cmds = append(cmds, tickUpdateMetadataCmd(m.ctx, m.snapshotActiveInstances(), m.list.GetSelectedInstance(), fullSweep, m.attachGen, m.usagePolicy()))
		}
		return m, tea.Batch(cmds...)
	case metadataSweepDoneMsg:
		// A one-shot background refresh fired on detach (sweepMetadataNowCmd). Apply the
		// results but do NOT reschedule the metadata tick — that chain is owned by
		// metadataUpdateDoneMsg above; touching it here would spawn a second tick loop —
		// and do NOT touch metadataTick, which phases the periodic full-sweep cadence.
		// Lost-session recovery is intentionally left to the periodic tick so its strike
		// debounce isn't shortened by a same-resume double observation.
		if msg.attachGen != m.attachGen {
			return m, nil // captured before an attach ran; stale (see home.attachGen)
		}
		return m, tea.Batch(m.applyMetadataResults(msg.results, false)...)
	case instancePolledMsg:
		// An off-cadence single-instance status refresh (selection change). Apply the state
		// but do NOT reschedule the metadata tick — that chain is owned by
		// metadataUpdateDoneMsg above; touching it here would spawn a second tick loop.
		if msg.attachGen != m.attachGen {
			return m, nil // captured before an attach ran; stale (see home.attachGen)
		}
		if msg.instance.GetStatus() != session.Paused {
			msg.instance.ApplyPaneState(msg.state)
		}
		return m, nil
	case promptDeliveredMsg:
		// Delivery confirmed: pop the delivered head (matched dequeue, so a stale
		// confirmation can't wipe a newer queued prompt) and persist so the drained queue
		// survives a restart. Flash a confirmation so the user can tell delivered from
		// still-queued from lost.
		msg.instance.ClearPrompt(msg.prompt)
		if err := m.persistInstances(); err != nil {
			log.ErrorLog.Printf("failed to persist after prompt delivery: %v", err)
		}
		return m, m.handleInfoNotice(fmt.Sprintf("delivered to %q", msg.instance.DisplayName()))
	case promptDeferredMsg:
		// Soft outcome (pane not ready, or delivery unconfirmed): keep the prompt queued
		// and only release the in-flight guard so the next tick retries. SendPrompt is
		// idempotent, so the retry re-submits an already-staged prompt rather than doubling it.
		msg.instance.ClearPromptSending()
		return m, nil
	case promptSendErrorMsg:
		// A queued initial prompt that hard-failed to deliver (the session died after the
		// readiness gate passed). Retire it so the loop doesn't spin retrying a dead pane,
		// and surface the loss like the manual send path rather than leaving the session
		// Ready-but-idle with no sign the prompt was lost.
		msg.instance.ClearPrompt(msg.prompt)
		return m, m.handleError(fmt.Errorf("failed to deliver prompt to %q: %w", msg.instance.Title, msg.err))
	case tea.MouseMsg:
		return m.handleMouse(msg)
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
		return m.handleTargetValidityResult(msg)
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
	case agentsDetectedMsg:
		if m.state == stateWelcome && m.welcomeOverlay != nil {
			// The overlay is already sized by updateHandleWindowSizeEvent; just
			// install the detected agents (SetDetected sizes the picker to fit).
			m.welcomeOverlay.SetDetected(msg.profiles)
		}
		return m, nil
	case programCheckedMsg:
		// The returning-user program check finished off the main loop; warn if the
		// effective program isn't installed (one-shot, guarded by pathWarned).
		if !msg.installed {
			return m, m.warnMissingProgram(msg.program)
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
	case tea.BackgroundColorMsg:
		// The terminal answered the OSC 11 query (from Init, a refocus, or a detach).
		//
		// A reply whose colour did not PARSE is fed to the ladder as a NON-REPLY —
		// literally the nil that ResolveScheme's *bool means — rather than
		// short-circuited here. That is not a stylistic choice: it keeps one latch
		// instead of two, and the one it keeps is the one applyDetectedScheme already
		// has to have for every other caller.
		//
		// The case is easy to miss and fails the dangerous way. ultraviolet builds
		// this message from ansi.XParseColor, which returns nil for anything it
		// cannot read, and its isDarkColor(nil) answers TRUE — so a garbled reply
		// arrives looking like a confident "dark" and would flip a correctly detected
		// light terminal. Absence of evidence has to be spelled as absence.
		var bgIsDark *bool
		if msg.Color != nil {
			// IsDark is Bubble Tea's own luminance test on the reported colour, so
			// Atrium does not second-guess it.
			bgIsDark = boolPtrOf(msg.IsDark())
		}
		// COLORFGBG is passed as "" deliberately, even on the no-reply branch: this
		// is the OSC 11 rung, the startup rung already read the variable once, and
		// re-reading it here would let a stale value answer for a live query.
		return m, m.applyDetectedScheme(theme.ResolveScheme(bgIsDark, ""))
	case tea.FocusMsg:
		// The terminal regained focus: while focused, background sessions stay silent
		// (the user is watching the fleet). See maybeNotify.
		m.focused = true
		// Refocus is also when to re-ask the terminal's background colour. Atrium does
		// not enable mode 2031 (see app/scheme.go), so a scheme change while blurred —
		// which is when an OS-level dark/light switch usually happens — is invisible
		// until something asks. Nil unless theme: auto; m.focused is untouched either
		// way, so the notification gate is unaffected.
		return m, m.requestSchemeCmd()
	case tea.BlurMsg:
		// The terminal lost focus: edges may notify again.
		m.focused = false
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	case tea.PasteMsg:
		return m.handlePaste(msg)
	case tea.WindowSizeMsg:
		// A resize invalidates hint mode's frozen geometry; exit rather than
		// redraw stale coordinates (cheap and correct — scroll-mode pragmatism).
		if m.state == stateHints {
			m.exitHintMode()
		}
		// A window shrunk below the splash floor can't render the screensaver;
		// wake up rather than draw a degenerate field.
		if m.state == stateScreensaver && !ui.SplashFits(msg.Width, msg.Height) {
			m.dismissScreensaver()
		}
		m.updateHandleWindowSizeEvent(msg)
		// First launch ever: show the interactive welcome once the size is known
		// (its async detection cmd is returned); returning users get the
		// always-on missing-program check instead.
		return m, m.maybeShowWelcome()
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action. A carried notice (the
		// kill teardown's "U to undo") flashes alongside the refresh, because a
		// recovery nobody can see is not a recovery.
		if msg.notice != "" {
			return m, tea.Batch(m.instanceChanged(), m.flashNotice(msg.notice, ui.NoticeInfo))
		}
		return m, m.instanceChanged()
	case batchResumeDoneMsg:
		// A confirmed "resume all" finished off the UI thread. Persist here on the
		// Update loop (the action ran in a goroutine and must not read m.list). All-
		// success gets a transient notice; any failures go to a persistent modal the
		// user must read (it names which sessions didn't come back and why). Either
		// way, refresh the list so the now-Running rows reflect the restore.
		if msg.resumed > 0 {
			if err := m.persistInstances(); err != nil {
				log.WarningLog.Printf("batch resume: failed to persist resumed instances: %v", err)
			}
		}
		return m, m.finishBatch(nil, len(msg.failures) > 0,
			fmt.Sprintf("resumed %d session%s", msg.resumed, plural(msg.resumed)),
			msg.summary())
	case batchPauseDoneMsg:
		// A confirmed "pause all" finished off the UI thread. Persist here on the
		// Update loop (the action ran in a goroutine and must not read m.list), then
		// tear down each parked session's preview terminal on the main loop (single-
		// session pause does the same after Pause). All-success gets a transient
		// notice; any failures go to a persistent modal naming which sessions didn't
		// park and why. Either way, refresh the list so the now-Paused rows reflect
		// the park.
		if msg.paused > 0 {
			if err := m.persistInstances(); err != nil {
				log.WarningLog.Printf("batch pause: failed to persist paused instances: %v", err)
			}
		}
		return m, m.finishBatch(msg.pausedInstances, len(msg.failures) > 0,
			fmt.Sprintf("paused %d session%s", msg.paused, plural(msg.paused)),
			msg.summary())
	case runCommandDoneMsg:
		// A dev command started or stopped (#389); persist the flag and report.
		return m, m.applyRunCommandDone(msg)
	case killDoneMsg:
		// A single kill finished its I/O; apply the model half here.
		return m, m.applyKillDone(msg)
	case batchKillDoneMsg:
		// A confirmed batch kill finished its I/O. Apply every model change it implies
		// here, on the main loop — storage deletes, row removals, bookkeeping — then
		// report. All-success gets a transient notice; any failures go to a persistent
		// modal naming which sessions survived and why.
		msg = m.applyBatchKill(msg)
		return m, m.finishBatch(msg.killedInstances, len(msg.failures) > 0,
			batchKilledNotice(msg.killed, msg.undoable),
			msg.summary())
	case undoDoneMsg:
		// A confirmed undo finished off the UI thread. The rows, the persist and the
		// journal bookkeeping all land here, where touching m.list is safe.
		return m, m.handleUndoDone(msg)
	case asyncActionDoneMsg:
		// An off-UI-thread action (see beginAsyncAction) finished. Clear the
		// in-flight state and progress row on the main loop, then feed the inner
		// result back through the runtime so its own case handles it (a success
		// message, an error, or a harmless nil).
		m.actionInFlight = false
		// ClearBusy, not SetState: a background operation may still be running, and
		// its line should come back rather than being wiped by the action that was
		// covering it. (SetInstance corrects to Empty if the list emptied.)
		m.menu.ClearBusy(ui.BusyAction)
		m.recomputeLayout() // the progress row gave up its line; panes reclaim it
		result := msg.result
		return m, func() tea.Msg { return result }
	case backgroundActionDoneMsg:
		// A background operation finished. It never armed actionInFlight, so this
		// must not clear it — a modal action may be running concurrently, and
		// releasing its key gate mid-teardown is exactly the interleave the gate
		// exists to prevent.
		m.menu.ClearBusy(ui.BusyBackground)
		m.recomputeLayout()
		result := msg.result
		return m, func() tea.Msg { return result }

	case runCustomCommandMsg:
		// A confirmation approved this. The work starts here rather than in the
		// confirmed closure because that closure runs synchronously on the update
		// thread — see runCustomCommandMsg.
		return m, m.startCustomCommand(msg.spec)

	case checkpointsLoadedMsg:
		return m.handleCheckpointsLoaded(msg)

	case customCommandDoneMsg:
		return m.handleCustomCommandDone(msg)
	case customCommandTerminalDoneMsg:
		return m.handleCustomCommandTerminalDone(msg)
	case renameDoneMsg:
		if msg.err != nil {
			// The rename failed partway (tmux renamed but git did not, say). Reopen
			// the dialog with what the user typed, exactly as the validation-failure
			// path does, so neither the name nor the note is lost.
			m.renameTarget = msg.instance
			m.renameOverlay = overlay.NewRenameOverlay(msg.value, msg.note, false)
			m.state = stateRename
			return m, m.handleError(msg.err)
		}
		// Adopt the identity the I/O earned. This is the only writer of Title and
		// Branch, and it is on the update thread — the renderer reads Title unguarded.
		msg.instance.AdoptRename(msg.renamed)
		// The deep rename replaced the real title, so the cosmetic label must go or
		// it would keep shadowing it.
		msg.instance.SetDisplayName("")
		msg.instance.SetNote(msg.note)
		if err := m.persistInstances(); err != nil {
			return m, m.handleError(err)
		}
		return m, m.instanceChanged()
	case suggestionAcceptedMsg:
		if msg.err != nil {
			return m, m.handleError(fmt.Errorf("accept suggestion: %w", msg.err))
		}
		if !msg.accepted {
			return m, m.handleInfoNotice("agent isn't waiting on a prompt — nothing to approve or accept")
		}
		// Optimistic flip on the main thread: it updates the row glyph immediately
		// and turns a double-press into the guard notice instead of a second Enter.
		// Self-correcting — the next poll tick reclassifies the pane.
		msg.instance.SetStatus(session.Running)
		return m, m.handleInfoNotice(fmt.Sprintf("accepted suggestion — sent to '%s'", msg.instance.DisplayName()))
	case pushedMsg:
		// A confirmed push succeeded: acknowledge it and refresh so the create-PR
		// hint flips now that the branch is pushed (matching prCreatedMsg).
		return m, tea.Batch(
			m.handleInfoNotice("pushed changes"),
			m.instanceChanged(),
		)
	case pauseDoneMsg:
		// A single off-UI-thread pause finished: tear down + persist + open the note.
		return m, m.handlePauseDone(msg)
	case resumeDoneMsg:
		// A single off-UI-thread resume finished: persist/refresh, or drive recovery.
		return m, m.handleResumeDone(msg)
	case prMergedMsg:
		// A confirmed merge succeeded. Refresh so the PR badge reflects the merged
		// state on the next poll, then offer to clean up the finished session (#384) —
		// the offer's message announces the merge, so it replaces the plain notice.
		// Falls back to the notice when there is no session to clean up.
		refresh := m.instanceChanged()
		if m.offerCleanupAfterMerge(msg.instance, msg.number) {
			return m, refresh
		}
		return m, tea.Batch(
			m.handleInfoNotice(fmt.Sprintf("merged PR #%d", msg.number)),
			refresh,
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
		return m.handleAttachFinished(msg)
	case infoMsg:
		// An action requested a dismissible info modal (e.g. an actionable resume
		// error). Unlike handleError's transient box, this persists until dismissed.
		return m, m.showInfo(string(msg))
	case instanceStartedMsg:
		return m.handleInstanceStarted(msg)
	case spinner.TickMsg:
		// Let the loop die when no row is drawing a spinner frame; armSpinnerTick
		// revives it within a tick of one starting. Dropping the re-arm Cmd is what
		// stops the loop — the spinner model has no other off switch — so the flag
		// must be cleared in the same breath or nothing will ever restart it.
		if !m.spinnerAnimating() {
			m.spinnerTicking = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// finishBatch renders the shared Update-side outcome of a batch resume/pause/kill.
// It tears down the preview terminal of each torn-down session (cleanup is empty
// for resume, which only flips in-memory status and must keep its preview
// terminal), then either flashes the all-success notice or raises the failure
// modal, and always refreshes the list. notice and summary are precomputed by the
// caller so the three distinct summary() verbs stay intact.
func (m *home) finishBatch(cleanup []*session.Instance, hasFailures bool, notice, summary string) tea.Cmd {
	for _, inst := range cleanup {
		cleanupTerminalForInstance(m.tabbedWindow, inst)
		m.forgetInstance(inst) // drop the removed session's notify/recovery bookkeeping
	}
	if !hasFailures {
		return tea.Batch(m.handleInfoNotice(notice), m.instanceChanged())
	}
	return tea.Batch(m.showInfo(summary), m.instanceChanged())
}

// handleQuit is the key-initiated quit authority (q / ctrl+c from the list).
//
// It defers the exit while any session is still Loading (issue #268): a Loading
// session isn't yet in the persisted set (SaveInstances only keeps Started()
// instances), so quitting now would drop it — the agent would keep running
// invisibly on the tmux socket and reusing its title would later fail with
// "branch exists". Instead it arms quitRequested and lets handleInstanceStarted
// complete the quit (via resumeQuitAfterStart) once the in-flight Start finishes.
//
// Pressing quit again while still Loading escalates to a force-quit confirm: a
// wedged Start (a stuck git/tmux subprocess) would otherwise never send its
// completion, leaving the TUI unquittable. Confirming abandons the starting
// session; cancelling keeps waiting.
//
// On a persist failure it opens a confirm modal rather than trapping the user in
// an unquittable TUI: with a full disk / read-only data dir SaveState fails on
// every attempt, and the old code re-showed the error toast forever with no
// escape hatch. tea.Quit is itself a tea.Cmd, so it can be the confirm action
// directly — confirming feeds QuitMsg back through the runtime.
func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if m.anyLoading() {
		if m.quitRequested {
			// Second explicit quit while a session is still starting: the user is
			// insisting, so offer to abandon it rather than trap them behind an
			// in-flight (or wedged) Start. Its branch/worktree may be left behind,
			// hence the confirm.
			return m, m.confirmAction(
				"A session is still starting.\n\nQuit and abandon it? Its branch/worktree may be left behind.",
				instantAction, tea.Quit,
			)
		}
		m.quitRequested = true
		return m, m.handleInfoNotice(quitAfterStartupNotice)
	}
	m.quitRequested = false
	if err := m.persistInstances(); err != nil {
		return m, m.confirmAction(
			"Could not save state: "+err.Error()+"\n\nQuit anyway? Unsaved state will be lost.",
			instantAction, tea.Quit,
		)
	}
	return m, tea.Quit
}

// resumeQuitAfterStart completes a quit that was deferred while a session was
// Loading (issue #268); handleInstanceStarted calls it once an in-flight Start
// settles. It waits for every still-Loading sibling, and only exits from the
// default view: a start completes on a background message that Update dispatches
// regardless of m.state, so quitting unconditionally here would yank the app out
// from under an open overlay (settings, rename, the new-session form, a confirm).
// If the user has navigated into an overlay, the deferred quit is dropped rather
// than fired blind — an explicit q still exits once they return to the list.
//
// The bool reports whether the returned command is the model's next command
// (true), or the deferred quit was dropped and the caller should fall through to
// its normal post-start handling (false).
func (m *home) resumeQuitAfterStart() (tea.Cmd, bool) {
	if m.anyLoading() {
		// A sibling is still starting; keep the deferred quit armed.
		return m.handleInfoNotice(quitAfterStartupNotice), true
	}
	if m.state != stateDefault {
		m.quitRequested = false
		return nil, false
	}
	// Nothing left Loading and we're on the list: complete the quit. handleQuit
	// persists and exits, or opens the save-failure "Quit anyway?" modal.
	_, cmd := m.handleQuit()
	return cmd, true
}

// anyLoading reports whether any session is still in its Start phase. Such a
// session is on the list but not yet persisted, so quitting must wait for it (see
// handleQuit). session.Loading has a single producer (createSessionFromForm) and
// a single completion signal (instanceStartedMsg), so this covers the whole set.
func (m *home) anyLoading() bool {
	for _, inst := range m.list.GetInstances() {
		if inst.GetStatus() == session.Loading {
			return true
		}
	}
	return false
}

// drainTimeout bounds how long shutdown reconciliation waits for an in-flight
// Start goroutine to settle. On signal shutdown the goroutine's subprocesses are
// already SIGKILLed by the cancelled context and it unwinds in well under a
// second; on force-quit a genuinely in-progress `git worktree add` is itself
// capped at gitLocalTimeout (30s), and 5s comfortably covers a warm-repo worktree
// add plus tmux new-session while capping worst-case exit delay. The one thing in
// that goroutine with no bound of its own is the per-repo setup script (#389), which
// is why abortSetupScripts runs before this wait rather than trusting this number to
// outlast an `npm ci`. A Start still
// running past this is treated as wedged and left as-is (the orphan #281 already
// produced) rather than risking a data race by touching a live start. A var, not
// a const, so tests can shrink the wait.
var drainTimeout = 5 * time.Second

// drainStarts waits up to timeout for every in-flight Start goroutine (tracked by
// startWG) to return, reporting whether they all settled. It must be bounded: on
// ctx-cancel Bubble Tea may drop a queued start command without ever running it,
// so that goroutine's deferred Done never fires and a bare Wait would hang.
func (m *home) drainStarts(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		m.startWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// abortSetupScripts ends every per-repo setup script still running as the app exits
// (#389).
//
// A script deliberately has no timeout — `npm ci` on a cold cache legitimately runs for
// minutes. On a signal shutdown the cancelled lifecycle context has already ended it and
// this is a no-op; on the force-quit path that context is still live, so this is the
// only thing that does. Without it a create-path script holds the Start goroutine past
// drainStarts, so the session is "left as-is" with a worktree and branch that never
// reached state.json — and either way the script outlives the app that started it.
//
// Every instance, with no status filter, and called before the anyLoading() gate.
// Loading is the create path only: Resume runs a script too, and does it while the
// instance is still Paused (session/pause.go) from a goroutine startWG never learns
// about — so a status gate here quietly excluded the whole resume half of the feature,
// including a "resume all" running one script per session. AbortSetupScript is a no-op
// when nothing is running, which is what makes the unfiltered sweep the cheap option
// rather than the thorough one.
func (m *home) abortSetupScripts() {
	for _, inst := range m.list.GetInstances() {
		inst.AbortSetupScript()
	}
}

// reconcileInFlightStarts finishes or tears down a session that was still Loading
// when the Bubble Tea event loop exited — the two paths that bypass the graceful
// #268/#281 quit machinery: a signal shutdown (ctx cancelled, so Update never ran
// handleQuit and the completion message was dropped) and the force-quit escape
// (tea.Quit issued while a session was still starting). A graceful quit persists
// the start before quitting, so nothing is Loading and this no-ops.
//
// After joining the Start goroutine (so its tmux/git children are quiescent and
// safe to rebind), each still-Loading instance is:
//   - signal shutdown + Start completed -> adopted: flipped to Running and
//     persisted, so the daemon handoff / next launch keeps the session;
//   - signal shutdown + partial/failed  -> torn down, rebinding its children to a
//     WithoutCancel context first so Kill's git/tmux teardown isn't insta-killed
//     by the cancelled lifecycle context;
//   - ctx still live                     -> torn down (no rebind; Kill works as-is):
//     the force-quit abandon, or a rare non-signal event-loop error from p.Run().
//     Either way, clean it up rather than leave it orphaned.
//
// If the drain times out a Start is still running; touching it would race the
// goroutine, so it is left as-is — the same orphan the force-quit path produced
// before this fix (no regression, and no hang).
func (m *home) reconcileInFlightStarts(ctx context.Context) {
	m.abortSetupScripts()
	if !m.anyLoading() {
		return
	}
	if !m.drainStarts(drainTimeout) {
		log.WarningLog.Printf("shutdown: in-flight session start did not settle within %s; left as-is", drainTimeout)
		return
	}

	signalShutdown := ctx.Err() != nil
	adopted := false
	for _, inst := range m.list.GetInstances() {
		if inst.GetStatus() != session.Loading {
			continue
		}
		switch {
		case signalShutdown && inst.Started():
			// Start finished; only its completion message was dropped. Adopt it so
			// the daemon handoff / next launch keeps the session.
			inst.SetStatus(session.Running)
			if m.autoYes {
				inst.AutoYes = true
			}
			adopted = true
		case signalShutdown:
			// Partial/failed under the cancelled ctx: its own deferred Kill ran on
			// the dead ctx and couldn't clean up. Rebind to a live ctx and retry.
			inst.RebindBaseContext(context.WithoutCancel(ctx))
			if err := inst.Kill(); err != nil {
				log.WarningLog.Printf("shutdown: teardown of in-flight session %q: %v", inst.Title, err)
			}
		default:
			// Ctx still live: the force-quit abandon, or a rare non-signal event-loop
			// error from p.Run(). Kill's teardown works as-is, no rebind needed.
			if err := inst.Kill(); err != nil {
				log.WarningLog.Printf("exit: teardown of in-flight session %q: %v", inst.Title, err)
			}
		}
	}

	if adopted {
		if err := m.persistInstances(); err != nil {
			log.WarningLog.Printf("shutdown: failed to persist adopted session(s): %v", err)
		}
	}
}

// handlePaste delivers a bracketed paste to the focused text surface.
//
// v1 had no paste message: a paste arrived as an ordinary KeyMsg whose Runes were
// the pasted text, so it flowed through the normal key dispatch into whatever had
// focus. v2 gives paste its own type — which means it stopped reaching that
// dispatch entirely, and pasting into the new-session form silently did nothing.
// Nothing failed to compile, because nothing had ever named the v1 Paste flag.
//
// The fix is not to convert the paste back into a key. v1 could afford to let one
// flow through dispatch because its Key.String() wrapped pasted runes in "[...]"
// specifically so a paste could never match a binding; v2's String() returns the
// text verbatim, so a synthesized key is indistinguishable from a keypress at
// every `switch msg.String()` site. That gap is not theoretical: a clipboard
// holding "q" would quit the app with no confirmation, and one holding the word
// "esc" would cancel the create form and discard the draft.
//
// So paste gets its own routing, and the states below are the enumeration of
// where text can land. Anywhere else — the list, the rail, a confirmation, hint
// mode — a paste is inert, because there is nothing there for text to mean. That
// is the property v1's brackets bought, reached without borrowing the mechanism.
func (m *home) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if msg.Content == "" {
		return m, nil
	}

	// The nil checks are the belt to the state's braces: a state and its overlay are
	// set together, so each is unreachable — but paste is the one input that can
	// arrive without a keystroke to precede it, and a dropped paste beats a crash.
	switch m.state {
	case statePrompt:
		if m.textInputOverlay == nil {
			return m, nil
		}
		return m.handlePromptPaste(msg)

	case stateFilter:
		// The list owns the query; a paste extends it exactly as typing does.
		m.list.SetFilter(m.list.FilterQuery() + msg.Content)
		return m, m.instanceChanged()

	case stateRename:
		if m.renameOverlay != nil {
			m.renameOverlay.HandlePaste(msg)
		}

	case stateCommandPalette:
		if m.commandPaletteOverlay != nil {
			m.commandPaletteOverlay.HandlePaste(msg.Content)
		}

	case stateSettings:
		if m.settingsOverlay != nil {
			m.settingsOverlay.HandlePaste(msg)
		}

	case stateAccounts:
		if m.accountsOverlay != nil {
			m.accountsOverlay.HandlePaste(msg)
		}
	}
	return m, nil
}

// handlePromptPaste routes a paste inside the create/quick-send/compose overlay to
// the focused field, then runs the same follow-up an edit owes (branch search,
// title verdict). It cannot close the overlay, so the submit/cancel arms of
// handlePromptState have no counterpart here.
func (m *home) handlePromptPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	prevTitle := ""
	if m.textInputOverlay.IsCreateForm() {
		prevTitle = m.textInputOverlay.GetTitle()
	}
	branchFilterChanged := m.textInputOverlay.HandlePaste(msg)
	// The diff-comment composer is the same overlay with a different submit path;
	// a paste only ever edits its textarea, so it needs no separate routing.
	return m.afterPromptEdit(prevTitle, branchFilterChanged)
}

func (m *home) handleKeyPress(msg tea.KeyPressMsg) (mod tea.Model, cmd tea.Cmd) {
	// Ctrl+L forces a full repaint. The alt-screen renderer updates incrementally and
	// never erases lines, so it desyncs (leaving accumulating ghost rows) if the terminal
	// ever renders a line wider than measured — e.g. a font lacking a combined emoji glyph.
	// theme.SanitizeWidth prevents the known cases; this is the universal manual-redraw
	// escape hatch for any residual artifact, in any state.
	if msg.String() == "ctrl+l" {
		return m, tea.ClearScreen
	}

	// The screensaver dismisses on any key, and the key is consumed — a stray
	// 'n' (or 'q') must wake the screen, not open the new-session form or quit.
	// Runs before every other state handler; only ctrl+l above bypasses it, so
	// a repaint doesn't tear the screensaver down.
	if m.state == stateScreensaver {
		m.dismissScreensaver()
		return m, nil
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateWelcome {
		return m.handleWelcomeState(msg)
	}

	if m.state == stateInfo {
		return m.handleInfoState(msg)
	}

	if m.state == statePrompt {
		return m.handlePromptState(msg)
	}

	if m.state == stateHistory {
		return m.handleHistoryState(msg)
	}

	if m.state == stateConfirm {
		return m.handleConfirmState(msg)
	}

	// Rename must run before the global q/ctrl+c quit handling below so those keys
	// edit (or cancel) the label instead of quitting the app.
	if m.state == stateRename {
		return m.handleRenameState(msg)
	}

	if m.state == stateQueue {
		return m.handleQueueState(msg)
	}

	if m.state == stateCmdLog {
		return m.handleCmdLogState(msg)
	}

	// The checkpoint timeline must run before the global quit handling too: r
	// reloads it here rather than resuming a paused session, and q must be swallowed
	// rather than quit the app out from under an open box (as in the queue overlay,
	// esc is what closes).
	if m.state == stateCheckpoints {
		return m.handleCheckpointsState(msg)
	}

	// The palette, like the other overlay states, must run before the global quit
	// handling so that q and every other printable key narrows the filter instead
	// of quitting the app mid-query.
	if m.state == stateCommandPalette {
		return m.handleCommandPaletteState(msg)
	}

	// The custom-commands menu must run before the global quit handling for the same
	// reason, and more sharply: its rows are keyed by whatever the user configured,
	// so q really can be a command key here.
	if m.state == stateCustomCommands {
		return m.handleCustomCommandsState(msg)
	}

	// Settings, like the other overlay states, must run before the global quit
	// handling so q/esc and printable keys reach the panel.
	if m.state == stateSettings {
		return m.handleSettingsState(msg)
	}

	// Accounts, like the other overlay states, must run before the global quit
	// handling so q/esc and printable keys reach the panel.
	if m.state == stateAccounts {
		return m.handleAccountsState(msg)
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

	// Multi-select (visual) mode: space marks, lifecycle keys act on the marked
	// set, esc exits. Must run before the global esc/quit handling below so esc
	// clears the marks (not the filter) and q never quits.
	if m.state == stateVisual {
		return m.handleMultiSelectState(msg)
	}

	// Diff-comment mode: the line cursor moves with j/k, enter opens the composer,
	// esc exits. Must run before the global esc/quit handling below so esc leaves
	// comment mode (not the app) and q never quits.
	if m.state == stateDiffComment {
		return m.handleDiffCommentState(msg)
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Code == tea.KeyEsc {
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
		// Focus mode hides the list; Esc backs out to the preset that preceded it
		// so focus is never a dead end (the layout key instead cycles onward). This
		// is the last Esc branch: it only fires once scroll mode and any filter are
		// already cleared, matching what a user expects a repeated Esc to unwind.
		if m.listHidden() {
			return m, m.exitFocusLayout()
		}
	}

	// ctrl+c quits from anywhere the overlay states above have not already
	// claimed it. It is matched literally, and stays that way: no Registry entry
	// claims it, it is the terminal's universal abort, and it is the escape hatch
	// that must keep working when a rebind has gone wrong. The quit *key* is a
	// registered action and resolves through the dispatch map below like any
	// other, so rebinding it moves the key and every surface that names it
	// together.
	if msg.String() == "ctrl+c" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	// While an action runs off the UI thread, keys stay live (unlike the old
	// synchronous freeze). Allow only navigation/scroll/view keys through; swallow
	// every per-session mutating key and overlay-opener with a busy notice. This
	// closes two windows the freeze used to cover: driving tmux/git on the very
	// instance an in-flight Pause is tearing down (e.g. attach), and opening an
	// overlay that a completion handler (pause → rename) would then clobber.
	// ctrl+c and ctrl+l are handled above, and quit is on the allowlist below, so
	// a wedged action stays escapable.
	if m.actionInFlight && !keyAllowedWhileBusy(name) {
		// Fix the SENTENCE for a missing label rather than inventing copy for it:
		// "busy — " only parses because the label is a gerund, so with no label the
		// dash dangled at the user. "busy" alone is at least true. The required
		// label parameter makes this unreachable in practice; it is the belt to
		// that braces.
		notice := "busy"
		if label := m.menu.BusyText(); label != "" {
			notice = "busy — " + label
		}
		return m, m.handleInfoNotice(notice)
	}

	return m.dispatchAction(name)
}

// dispatchAction runs the action name identifies, against the current selection
// and state. It is the one place a KeyName becomes work, so a caller that is not
// a keypress — the command palette (#374) — runs exactly what the key runs, and
// TestEveryRegistryActionHasADispatchCase can prove every registered action has
// somewhere to land.
//
// The prelude stays in handleKeyPress: state routing, esc's contextual roles,
// quit, and the actionInFlight gate all run before a name is even resolved. A
// caller reaching this directly bypasses all of them and must re-apply whichever
// it needs (the palette re-checks the busy gate; see runPaletteAction).
func (m *home) dispatchAction(name keys.KeyName) (tea.Model, tea.Cmd) {
	switch name {
	case keys.KeyHelp:
		// The cheatsheet is the only site that populates commands: the user's own
		// verbs are config, so they cannot come from the keys registry the rest of
		// the screen is projected from.
		return m.showHelpScreen(helpTypeGeneral{commands: m.customCommands}, nil)
	case keys.KeySettings:
		if key := m.noticeSettingKey; key != "" {
			m.noticeSettingKey = ""
			return m, m.openSettingsAt(key)
		}
		return m, m.openSettings()
	case keys.KeyAccounts:
		return m, m.openAccounts()
	case keys.KeyScreensaver:
		// The full-window splash easter egg. Silently ignored when the window
		// is below the splash floor (nothing legible to show).
		//
		// Deliberately absent from keyAllowedWhileBusy, unlike the other
		// read-only view key (KeyHelp): this one blanks the whole frame, and
		// the frame is where an in-flight action's busy text and spinner live.
		// Hiding the feedback for work the user is waiting on is worth the
		// busy notice, even though the screensaver itself touches no tmux/git.
		if !ui.SplashFits(m.windowWidth, m.windowHeight) {
			return m, nil
		}
		// Random mode shows a fresh pattern each time; a pinned config keeps its
		// pick. Arm the animation loop directly — the splash isn't waiting for
		// the preview tick to notice it.
		ui.RerollSplashVariant()
		m.state = stateScreensaver
		return m, m.armSplashTick()
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
		return m, tea.RequestWindowSize
	case keys.KeyQuickSend:
		return m.openQuickSend()
	case keys.KeyDiffComment:
		return m.enterDiffComment()
	case keys.KeyQueue:
		return m.openQueue()
	case keys.KeyCmdLog:
		return m.openCmdLog()
	case keys.KeyCheckpoints:
		return m.openCheckpoints()
	case keys.KeyCommandPalette:
		return m.openCommandPalette()
	case keys.KeyCustomCommands:
		return m.openCustomCommands()
	case keys.KeyApprove:
		return m.approveSelected()
	case keys.KeyRunCommand:
		return m.toggleRunCommand()
	case keys.KeyCopyContent:
		return m.copyPaneContent()
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
		return m, m.adjustListCols(-listColStep)
	case keys.KeyGrowList:
		return m, m.adjustListCols(+listColStep)
	case keys.KeyLayoutPreset:
		// One key steps the named layout presets (monitor → default → review →
		// focus → wrap); the active preset's name flashes on the notice row.
		return m, m.cycleLayoutPreset()
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		return m, m.tabChanged()
	case keys.KeyShiftTab:
		m.tabbedWindow.ToggleReverse()
		return m, m.tabChanged()
	case keys.KeyTabPreview, keys.KeyTabDiff, keys.KeyTabTerminal:
		// Direct tab jump by number, complementing Tab/Shift+Tab cycling. The
		// three KeyNames are consecutive, so the offset from KeyTabPreview is the
		// tab index (PreviewTab/DiffTab/TerminalTab are likewise 0/1/2).
		m.tabbedWindow.SetActiveTab(int(name - keys.KeyTabPreview))
		return m, m.tabChanged()
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
	case keys.KeyMultiSelect:
		return m.enterMultiSelect()
	case keys.KeyRename:
		return m.openRenameSelected()
	case keys.KeyAutoName:
		return m.startAutoNameSelected()
	case keys.KeyMute:
		return m.toggleMuteSelected()
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
		// J/K reorders within a repo group; only a within-group status sort owns that
		// order. Account grouping leaves J/K available (clustering never touches
		// within-block order), so the hint names only the sort.
		//
		// The hint scopes itself to the *session* ladder because the sort disables only
		// that one — { / } and [ / ] stay live (ui.List.AccountReorderEnabled) — while
		// "manual" is the settings screen's word for group order too ("Group order stays
		// manual ({ / })", config.SessionSortCreation). An unscoped "manual reorder is
		// off" therefore contradicted what the user just read there (#346). "session"
		// matches the ladder hiddenNeighborNotice names, and , opens the setting that
		// lifts this — the same key the [ / ] refusal below points at.
		if !m.list.SessionReorderEnabled() {
			return m, m.settingNotice(
				"session reorder is off while sorting by status (, to switch)",
				ui.NoticeInfo, "session_sort")
		}
		// Refuse a swap with a sibling that is not on screen, and say so: the order would
		// change, and persist, with nothing visibly moving (#339). Checked after the sort
		// guard, whose reason a cleared filter would not lift.
		up := name == keys.KeyMoveUp
		if m.list.MoveNeighborHidden(up) {
			return m, m.handleInfoNotice(m.hiddenNeighborNotice("session"))
		}
		if up {
			return m.moveAndPersist(m.list.MoveUp)
		}
		return m.moveAndPersist(m.list.MoveDown)
	case keys.KeyMoveGroupUp, keys.KeyMoveGroupDown:
		// Whole-group moves work within an account cluster; a move across an account
		// boundary is refused (clustering owns cross-account block order), so explain
		// that rather than leaving a silent no-op (mirroring the J/K feedback above)
		// and point at the key that does move the cluster.
		up := name == keys.KeyMoveGroupUp
		if m.list.GroupMoveCrossesAccount(up) {
			return m, m.handleInfoNotice("group reorder stays within an account — [ / ] moves the cluster")
		}
		// A block the filter has emptied renders nothing, so the transpose would be
		// invisible (#339). Checked after the account boundary, which a cleared filter
		// would not lift — and whose advice ([ / ]) is itself off while filtering.
		if m.list.GroupMoveNeighborHidden(up) {
			return m, m.handleInfoNotice(m.hiddenNeighborNotice("group"))
		}
		if up {
			return m.moveAndPersist(m.list.MoveGroupUp)
		}
		return m.moveAndPersist(m.list.MoveGroupDown)
	case keys.KeyMoveAccountUp, keys.KeyMoveAccountDown:
		// [ / ] reorder whole account clusters. Both refusals are explained rather than
		// left as a silent no-op, and they are told apart because the advice differs: one
		// is a mode to switch, the other is nothing to reorder. Neither is simply "you only
		// have one account" — a repo whose sessions span accounts still renders as a single
		// cluster — so both name the cluster, not the account count, which is also the
		// ladder word help and the settings label use ("account cluster") (#346).
		if !m.list.AccountGrouped() {
			return m, m.settingNotice(
				"cluster reorder needs account grouping (, to switch)",
				ui.NoticeInfo, "group_mode")
		}
		if !m.list.AccountReorderEnabled() {
			return m, m.handleInfoNotice("only one account cluster to reorder")
		}
		// A cluster the filter has emptied renders nothing, so the swap would rewrite the
		// stored order with the list standing still (#339) — the form the issue confirmed
		// live. Checked last: neither guard above is lifted by clearing the filter.
		if m.list.AccountMoveNeighborHidden(name == keys.KeyMoveAccountUp) {
			return m, m.handleInfoNotice(m.hiddenNeighborNotice("cluster"))
		}
		moved := m.list.MoveAccountUp
		if name == keys.KeyMoveAccountDown {
			moved = m.list.MoveAccountDown
		}
		if !moved() {
			return m, nil
		}
		// The cluster order is a stored preference, not the session order, so it
		// persists to state — moveAndPersist would save the (unchanged) instance array.
		if err := m.appState.SetAccountOrder(m.list.AccountOrder()); err != nil {
			return m, m.handleError(err)
		}
		return m, m.instanceChanged()
	case keys.KeyCollapse, keys.KeyExpand, keys.KeyCollapseAll:
		return m.foldKey(name)
	case keys.KeyResume:
		return m.resumeSelectedKey()
	case keys.KeyResumeAll:
		return m, m.resumeAll()
	case keys.KeyUndoKill:
		return m, m.undoLastKill()
	case keys.KeyPauseAll:
		return m, m.pauseAll()
	case keys.KeyEnter, keys.KeyAttachToggle:
		return m.attachSelected()
	case keys.KeyQuit:
		// Reached both ways: by the quit key, which now resolves through the
		// dispatch map like every other action so a rebind moves it, and by name
		// from the command palette. It used to be palette-only, with "q" matched
		// literally in handleKeyPress's prelude — which meant the one action whose
		// key a user is most likely to want back was the one the registry did not
		// actually own. ctrl+c is still matched there, and is the escape hatch if
		// this key is ever bound somewhere unreachable.
		return m.handleQuit()
	default:
		return m, nil
	}
}

// dispatchExempt names the registered actions that deliberately have no case in
// dispatchAction, and why. An exemption is a claim that the action is handled
// somewhere else — never that it is unhandled — and
// TestEveryDispatchExemptionIsRealAndReasoned rejects the three ways that claim
// could rot. The command palette reads it too: an action it cannot run by name is
// an action it must not offer.
var dispatchExempt = map[keys.KeyName]string{
	keys.KeyToggleMark: "consumed only by handleMultiSelectState; space marks a row in " +
		"multi-select mode and does nothing in the default state (keys.go says so too)",
}

// keyAllowedWhileBusy reports whether a key may act while an off-UI-thread action
// is in flight (see the guard in handleKeyPress). The allowlist is deliberately
// narrow: pure navigation, scrolling, pane sizing, tab switching, list collapse,
// and help — nothing that mutates a session, opens an overlay, or drives tmux/git.
//
// KeyUndoKill's absence is load-bearing rather than an oversight: this gate is the
// only thing making an undo single-flight. Two presses before the restore returns
// would run the same record twice, and the second run would recreate a branch and
// a worktree the first one has already claimed.
//
// KeyQuit is here for the opposite reason. It used to bypass the gate by being
// matched literally before the dispatch lookup; now that it resolves like every
// other action, swallowing it with a "busy" notice would leave a wedged action
// with no way out but ctrl+c.
func keyAllowedWhileBusy(name keys.KeyName) bool {
	switch name {
	case keys.KeyQuit,
		keys.KeyHelp,
		keys.KeyUp, keys.KeyDown, keys.KeyNextUnread, keys.KeyNextNeedsInput,
		keys.KeyShiftUp, keys.KeyShiftDown, keys.KeyShrinkList, keys.KeyGrowList,
		keys.KeyLayoutPreset,
		keys.KeyTab, keys.KeyShiftTab, keys.KeyTabPreview, keys.KeyTabDiff, keys.KeyTabTerminal,
		keys.KeyCollapse, keys.KeyExpand, keys.KeyCollapseAll,
		// Copying reads cached content and writes the clipboard; it mutates no
		// session state, so there is nothing for an in-flight action to race.
		keys.KeyCopyContent:
		return true
	default:
		return false
	}
}

// openSettings opens the configuration panel on the rail entry the last open left it on.
// Returns the command that re-sizes it; every caller is a key handler that returns it
// straight through.
func (m *home) openSettings() tea.Cmd {
	m.state = stateSettings
	m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
	if m.settingsRail != nil {
		m.settingsOverlay.SetRailIndex(*m.settingsRail)
	}
	m.refreshSettingsClusteringGate()
	m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
	return tea.RequestWindowSize
}

// openSettingsAt opens the configuration panel focused on one row — the deep link of spec
// §12. It falls back to the remembered rail when the key is unknown, so a stale caller
// degrades to today's behavior rather than opening a panel with an invisible cursor.
func (m *home) openSettingsAt(key string) tea.Cmd {
	cmd := m.openSettings()
	m.settingsOverlay.OpenAt(key)
	return cmd
}

// openAccounts opens the Claude/GitHub/Antigravity account manager — the '@' key's overlay,
// and the surface the settings rail's Accounts entry hands off to.
func (m *home) openAccounts() tea.Cmd {
	m.state = stateAccounts
	m.accountsOverlay = overlay.NewAccountsOverlay(m.appConfig, m.appState)
	m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
	return tea.RequestWindowSize
}

// handleInfoState dismisses the info modal on any key press (scroll keys
// scroll first while the text overflows, exactly like the help state).
func (m *home) handleInfoState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.textOverlay.HandleKeyPress(msg) {
		return m.closeTextOverlay()
	}
	return m, nil
}
