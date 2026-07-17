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
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// wheelScrollLines is how many lines one mouse-wheel notch scrolls the preview
// pane in scroll mode. A notch moves several lines for a fluid feel; the
// keyboard scroll keys move one line for precise positioning.
const wheelScrollLines = 3

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
	case splashTickMsg:
		return m.handleSplashTick()
	case autoNameDoneMsg:
		m.generatingName = false
		if msg.err != nil {
			// The progress row goes away and we return to plain navigation; surface the
			// failure and leave the name untouched rather than applying a junk fallback.
			// Don't clobber a concurrent "busy" row: if an action is in flight, leave
			// StateBusy in place (asyncActionDoneMsg restores StateDefault later).
			if !m.actionInFlight {
				m.menu.SetState(ui.StateDefault)
			}
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
		return m.handleSmartDispatchDone(msg)
	case metadataUpdateDoneMsg:
		// Drop results captured before a terminal attach ran (see home.attachGen):
		// the keeper may have advanced those panes mid-attach, so replaying a stale
		// PanePrompt would tap whatever dialog is up now. The post-detach sweep
		// re-polls everything, so nothing is lost — but the tick must still re-arm.
		var cmds []tea.Cmd
		if msg.attachGen == m.attachGen {
			recoveries := recoverLostInstances(msg.results, m.lostStrikes)
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
			m.applyOSChrome(len(recoveries) > 0)
		}
		m.metadataTick++
		fullSweep := m.metadataTick%metadataFullSweepEvery == 0
		// Stop the self-chaining tick once the app context is cancelled (shutdown):
		// re-arming would only spawn a Cmd that immediately returns on ctx.Done().
		if m.ctx.Err() == nil {
			cmds = append(cmds, tickUpdateMetadataCmd(m.ctx, m.snapshotActiveInstances(), m.list.GetSelectedInstance(), fullSweep, m.attachGen))
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
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		// A resize invalidates hint mode's frozen geometry; exit rather than
		// redraw stale coordinates (cheap and correct — scroll-mode pragmatism).
		if m.state == stateHints {
			m.exitHintMode()
		}
		// A window shrunk below the splash floor can't render the screensaver;
		// wake up rather than draw a degenerate field.
		if m.state == stateScreensaver && !ui.SplashFits(msg.Width, msg.Height) {
			m.state = stateDefault
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
		// Handle instance changed after confirmation action
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
	case batchKillDoneMsg:
		// A confirmed batch kill finished. Tear down each killed session's preview
		// terminal on the main loop (single-session kill does the same). All-success
		// gets a transient notice; any failures go to a persistent modal naming which
		// sessions survived and why. Either way, refresh the list so the now-gone rows
		// disappear.
		return m, m.finishBatch(msg.killedInstances, len(msg.failures) > 0,
			fmt.Sprintf("killed %d session%s", msg.killed, plural(msg.killed)),
			msg.summary())
	case asyncActionDoneMsg:
		// An off-UI-thread action (see beginAsyncAction) finished. Clear the
		// in-flight state and progress row on the main loop, then feed the inner
		// result back through the runtime so its own case handles it (a success
		// message, an error, or a harmless nil).
		m.actionInFlight = false
		m.menu.SetState(ui.StateDefault) // SetInstance corrects to Empty if the list emptied
		m.recomputeLayout()              // the progress row gave up its line; panes reclaim it
		result := msg.result
		return m, func() tea.Msg { return result }
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
				tea.Quit,
			)
		}
		m.quitRequested = true
		return m, m.handleInfoNotice(quitAfterStartupNotice)
	}
	m.quitRequested = false
	if err := m.persistInstances(); err != nil {
		return m, m.confirmAction(
			"Could not save state: "+err.Error()+"\n\nQuit anyway? Unsaved state will be lost.",
			tea.Quit,
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
// add plus tmux new-session while capping worst-case exit delay. A Start still
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

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
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
		m.state = stateDefault
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
		// Focus mode hides the list; Esc backs out to the preset that preceded it
		// so focus is never a dead end (the layout key instead cycles onward). This
		// is the last Esc branch: it only fires once scroll mode and any filter are
		// already cleared, matching what a user expects a repeated Esc to unwind.
		if m.listHidden() {
			return m, m.exitFocusLayout()
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

	// While an action runs off the UI thread, keys stay live (unlike the old
	// synchronous freeze). Allow only navigation/scroll/view keys through; swallow
	// every per-session mutating key and overlay-opener with a busy notice. This
	// closes two windows the freeze used to cover: driving tmux/git on the very
	// instance an in-flight Pause is tearing down (e.g. attach), and opening an
	// overlay that a completion handler (pause → rename) would then clobber. Quit
	// and ctrl+l are handled above, so a wedged action stays escapable.
	if m.actionInFlight && !keyAllowedWhileBusy(name) {
		return m, m.handleInfoNotice("busy — " + m.menu.BusyText())
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeySettings:
		m.state = stateSettings
		m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
		m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
		return m, tea.WindowSize()
	case keys.KeyAccounts:
		m.state = stateAccounts
		m.accountsOverlay = overlay.NewAccountsOverlay(m.appConfig)
		m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
		return m, tea.WindowSize()
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
		return m, tea.WindowSize()
	case keys.KeyQuickSend:
		return m.openQuickSend()
	case keys.KeyQueue:
		return m.openQueue()
	case keys.KeyCmdLog:
		return m.openCmdLog()
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
		return m, m.adjustListCols(-listColStep)
	case keys.KeyGrowList:
		return m, m.adjustListCols(+listColStep)
	case keys.KeyLayoutPreset:
		// One key steps the named layout presets (monitor → default → review →
		// focus → wrap); the active preset's name flashes on the notice row.
		return m, m.cycleLayoutPreset()
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
	case keys.KeyMultiSelect:
		return m.enterMultiSelect()
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
			return m, m.handleInfoNotice("session reorder is off while sorting by status (, to switch)")
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
			return m, m.handleInfoNotice("cluster reorder needs account grouping (, to switch)")
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

// keyAllowedWhileBusy reports whether a key may act while an off-UI-thread action
// is in flight (see the guard in handleKeyPress). The allowlist is deliberately
// narrow: pure navigation, scrolling, pane sizing, tab switching, list collapse,
// and help — nothing that mutates a session, opens an overlay, or drives tmux/git.
func keyAllowedWhileBusy(name keys.KeyName) bool {
	switch name {
	case keys.KeyHelp,
		keys.KeyUp, keys.KeyDown, keys.KeyNextUnread, keys.KeyNextNeedsInput,
		keys.KeyShiftUp, keys.KeyShiftDown, keys.KeyShrinkList, keys.KeyGrowList,
		keys.KeyLayoutPreset,
		keys.KeyTab, keys.KeyShiftTab, keys.KeyTabPreview, keys.KeyTabDiff, keys.KeyTabTerminal,
		keys.KeyCollapse, keys.KeyExpand, keys.KeyCollapseAll:
		return true
	default:
		return false
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
