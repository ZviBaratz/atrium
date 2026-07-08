package app

// Session lifecycle actions: create, kill, resume, rename, auto-name.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
)

// cycleTarget returns the sibling to re-attach when an in-session cycle key
// (Ctrl+PgUp/PgDn) ended the attach, or nil for a normal detach. Cycling stays
// inside Atrium's model — each hop is a real detach+attach, correctly sized via the
// existing attach path. (A tmux switch-client would avoid the repaint but mis-sizes
// panes here, since every session permanently holds its own pty client.)
// SiblingInGroup returns attached itself when there is no other attachable sibling,
// making a stray cycle key a harmless re-attach.
func (m *home) cycleTarget(attached *session.Instance) *session.Instance {
	switch attached.AttachExitReason() {
	case tmux.DetachNext:
		return m.list.SiblingInGroup(attached, +1)
	case tmux.DetachPrev:
		return m.list.SiblingInGroup(attached, -1)
	}
	return nil
}

// pushSessionContexts refreshes the in-session context bar for every live session.
// SetContext caches per session, so an unchanged tick costs only string comparisons
// rather than tmux subprocesses. No-op when the feature is disabled.
func (m *home) pushSessionContexts() {
	if !m.appConfig.GetSessionContextBar() {
		return
	}
	for _, inst := range m.list.GetInstances() {
		m.pushOneContext(inst)
	}
}

// pushOneContext composes and pushes the context bar for a single session, skipping
// sessions that have no live tmux pane to render it in (unstarted, paused, dead).
func (m *home) pushOneContext(inst *session.Instance) {
	if !m.appConfig.GetSessionContextBar() || !inst.Started() || inst.Paused() || !inst.TmuxAlive() {
		return
	}
	name, left := ui.ComposeSessionContext(inst, ui.RepoKey(inst))
	if err := inst.SetContext(name, left); err != nil {
		log.WarningLog.Printf("failed to push session context for %q: %v", inst.Title, err)
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
// deepRename renames the selected instance's title, git branch, worktree directory, and tmux
// session to value, then clears the cosmetic label so the list shows the corrected name.
// Persisting the result is the caller's responsibility, so the rename and any note edit land
// in a single state.json write. It rejects an empty title or one already used in the instance's
// repo group — comparing derived
// names (tmux segment, branch slug), not raw titles, and also reserving the qualified tmux
// name the rename would mint (plus its "_term" terminal-shell sibling) against every session.
// Same-titled sessions in other groups are fine: their qualified tmux names differ.
// Runs synchronously on the main event loop — the rename is a handful of instant subprocesses,
// and the git/tmux structs guard the fields the background poll loop reads.
func (m *home) deepRename(selected *session.Instance, value string) error {
	if value == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	group := selected.GroupKey()
	cand := tmux.QualifiedSessionName(group, value)
	for _, inst := range m.list.GetInstances() {
		if inst == selected {
			continue
		}
		if inst.GroupKey() == group && session.DerivedNamesCollide(m.appConfig.BranchPrefix, inst.Title, value) {
			return fmt.Errorf("a session named %q already exists in %s", value, group)
		}
		if name := inst.TmuxSessionName(); name != "" {
			if cand == name || cand == name+"_term" || cand+"_term" == name {
				return fmt.Errorf("renaming to %q collides with session %q", value, inst.Title)
			}
		}
	}
	if err := selected.Rename(value); err != nil {
		return err
	}
	selected.SetDisplayName("")
	return nil
}

type instanceStartedMsg struct {
	instance *session.Instance
	err      error
	// hadPrompt records that the session was created with an initial prompt. The
	// auto-open gate keys on this, not on the live Prompt(): when Start() completes
	// mid-attach this message parks, and the attach keeper may deliver — and clear —
	// the prompt before it is handled, which would otherwise flip shouldAutoOpen and
	// force-attach the user into this session the moment they detach.
	hadPrompt bool
}

// shouldAutoOpen reports whether a freshly started session should be attached
// automatically. It is gated by the auto_attach config flag and skipped when the
// session was created with an initial prompt (hadPrompt): delivery is asynchronous
// (metadata tick), and while attached only the keeper delivers — which excludes the
// attached session itself, so auto-opening it would starve its own prompt. The
// creation-time flag, not the live Prompt(), is the signal: the keeper may already
// have delivered and cleared the prompt while this message was parked. The
// Started/TmuxAlive guards avoid attaching a
// session that did not come up — and, because Started() short-circuits before
// TmuxAlive() (which dereferences tmuxSession), keep unstarted instances (e.g. in
// tests) off both the panic and the attach path.
func (m *home) shouldAutoOpen(inst *session.Instance, hadPrompt bool) bool {
	return m.appConfig.GetAutoAttach() && !hadPrompt && inst.Started() && inst.TmuxAlive()
}

// autoNameDoneMsg is sent when a background name generation completes. instance
// identifies which session the name was generated for, so the result lands on the
// right one even if the selection moved meanwhile.
type autoNameDoneMsg struct {
	instance *session.Instance
	name     string
	err      error
}

// runAutoNameCmd returns a Cmd that generates a display name in a background
// goroutine (the agent subprocess can take a few seconds) so the UI stays
// responsive. The session's own agent does the naming when it supports
// headless one-shot prompting (see session.GenerateName).
func runAutoNameCmd(ctx context.Context, instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		// Compute the full diff here, off the UI thread. The cached stats are often the
		// lightweight numstat form (Content empty) — that's all that's kept for a
		// session unless it is the selected one during a diff poll — which would starve
		// the namer of signal and yield a confabulated name. ComputeDiff is
		// goroutine-safe; fall back to the cached stats if it can't run (e.g. paused).
		stats := instance.ComputeDiff()
		if stats == nil || stats.Content == "" {
			if cached := instance.GetDiffStats(); cached != nil {
				stats = cached
			}
		}
		name, err := session.GenerateName(ctx, instance.Program, prompt, stats)
		return autoNameDoneMsg{instance: instance, name: name, err: err}
	}
}

// smartDispatchDoneMsg carries the result of an async smart-dispatch routing call.
// form identifies the exact pre-filled overlay the call was launched for, so a result
// that lands after the user moved on (cancelled, submitted, or opened a different form)
// is discarded by identity. project is a repo basename ("" = no confident route); title
// is a proposed session title ("" = none).
type smartDispatchDoneMsg struct {
	form    *overlay.TextInputOverlay
	project string
	title   string
	err     error
}

// handleSmartDispatchSubmit routes a free-form line to a project and either creates the
// session directly (opt-in auto-dispatch on a confident local match) or opens the
// new-session form pre-filled for confirmation. When no project matches locally it opens
// the form and kicks an async routing call (GenerateDispatch) to fill it in.
func (m *home) handleSmartDispatchSubmit(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		m.textInputOverlay = nil
		m.state = stateDefault
		return nil
	}
	if limit := m.appConfig.GetMaxSessions(); limit > 0 && m.list.NumInstances() >= limit {
		m.textInputOverlay = nil
		m.state = stateDefault
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", limit))
	}

	// Seed the contextual default so it heads the candidate list (and is what an
	// unmatched line falls back to), then route the line against the known repos.
	m.newSessionPath = m.defaultNewSessionPath()
	candidates := m.candidateRepoPaths()
	res := ParsePrefill(line, candidates)

	// Opt-in auto-dispatch: a confident, conflict-free local match creates the session
	// without the form. Never fires on an LLM guess (those are never Confident).
	if res.Confident && m.appConfig.GetSmartDispatchAuto() {
		if cmd, ok := m.autoDispatch(res); ok {
			return cmd
		}
		// A conflict or invalid target falls through to the seeded form below.
	}

	formCmd := m.openCreateFormSeeded(res.Path, false, &res)
	if m.textInputOverlay == nil {
		// The open was refused (e.g. max sessions); formCmd already carries the error.
		return formCmd
	}

	// Decide whether to spend an async LLM call. An unconfident match needs the router
	// for the project (and a title); a confident match already has the project but still
	// upgrades a prose-y placeholder title. A confident, clean title needs neither and
	// stays instant.
	routing := !res.Confident
	upgrade := res.Confident && res.TitleIsRough
	if !routing && !upgrade {
		return formCmd
	}

	m.smartDispatchSeededTitle = m.textInputOverlay.GetTitle()
	hint := "refining title…"
	if routing {
		hint = "detecting project…"
	}
	m.textInputOverlay.SetProjectHint(hint)
	return tea.Batch(formCmd, m.runSmartDispatchCmd(line, candidates, m.textInputOverlay))
}

// autoDispatch creates a session directly from a confident match, returning (cmd, true)
// on success. It returns (nil, false) when the target is invalid or the title would
// collide, so the caller can fall back to the confirmation form. Because it bypasses the
// form, the session launches with the agent's default permission mode — opting into
// smart_dispatch_auto deliberately trades away the per-session permission choice the
// form's Permissions chip would otherwise offer.
func (m *home) autoDispatch(res PrefillResult) (tea.Cmd, bool) {
	valid, direct, _ := targetValidity(m.ctx, res.Path)
	if !valid {
		return nil, false
	}
	m.newSessionGroup = git.RepoGroupKey(m.ctx, res.Path)
	if m.titleConflict(res.Title) != "" {
		return nil, false
	}
	cmd, err := m.startNewSession(res.Title, res.Path, direct, m.program, "", res.Prompt, nil)
	if err != nil {
		return nil, false
	}
	m.textInputOverlay = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
	return cmd, true
}

// runSmartDispatchCmd routes line against the candidate repos in a background goroutine
// (the agent subprocess takes a few seconds) so the form stays responsive. The result
// is tagged with the originating form so a stale answer is dropped by the handler.
func (m *home) runSmartDispatchCmd(line string, candidates []string, form *overlay.TextInputOverlay) tea.Cmd {
	ctx := m.ctx
	program := m.program
	return func() tea.Msg {
		project, title, err := session.GenerateDispatch(ctx, program, line, candidates)
		return smartDispatchDoneMsg{form: form, project: project, title: title, err: err}
	}
}

// candidatePathForBasename maps a routing result's repo basename back to a concrete
// candidate path, preferring the most-recent (first listed) on a basename collision.
func (m *home) candidatePathForBasename(basename string) string {
	for _, p := range m.candidateRepoPaths() {
		if filepath.Base(p) == basename {
			return p
		}
	}
	return ""
}

// isBranchBusyError reports whether err is (or wraps) a *git.BranchCheckedOutError,
// returning the typed error when so. Both the interactive resume path and the batch
// summary key off this — the type, not the message, is the cross-package contract.
func isBranchBusyError(err error) (*git.BranchCheckedOutError, bool) {
	var busy *git.BranchCheckedOutError
	if errors.As(err, &busy) {
		return busy, true
	}
	return nil, false
}

// pauseDoneMsg reports the outcome of a single off-UI-thread pause (pauseSelected)
// back through Update so the terminal teardown, persistence, and rename-overlay
// open all run on the main loop. err is set when Pause failed.
type pauseDoneMsg struct {
	instance *session.Instance
	err      error
}

// resumeDoneMsg reports the outcome of a single off-UI-thread resume
// (resumeSelected) back through Update so persistence and any recovery UI run on
// the main loop. On a plain success err is nil. On a branch-busy failure the
// goroutine also records the diagnosis (recoverable/heldByBase/branchName +
// worktree) so the handler can decide between a dismissible info modal and the
// detach-and-resume confirmation without doing any I/O on the UI thread.
type resumeDoneMsg struct {
	instance    *session.Instance
	err         error
	recoverable bool
	heldByBase  bool
	branchName  string
	worktree    *git.Worktree
}

// resumeSelected resumes a paused instance off the UI thread and persists the new
// running state on the Update loop (Resume itself only mutates in-memory status, so
// without this a crash before the next save would leave the session stamped
// Paused). When resume is blocked because the session branch is checked out in the
// BASE repo — the common result of the Checkout action — it offers to detach the
// base repo and retry. When the branch is held by a sibling worktree it surfaces a
// dismissible modal naming the holder rather than auto-touching another live
// worktree. The whole diagnosis runs in the goroutine; handleResumeDone only
// decides the UI.
func (m *home) resumeSelected(selected *session.Instance) tea.Cmd {
	return m.beginAsyncAction("resuming…", func() tea.Msg {
		err := selected.Resume()
		if err == nil {
			return resumeDoneMsg{instance: selected}
		}
		// Only a branch-busy failure is recoverable; surface anything else as-is.
		if _, ok := isBranchBusyError(err); !ok {
			return resumeDoneMsg{instance: selected, err: err}
		}
		wt, gerr := selected.GetGitWorktree()
		if gerr != nil {
			return resumeDoneMsg{instance: selected, err: err}
		}
		heldByBase, herr := wt.IsBranchHeldByBaseRepo()
		if herr != nil {
			// Couldn't determine where the branch is held: surface that failure rather
			// than masking it behind the (less informative) branch-busy message.
			return resumeDoneMsg{instance: selected, err: herr}
		}
		return resumeDoneMsg{
			instance:    selected,
			err:         err,
			recoverable: true,
			heldByBase:  heldByBase,
			branchName:  wt.GetBranchName(),
			worktree:    wt,
		}
	})
}

// handlePauseDone completes a single pause on the Update loop: it tears down the
// preview terminal, persists (so an esc'd rename can't leave the pause unpersisted),
// and opens the rename overlay focused on the note field so "park this, jot why" is
// one motion — esc / empty-enter leaves the note unchanged, the session stays paused
// either way.
func (m *home) handlePauseDone(msg pauseDoneMsg) tea.Cmd {
	if msg.err != nil {
		return m.handleError(msg.err)
	}
	selected := msg.instance
	m.tabbedWindow.CleanupTerminalForInstance(selected)
	if serr := m.persistInstances(); serr != nil {
		log.WarningLog.Printf("failed to persist paused instance %s: %v", selected.Title, serr)
	}
	m.renameTarget = selected
	m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName(), selected.Note(), true)
	m.state = stateRename
	return m.instanceChanged()
}

// handleResumeDone completes a single resume on the Update loop, acting on the
// diagnosis resumeSelected's goroutine recorded. Success persists and refreshes; a
// non-recoverable (or diagnosis-failed) error surfaces as-is; a sibling-held branch
// gets a dismissible modal; a base-repo-held branch offers detach-and-resume, which
// runs off the UI thread again.
func (m *home) handleResumeDone(msg resumeDoneMsg) tea.Cmd {
	if msg.err == nil {
		if serr := m.persistInstances(); serr != nil {
			log.WarningLog.Printf("failed to persist resumed instance %s: %v", msg.instance.Title, serr)
		}
		return tea.WindowSize()
	}
	if !msg.recoverable {
		return m.handleError(msg.err)
	}
	if !msg.heldByBase {
		// Held by a sibling worktree: report where it lives in a dismissible modal;
		// never auto-detach another live worktree.
		return m.showInfo(msg.err.Error())
	}
	selected, wt := msg.instance, msg.worktree
	message := fmt.Sprintf("Branch '%s' is checked out in the main repo. Detach it and resume?", msg.branchName)
	return m.confirmAsyncAction(message, "resuming…", func() tea.Msg {
		if derr := wt.DetachBranchInBaseRepo(); derr != nil {
			// e.g. the dirty-repo refusal — show it in a modal the user can read.
			return infoMsg(derr.Error())
		}
		if rerr := selected.Resume(); rerr != nil {
			return resumeDoneMsg{instance: selected, err: rerr}
		}
		return resumeDoneMsg{instance: selected}
	})
}

// resumeFailure records one instance that could not be resumed during a batch
// "resume all", paired with the reason so the summary can name it.
type resumeFailure struct {
	title string
	err   error
}

// batchResumeDoneMsg reports the outcome of a "resume all" run back through
// Update so the feedback (notice vs. modal) and list refresh run on the main
// loop. resumed counts the successes; failures lists the rest, in list order.
type batchResumeDoneMsg struct {
	resumed  int
	failures []resumeFailure
}

// summary renders the dismissible-modal text for a batch resume that had at
// least one failure. It is empty when nothing failed (the caller uses a
// transient notice for the all-success case instead). A branch-busy failure is
// rendered as a short, actionable reason rather than the raw wrapped error.
func (msg batchResumeDoneMsg) summary() string {
	if len(msg.failures) == 0 {
		return ""
	}
	total := msg.resumed + len(msg.failures)
	var b strings.Builder
	fmt.Fprintf(&b, "Resumed %d of %d session%s. %d could not resume:",
		msg.resumed, total, plural(total), len(msg.failures))
	for _, f := range msg.failures {
		reason := f.err.Error()
		if _, ok := isBranchBusyError(f.err); ok {
			reason = "branch checked out elsewhere"
		}
		fmt.Fprintf(&b, "\n  • %s — %s", f.title, reason)
	}
	return b.String()
}

// resumeAll resumes every paused session in the current view behind a count
// confirmation. Unlike resumeSelected, the batch cannot stop to prompt for each
// branch-busy session, so a per-instance failure (e.g. BranchCheckedOutError) is
// recorded and the run continues; the outcome is surfaced as a summary. The resume
// runs off the UI thread; state is persisted once, on the Update loop, when the
// batchResumeDoneMsg lands (mirroring resumeSelected).
func (m *home) resumeAll() tea.Cmd {
	paused := m.list.PausedInstancesInView()
	if len(paused) == 0 {
		return m.handleInfoNotice("no paused sessions to resume")
	}
	message := fmt.Sprintf("Resume %d paused session%s?", len(paused), plural(len(paused)))
	return m.resumeInstances(paused, message)
}

// resumeInstances resumes an explicit set of (already-paused) sessions behind a
// count confirmation — the shared core of resumeAll and resumeMarked. A
// per-instance failure (e.g. BranchCheckedOutError) is recorded and the run
// continues; the outcome is surfaced as a summary. The resume runs off the UI
// thread (confirmAsyncAction); state is persisted once, on the Update loop, in the
// batchResumeDoneMsg handler. Resume is non-destructive, so it keeps the default
// accent border (only kill wears the danger border).
func (m *home) resumeInstances(insts []*session.Instance, message string) tea.Cmd {
	action := func() tea.Msg {
		var res batchResumeDoneMsg
		for _, inst := range insts {
			if err := inst.Resume(); err != nil {
				res.failures = append(res.failures, resumeFailure{inst.Title, err})
				continue
			}
			res.resumed++
		}
		// Persistence reads m.list, so it must run on the Update loop, not in this
		// goroutine — the batchResumeDoneMsg handler persists when resumed > 0.
		return res
	}
	label := fmt.Sprintf("resuming %d session%s…", len(insts), plural(len(insts)))
	return m.confirmAsyncAction(message, label, action)
}

// plural returns the "s" suffix for a count: "" for exactly one, "s" otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pauseFailure records one instance that could not be paused during a batch
// "pause all", paired with the reason so the summary can name it.
type pauseFailure struct {
	title string
	err   error
}

// batchPauseDoneMsg reports the outcome of a "pause all" run back through Update
// so the feedback (notice vs. modal), preview-terminal teardown, and list refresh
// all run on the main loop. paused counts the successes; pausedInstances carries
// the parked instances so Update can clean up their terminals (Pause does the
// git/tmux work, but tearing down the UI terminal must happen on the main loop);
// failures lists the rest, in list order.
type batchPauseDoneMsg struct {
	paused          int
	pausedInstances []*session.Instance
	failures        []pauseFailure
}

// summary renders the dismissible-modal text for a batch pause that had at least
// one failure. It is empty when nothing failed (the caller uses a transient
// notice for the all-success case instead).
func (msg batchPauseDoneMsg) summary() string {
	if len(msg.failures) == 0 {
		return ""
	}
	total := msg.paused + len(msg.failures)
	var b strings.Builder
	fmt.Fprintf(&b, "Paused %d of %d session%s. %d could not pause:",
		msg.paused, total, plural(total), len(msg.failures))
	for _, f := range msg.failures {
		fmt.Fprintf(&b, "\n  • %s — %s", f.title, f.err.Error())
	}
	return b.String()
}

// pauseAll parks every pausable (non-paused, non-loading, non-direct) session in
// the current view (see ActiveInstancesInView) behind a count confirmation — the
// intentional "prepare for restart" path,
// the inverse of resumeAll. Each Pause commits WIP, detaches tmux, and removes the
// worktree (keeping the branch); a per-instance failure is recorded and the run
// continues, with the outcome surfaced as a summary. The run happens off the UI
// thread; state is persisted once, on the Update loop, in the batchPauseDoneMsg
// handler (mirroring resumeAll).
func (m *home) pauseAll() tea.Cmd {
	active := m.list.ActiveInstancesInView()
	if len(active) == 0 {
		return m.handleInfoNotice("no active sessions to pause")
	}
	message := fmt.Sprintf("Pause %d active session%s?", len(active), plural(len(active)))
	return m.pauseInstances(active, message)
}

// pauseInstances parks an explicit set of (pausable) sessions behind a count
// confirmation — the shared core of pauseAll and pauseMarked. Each Pause commits
// WIP, detaches tmux, and removes the worktree (keeping the branch); a
// per-instance failure is recorded and the run continues, with the outcome
// surfaced as a summary. The run happens off the UI thread (confirmAsyncAction);
// state is persisted once, on the Update loop, in the batchPauseDoneMsg handler.
// Pause is non-destructive (every branch is kept), so it keeps the default accent
// border.
func (m *home) pauseInstances(insts []*session.Instance, message string) tea.Cmd {
	action := func() tea.Msg {
		var res batchPauseDoneMsg
		for _, inst := range insts {
			if err := inst.Pause(); err != nil {
				res.failures = append(res.failures, pauseFailure{inst.Title, err})
				continue
			}
			res.paused++
			res.pausedInstances = append(res.pausedInstances, inst)
		}
		// Persistence reads m.list, so it must run on the Update loop, not in this
		// goroutine — the batchPauseDoneMsg handler persists when paused > 0.
		return res
	}
	label := fmt.Sprintf("pausing %d session%s…", len(insts), plural(len(insts)))
	return m.confirmAsyncAction(message, label, action)
}

// killFailure records one instance that could not be killed during a batch kill,
// paired with the reason so the summary can name it.
type killFailure struct {
	title string
	err   error
}

// batchKillDoneMsg reports the outcome of a batch kill back through Update so the
// feedback (notice vs. modal), preview-terminal teardown, and list refresh all
// run on the main loop. killed counts the successes; killedInstances carries the
// torn-down instances so Update can clean up their preview terminals; failures
// lists the rest, in list order.
type batchKillDoneMsg struct {
	killed          int
	killedInstances []*session.Instance
	failures        []killFailure
}

// summary renders the dismissible-modal text for a batch kill that had at least
// one failure. It is empty when nothing failed (the caller uses a transient
// notice for the all-success case instead).
func (msg batchKillDoneMsg) summary() string {
	if len(msg.failures) == 0 {
		return ""
	}
	total := msg.killed + len(msg.failures)
	var b strings.Builder
	fmt.Fprintf(&b, "Killed %d of %d session%s. %d reported errors:",
		msg.killed, total, plural(total), len(msg.failures))
	for _, f := range msg.failures {
		fmt.Fprintf(&b, "\n  • %s — %s", f.title, f.err.Error())
	}
	return b.String()
}

// forgetInstance drops the per-instance bookkeeping a removed session leaves behind: its
// notify first-observation/throttle state and any lost-recovery strike count. Both maps
// are keyed by *session.Instance, so without this a killed session would pin its Instance
// object (and everything it references) in memory for the process lifetime. Every caller
// runs on the main loop, where these maps are otherwise read and written, so the deletes
// don't race (and delete on a nil map is a harmless no-op for hand-built test homes).
func (m *home) forgetInstance(inst *session.Instance) {
	delete(m.notifySeen, inst)
	delete(m.lostStrikes, inst)
}

// killInstances tears down an explicit set of sessions behind a single count
// confirmation — the batch counterpart of confirmKill, used by killMarked. Each
// teardown reuses confirmKill's per-instance logic: it refuses only when the
// branch is held by the base repo itself (recorded as a failure so the run
// continues), then deletes from storage and kills. Storage deletion and
// KillInstance run on the main loop (the action is invoked there), so they don't
// race the list. Kill is destructive, so the confirmation wears the danger
// border like the single-kill dialog.
func (m *home) killInstances(insts []*session.Instance, message string) tea.Cmd {
	action := func() tea.Msg {
		var res batchKillDoneMsg
		for _, inst := range insts {
			// Mirror confirmKill: refuse only when the branch is checked out in the
			// primary repo itself; a live session's branch is in its own worktree, so
			// IsBranchHeldByBaseRepo (not IsBranchCheckedOut) is the right predicate.
			// Fail open if the worktree/repo is unreachable so an orphan can still be
			// deleted. Direct sessions have no branch/worktree, so skip the check.
			if !inst.IsDirect() {
				if worktree, err := inst.GetGitWorktree(); err != nil {
					log.WarningLog.Printf("kill %s: cannot resolve worktree, proceeding: %v", inst.Title, err)
				} else if heldByBase, cerr := worktree.IsBranchHeldByBaseRepo(); cerr != nil {
					log.WarningLog.Printf("kill %s: cannot verify branch checkout, proceeding: %v", inst.Title, cerr)
				} else if heldByBase {
					res.failures = append(res.failures, killFailure{inst.Title,
						fmt.Errorf("branch checked out in the main repo")})
					continue
				}
			}
			if err := m.storage.DeleteInstance(inst.Title, inst.Path); err != nil {
				res.failures = append(res.failures, killFailure{inst.Title, err})
				continue
			}
			// KillInstance removes the row regardless of teardown outcome, so its
			// preview terminal must be cleaned up either way. A teardown failure is
			// recorded (naming what leaked) but does not count toward killed, so the
			// "killed N" notice reflects only clean teardowns. The row and storage
			// entry are already gone, so the message says so rather than implying the
			// session survived.
			if err := m.list.KillInstance(inst); err != nil {
				res.failures = append(res.failures, killFailure{inst.Title,
					fmt.Errorf("removed, but teardown was incomplete: %w", err)})
			} else {
				res.killed++
			}
			res.killedInstances = append(res.killedInstances, inst)
		}
		return res
	}
	cmd := m.confirmAction(message, action)
	// Kill is destructive, so it wears the danger border (confirmAction created
	// m.confirmationOverlay synchronously above).
	m.confirmationOverlay.SetBorderColor(theme.Current().Palette.Danger)
	return cmd
}

// pauseMarked parks the pausable subset of the multi-select-marked sessions
// (mirroring ActiveInstancesInView's predicate: not paused/loading/direct). With
// nothing eligible it explains itself and stays in the mode; otherwise it leaves
// visual mode (capturing the slice first) so a cancelled confirmation leaves no
// stale marks behind.
func (m *home) pauseMarked() tea.Cmd {
	var insts []*session.Instance
	for _, inst := range m.list.MarkedInstancesInView() {
		status := inst.GetStatus()
		if status != session.Paused && status != session.Loading && !inst.IsDirect() {
			insts = append(insts, inst)
		}
	}
	if len(insts) == 0 {
		return m.handleInfoNotice("no marked sessions to pause")
	}
	m.exitVisualMode()
	message := fmt.Sprintf("Pause %d marked session%s?", len(insts), plural(len(insts)))
	return m.pauseInstances(insts, message)
}

// resumeMarked resumes the paused subset of the multi-select-marked sessions.
// Same eligibility/exit semantics as pauseMarked.
func (m *home) resumeMarked() tea.Cmd {
	var insts []*session.Instance
	for _, inst := range m.list.MarkedInstancesInView() {
		if inst.GetStatus() == session.Paused {
			insts = append(insts, inst)
		}
	}
	if len(insts) == 0 {
		return m.handleInfoNotice("no marked sessions to resume")
	}
	m.exitVisualMode()
	message := fmt.Sprintf("Resume %d marked session%s?", len(insts), plural(len(insts)))
	return m.resumeInstances(insts, message)
}

// killMarked tears down the killable subset of the multi-select-marked sessions
// (everything except a still-Loading session, which single-kill also refuses).
// Same eligibility/exit semantics as pauseMarked.
func (m *home) killMarked() tea.Cmd {
	var insts []*session.Instance
	for _, inst := range m.list.MarkedInstancesInView() {
		if inst.GetStatus() != session.Loading {
			insts = append(insts, inst)
		}
	}
	if len(insts) == 0 {
		return m.handleInfoNotice("no marked sessions to kill")
	}
	m.exitVisualMode()
	message := fmt.Sprintf("Kill %d marked session%s?", len(insts), plural(len(insts)))
	if n := sessionsWithUnmergedWork(insts); n > 0 {
		message += fmt.Sprintf(" (%d of %d have unmerged work)", n, len(insts))
	}
	return m.killInstances(insts, message)
}

// newSessionFormOverlay builds the unified new-session form (title, project, optional
// profile, branch, prompt) shared by both creation flows. It also reports whether the
// seeded target is a git repo, so openCreateForm can gate the open-time branch plumbing
// without re-running the git checks.
func (m *home) newSessionFormOverlay() (_ *overlay.TextInputOverlay, isGit bool) {
	ov := overlay.NewSessionCreateOverlay(m.appConfig.GetProfiles(), m.appConfig.ClaudeAccounts, m.candidateRepoPaths(), m.program)
	// Seed the initial validity so the picker can flag the default target before the user
	// navigates: a non-git default directory shows the direct-session hint (and an inert
	// branch section), not a block.
	valid, direct, head := targetValidity(m.ctx, m.newSessionPath)
	ov.SetTargetValidity(valid, direct, head)
	return ov, valid && !direct
}

// seedOverlay fills a freshly built create-form overlay with the free-text values and
// project selection shared by the prefill and crash-recovery paths. SetTitleValue("")
// is a harmless no-op on a fresh field; SelectPath is skipped for an empty path (and a
// no-op for a non-candidate one). Branch/profile/model/account are not seeded — they
// are re-derived from the target, exactly as on a fresh form.
func seedOverlay(ov *overlay.TextInputOverlay, title, prompt, path string) {
	ov.SetTitleValue(title)
	ov.SetPrompt(prompt)
	if path != "" {
		ov.SelectPath(path)
	}
}

// preselectAccountFor points ov's account picker at the Claude account auto-routed for
// target, so the form shows the same account a submit would resolve. A no-op once the
// user drives the picker (see AccountPicker.SelectByName). isGit gates the remote
// lookup: a non-git target has no remote and routes by path.
func (m *home) preselectAccountFor(ov *overlay.TextInputOverlay, target string, isGit bool) {
	remoteURL := ""
	if isGit {
		remoteURL = git.GetRemoteURL(m.ctx, target)
	}
	if name, _, _ := m.appConfig.ResolveClaudeAccount(remoteURL, target); name != "" {
		ov.PreselectAccount(name)
	}
}

// openCreateForm opens the unified new-session form — the single creation flow
// behind both `n` (focusTitle, for "type a name and go") and `N` (project picker
// first). The session itself is not created (and no list row appears) until the
// form is submitted. The contextual target is derived up front and, when it is a
// git repo, a background fetch kicked off so branches are current by the time the
// user reaches the branch field.
func (m *home) openCreateForm(focusTitle bool) tea.Cmd {
	return m.openCreateFormSeeded("", focusTitle, nil)
}

// openCreateFormSeeded is the shared body of openCreateForm and the smart-dispatch
// confirm path. seedPath, when non-empty, overrides the contextual target (the matched
// project). prefill, when non-nil, pre-fills the prompt/title and pre-selects the
// project, and a confident prefill lands focus on Create rather than the title.
func (m *home) openCreateFormSeeded(seedPath string, focusTitle bool, prefill *PrefillResult) tea.Cmd {
	if limit := m.appConfig.GetMaxSessions(); limit > 0 && m.list.NumInstances() >= limit {
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", limit))
	}

	// On the first bare n/N open after a restart, rebuild a crash-persisted draft into
	// the in-memory stash so the restore branch below surfaces it exactly like an
	// in-run Escape stash — same overlay, same auto-routed account, indistinguishable
	// from a fresh form. Skipped when this run already has a stash (its live overlay
	// wins, carrying every field) or the open carries explicit intent (prefill/seed).
	// The disk copy is left intact here; the consume site clears it, so disk and the
	// in-memory stash stay in lockstep at every handler boundary.
	if m.stashedDraft == nil && prefill == nil && seedPath == "" {
		if d := m.appState.GetDraft(); d != nil {
			// Set the target before building the overlay (newSessionFormOverlay reads
			// m.newSessionPath); the restore branch below re-derives it from the overlay's
			// own selection, which round-trips this value (the draft path is candidate[0]).
			m.newSessionPath = d.Path
			if m.newSessionPath == "" {
				m.newSessionPath = m.defaultNewSessionPath()
			}
			ov, isGit := m.newSessionFormOverlay()
			seedOverlay(ov, d.Title, d.Prompt, d.Path)
			// Re-route the account like a fresh form so the recovered draft shows the
			// project's auto-routed account, not a misleading first-account default.
			m.preselectAccountFor(ov, m.newSessionPath, isGit)
			m.stashedDraft = ov
		}
	}

	// Restore a stashed draft only on the bare n/N entry (no seed path, no prefill):
	// the smart-dispatch and seeded paths carry explicit intent that must win.
	restore := prefill == nil && seedPath == "" && m.stashedDraft != nil

	if restore {
		m.newSessionPath = m.stashedDraft.GetSelectedPath()
	} else {
		m.newSessionPath = seedPath
	}
	if m.newSessionPath == "" {
		m.newSessionPath = m.defaultNewSessionPath()
	}
	target := m.newSessionPath
	// Scope the duplicate-title check to the target's repo group from the first
	// keystroke; the async validity check re-points it as the picker moves.
	m.newSessionGroup = git.RepoGroupKey(m.ctx, target)
	m.resetTitleCheck()
	m.state = statePrompt

	var isGit bool
	if restore {
		m.textInputOverlay = m.stashedDraft
		m.stashedDraft = nil
		// The draft is now live in the form; drop the disk copy to mirror the stash.
		m.clearPersistedDraft()
		valid, direct, _ := targetValidity(m.ctx, target)
		isGit = valid && !direct
		// Re-run the inline duplicate verdict for the restored title.
		m.refreshTitleError()
	} else {
		var ov *overlay.TextInputOverlay
		ov, isGit = m.newSessionFormOverlay()
		m.textInputOverlay = ov
		if prefill != nil {
			seedOverlay(ov, prefill.Title, prefill.Prompt, prefill.Path)
			if prefill.Confident {
				// Project and title are trusted; land on the Permissions chip — the one
				// decision smart dispatch defers. Falls back to Create on non-claude.
				ov.FocusMode()
			} else {
				ov.FocusTitle()
			}
			// A pre-filled title needs the same duplicate verdict a keystroke triggers.
			m.refreshTitleError()
		} else if focusTitle {
			m.textInputOverlay.FocusTitle()
		}
		// Open the account picker on the auto-routed account for the contextual target.
		m.preselectAccountFor(m.textInputOverlay, target, isGit)
	}

	// Branch plumbing only applies to a git target: seed the fetched-once set and
	// kick the background fetch plus the initial (undebounced) branch search.
	m.fetchedPaths = map[string]bool{}
	cmds := []tea.Cmd{tea.WindowSize()}
	// Refresh the repo scan when the last completed one has gone stale (a
	// long-running TUI would otherwise serve launch-time results forever). The
	// completion live-updates this form's picker in place.
	if !m.scanInFlight && time.Since(m.lastScanAt) > projectScanTTL {
		cmds = append(cmds, m.startProjectScan())
	}
	if isGit {
		m.fetchedPaths[target] = true
		cmds = append(cmds,
			m.runBranchFetch(target),
			m.runBranchSearch("", m.textInputOverlay.BranchFilterVersion()))
	}
	// Verify a pre-filled or restored title against orphan branches in the target repo,
	// the same async check a keystroke schedules.
	if title := m.textInputOverlay.GetTitle(); title != "" {
		cmds = append(cmds, m.scheduleTitleCheck(title, target))
	}
	return tea.Batch(cmds...)
}

// titleConflict reports why title cannot be used for a new session in the
// current target group ("" = no conflict). It compares derived names — not raw
// titles — against every listed instance regardless of status (a Paused session
// still owns its branch and tmux name):
//   - same group + colliding tmux segment or branch slug → duplicate;
//   - any instance whose tmux name equals the qualified name the title would
//     mint (a legacy unqualified name can shadow a qualified one), or its
//     "_term" sibling — the terminal-shell session derived from it;
//   - the latest async verdict that the title's branch already exists in the
//     target repo (an orphan from a killed session would make Start fail late).
func (m *home) titleConflict(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	group := m.newSessionGroup
	prefix := m.appConfig.BranchPrefix
	cand := tmux.QualifiedSessionName(group, title)
	for _, inst := range m.list.GetInstances() {
		if inst.GroupKey() == group && session.DerivedNamesCollide(prefix, inst.Title, title) {
			return fmt.Sprintf("already used in %s", group)
		}
		if name := inst.TmuxSessionName(); name != "" {
			if cand == name || cand == name+"_term" || cand+"_term" == name {
				return fmt.Sprintf("name collides with session %q", inst.Title)
			}
		}
	}
	if m.titleBranchExists && m.titleBranchName == git.BranchNameForSession(prefix, title) {
		return fmt.Sprintf("branch %s exists in %s", m.titleBranchName, group)
	}
	return ""
}

// refreshTitleError recomputes the inline title verdict and pushes it into the
// form. Called on title keystrokes, on path/group changes, and when an async
// branch verdict lands.
func (m *home) refreshTitleError() {
	if m.textInputOverlay == nil || !m.textInputOverlay.IsCreateForm() {
		return
	}
	m.textInputOverlay.SetTitleError(m.titleConflict(m.textInputOverlay.GetTitle()))
}

// resetTitleCheck clears the new-session validation state (group scope + async
// branch verdict) when the form closes or its target moves.
func (m *home) resetTitleCheck() {
	m.titleBranchExists = false
	m.titleBranchName = ""
}

// composeProgramFlags folds the optional Claude model and permission-mode overrides
// into program, returning the augmented command. Each is applied only when non-empty
// and the program resolves to claude — the sole agent whose --model / --permission-mode
// flags these compose — and is re-validated as a backstop: the create form already
// filters the model field to agent.ValidModelName's charset (see ui/overlay/modelField.go)
// and offers a closed set of valid mode chips, so an invalid value reaching here means
// drift between the UI and the agent enums, caught before a dead launch rather than
// after. The mode check sees the model-augmented program, matching the form's submit
// order; since --model leaves the base command claude, Resolve is unaffected.
func composeProgramFlags(program, model, mode string) (string, error) {
	if model != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidModelName(model) {
			return "", fmt.Errorf("invalid model name %q (letters, digits, . _ : / - only)", model)
		}
		program = agent.WithModelFlag(program, model)
	}
	if mode != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidPermissionMode(mode) {
			return "", fmt.Errorf("invalid permission mode %q", mode)
		}
		program = agent.WithPermissionModeFlag(program, mode)
	}
	return program, nil
}

// createSessionFromForm validates the submitted new-session form, creates the session,
// adds it to the list, and starts it in the background with the entered prompt. On a
// validation error it leaves the overlay open (clearing the submitted flag) and surfaces
// the error so the user can correct the offending field.
func (m *home) createSessionFromForm(prompt string) tea.Cmd {
	ov := m.textInputOverlay

	title := ov.GetTitle()
	if title == "" {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("title cannot be empty"))
	}

	path := ov.GetSelectedPath()
	if path == "" {
		path = m.newSessionPath
	}
	if path == "" {
		// No target at all (no picker selection and no contextual default): say so
		// plainly instead of letting targetValidity report '"" is not a directory'.
		ov.Submitted = false
		return m.handleError(fmt.Errorf("no directory selected"))
	}
	// A non-git directory becomes a direct session (agent runs in place, no worktree).
	valid, direct, _ := targetValidity(m.ctx, path)
	if !valid {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("%q is not a directory", path))
	}

	// Duplicate gate. Re-derive the group for the path actually being submitted
	// (the picker may have moved without an async verdict landing yet), re-run
	// the in-memory conflict checks, and re-verify branch existence synchronously
	// — one local ref lookup — so a submit that beats the debounce can't slip
	// through and die in the background Start. On conflict the form stays open
	// with the inline error and focus on the title; no toast to miss.
	m.newSessionGroup = git.RepoGroupKey(m.ctx, path)
	conflict := m.titleConflict(title)
	if conflict == "" && !direct {
		if branch := git.BranchNameForSession(m.appConfig.BranchPrefix, title); git.LocalBranchExists(m.ctx, path, branch) {
			conflict = fmt.Sprintf("branch %s exists in %s", branch, m.newSessionGroup)
		}
	}
	if conflict != "" {
		ov.Submitted = false
		ov.SetTitleError(conflict)
		ov.FocusTitle()
		return nil
	}

	program := m.program
	if p := ov.GetSelectedProgram(); p != "" {
		program = p
	}
	// Fold the model and permission-mode overrides into the persisted program
	// string, so launch, pause/resume, and the daemon all see them with no extra
	// plumbing. composeProgramFlags re-validates each as a backstop behind the
	// form's own gating (the fields are inert for non-claude programs, and the
	// model field filters keystrokes to the valid charset).
	program, err := composeProgramFlags(program, ov.GetModel(), ov.GetPermissionMode())
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
	}

	var accountOverride *config.ClaudeAccount
	if acct, ok := ov.GetSelectedAccount(); ok && acct.Name != "" {
		accountOverride = &acct
	}
	created, err := m.startNewSession(title, path, direct, program, ov.GetSelectedBranch(), prompt, accountOverride)
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
	}

	m.textInputOverlay = nil
	m.stashedDraft = nil
	// The form was submitted, so any persisted draft is now stale — drop it.
	m.clearPersistedDraft()
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()

	return created
}

// startNewSession builds, registers, and starts a new session from already-validated
// inputs, returning the batch that boots it in the background. It is the shared core of
// the form-submit and smart-auto-dispatch paths: caller-supplied validation (title
// conflict, target validity) must already have passed. accountOverride, when non-nil, is
// an explicit Claude-account choice that wins over auto-routing.
func (m *home) startNewSession(title, path string, direct bool, program, branch, prompt string, accountOverride *config.ClaudeAccount) (tea.Cmd, error) {
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    path,
		Program: program,
		Direct:  direct,
	})
	if err != nil {
		return nil, err
	}
	instance.SetBaseContext(m.ctx)

	// Resolve which Claude Code account this worktree runs under from its origin
	// remote (or, for a direct/non-git session with no remote, its directory path),
	// and pin it on the instance (stored verbatim, injected at launch). Empty
	// claude_accounts leaves all fields empty (feature dormant).
	remoteURL := ""
	if !direct {
		remoteURL = git.GetRemoteURL(m.ctx, path)
	}
	accName, accDir, accIsDefault := m.appConfig.ResolveClaudeAccount(remoteURL, path)
	if accountOverride != nil {
		// An explicit picker choice wins over auto-routing. Picking the catch-all
		// (no-rule) account stays dim; a routed account shows accented — the same
		// rule the resolver applies.
		accName, accDir, accIsDefault = accountOverride.Name, accountOverride.ResolvedConfigDir(), accountOverride.IsCatchAll()
	}
	instance.SetClaudeAccount(accName, accDir, accIsDefault)
	// The gh account routes from the origin remote (or path) independently of the
	// Claude-account override: gh access is determined by the actual repo, not by
	// which Claude login was picked. "" leaves gh on the ambient global account.
	// TokenEnv (if any) names the env vars the account's gh token is injected under
	// at launch (e.g. GITHUB_PERSONAL_ACCESS_TOKEN for the github MCP).
	ghDir, ghTokenEnv := m.appConfig.ResolveGHAccount(remoteURL, path)
	instance.SetGHConfigDir(ghDir)
	instance.SetGitHubTokenEnv(ghTokenEnv)

	// Create the list row only now, on submit. AddInstance may insert it mid-list under its
	// repo group, so select it by identity.
	finalizer := m.list.AddInstance(instance)
	m.list.SelectInstance(instance)
	if branch != "" {
		instance.SetBaseBranch(branch)
	}
	instance.QueuePrompt(prompt)
	instance.SetStatus(session.Loading)
	finalizer()

	startCmd := func() tea.Msg {
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err, hadPrompt: prompt != ""}
	}
	return tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd), nil
}

// defaultNewSessionPath returns the contextual target repo for a new session: the
// highlighted session's repo, falling back to the current working directory. The
// empty string is returned only if there is no repo context at all (no highlighted
// session and cwd is unavailable).
func (m *home) defaultNewSessionPath() string {
	if selected := m.list.GetSelectedInstance(); selected != nil && selected.Path != "" {
		return selected.Path
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// candidateRepoPaths returns the deduped candidate target paths for the directory
// picker, in priority order: the current target first, then existing sessions'
// repos, then recently-used project directories, then the durable known-projects
// tail, then background-scanned repos, then cwd. The picker's fuzzy ranking is
// order-stable on ties, so this ordering is also the empty-filter display order
// and the tiebreak between equal-scoring matches.
func (m *home) candidateRepoPaths() []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	add(m.newSessionPath)
	for _, inst := range m.list.GetInstances() {
		add(inst.Path)
	}
	for _, p := range m.appState.GetRecentPaths() {
		// Skip recent paths that no longer exist so deleted/moved repos don't clutter
		// the picker or error only when selected.
		if !config.DirExists(p) {
			continue
		}
		add(p)
	}
	for _, p := range m.appState.GetKnownProjects() {
		// Same staleness pruning as recents (≤100 stats, same cost class).
		if !config.DirExists(p) {
			continue
		}
		add(p)
	}
	// Scanned repos arrive already ranked (most-recently-active first) and are
	// deliberately not re-stat'd here — up to 2000 synchronous stats on form
	// open would hurt on slow filesystems, and a stale entry is caught by the
	// async validity check on selection plus the submit gate.
	for _, p := range m.scannedRepos {
		add(p)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	return paths
}

// recordRecentPath records a newly-started session's repo path in the MRU list. It is
// best-effort: a persistence error is logged but does not interrupt the session flow.
func (m *home) recordRecentPath(path string) {
	if err := m.appState.AddRecentPath(path); err != nil {
		log.WarningLog.Printf("failed to record recent path %q: %v", path, err)
	}
}

// persistDraft mirrors the in-memory stashed draft to state.json so a non-destructive
// Escape survives a crash or quit, not just an in-run reopen. It serializes only the
// values with stable overlay setters (title, prompt, project) — the live overlay is
// not serializable. Best-effort, like recordRecentPath: a write error is logged, never
// surfaced. Only called for a dirty create form, so it never persists an empty draft.
func (m *home) persistDraft(ov *overlay.TextInputOverlay) {
	draft := &config.SessionDraft{
		Title:  ov.GetTitle(),
		Prompt: ov.GetValue(),
		Path:   ov.GetSelectedPath(),
	}
	if err := m.appState.SetDraft(draft); err != nil {
		log.WarningLog.Printf("failed to persist new-session draft: %v", err)
	}
}

// clearPersistedDraft drops the on-disk stashed draft, keeping it in lockstep with the
// in-memory stash (cleared on submit, restore-consume, and clear-form). Best-effort.
func (m *home) clearPersistedDraft() {
	if err := m.appState.ClearDraft(); err != nil {
		log.WarningLog.Printf("failed to clear new-session draft: %v", err)
	}
}

// cancelPromptOverlay cancels the prompt overlay.
func (m *home) cancelPromptOverlay() tea.Cmd {
	// Keep a dirty create form as a draft so a deliberate Escape-to-check-something
	// is non-destructive; everything else (clean form, quick-send, smart-dispatch)
	// is discarded as before.
	if m.textInputOverlay != nil && m.textInputOverlay.IsCreateForm() && m.textInputOverlay.IsDirty() {
		// Drop any pending "⌃R again" arm so it can't survive a Ctrl+C cancel (which
		// bypasses the overlay's own disarm) and make the next single Ctrl+R a wipe.
		m.textInputOverlay.DisarmClear()
		// The stash reuses this very overlay, whose Canceled flag was just set by the
		// Escape that triggered this stash. Clear the transient submit/cancel flags so
		// the restored draft is a clean, submittable form — otherwise handlePromptState
		// checks IsCanceled before IsSubmitted, so every later Enter/Ctrl+S on the
		// restored form is misread as a cancel and the session is never created.
		m.textInputOverlay.Canceled = false
		m.textInputOverlay.Submitted = false
		m.stashedDraft = m.textInputOverlay
		// Mirror the stash to disk so it outlives a crash/quit before the reopen.
		m.persistDraft(m.textInputOverlay)
	}
	m.textInputOverlay = nil
	m.state = stateDefault
	m.resetTitleCheck()
	return tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// killDataWarning returns a parenthetical suffix for the kill confirmation that
// warns when killing would discard local work, or "" when the branch has nothing
// unmerged. commits is the count of commits ahead of base — pause folds its
// auto-WIP commit in via noteAutoPauseCommit, so a paused-then-dirty session reads
// Dirty=false with commits>=1. Kill runs `git branch -D`, so these commits are
// destroyed with no user-facing recovery path.
func killDataWarning(dirty bool, commits int) string {
	switch {
	case dirty && commits > 0:
		return fmt.Sprintf(" (has uncommitted changes and %d unmerged commit%s — deleting discards them)", commits, plural(commits))
	case dirty:
		return " (has uncommitted changes)"
	case commits > 0:
		return fmt.Sprintf(" (has %d unmerged commit%s — deleting discards them)", commits, plural(commits))
	}
	return ""
}

// sessionsWithUnmergedWork counts how many of insts carry uncommitted changes or
// commits ahead of base, for the batch-kill confirmation's aggregate warning.
func sessionsWithUnmergedWork(insts []*session.Instance) int {
	n := 0
	for _, inst := range insts {
		if s := inst.GetDiffStats(); s != nil && (s.Dirty || s.Commits > 0) {
			n++
		}
	}
	return n
}

// confirmKill shows the kill-confirmation overlay for inst and stashes the
// teardown action. inst need not be the selected instance: the in-session kill
// key (Ctrl+X) and the auto-open path target a specific session regardless of
// the current list selection, so the action keys on inst (and KillInstance)
// rather than on whatever happens to be selected when the user confirms.
func (m *home) confirmKill(inst *session.Instance) tea.Cmd {
	if inst == nil || inst.GetStatus() == session.Loading {
		return nil
	}

	killAction := func() tea.Msg {
		// Refuse to kill only when the branch is checked out in the primary repo
		// itself (deleting it would strand the user's main checkout on a dangling
		// branch). A live session's branch is always checked out in the session's
		// OWN worktree, so we must NOT use IsBranchCheckedOut here — that any-worktree
		// check would refuse every running session. IsBranchHeldByBaseRepo is the
		// base-repo-only predicate. This is a teardown path: if the worktree or its
		// repo is unreachable — e.g. the user renamed/removed the project directory —
		// fail open and proceed, otherwise an orphaned session can never be deleted.
		// A direct (non-git) session has no branch or worktree, so skip the base-repo
		// branch check entirely — calling GetGitWorktree would only log a misleading
		// "cannot resolve worktree" warning for a session that never had one.
		if !inst.IsDirect() {
			if worktree, err := inst.GetGitWorktree(); err != nil {
				log.WarningLog.Printf("kill %s: cannot resolve worktree, proceeding: %v", inst.Title, err)
			} else if heldByBase, cerr := worktree.IsBranchHeldByBaseRepo(); cerr != nil {
				log.WarningLog.Printf("kill %s: cannot verify branch checkout, proceeding: %v", inst.Title, cerr)
			} else if heldByBase {
				return fmt.Errorf("branch for %s is checked out in the main repo; switch it away before deleting", inst.DisplayName())
			}
		}

		// Clean up terminal session for this instance
		m.tabbedWindow.CleanupTerminalForInstance(inst)

		// Delete from storage first
		if err := m.storage.DeleteInstance(inst.Title, inst.Path); err != nil {
			return err
		}

		// Then kill the instance. The row and storage entry are gone regardless, but
		// if teardown left something behind (a live tmux session or a leftover
		// branch) name the session and surface what leaked instead of reporting a
		// clean kill.
		killErr := m.list.KillInstance(inst)
		m.forgetInstance(inst) // the row is gone regardless of teardown outcome; drop its bookkeeping
		if killErr != nil {
			return fmt.Errorf("killed '%s' but teardown was incomplete: %w", inst.DisplayName(), killErr)
		}
		return instanceChangedMsg{}
	}

	message := fmt.Sprintf("Kill session '%s'?", inst.DisplayName())
	if stats := inst.GetDiffStats(); stats != nil {
		message += killDataWarning(stats.Dirty, stats.Commits)
	}
	cmd := m.confirmAction(message, killAction)
	// Kill is the one destructive confirmation, so it alone wears the danger
	// border (the default is accent); confirmAction created m.confirmationOverlay
	// synchronously above.
	m.confirmationOverlay.SetBorderColor(theme.Current().Palette.Danger)
	// Opt-in: a second press of the kill key confirms the dialog, so Ctrl+X Ctrl+X
	// kills in one motion. Scoped to the kill dialog (other confirmations still
	// require 'y').
	if m.appConfig.GetKillDoubleTapConfirm() {
		m.confirmationOverlay.SetConfirmAltKey(keys.KillKey)
	}
	return cmd
}

// confirmWidth is the confirmation dialog's width for the given terminal
// width: the classic 50 columns when they fit, shrinking with the terminal
// (border + a margin) on narrow ones so the box never spills off-screen. A
// zero terminal width (startup, tests) keeps the default.
func confirmWidth(termWidth int) int {
	const preferred = 50
	if termWidth <= 0 {
		return preferred
	}
	return max(20, min(preferred, termWidth-4))
}

// confirmAction shows a confirmation modal and stores the action to execute on
// confirm. The action is run (and its result dispatched) by the stateConfirm key
// handler, not here, so its returned message — including any error — flows through
// Update instead of being discarded.
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm
	m.pendingConfirmAction = action

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	m.confirmationOverlay.SetWidth(confirmWidth(m.windowWidth))

	return nil
}

// confirmAsyncAction is confirmAction for a confirmed action that should run off
// the UI thread. It stashes busyLabel so handleConfirmState launches the action
// through beginAsyncAction (goroutine + "busy" progress row) instead of running it
// inline. The action closure must be UI-thread-safe: touch only its captured
// instance/worktree and return a message — never read or mutate m.* (list, storage,
// menu), which belongs on the Update loop. The overlay is still built synchronously
// (via confirmAction) so callers that restyle it keep working.
func (m *home) confirmAsyncAction(message, busyLabel string, action tea.Cmd) tea.Cmd {
	cmd := m.confirmAction(message, action)
	m.pendingConfirmBusyLabel = busyLabel
	return cmd
}

// asyncActionDoneMsg wraps the result of an off-UI-thread action (see
// beginAsyncAction) so Update can clear the in-flight state before the inner
// message is handled. result is whatever the action returned (a success message,
// an error, or nil).
type asyncActionDoneMsg struct{ result tea.Msg }

// beginAsyncAction runs cmd off the UI thread behind a "busy" progress row. It
// arms the StateBusy bar (label), marks actionInFlight so handleKeyPress refuses
// re-entry and mutating keys, and reclaims a layout row. The returned command runs
// cmd in a goroutine and wraps whatever it returns in asyncActionDoneMsg, so the
// in-flight state is cleared and the inner message dispatched on the Update loop.
func (m *home) beginAsyncAction(label string, cmd tea.Cmd) tea.Cmd {
	m.actionInFlight = true
	m.menu.SetBusy(label)
	m.recomputeLayout() // the progress row now claims a line; shrink the panes to fit
	return func() tea.Msg {
		var result tea.Msg
		if cmd != nil {
			result = cmd()
		}
		return asyncActionDoneMsg{result: result}
	}
}
