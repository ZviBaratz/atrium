package app

// Error and transient-notice feedback for the home model.

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/internal/parkreport"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
)

// hideErrMsg implements tea.Msg and clears the transient toast (menu notice or
// error box). gen identifies which toast the timer belongs to: a stale timer's
// message must not clear a newer toast.
type hideErrMsg struct {
	gen int
}

// infoMsg requests a dismissible information modal carrying actionable text.
// Confirmation-action callbacks return it to surface a message that must persist
// until the user dismisses it, instead of the auto-hiding transient error box.
type infoMsg string

// errToastDuration is how long the transient error box stays before auto-hiding.
const errToastDuration = 5 * time.Second

// handleError surfaces an error in the UI. Short, single-line errors get a
// transient toast (auto-hidden after errToastDuration): when the always-on hint
// bar is up, the toast rides the bar's reserved row so the layout never shifts;
// otherwise it falls back to the error box's own row. An error that a one-line
// toast cannot actually convey — multi-line, or wider than the row can show
// (e.g. a failed push's git output) — is routed to the persistent info modal
// instead, but only from stateDefault: in any overlay state (e.g. a form
// validation error) switching to stateInfo would clobber the open overlay, so
// those always use the toast.
func (m *home) handleError(err error) tea.Cmd {
	if m.state == stateDefault && !m.errBox.Fits(err) {
		return m.showInfo(err.Error()) // showInfo logs the message itself
	}
	log.ErrorLog.Printf("%v", err)
	return m.flashNotice(err.Error(), ui.NoticeError)
}

// persistInstances writes the current instance list to disk. It is the single
// chokepoint for the SaveInstances pattern; callers choose how to surface the error
// (m.handleError for user-driven actions, log for bulk/background paths). It saves
// the canonical manual order (InstancesForPersist), so an active sort mode never
// overwrites the user's manual ordering on disk.
func (m *home) persistInstances() error {
	saved := m.list.InstancesForPersist()
	if err := m.storage.SaveInstances(saved); err != nil {
		return err
	}
	// A save that lands is what makes an "the session exists and nothing records it"
	// disclosure false — but only for the rows it actually wrote, which is why the list goes
	// with it rather than the bare fact of success (#731, #732).
	m.withdrawUnrecordedCreates(saved)
	return nil
}

// moveAndPersist runs a list-reorder closure; if it changed the order it persists
// and refreshes the selected session's preview. A persist failure is surfaced; a
// no-op move is a clean no-op.
func (m *home) moveAndPersist(move func() bool) (tea.Model, tea.Cmd) {
	if !move() {
		return m, nil
	}
	if err := m.persistInstances(); err != nil {
		return m, m.handleError(err)
	}
	return m, m.instanceChanged()
}

// --- The confirmation and refusal voice (#399) --------------------------------
//
// One rule governs every confirmation dialog and every refusal notice in Atrium:
// ask with the verb, then name what the user cannot see — and only then.
//
// *Ask with the verb.* The question leads with the action in the user's words
// ("Pause 3 marked sessions?", "Merge PR #12 from 'x' as a squash merge?"), and the
// key hint names that same verb rather than the generic "confirm" (see
// overlay.ConfirmationOverlay.SetConfirmLabel — hint text only; the keys stay
// y / n / esc).
//
// *Name what the user cannot see.* A parenthetical follows only for a consequence or
// a blocker that is off-screen at the moment of asking: the work a kill would destroy
// (killDataWarning), the gitignored files a pause deletes (pauseConfirmMessage), CI
// still running behind a merge, the host capacity a create (overCapMessage) or a
// resume (resumeCapClause) would exceed, the commit and the browser tab a push makes
// on the way (pushConsequenceClause). Nothing invisible, no parenthetical: create-PR
// does exactly what it says, because the branch was already pushed before the key was
// even offered (PRStatus.CreateBlockedReason), so it gains a verb label and nothing
// else.
//
// Push used to be listed there beside create-PR, and it was wrong: the audit that
// wrote this rule read "Push … ?" as naming its destination when it names its SOURCE,
// and passed over the two effects above. A dialog that hides something is not
// identified by how complete its sentence sounds (#469).
//
// The converse is #399's amended AC #2, which the refusal notices obey: a refusal the
// user can SEE stays silent — a cluster already at the top of the order, a lone repo
// group with nothing to fold — because toasting it is noise, while "already folded"
// under a live filter describes a group rendering expanded and must be explained
// (#448). One rule read from both ends: dialogs add what is invisible, notices skip
// what is visible.
//
// The rule prunes itself, which is the evidence it is real. Applied across every
// confirmation in the app it rewrote three messages — batch pause, batch resume, and
// the merge caveat — and left the rest as they were: kill (single and batch),
// cleanup-after-merge, the quit pair, over-cap create, all-accounts-exhausted, the
// branch-busy resume, and create-PR. Push was in that untouched list and should not
// have been; #469 rewrote it later. The kill dialog is not
// consequence-*first* and was never meant to be — it is question-first with a
// risk-only parenthetical, and that shape is what the others adopted.

// hiddenNeighborNotice explains a reorder refused because the thing it would swap with
// is not on screen — the move would change, and persist, an order with nothing visibly
// moving (#339). scope names what the key moves ("session", "group", "cluster") — the
// one ladder every reorder notice names its scope by, which the sibling refusals in
// handleKeyPress share (#346).
//
// These sibling notices lead with the scope ("group reorder stays within an account")
// while this one leads with the verb, and that difference is deliberate: they state a
// standing rule, whose scope must be named or it over-claims (the bug #346 fixed in the
// status-sort hint), whereas this reports one refused move, whose scope the keypress
// already fixed and the object noun echoes. Scope and object are one-to-one here, so
// naming both would only repeat the sentence back to the user.
//
// The filter is named whenever one is live, because it overrides a fold in the render
// (see ui.List.isHidden): a folded group under a filter is on screen expanded, so blaming
// the fold would describe something the user cannot see — and following that advice would
// persist an expand while the reorder stayed refused. Only a filter can empty a whole
// group or cluster (a folded block still renders its header), so the fold half is
// reachable only for a session.
func (m *home) hiddenNeighborNotice(scope string) string {
	if m.list.Filtering() {
		return "reorder won't swap past a filter-hidden " + scope + " (esc to clear)"
	}
	return "reorder won't swap past a folded " + scope + " (→ to expand)"
}

// showMenuNotice shows a transient toast on the hint bar's reserved row when that
// row is available, returning the command that auto-hides it; it returns nil (showing
// nothing) when it isn't — a modal overlay owns the screen (menuVisible false) or
// there is no menu. In plain navigation the row is always reserved now, even with the
// hint bar off (it renders blank — #438), so a notice rides it there rather than
// falling back. On success it clears any stale errBox fallback row so only one surface
// ever carries a toast. Callers that have their own persistent fallback for the
// row-unavailable case (the drift panel badge, the buffered update notice) use this
// directly so they don't spill onto the errBox row (#287/#108).
func (m *home) showMenuNotice(text string, level ui.NoticeLevel) tea.Cmd {
	if !m.menuVisible() || m.menu == nil {
		return nil
	}
	m.menu.SetNotice(text, level)
	// One surface at a time: the notice now rides the menu row, so drop any stale
	// errBox fallback row from an earlier notice and recompute so the panes
	// reclaim that row (else the frame renders one line short of the terminal).
	if m.errBox != nil && m.errBox.HasContent() {
		m.errBox.Clear()
		m.recomputeLayout()
	}
	return m.scheduleNoticeHide()
}

// flashNotice shows a transient toast on the hint bar's reserved row when that row
// is available (see showMenuNotice — in plain navigation it always is, blank bar
// included), else on the errBox's fallback row when an overlay owns the screen,
// styled by level. The toast auto-hides after errToastDuration via scheduleNoticeHide.
// It is the single chokepoint for menu-or-errBox fallback shared by handleError,
// handleInfoNotice, and warnMissingProgram (#287).
func (m *home) flashNotice(text string, level ui.NoticeLevel) tea.Cmd {
	if cmd := m.showMenuNotice(text, level); cmd != nil {
		return cmd // showMenuNotice already dropped any stale errBox row
	}
	if m.menu != nil {
		m.menu.ClearNotice() // one surface at a time: drop any stale menu notice
	}
	m.errBox.SetNotice(text, level)
	m.recomputeLayout() // give the notice its row; panes shrink by one
	return m.scheduleNoticeHide()
}

// handleInfoNotice flashes a neutral acknowledgment ("branch copied"). It rides the
// hint bar's reserved row, which stays reserved in plain navigation even with the bar
// off (blank), so the ack shows without a reflow (#438) rather than being dropped
// (#287); only behind an overlay does it fall back to the errBox row.
func (m *home) handleInfoNotice(text string) tea.Cmd {
	return m.flashNotice(text, ui.NoticeInfo)
}

// settingNotice flashes a notice that names ',' and points that ',' at the setting it is
// about. The notice already told the user which key to press; this is what makes the key
// land somewhere useful instead of on the rail entry they last visited.
//
// It takes the level because the call sites disagree: a reorder refusal is informational,
// while a missing default program is an error. The arm lives exactly as long as the advice —
// scheduleNoticeHide clears it for any newer notice, and the hideErrMsg handler clears it
// when the toast expires. TestEveryCommaNoticeGoesThroughSettingNotice is what keeps every
// ','-advertising notice on this path rather than the generic one.
func (m *home) settingNotice(text string, level ui.NoticeLevel, key string) tea.Cmd {
	cmd := m.flashNotice(text, level)
	m.noticeSettingKey = key
	return cmd
}

// surfaceLostRecoveries makes lost-session recoveries visible instead of a silent
// Running→Paused that looks like a user pause (#270). Two outcomes claim the whole
// message because the user must act on them: a failed recovery → an error, and a crash
// within seconds of launch → a persistent modal naming the command, since a typo'd
// program/profile would otherwise loop invisibly on every Resume.
//
// Everything else is one neutral toast, and the two kinds it can carry are JOINED rather
// than ranked. Ranking them was the bug this shape replaces: the relaunch outranked the
// parks, so a tick with one repair and three parks reported the repair and left three
// sessions silently Paused — the exact silent-transition shape the function exists to
// close. The relaunch clause still comes first, because it is the one the row shows
// nothing of: a park announces itself (the row turns Paused and stays there until the
// user acts), while a blank relaunch leaves the row exactly as it was, so this line is
// the only thing that ever says the agent lost its conversation.
//
// A launch crash still swallows a relaunch that shares its tick — one modal, and the
// modal is the more urgent — so the relaunch is recorded in the log either way
// (RepairResumingLaunch).
func (m *home) surfaceLostRecoveries(recoveries []lostRecovery) tea.Cmd {
	var parked, relaunched []string
	var failed, launchCrash *lostRecovery
	for i := range recoveries {
		switch r := &recoveries[i]; {
		case r.err != nil:
			failed = r
		case r.relaunchedBlank:
			relaunched = append(relaunched, r.title)
		case r.launchCmd != "":
			launchCrash = r
		default:
			parked = append(parked, r.title)
		}
	}
	switch {
	case failed != nil:
		return m.handleError(fmt.Errorf("session %q could not be parked cleanly: %w — press %s to resume or %s to kill",
			failed.title, failed.err, keys.LabelOf(keys.KeyResume), keys.LabelOf(keys.KeyKill)))
	case launchCrash != nil:
		return m.showLaunchCrash(launchCrash)
	}
	var clauses []string
	switch {
	case len(relaunched) == 1:
		clauses = append(clauses, fmt.Sprintf("session %q died at launch — restarted without resuming its conversation", relaunched[0]))
	case len(relaunched) > 1:
		clauses = append(clauses, fmt.Sprintf("%d sessions died at launch — restarted without resuming their conversations", len(relaunched)))
	}
	switch {
	case len(parked) == 1:
		clauses = append(clauses, fmt.Sprintf("session %q terminal exited — parked as paused; %s", parked[0], pressToResume()))
	case len(parked) > 1:
		clauses = append(clauses, fmt.Sprintf("%d sessions' terminals exited — parked as paused; %s", len(parked), pressToResume()))
	}
	if len(clauses) == 0 {
		return nil
	}
	return m.handleInfoNotice(strings.Join(clauses, ". "))
}

// flushDeferredRecovery toasts the startup recoveries the host session budget
// deferred, once there is a frame to show it on. Nil when there was none or an
// overlay owns the screen, and the buffer is cleared as it fires so the 100ms
// preview tick cannot re-toast it forever (the shape flushCustomCommandProblems
// uses).
//
// Unlike the background update notice this is not suppressed with hint_bar off: it
// reports a consequence of the launch the user just performed, so it rides the
// reserved row the same way surfaceLostRecoveries' own park toast does (#438).
//
// It flushes either of two buffers: this load's own report, and one an earlier process
// spooled (#622). They are mutually exclusive by construction — pendingParkReports reads
// the spool only when this load deferred nothing — so the "either" never arbitrates; the
// in-process report is checked first anyway, because a park the user's own launch just
// made is the more urgent of the two and the other is bounded by the spool's TTL.
//
// The spool file is unlinked here rather than at the read, so a quit inside the window
// before the first preview tick leaves the explanation on disk for the next launch
// instead of erasing it — which is the whole failure this path was added for.
func (m *home) flushDeferredRecovery() tea.Cmd {
	if m.state != stateDefault {
		return nil
	}
	if text := startupParkNotice(m.pendingDeferredRecovery); text != "" {
		m.pendingDeferredRecovery = session.DeferredRecovery{}
		return m.handleInfoNotice(text)
	}
	text := earlierParkNotice(m.pendingEarlierRecovery)
	if text == "" {
		return nil
	}
	m.pendingEarlierRecovery = session.DeferredRecovery{}
	if err := parkreport.Remove(); err != nil {
		// Logged, not surfaced: the notice the user needed is already on screen. A
		// persistent failure (a read-only data dir, an immutable file) repeats the toast on
		// every later launch until the TTL expires or the rows stop reconciling — no
		// poisoning set like the outbox drain's, because re-delivery here costs a duplicate
		// notice rather than a duplicate prompt injected into a session, and the notice it
		// repeats is still true of rows that are still parked.
		log.ErrorLog.Printf("could not remove a delivered deferred-recovery report: %v", err)
	}
	return m.handleInfoNotice(text)
}

// showLaunchCrash surfaces a crash-at-launch recovery as a persistent modal
// naming the command. surfaceLostRecoveries runs on every background poll tick
// regardless of m.state, so — like showInfo's own stateDefault guard and the
// buffered release-notes/update notices — it must not switch to stateInfo while
// an overlay (form, rename, confirm, prompt) owns the screen: that would clobber
// the overlay and discard the user's in-progress input. When the screen is busy
// it buffers the crash for the preview tick to flush once we are back at default.
func (m *home) showLaunchCrash(lr *lostRecovery) tea.Cmd {
	if m.state != stateDefault {
		buffered := *lr
		m.pendingLaunchCrash = &buffered
		return nil
	}
	return m.showInfo(fmt.Sprintf(
		"session %q exited moments after launch — parked as paused.\ncommand: %s\nfix the command, then %s.",
		lr.title, lr.launchCmd, pressToResume()))
}

// flushPendingLaunchCrash opens a crash-at-launch modal that arrived while
// another overlay owned the screen, once the screen is free. nil when there is
// nothing buffered or an overlay is still up (mirrors flushPendingReleaseNotes).
func (m *home) flushPendingLaunchCrash() tea.Cmd {
	if m.pendingLaunchCrash == nil || m.state != stateDefault {
		return nil
	}
	lr := m.pendingLaunchCrash
	m.pendingLaunchCrash = nil
	return m.showLaunchCrash(lr)
}

// flushRepoScriptProblems opens the startup report for the repo_scripts entries
// validation refused (#389), once the screen is free. Nil while an overlay owns the
// screen, and the buffer is cleared as it fires so the preview tick cannot reopen it
// forever — the shape flushCustomCommandProblems uses.
//
// Separate from that one rather than merged into a single "your config has problems"
// modal: the two sections fail for unrelated reasons, and showInfo switches to
// stateInfo as it builds, so the second flush in the same tick defers itself to the
// next one rather than clobbering the first.
func (m *home) flushRepoScriptProblems() tea.Cmd {
	if len(m.pendingRepoScriptProblems) == 0 || m.state != stateDefault {
		return nil
	}
	problems := m.pendingRepoScriptProblems
	m.pendingRepoScriptProblems = nil
	return m.showInfo(repoScriptProblemsReport(problems))
}

// repoScriptProblemsReport is that modal's text, bounded on both axes for the reason
// customCommandProblemsReport is: a config can hold any number of broken entries, and
// two of the three fields in a Problem are user-authored.
func repoScriptProblemsReport(problems []repocfg.Problem) string {
	if len(problems) == 0 {
		return ""
	}
	noun := "entries"
	if len(problems) == 1 {
		noun = "entry"
	}
	lines := []string{fmt.Sprintf(
		"%d repo_scripts %s in config.json %s ignored:",
		len(problems), noun, wereOrWas(len(problems)))}
	shown := problems
	if len(shown) > customCommandProblemsShown {
		shown = shown[:customCommandProblemsShown]
	}
	for _, p := range shown {
		lines = append(lines, "  "+clipReportLine(p.Error()))
	}
	if len(problems) > len(shown) {
		lines = append(lines, fmt.Sprintf("  … and %d more", len(problems)-len(shown)))
	}
	// Naming the consequence, not just the refusal: a dropped entry means no setup
	// script and no session_env for every repo it routed, which is a silent symptom
	// ("my dependencies aren't installed") the user would not otherwise connect to this.
	lines = append(lines, "",
		"Sessions in those repos get no setup script and no session_env.",
		"The rest still work. `atrium doctor` reports the same list.")
	return strings.Join(lines, "\n")
}

// flushThemeProblems opens the startup report for the user theme files the loader
// refused (#813), once the screen is free. Nil while an overlay owns the screen, and
// the buffer is cleared as it fires so the preview tick cannot reopen it forever — the
// shape flushCustomCommandProblems uses.
//
// Its own report rather than a section of a shared "your config has problems" modal,
// for the reason flushRepoScriptProblems is separate: the two fail for unrelated
// reasons, and showInfo switches to stateInfo as it builds, so a second flush in the
// same tick defers itself to the next one rather than clobbering the first.
func (m *home) flushThemeProblems() tea.Cmd {
	if len(m.pendingThemeProblems) == 0 || m.state != stateDefault {
		return nil
	}
	problems := m.pendingThemeProblems
	m.pendingThemeProblems = nil
	return m.showInfo(themeProblemsReport(problems))
}

// themeProblemsReport is that modal's text, bounded on both axes for the reason
// customCommandProblemsReport is: a themes directory can hold any number of broken
// files, and both the filename and the failing value are user-authored.
//
// The heading counts PROBLEMS, not files, and that is the honest count rather than a
// vaguer one. Most entries are a refused file, but ApplyThemeAtLaunch pushes
// directory-level failures into the same slice — an unreadable themes/, or a data dir
// that would not resolve — and those are one entry standing for every theme the user
// owns. "1 theme file was ignored" would be a specific claim about a specific file in
// exactly the case where no file was read at all.
func themeProblemsReport(problems []error) string {
	if len(problems) == 0 {
		return ""
	}
	noun := "problems"
	if len(problems) == 1 {
		noun = "problem"
	}
	lines := []string{fmt.Sprintf(
		"%d %s loading user themes:", len(problems), noun)}
	shown := problems
	if len(shown) > customCommandProblemsShown {
		shown = shown[:customCommandProblemsShown]
	}
	for _, p := range shown {
		lines = append(lines, "  "+clipReportLine(p.Error()))
	}
	if len(problems) > len(shown) {
		lines = append(lines, fmt.Sprintf("  … and %d more", len(problems)-len(shown)))
	}
	// Naming the consequence: a refused theme is not in the picker at all, so the
	// symptom is a palette that is simply missing — and if config.json names it, the
	// UI falls back to the default without saying why.
	//
	// Three short lines rather than two long ones. The overlay hugs its content and caps
	// at the terminal width less four, and pads two columns each side, so 72 cells is
	// what survives an 80-column terminal unwrapped — and a wrapped line costs a row the
	// modal's height budget never counted. The entry lines above can still wrap: they
	// carry a user-authored filename and are only clipped at reportLineBudget, which is
	// the trade every one of this modal's siblings makes. These do not have to.
	// TestThemeProblemsReportFitsANarrowTerminal holds them to it.
	lines = append(lines, "",
		"Any palette named above is not selectable.",
		"A `theme` naming one falls back to the default.",
		"`atrium doctor` reports the same list, in full.")
	return strings.Join(lines, "\n")
}

// flushSetupFailures opens the report for a session whose repo environment came up
// short of what config.json asked for (#389): a setup script that failed, or a
// port_range with nothing free. Once the screen is free.
//
// It reads the fleet rather than a buffered message, which is what lets one call site
// cover every way a script can run: a fresh session's Start and a resume's
// re-materialization both record onto the instance, and neither has a completion
// message the app could hang this off. The recorded failure IS the buffer, and
// clearing it as the modal opens is what keeps the 100ms preview tick from reopening
// the same report forever — the shape flushCustomCommandProblems uses.
//
// One at a time on purpose: each report carries a tail of script output, and two
// stacked into one overlay would be unreadable. The next tick shows the next.
func (m *home) flushSetupFailures() tea.Cmd {
	if m.state != stateDefault {
		return nil
	}
	for _, inst := range m.list.GetInstances() {
		report := inst.SetupFailureReport()
		if report == "" {
			continue
		}
		inst.ClearSetupError()
		return m.showInfo(report)
	}
	// The other way a repo's environment can come up short of what it configured: a
	// port_range with nothing free (#389). Same channel, because it is the same
	// question — "why is this session missing something its config promised" — and the
	// same clear-as-you-show rule keeps the tick from reopening it.
	for _, inst := range m.list.GetInstances() {
		report := inst.PortProblem()
		if report == "" {
			continue
		}
		inst.ClearPortProblem()
		return m.showInfo(report)
	}
	return nil
}

// scheduleNoticeHide stamps the just-shown toast with a fresh generation and
// returns the command that clears it after errToastDuration. The generation
// keeps an older toast's timer from clearing a newer toast early.
func (m *home) scheduleNoticeHide() tea.Cmd {
	// Any new notice supersedes the previous one's settings jump, including the background
	// notices (drift, agent, update) that reach showMenuNotice without passing through
	// flashNotice — each of those bumps the generation below, so clearing in flashNotice
	// alone would strand an arm behind an unrelated toast. settingNotice re-arms after this
	// returns, so it is unaffected.
	m.noticeSettingKey = ""
	m.noticeGen++
	gen := m.noticeGen
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(errToastDuration):
		}

		return hideErrMsg{gen: gen}
	}
}

// showInfo displays an actionable message in a dismissible modal (reusing the
// TextOverlay the help screen uses). Unlike handleError's 3-second box, it stays
// until the user presses a key — appropriate for errors that require the user to
// read and act (e.g. "branch is checked out at <path>"). It reuses m.textOverlay,
// which is safe because only one modal state is active at a time.
func (m *home) showInfo(text string) tea.Cmd {
	log.ErrorLog.Printf("%s", text)
	m.textOverlay = overlay.NewTextOverlay(text)
	m.textOverlay.SetHint("press any key to close")
	m.state = stateInfo
	// Size the overlay now rather than waiting for the next resize.
	m.recomputeLayout()
	return nil
}
