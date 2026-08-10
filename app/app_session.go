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
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
)

// tmuxAvailable is the tmux-usability seam for the new-session pre-flight guards
// (the create-form gate and the smart-dispatch auto-create path). It reports a tmux
// that is missing *or* older than tmux.MinVersion. A package var (matching the
// detectAgents/checkDrift idiom) so tests inject a verdict without touching PATH.
var tmuxAvailable = tmux.Available

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

// pushSessionContexts refreshes the in-session context bar for every live session,
// returning a Cmd that performs the tmux writes off the update thread.
//
// Composition and the change check stay here (they are pure string work over
// main-thread state, and the cache they consult is main-thread-only); only the
// sessions whose bar actually changed are handed to the goroutine. This runs from
// the 500ms metadata apply, where it used to fire a has-session per instance plus a
// set-option batch per changed one — inline, on the thread that also handles keys
// (#380). Liveness now comes from the poll's own verdict instead of a second probe.
//
// No-op when the feature is disabled.
func (m *home) pushSessionContexts() tea.Cmd {
	if !m.appConfig.GetSessionContextBar() {
		return nil
	}
	type armed struct {
		inst       *session.Instance
		name, left string
	}
	var pending []armed
	for _, inst := range m.list.GetInstances() {
		if !inst.Started() || inst.Paused() || !inst.PaneLive() {
			continue
		}
		name, left := ui.ComposeSessionContext(inst, ui.RepoKey(inst))
		if inst.ArmContext(name, left) {
			pending = append(pending, armed{inst: inst, name: name, left: left})
		}
	}
	if len(pending) == 0 {
		return nil
	}
	return func() tea.Msg {
		var failed []*session.Instance
		for _, p := range pending {
			if err := p.inst.PushContext(p.name, p.left); err != nil {
				log.WarningLog.Printf("failed to push session context for %q: %v", p.inst.Title, err)
				failed = append(failed, p.inst)
			}
		}
		return contextPushFailedMsg{instances: failed}
	}
}

// pushOneContext composes and pushes the context bar for a single session
// synchronously, for the one caller that needs it to land before it hands the
// terminal over (the attach cycle). Skips sessions with no live pane to render in.
func (m *home) pushOneContext(inst *session.Instance) {
	if !m.appConfig.GetSessionContextBar() || !inst.Started() || inst.Paused() || !inst.TmuxAlive() {
		return
	}
	name, left := ui.ComposeSessionContext(inst, ui.RepoKey(inst))
	if err := inst.SetContext(name, left); err != nil {
		log.WarningLog.Printf("failed to push session context for %q: %v", inst.Title, err)
	}
}

// validateDeepRename checks whether selected may be renamed to value. It performs
// no I/O and changes nothing: the rename itself (tmux session, git branch, worktree
// directory) runs off the update thread in renameIOCmd, and persisting is the
// caller's responsibility so the rename and any note edit land in a single
// state.json write.
//
// The check stays on the main thread because it reads the whole instance list. It
// rejects an empty title or one already used in the instance's repo group — comparing
// derived names (tmux segment, branch slug), not raw titles, and also reserving the
// qualified tmux name the rename would mint (plus its derived siblings — the "_term"
// terminal shell and the "_run" run command, see session.DerivedTmuxNameCollides)
// against every session. Same-titled sessions in other groups are fine: their qualified
// tmux names differ.
func (m *home) validateDeepRename(selected *session.Instance, value string) error {
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
		if session.DerivedTmuxNameCollides(cand, inst.TmuxSessionName()) {
			return fmt.Errorf("renaming to %q collides with session %q", value, inst.Title)
		}
	}
	return nil
}

// renameDoneMsg carries a deep rename's result back to the main loop. value and
// note are echoed so a failure can reopen the overlay with exactly what the user
// typed — nothing is persisted until a rename succeeds. renamed is the identity
// the I/O earned, applied on the update thread by the handler (see AdoptRename).
type renameDoneMsg struct {
	instance *session.Instance
	value    string
	note     string
	renamed  session.RenamedIdentity
	err      error
}

// renameIOCmd performs the deep rename off the update thread: a tmux
// rename-session, a git branch rename, and a worktree DIRECTORY MOVE. The old
// doc comment called that "a handful of instant subprocesses" — an unmeasured
// claim of exactly the kind the named-loading policy exists to stop (#380).
// Validation stays on the main thread (it reads m.list); only the I/O moves.
//
// The I/O half writes no model state at all: Instance.Rename returns the new
// identity rather than assigning Title/Branch, because those are read unguarded
// by the renderer on every frame. The handler adopts it.
func renameIOCmd(inst *session.Instance, value, note string) tea.Cmd {
	return func() tea.Msg {
		renamed, err := inst.Rename(value)
		return renameDoneMsg{instance: inst, value: value, note: note, renamed: renamed, err: err}
	}
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
	// fromBatch marks a session spawned as one variant of a multi-session fan-out
	// (#387). Auto-attach is suppressed for a batch: attaching into one variant and
	// then, on each detach, chaining through the rest would bury the list the bake-off
	// is meant to be viewed from.
	fromBatch bool
}

// autoAttachEligible is the pure auto-attach policy, independent of session liveness:
// attach on the auto_attach flag, but never for a session created with a boot prompt
// (hadPrompt — see shouldAutoOpen) and never for a fan-out variant (fromBatch — a
// bake-off spawns N sessions from one submit; dropping the user into one and chaining
// them through the rest on each detach is never what they want, so they should land on
// the list). Split from shouldAutoOpen so the policy is unit-testable without a live
// (Started/TmuxAlive) session.
func (m *home) autoAttachEligible(hadPrompt, fromBatch bool) bool {
	return m.appConfig.GetAutoAttach() && !hadPrompt && !fromBatch
}

// shouldAutoOpen reports whether a freshly started session should be attached
// automatically. It is the auto-attach policy (autoAttachEligible) gated by session
// liveness. The policy is skipped when the session was created with an initial prompt
// (hadPrompt): delivery is asynchronous (metadata tick), and while attached only the
// keeper delivers — which excludes the attached session itself, so auto-opening it
// would starve its own prompt. The creation-time flag, not the live Prompt(), is the
// signal: the keeper may already have delivered and cleared the prompt while this
// message was parked. The Started/TmuxAlive guards avoid attaching a session that did
// not come up — and, because Started() short-circuits before TmuxAlive() (which
// dereferences tmuxSession), keep unstarted instances (e.g. in tests) off both the
// panic and the attach path.
func (m *home) shouldAutoOpen(inst *session.Instance, hadPrompt, fromBatch bool) bool {
	return m.autoAttachEligible(hadPrompt, fromBatch) && inst.Started() && inst.TmuxAlive()
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
// headless one-shot prompting (see session.GenerateName), billed to the session's
// own Claude account — ClaudeConfigDir is the dir this session actually launched
// under, so naming a session cannot spend an account the session never touched.
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
		name, err := session.GenerateName(ctx, instance.Program, instance.ClaudeConfigDir(), prompt, stats)
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
	// Refuse only a hard (explicit) cap up front; a host-derived soft cap lets the
	// route/form proceed and confirms at the actual create (autoDispatch below or the
	// form submit).
	if sc := m.sessionCap(); capVerdict(sc, m.list.NumInstances(), 1) == capBlock {
		m.textInputOverlay = nil
		m.state = stateDefault
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", sc.Limit))
	}

	// Refuse before routing when tmux is unusable — missing, or too old for the
	// `new-session -e` every launch passes: no session can be created either way, so
	// neither auto-dispatch nor the seeded form should proceed. This path
	// enters at statePrompt with the dispatch overlay open, so reset to stateDefault
	// first — that clears the stale overlay and lets handleError route the wide
	// sentinel to the persistent info modal (it only does so from stateDefault)
	// rather than a truncated toast. Mirrors openCreateFormSeeded's bare n/N guard.
	if err := tmuxAvailable(); err != nil {
		m.textInputOverlay = nil
		m.state = stateDefault
		return m.handleError(err)
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

// autoDispatch handles a confident match without the form, returning (cmd, true) when it
// takes ownership of the line and (nil, false) when it declines — an invalid target or a
// title collision — so the caller falls back to the confirmation form. Taking ownership
// usually means the session is created directly, but crossing the host-derived soft cap,
// or routing to a pool with no available member, instead returns a confirmation command
// (still true): nothing is created until the user accepts. Because it bypasses the form,
// the session launches with the agent's default permission mode — opting into
// smart_dispatch_auto deliberately trades away the per-session permission choice the
// form's Permissions chip would otherwise offer.
//
// What it does not trade away is the account gates. Opting into smart_dispatch_auto says
// "don't make me confirm the routing", not "spend an account I have flagged unusable" —
// so this path runs the same all-exhausted gate the create form does, and gets the same
// answer (#483). Skipping it was how a fully rate-limited pool spawned silently here, on
// the rotation cursor's member rather than the soonest-to-reset one the confirm pins.
func (m *home) autoDispatch(res PrefillResult) (tea.Cmd, bool) {
	valid, direct, _ := targetValidity(m.ctx, res.Path)
	if !valid {
		return nil, false
	}
	m.newSessionGroup = git.RepoGroupKey(m.ctx, res.Path)
	if m.titleConflict(res.Title) != "" {
		return nil, false
	}
	// Host-capacity gate for the formless auto-dispatch. A hard cap can't have been
	// reached here (the pre-route guard refused it), but fall back to the form if it
	// somehow is; a soft cap over budget confirms before creating.
	sc := m.sessionCap()
	count := m.capCount(sc)
	plan := spawnPlan{
		titles: []string{res.Title}, path: res.Path, direct: direct,
		programs: []string{m.program}, prompt: res.Prompt,
	}
	verdict := capVerdict(sc, count, 1)
	if verdict == capBlock {
		return nil, false
	}

	// Gate order mirrors createSessionFromForm, and is load-bearing for the same
	// reason: neither accept path re-checks the other gate, so whichever confirm is
	// staged first is the only one that ever runs. Hard cap first (already refused
	// above), then all-exhausted, then the soft cap — staging the soft-cap confirm
	// ahead of this would let accepting it spawn on a fully rate-limited pool without
	// ever asking.
	if cmd, gated := m.gateAllExhausted(plan); gated {
		return cmd, true
	}

	if verdict == capConfirm {
		return m.confirmOverCap(plan, sc.Limit, count), true
	}
	cmd, err := m.startNewSession(res.Title, res.Path, direct, m.program, "", res.Prompt, nil, false, nil)
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
//
// The account is resolved here, on the Update goroutine, for the same reason ctx and
// program are: the closure runs off it and must not read the model. Routing is asked
// with no remote and no path, which is not a placeholder — it is this call's actual
// situation, and the router already answers it with the catch-all account (or "",
// inherit the ambient env, when no catch-all is configured). A routing call cannot
// use the route it exists to propose, so it bills the account an unroutable session
// would get (#497).
func (m *home) runSmartDispatchCmd(line string, candidates []string, form *overlay.TextInputOverlay) tea.Cmd {
	ctx := m.ctx
	program := m.program
	_, acctDir, _ := m.appConfig.ResolveClaudeAccount("", "")
	return func() tea.Msg {
		project, title, err := session.GenerateDispatch(ctx, program, acctDir, line, candidates)
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
//
// One session is a small overshoot, but r pressed twelve times is the same host load
// as one "resume all", so it takes the same host-capacity question (#463) — only when
// it crosses, leaving the key as frictionless as it was for every resume that fits.
// The question names the session and the capacity and stops there: r has never
// described what resume does, and the batch dialog is where those semantics are named.
// Accepting runs this same action off the UI thread behind the same progress row
// (handleConfirmState re-arms beginAsyncAction with the busy label).
func (m *home) resumeSelected(selected *session.Instance) tea.Cmd {
	// Before the confirmation, not inside the action: a session that must not run on
	// this login should not first ask the user whether to spend host capacity on it.
	if err := m.verifyResumeIdentity(selected); err != nil {
		return m.handleError(err)
	}
	action := func() tea.Msg {
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
	}
	// One label for both paths, so the progress row reads the same whether the resume
	// was immediate or came through the capacity confirmation — the same anti-drift
	// reason SetConfirmLabel sits next to the message it labels.
	const resumeLabel busyLabel = "resuming…"
	if clause := m.resumeCapNotice(1); clause != "" {
		cmd := m.confirmAction(
			fmt.Sprintf("Resume session '%s'?\n%s", selected.DisplayName(), clause),
			resumeLabel, action)
		m.confirmationOverlay.SetConfirmLabel("resume")
		return cmd
	}
	return m.beginAsyncAction(string(resumeLabel), action)
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
		// A resume re-points the preview at a brand-new pane behind the same
		// *Instance, so instanceChanged cannot see it — the pointer did not move. Say
		// so explicitly, or a quiet run built before the pause would still be settled
		// afterwards and the freshly resumed pane would never be captured at the frame
		// cadence (framegate.go).
		m.noteFrameTargetChange()
		return tea.RequestWindowSize
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
	return m.confirmAction(message, "resuming…", func() tea.Msg {
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
	return m.resumeInstances(paused, resumeConfirmMessage("paused", len(paused)))
}

// resumeConfirmMessage is the batch-resume question for n sessions described by kind
// ("paused" for the whole view, "marked" for a multi-select subset), plus what resume
// puts back — the confirmation-voice rule in app_feedback.go: ask with the verb, then
// name what the user cannot see. What the list cannot show is that resume is the
// inverse of pause: the worktree is materialized again at the same path (with the
// auto-WIP commit unwound) and the agent comes back.
//
// It says "each *removed* worktree", not "each worktree", because two of
// Instance.Resume's three paths rebuild nothing, and both are reachable from a batch:
// a direct session has no worktree at all — Pause refuses one, but RecoverLostSession
// parks it when its pane dies, and PausedInstancesInView takes every Paused row, where
// ActiveInstancesInView (pause's scope) filters IsDirect out, so the two batch scopes
// are not mirror images — and a park that left the worktree materialized has one Resume
// reuses rather than re-adding over the work it holds, which several parks do (see
// session.Instance.Resume for which). Reattaching the agent is the half true of every
// path, so that half carries no qualifier.
//
// It says *reattaches* because pause detaches tmux rather than closing it
// (session.Instance.pause), so the agent process is normally still alive and Resume
// only restores the PTY; "restarts" would read as losing the conversation and scare
// the user off a non-destructive action. One literal, shared by both entry points, so
// resumeAll and resumeMarked cannot drift apart.
func resumeConfirmMessage(kind string, n int) string {
	return fmt.Sprintf("Resume %d %s session%s? (rebuilds each removed worktree and reattaches every agent)",
		n, kind, plural(n))
}

// resumeInstances resumes an explicit set of (already-paused) sessions behind a
// count confirmation — the shared core of resumeAll and resumeMarked. A
// per-instance failure (e.g. BranchCheckedOutError) is recorded and the run
// continues; the outcome is surfaced as a summary. The resume runs off the UI
// thread (confirmAction with a busy label); state is persisted once, on the Update loop, in the
// batchResumeDoneMsg handler. Resume is non-destructive, so it keeps the default
// accent border (only kill wears the danger border).
//
// A batch that would put the live population past the host-derived budget says so in
// this same dialog rather than a second one (#463): resume is the only action that
// grows that population without creating a session, and the confirmation the user
// already has to answer is where the cost belongs. Computed here, in the shared core,
// so resumeAll and resumeMarked cannot drift — the reason SetConfirmLabel is here too.
func (m *home) resumeInstances(insts []*session.Instance, message string) tea.Cmd {
	if clause := m.resumeCapNotice(len(insts)); clause != "" {
		message += "\n" + clause
	}
	action := func() tea.Msg {
		var res batchResumeDoneMsg
		for _, inst := range insts {
			// Per instance, not as a pre-filter: a batch may mix sessions on
			// different accounts, and one wrong login must not cancel the resume of
			// every session beside it. A refusal reports like any other resume
			// failure, so the batch summary names it.
			if err := m.verifyResumeIdentity(inst); err != nil {
				res.failures = append(res.failures, resumeFailure{inst.Title, err})
				continue
			}
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
	cmd := m.confirmAction(message, busyLabel(label), action)
	// The hint names the action rather than saying "confirm" (#399). Set here, in the
	// shared core, so both entry points get the same label from the same count
	// (confirmAction created m.confirmationOverlay synchronously above).
	m.confirmationOverlay.SetConfirmLabel(fmt.Sprintf("resume %d session%s", len(insts), plural(len(insts))))
	return cmd
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
	return m.pauseInstances(active, pauseConfirmMessage("active", len(active)))
}

// pauseConfirmMessage is the batch-pause question for n sessions described by kind
// ("active" for the whole view, "marked" for a multi-select subset), plus the
// consequence — the confirmation-voice rule in app_feedback.go: ask with the verb,
// then name what the user cannot see. Pause stages and commits everything git tracks
// (Worktree.CommitChanges) and then removes the worktree, so the gitignored files
// living in it — a local .env, a build cache, downloaded dependencies — go with it,
// and resume rebuilds the worktree from the branch. The README documents only the
// carry_files slice of this (those entries are re-seeded on every Setup, resume
// included); nothing names the general loss, and nothing brings those files back.
// Undo (#391) does not change that: it restores the branch, the worktree and the
// agent, but a gitignored file was never in a commit for it to restore from — which
// is why the undo confirmation names the same loss in its own words.
//
// Unconditional, unlike killDataWarning: a pause that completes removes the worktree,
// so only the magnitude of the loss varies, and measuring that would mean a
// per-session `git status --ignored` on the UI thread. The one path that keeps the
// worktree is a failure — when the auto-WIP commit fails, pause deliberately leaves it
// on disk rather than destroy the work it exists to protect (session.Instance.pause)
// and reports the error through the batch summary, so the promise here is the one a
// successful pause keeps, not a claim the dialog can break silently.
//
// It claims deletion rather than "not restored on resume" because carry_files re-seeds
// its own (gitignored) entries into every fresh worktree, resume included — the
// deleted copy is still gone, but a blanket "nothing ignored comes back" would be
// false for those. That cuts against .env as the example, since .env is the archetypal
// carry_files entry: a user who listed it sees the origin's copy reappear, having lost
// only the session's edits to it. It stays because the default carry list is just
// .claude/settings.local.json (config.defaultCarryFiles), so for an unconfigured user
// — the one this dialog is warning — .env really is gone.
func pauseConfirmMessage(kind string, n int) string {
	return fmt.Sprintf("Pause %d %s session%s? (commits any work in progress, then removes "+
		"each worktree — gitignored files like .env or build caches are deleted for good)",
		n, kind, plural(n))
}

// pauseInstances parks an explicit set of (pausable) sessions behind a count
// confirmation — the shared core of pauseAll and pauseMarked. Each Pause commits
// WIP, detaches tmux, and removes the worktree (keeping the branch); a
// per-instance failure is recorded and the run continues, with the outcome
// surfaced as a summary. The run happens off the UI thread (confirmAction with a label);
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
	cmd := m.confirmAction(message, busyLabel(label), action)
	// The hint names the action rather than saying "confirm" (#399). Set here, in the
	// shared core, so both entry points get the same label from the same count
	// (confirmAction created m.confirmationOverlay synchronously above).
	m.confirmationOverlay.SetConfirmLabel(fmt.Sprintf("pause %d session%s", len(insts), plural(len(insts))))
	return cmd
}

// killFailure records one instance that could not be killed during a batch kill,
// paired with the reason so the summary can name it.
type killFailure struct {
	title string
	err   error
}

// killOutcome pairs one instance with the result of tearing its tmux session and
// worktree down. It is what the goroutine half of a kill produces; the model half
// (storage, row, bookkeeping) is applied from it on the update thread.
type killOutcome struct {
	inst *session.Instance
	err  error
}

// batchKillDoneMsg carries a batch kill's teardown results back to the main loop,
// which applies every model change they imply — storage deletes, row removals,
// preview-terminal cleanup — and then reports the outcome (notice vs. modal).
//
// torndown holds one entry per instance that got as far as teardown, successful or
// not; failures holds the ones refused before that (a branch held by the base
// repo). killed and killedInstances are filled in by the handler as it applies the
// results, so the counts describe what actually happened to the model.
type batchKillDoneMsg struct {
	torndown        []killOutcome
	killed          int
	killedInstances []*session.Instance
	failures        []killFailure
	// undoable counts the entries the journal actually recorded, so the notice can
	// only advertise a recovery that exists.
	undoable int
	// armed is every instance the confirm-time hook marked retiring, carried back so
	// the handler can clear all of them. It is not the same set as torndown: an
	// instance refused for a base-repo branch checkout is recorded as a failure and
	// never torn down, but its mark still has to go.
	armed []*session.Instance
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

// applyBatchKill lands a batch kill's teardown results on the model: it deletes
// each retired session from storage, drops its row, and forgets its bookkeeping.
// Main thread only — every line here touches shared model state, which is exactly
// why it is not in the goroutine that did the tearing down.
//
// Ordering note: the split means the tmux session and worktree are already gone by
// the time storage is written, so a DeleteInstance failure can no longer abort the
// removal — the session is retired whether or not its record could be cleared. The
// row goes regardless (a row pointing at a deleted worktree is worse than none)
// and the failure is named, so a persisted ghost is reported rather than silent.
func (m *home) applyBatchKill(msg batchKillDoneMsg) batchKillDoneMsg {
	// Every instance the batch armed, not just the ones it managed to tear down —
	// a refused session keeps its row and must keep its recovery.
	m.endTeardown(msg.armed)
	for _, out := range msg.torndown {
		inst := out.inst
		m.tabbedWindow.CleanupTerminalForInstance(inst)
		storeErr := m.storage.DeleteInstance(inst.Title, inst.Path)
		m.list.RemoveInstance(inst)
		m.forgetInstance(inst)

		switch {
		case out.err != nil:
			msg.failures = append(msg.failures, killFailure{inst.Title,
				fmt.Errorf("removed, but teardown was incomplete: %w", out.err)})
		case storeErr != nil:
			msg.failures = append(msg.failures, killFailure{inst.Title,
				fmt.Errorf("removed, but its saved state could not be cleared: %w", storeErr)})
		default:
			msg.killed++
		}
		msg.killedInstances = append(msg.killedInstances, inst)
	}
	return msg
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
// continues), then kills. Kill is destructive, so the confirmation wears the
// danger border like the single-kill dialog.
//
// Only the I/O runs in the goroutine; storage, the rows and the per-instance
// bookkeeping are applied by applyBatchKill on the update thread. It arms through
// the same armTeardown as the single kill, which matters most here: this is the
// LONGEST teardown window in the app, so it is the likeliest to have a poll tick
// land mid-flight and the costliest to leave un-joined at quit.
//
// altConfirmKey is the key that opened the dialog, which double-taps to confirm when
// kill_double_tap_confirm is on. It is a parameter rather than keys.KillKey() because
// visual mode advertises plain "x" and accepts ctrl+x too, so the key the dialog must
// echo is the one the user pressed — hard-coding the chord would print "(or ctrl+x)"
// at someone who never pressed it.
func (m *home) killInstances(insts []*session.Instance, message string, altConfirmKey string) tea.Cmd {
	// One ID for the whole run, minted when the batch is armed rather than inside
	// the loop, so every entry it writes belongs to the same undoable action.
	batchID, err := undo.NewID(time.Now())
	if err != nil {
		log.WarningLog.Printf("undo: cannot mint a batch id, this kill will not be undoable: %v", err)
	}
	// The teardown I/O only. Everything that touches the model — storage, the row,
	// per-instance bookkeeping — is applied by the batchKillDoneMsg handler on the
	// update thread. Before the split this whole loop ran inline on that thread with
	// no progress row at all: ten sessions is ~60 subprocesses and ten recursive
	// worktree deletes, so the UI simply stopped for seconds (#380).
	//
	// Journalling belongs here rather than in the handler for the same reason the
	// rest of it does: it is git I/O, and it has to happen before Kill destroys what
	// it records.
	io := func() tea.Msg {
		res := batchKillDoneMsg{armed: insts}
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
			// Record and pin before the session stops existing, exactly as the
			// single-kill path does — visual-mode `x` tears down everything marked,
			// which is the kill an undo is most wanted for. One batch ID groups the
			// whole run so undoing it reverses the one action the user took.
			if _, undoable := m.journalKill(inst, batchID); undoable {
				res.undoable++
			}
			// Tear the session down (tmux + worktree + branch). A failure is recorded
			// naming what leaked, but the row still goes: the session is retired either
			// way, and the handler says so rather than implying it survived.
			res.torndown = append(res.torndown, killOutcome{inst: inst, err: inst.Kill()})
		}
		return res
	}
	label := fmt.Sprintf("killing %d session%s…", len(insts), plural(len(insts)))
	arm, action := m.armTeardown(insts, io)
	cmd := m.confirmAction(message, busyLabel(label), action)
	m.armOnConfirm(arm)
	// Kill is destructive, so it wears the danger border (confirmAction created
	// m.confirmationOverlay synchronously above).
	m.confirmationOverlay.SetDestructive()
	// Mirror confirmKill's double-tap shortcut: with kill_double_tap_confirm on,
	// pressing the opening key again confirms the batch dialog, matching single-kill
	// muscle memory (x x, or Ctrl+X Ctrl+X).
	if altConfirmKey != "" && m.appConfig.GetKillDoubleTapConfirm() {
		m.confirmationOverlay.SetConfirmAltKey(altConfirmKey)
	}
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
	return m.pauseInstances(insts, pauseConfirmMessage("marked", len(insts)))
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
	return m.resumeInstances(insts, resumeConfirmMessage("marked", len(insts)))
}

// killMarked tears down the killable subset of the multi-select-marked sessions
// (everything except a still-Loading session, which single-kill also refuses).
// Same eligibility/exit semantics as pauseMarked. openedBy is the key that got here
// (x or ctrl+x), forwarded so the confirmation double-taps on that same key.
func (m *home) killMarked(openedBy string) tea.Cmd {
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
	if n := sessionsWithUnpushedWork(insts); n > 0 {
		message += fmt.Sprintf(" (%d of %d have unpushed work)", n, len(insts))
	}
	return m.killInstances(insts, message, openedBy)
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

// preselectAccountFor points ov's account picker at the pool auto-routed for target
// (its ⇄ entry, so a rotating pool is what highlights), so the form shows the same
// cluster a submit would resolve. A no-op once the user drives the picker (see
// AccountPicker.SelectByName). isGit gates the remote lookup: a non-git target has
// no remote and routes by path.
func (m *home) preselectAccountFor(ov *overlay.TextInputOverlay, target string, isGit bool) {
	remoteURL := ""
	if isGit {
		remoteURL = git.GetRemoteURL(m.ctx, target)
	}
	if poolName, _, _ := m.appConfig.ResolveClaudePool(remoteURL, target); poolName != "" {
		ov.PreselectAccount(poolName)
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
	// A hard (explicit) cap refuses opening a form that could not be submitted; a
	// host-derived soft cap lets the form open and confirms at submit instead.
	if sc := m.sessionCap(); capVerdict(sc, m.list.NumInstances(), 1) == capBlock {
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", sc.Limit))
	}

	// Refuse to open the form when tmux is unusable (missing, or too old for the
	// `new-session -e` every launch passes): every session runs inside tmux, so
	// creation would fail anyway — but only after the user filled in the form and
	// a worktree was built and rolled back. Bailing here (all create paths route
	// through this function) shows one actionable message up front; the wide sentinel
	// routes through handleError to the persistent info modal.
	//
	// Reset to stateDefault (and drop any live overlay) FIRST, exactly as
	// handleSmartDispatchSubmit does. The n/N and palette entries are already there so
	// it is a no-op for them, but the Ctrl+R rebuild (app_keys.go) re-enters from
	// statePrompt with the old form still mounted: without this the refusal is a
	// truncated toast — handleError only reaches the info modal from stateDefault —
	// and the user is left staring at the stale form it just refused to rebuild.
	if err := tmuxAvailable(); err != nil {
		m.textInputOverlay = nil
		m.state = stateDefault
		return m.handleError(err)
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

	// Disarm any fork on the way in. openForkForm re-arms immediately after its own
	// call, so a fork survives only the open that meant it — and a restored draft
	// gets back the one that was parked with it, because the heading it still shows
	// promises a fork. Every other route in (a bare n, smart dispatch, a rebuilt
	// crash draft) starts disarmed, which is what stops an abandoned fork from being
	// spawned by a form that says nothing about it.
	m.pendingFork = nil
	if restore {
		m.newSessionPath = m.stashedDraft.GetSelectedPath()
		m.restorePendingFork()
	} else {
		m.stashedFork = nil
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
	cmds := []tea.Cmd{tea.RequestWindowSize}
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

// The title field's verdicts. Each is written to titleVerdictBudget — 21 cells, the
// room left on the Title row's 42 once the label (5), the two-space gap, the input's
// floor of 10 (+1 for bubbles' end-of-line cursor cell) and the " (" … ")" the verdict
// is wrapped in are paid for: 42 - 5 - 2 - 11 - 3. Past that the row grows instead of
// the input shrinking, and fitOverlay cuts it silently (#545).
//
// They are constants, and that is the fix rather than an accident of wording. Every
// one of these used to interpolate a name — the repo group, the derived branch, the
// colliding session's title — none of which has a ceiling, so the row's width was
// set by how long the user's branch happened to be. What the names said is on screen
// anyway: the target is in the Project row two lines up, and the branch is derived
// from the title being typed. Same trade as the variant refusals above.
//
// Naming them also makes the set enumerable, which is what
// TestTitleVerdicts_SurviveAn80ColRender needs: titleErrNameTaken fires only against
// a *started* session's tmux name, so it cannot be driven in a hermetic test, and a
// guard that only covered the senders it could reach would leave it unmeasured.
const (
	titleErrAlreadyUsed  = "already used"
	titleErrBranchExists = "branch already exists"
	titleErrNameTaken    = "session name taken"
	titleErrNoFreeNames  = "not enough free names"
)

// titleConflict reports why title cannot be used for a new session in the
// current target group ("" = no conflict). It compares derived names — not raw
// titles — against every listed instance regardless of status (a Paused session
// still owns its branch and tmux name):
//   - same group + colliding tmux segment or branch slug → duplicate;
//   - any instance whose tmux name equals the qualified name the title would
//     mint (a legacy unqualified name can shadow a qualified one), or either of
//     its derived siblings — the "_term" terminal shell and the "_run" run
//     command (session.DerivedTmuxNameCollides);
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
			return titleErrAlreadyUsed
		}
		if session.DerivedTmuxNameCollides(cand, inst.TmuxSessionName()) {
			return titleErrNameTaken
		}
	}
	if m.titleBranchExists && m.titleBranchName == git.BranchNameForSession(prefix, title) {
		return titleErrBranchExists
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

// composeProgramFlags folds the optional Claude model, permission-mode, and
// effort overrides into program, returning the augmented command. Each is applied only when non-empty
// and the program resolves to claude — the sole agent whose --model / --permission-mode
// flags these compose — and is re-validated as a backstop: the create form already
// filters the model field to agent.ValidModelName's charset (see ui/overlay/modelField.go)
// and offers a closed set of valid mode chips, so an invalid value reaching here means
// drift between the UI and the agent enums, caught before a dead launch rather than
// after. The mode check sees the model-augmented program, matching the form's submit
// order; since --model leaves the base command claude, Resolve is unaffected.
// Effort is composed the same way, but note the CLI soft-validates --effort
// (an unknown value is warned-and-ignored, not rejected like --permission-mode),
// so ValidEffort here is a UI/enum-drift backstop rather than a launch guard.
func composeProgramFlags(program, model, mode, effort string) (string, error) {
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
	if effort != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidEffort(effort) {
			return "", fmt.Errorf("invalid effort level %q", effort)
		}
		program = agent.WithEffortFlag(program, effort)
	}
	return program, nil
}

const (
	// maxVariantBatch caps how many sessions a single create-form submit may fan out
	// to (#387) — a fixed sanity bound on one batch, independent of max_sessions. The
	// effective session cap (SessionCap: the host-derived soft default or an explicit
	// value) is enforced separately and is usually the tighter limit. The per-profile
	// stepper is bounded too, but a multi-profile total is enforced here.
	maxVariantBatch = 20
	// variantTitleScan bounds how far past the requested count the suffix search
	// probes for free <title>-N names, so a repo dense with orphan <title>-N branches
	// cannot loop unboundedly. Generous — the common case finds names immediately.
	variantTitleScan = 100
)

// variantTitleConflict reports why candidate cannot be used as a new session title
// in the current target group ("" = free). It is titleConflict plus the synchronous
// local-branch-exists check the single-create submit runs, so every generated variant
// title is validated to the same bar (an orphan branch from a killed session would
// otherwise make a background Start fail late).
func (m *home) variantTitleConflict(candidate, path string, direct bool) string {
	if c := m.titleConflict(candidate); c != "" {
		return c
	}
	if !direct {
		branch := git.BranchNameForSession(m.appConfig.BranchPrefix, candidate)
		if git.LocalBranchExists(m.ctx, path, branch) {
			// The same verdict titleConflict returns for the async check, and it rides
			// the same Title row — so it is the same constant. Two spellings of one
			// fact is how the interpolating version survived here after the other was
			// bounded (#545).
			return titleErrBranchExists
		}
	}
	return ""
}

// planVariantTitles allocates total unique session titles from stem for a batch (#387).
// A single session keeps the bare stem — the pre-#387 contract, so an ordinary create is
// unchanged. A batch of N derives stem-1, stem-2, … in ascending order, skipping any suffix
// that collides with an existing session or an orphan branch in the target repo (the
// candidates are distinct by construction, so no in-batch bookkeeping is needed). It returns
// a non-empty conflict reason when the bare single title collides (mirroring the old inline
// error) or the suffix search is exhausted; otherwise the titles and "".
func (m *home) planVariantTitles(stem string, total int, path string, direct bool) ([]string, string) {
	if total <= 1 {
		if c := m.variantTitleConflict(stem, path, direct); c != "" {
			return nil, c
		}
		return []string{stem}, ""
	}
	titles := make([]string, 0, total)
	for n := 1; len(titles) < total && n <= total+variantTitleScan; n++ {
		cand := fmt.Sprintf("%s-%d", stem, n)
		if m.variantTitleConflict(cand, path, direct) != "" {
			continue
		}
		titles = append(titles, cand)
	}
	if len(titles) < total {
		return nil, titleErrNoFreeNames
	}
	return titles, ""
}

// createSessionFromForm validates the submitted new-session form and spawns one or more
// sessions from it, adding each to the list and starting it in the background with the
// entered prompt. A plain submit creates a single session; the variant control fans the
// one prompt + base branch out across N sessions for a bake-off (#387) — which needs a git
// target, since each variant wants its own worktree. The whole batch is validated up front —
// a git target, title uniqueness, the session cap, the batch ceiling — before any session is
// created, so a rejected batch never spawns partially. An explicit (hard) cap refuses an
// over-budget batch outright; the host-derived soft cap instead confirms once and, on
// acceptance, spawns via proceedOverCapMsg. Past validation the sessions are created in a
// loop; a deep per-variant start failure (rare — the
// pre-flight has cleared every checkable cause) closes the form and surfaces the error
// alongside whatever variants did start. On a validation error it leaves the overlay open
// (clearing the submitted flag) and surfaces the error so the user can correct the offending
// field.
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

	// Re-derive the group for the path actually being submitted (the picker may
	// have moved without an async verdict landing yet); title de-duplication and
	// branch checks below scope to it.
	m.newSessionGroup = git.RepoGroupKey(m.ctx, path)

	// The variant multiset: one program string per session to spawn (#387). An
	// untouched form yields a single default-program session, so the common path is
	// unchanged; a fan-out batch repeats the one prompt + base branch across the
	// chosen profiles for a side-by-side bake-off.
	variants := ov.GetVariants()
	if len(variants) == 0 {
		variants = []string{m.program} // defensive: the stepper guarantees ≥1, but never spawn zero
	}
	total := len(variants)

	// Whole-batch pre-flight (AC3): validate the entire batch up front and refuse it as
	// a unit before any session is created — a rejected batch never spawns partially. The
	// reason rides the variant control (an inline message visible with the form open; a
	// toast is swallowed behind the modal).
	//
	// Every message below is written to 32 cells: VariantPicker.Render puts it on the
	// label line, so the composed line is "Variants" + two spaces + the message, against
	// the 42 inner cells an 80-col terminal gives the overlay. fitOverlay truncates the
	// overflow *silently*, and all three of these shipped cut mid-clause (#541) — the
	// first losing the whole parenthetical that said what to do about it. Unlike the
	// form's hints these are not laddered by fitHint: a hint's tail is optional detail,
	// but a refusal's tail is the reason, and a ladder would hand the short rung to the
	// default terminal. One spelling that fits everywhere is the right shape here.
	//
	// The widths are bounded, not merely short: none of the three interpolates a value
	// derived from config, which is what makes each one's worst case a thing you can
	// state rather than assume — see each call site. app's
	// TestVariantRefusals_SurviveAn80ColRender drives all three and fails if a reword
	// stops fitting; ui/overlay's variantErrorBudget owns the 32.
	ov.SetVariantError("")
	// A batch needs worktree isolation so the variants don't clobber one another; a
	// direct (non-git) session runs the agent in the target directory itself, so N of
	// them would share it. Refuse a fan-out on a direct target (a single one is fine).
	if direct && total > 1 {
		ov.Submitted = false
		// 24 cells, no interpolation. The dropped "(each variant needs its own
		// worktree)" was the mechanism, which the sentence above documents and the
		// user does not need in order to act: point the form at a repo.
		ov.SetVariantError("fan-out needs a git repo")
		return nil
	}
	// A fork cannot fan out. Each session would need its own --session-id and its
	// own print run to materialize a separate copy of the truncated conversation,
	// and N sessions sharing one id is not a thing claude will do — it refuses a
	// --session-id already in use. Forking a checkpoint into a bake-off is a real
	// want (it is #387's shape exactly) but it is a batch of forks, not this.
	if m.pendingFork != nil && total > 1 {
		ov.Submitted = false
		// 31 cells, one under the 32 the variant row gives, and no interpolation —
		// every word is a constant. What to do about it is the chip row the message
		// sits on: put the counts back to one.
		ov.SetVariantError("forking spawns one session only")
		return nil
	}
	// The fork's print run is what materializes the truncated conversation, and it
	// refuses to run without a prompt — driven on claude 2.1.226, `-p ""` exits 1
	// with "Provide a prompt to continue the conversation" and writes no transcript
	// at all. So a fork needs a first prompt in a way an ordinary create does not,
	// where the field is explicitly optional, and the requirement is claude's rather
	// than a house rule. Refused here with the form open, rather than discovered as
	// a failed start after a worktree was built.
	//
	// After the fan-out check, not before: a form with both problems has to be told
	// about the counts, because that refusal rides the row the counts are on while
	// this one is a toast.
	if m.pendingFork != nil && strings.TrimSpace(prompt) == "" {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("a fork needs a first prompt — it is the turn the forked conversation starts from"))
	}
	if total > maxVariantBatch {
		ov.Submitted = false
		// 29 cells plus the digits of maxVariantBatch, a compile-time constant — 31 at
		// today's 20. Nothing here is derived from config, so that is a fact about the
		// code rather than a claim about realistic use. The batch's own total is
		// deliberately not interpolated: it is variantCountMax x len(profiles), and
		// len(profiles) has no ceiling anywhere, so a message carrying it would be
		// bounded only by such a claim — and by one no fixture could ever test, since
		// overflowing takes thousands of configured profiles. Same trade as the
		// max_sessions refusal below: the limit is the number the user has to get
		// under, and the counts they have to lower are on the chip row already.
		//
		// Spending 8 of those cells on "-session" is what dropping the total bought.
		// It names what the 20 counts, which "the 20 limit" left to inference next to
		// a max_sessions refusal that also ends in a number — but it leaves one cell
		// spare, so this is the one message here that could not also carry a word.
		ov.SetVariantError(fmt.Sprintf("batch over the %d-session limit", maxVariantBatch))
		return nil
	}
	// Allocate one unique title per variant before spawning any (AC2). A single
	// session keeps the bare title (the pre-#387 contract); a batch derives
	// <title>-1, <title>-2, … The conflict verdict surfaces inline on the title, with
	// focus returned there — no toast to miss.
	titles, conflict := m.planVariantTitles(title, total, path, direct)
	if conflict != "" {
		ov.Submitted = false
		ov.SetTitleError(conflict)
		ov.FocusTitle()
		return nil
	}

	// Pre-compose each variant's program so an invalid model/effort/mode override
	// aborts the whole batch before any session is created. composeProgramFlags gates
	// the claude overrides on each program independently, so a mixed batch (AC5)
	// carries the flags on its claude sessions alone and leaves codex/aider variants
	// untouched. The flags are folded into the persisted program string, so launch,
	// pause/resume, and the daemon all see them with no extra plumbing.
	model, mode, effort := ov.GetModel(), ov.GetPermissionMode(), ov.GetEffort()
	programs := make([]string, total)
	for i, vprog := range variants {
		program, err := composeProgramFlags(vprog, model, mode, effort)
		if err != nil {
			ov.Submitted = false
			return m.handleError(err)
		}
		programs[i] = program
	}

	sel := ov.GetSelectedAccount() // *overlay.AccountSelection, nil when untouched

	// Mint the fork's session id now, at the last gate before the plan is captured:
	// an id is refused by claude once used, so it must not outlive an abandoned
	// submit. Nil for an ordinary create, which is every path that did not come
	// through the checkpoint timeline.
	fork, err := m.forkSeedForSpawn()
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
	}

	// Host-capacity gate, evaluated once for the whole batch now that every title and
	// program has passed pre-flight. An explicit cap refuses an over-budget batch as a
	// unit (unchanged); the host-derived soft cap instead asks once and, on confirm,
	// spawns the staged plan via proceedOverCapMsg. An explicit "unlimited"
	// (max_sessions ≤ 0) yields Limit 0 and never fires.
	branch := ov.GetSelectedBranch()
	plan := spawnPlan{
		titles: titles, path: path, direct: direct, programs: programs,
		branch: branch, prompt: prompt, account: sel, fork: fork,
	}

	// Session-cap gate FIRST. A hard cap must hard-refuse an over-budget batch even
	// when the routed pool is fully rate-limited: the all-exhausted accept path
	// (proceedExhaustedMsg → spawnVariants) does NOT re-check the cap, so running the
	// exhausted gate first would let accepting it spawn past the user's explicit hard
	// limit — a safeguard bypass. Only once the batch is not hard-blocked do we run the
	// exhausted gate; the accept path spawning without a cap re-check is then safe.
	sc := m.sessionCap()
	count := m.capCount(sc)
	verdict := capVerdict(sc, count, total)
	if verdict == capBlock {
		ov.Submitted = false
		free := sc.Limit - count
		if free < 0 {
			free = 0
		}
		// 27 cells plus the digits of total and free, both provably two: total is at
		// most maxVariantBatch because the check above returns first, and free < total
		// because capBlock means count+total > Limit while free = Limit-count. Dropping
		// sc.Limit is what makes this bounded at all — it is the one input here with no
		// ceiling. That loses a fact, deliberately: the limit is on the configuration
		// panel's Session limit row, which (max_sessions) names; the free count is not
		// recoverable anywhere.
		ov.SetVariantError(fmt.Sprintf("need %d, %d free (max_sessions)", total, free))
		return nil
	}

	// All-members-rate-limited gate (once per batch). When both a soft cap (capConfirm)
	// and all-exhausted apply, the exhausted confirm is the one shown (accepting it then
	// spawns — an accepted v1 simplification for the soft cap only; the hard cap is
	// already ruled out above).
	if cmd, gated := m.gateAllExhausted(plan); gated {
		return cmd
	}

	if verdict == capConfirm {
		return m.confirmOverCap(plan, sc.Limit, count)
	}

	return m.spawnVariants(plan)
}

// startNewSession builds, registers, and starts a new session from already-validated
// inputs, returning the batch that boots it in the background. It is the shared core of
// the form-submit and smart-auto-dispatch paths: caller-supplied validation (title
// conflict, target validity) must already have passed. sel, when non-nil, is the
// create-form's account choice: a member pin wins over auto-routing (and availability),
// while a bare pool routes rotation to that pool. fork, when non-nil, seeds the session's
// conversation from a checkpoint of another one (#644) — an explicit parameter rather than
// a read of m.pendingFork, because the post-confirm resume path spawns from a captured
// plan with no form left to consult.
func (m *home) startNewSession(title, path string, direct bool, program, branch, prompt string, sel *overlay.AccountSelection, fromBatch bool, fork *session.ForkSeed) (tea.Cmd, error) {
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

	// Arm the fork before Start, which consumes it. The prompt goes to the fork's
	// print run rather than the queue: that run is what writes the truncated
	// conversation to disk, so it has to ask something, and the session opens on its
	// answer instead of paying for a throwaway turn and then asking again.
	if fork != nil {
		instance.SetForkSeed(fork, prompt)
	}

	// Resolve the pool this worktree routes to, then pick the account: a picker
	// member-pin wins outright; otherwise rotate to the next available member and
	// advance the per-pool cursor (so an unpinned fan-out batch spreads across the
	// pool). Empty claude_accounts leaves all fields empty (feature dormant).
	remoteURL := ""
	if !direct {
		remoteURL = git.GetRemoteURL(m.ctx, path)
	}
	poolName, members, _ := m.appConfig.ResolveClaudePool(remoteURL, path)
	if sel != nil && sel.Pool != "" {
		poolName, members = sel.Pool, m.appConfig.PoolMembers(sel.Pool)
	}

	var accName, accDir string
	var accIsDefault bool
	// chosenAcct is the account the switch settled on, kept whole for the identity
	// gate below: it needs the expect_account that accName/accDir do not carry. Nil
	// in the dormant case (no claude_accounts), which the gate treats as "proceed".
	var chosenAcct *config.ClaudeAccount
	switch {
	case sel != nil && sel.Member != nil:
		accName, accDir, accIsDefault = sel.Member.Name, sel.Member.ResolvedConfigDir(), sel.Member.IsCatchAll()
		chosenAcct = sel.Member
		// A member pin clusters under its OWN declared pool (sel.Pool), not whatever
		// routing resolved — otherwise pinning an ungrouped account in a repo that
		// routes to a named pool would mis-cluster the session under that pool. An
		// ungrouped pin (sel.Pool == "") stamps no pool, so accountKey falls back to
		// the account name — byte-for-byte dormant, matching the rotate branch.
		poolName = sel.Pool
	case len(members) == 0:
		// dormant: leave everything empty
	default:
		avail := m.appState.GetAccountAvailability()
		now := time.Now()
		// Shared with the accounts overlay's routing preview, so what the preview shows
		// and what creation does cannot drift.
		chosen, allLimited := config.SelectPoolMember(members, avail, m.appState.GetAccountRotation(poolName), now)
		// Fail closed rather than silently. Both spawn paths run gateAllExhausted
		// before reaching this, so an unpinned all-limited pool should be impossible
		// here — but "should be impossible" is exactly what this branch quietly
		// assumed while smart auto-dispatch skipped the gate and spawned on the
		// cursor's member for a whole release (#483). A third caller that forgets the
		// gate now gets a refusal it has to handle, not a session on an account the
		// user marked unusable.
		//
		// len(members) > 1 is not a nicety: SelectPoolMember reports allLimited for a
		// singleton pool whose one account is limited, and gateAllExhausted
		// deliberately exempts that case because a pool of one has nothing to rotate.
		// Without the same exemption, flagging a lone unpooled account would start
		// refusing every session it routes.
		if allLimited && len(members) > 1 {
			return nil, fmt.Errorf(
				"every account in pool %q is rate-limited; pick a member explicitly to override", poolName)
		}
		// Only advance a rotation cursor for a real (multi-member) pool: a 1-member
		// pool has nothing to rotate, and writing the cursor would add an
		// account_rotation key for an accounts-configured-but-no-pool user.
		if len(members) > 1 {
			if err := m.appState.SetAccountRotation(poolName, chosen+1); err != nil {
				log.WarningLog.Printf("failed to persist rotation cursor: %v", err)
			}
		}
		accName, accDir, accIsDefault = members[chosen].Name, members[chosen].ResolvedConfigDir(), members[chosen].IsCatchAll()
		chosenAcct = &members[chosen]
		// Stamp a cluster pool only for a REAL declared pool (the chosen member has a
		// non-empty Pool); an ungrouped account leaves the stamp empty so accountKey
		// falls back to the account name — byte-for-byte dormant for a has-accounts-
		// no-pool config. (len(members) > 1 already implies a shared non-empty Pool.)
		if members[chosen].Pool == "" {
			poolName = ""
		}
	}
	// Verify the resolved account really holds the login it claims, before anything
	// is committed: no list row, no worktree, no tmux session. A refusal here is
	// indistinguishable to the caller from any other pre-flight failure, so
	// spawnVariants keeps the create form open on the first variant and the user can
	// correct config without losing what they typed.
	//
	// After rotation, not before — a pool picks its member here, and the member that
	// will actually be injected is the only one worth checking.
	if err := m.verifyAccountIdentity(chosenAcct); err != nil {
		return nil, err
	}

	instance.SetClaudeAccount(accName, accDir, accIsDefault)
	instance.SetClaudeAccountPool(poolName)
	// The gh account routes from the origin remote (or path) independently of the
	// Claude-account override: gh access is determined by the actual repo, not by
	// which Claude login was picked. "" leaves gh on the ambient global account.
	// TokenEnv (if any) names the env vars the account's gh token is injected under
	// at launch (e.g. GITHUB_PERSONAL_ACCESS_TOKEN for the github MCP).
	ghDir, ghTokenEnv := m.appConfig.ResolveGHAccount(remoteURL, path)
	instance.SetGHConfigDir(ghDir)
	instance.SetGitHubTokenEnv(ghTokenEnv)

	// Resolve the Antigravity CLI account and config directory, if applicable.
	agyAccName, agyAccDir, _ := m.appConfig.ResolveAgyAccount(remoteURL, path)
	instance.SetAgyAccount(agyAccName, agyAccDir)

	// Creating a session reclaims an identity a killed one may still hold a record
	// for, so that record stops being offered. Creation wins deliberately: a user
	// who killed a session to free its name must not be locked out of the name, and
	// an undo that fired afterwards would rebuild onto a live tmux session. The
	// retention ref is left alone, so the refusal path can still point at it and the
	// commits stay recoverable by hand until the horizon passes.
	m.supersedeUndoFor(instance)

	// Create the list row only now, on submit. AddInstance may insert it mid-list under its
	// repo group, so select it by identity.
	finalizer := m.list.AddInstance(instance)
	m.list.SelectInstance(instance)
	if branch != "" {
		instance.SetBaseBranch(branch)
	}
	// A fork's prompt is NOT queued: the print run that materializes the truncated
	// conversation already asked it, and the session opens on that answer. Queuing it
	// as well types it into the pane a second time, which reads as the agent being
	// asked the same thing twice — and is how this was found, by pressing the key.
	if fork == nil {
		instance.QueuePrompt(prompt)
	}
	instance.SetStatus(session.Loading)
	finalizer()

	// Track the in-flight Start so app.Run can join it during shutdown/force-quit
	// reconciliation (#282). Add here on the Update goroutine so it happens-before
	// app.Run's wait; Done fires when the goroutine returns, whether or not Bubble
	// Tea delivered the resulting message.
	m.startWG.Add(1)
	startCmd := func() tea.Msg {
		defer m.startWG.Done()
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err, hadPrompt: prompt != "", fromBatch: fromBatch}
	}
	return tea.Batch(tea.RequestWindowSize, m.instanceChanged(), startCmd), nil
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

// stashDirtyCreateForm preserves a dirty create form as a restorable draft — in
// memory (m.stashedDraft) and mirrored to disk — so a non-committing exit never
// discards typed input. It backs every such exit: a deliberate Escape-to-check-
// something and declining the host-capacity confirmation alike. Everything else
// (a clean form, quick-send, smart-dispatch) leaves no stash, and the caller is
// still responsible for clearing m.textInputOverlay afterward.
func (m *home) stashDirtyCreateForm() {
	if m.textInputOverlay == nil || !m.textInputOverlay.IsCreateForm() || !m.textInputOverlay.IsDirty() {
		return
	}
	// Drop any pending "⌃R again" arm so it can't survive a Ctrl+C cancel (which
	// bypasses the overlay's own disarm) and make the next single Ctrl+R a wipe.
	m.textInputOverlay.DisarmClear()
	// The draft carries the fork's heading, so the fork travels with it (see
	// stashPendingFork). Restored together by openCreateFormSeeded.
	m.stashPendingFork()
	// The stash reuses this very overlay, whose Canceled flag may have been set by the
	// Escape that triggered it (or Submitted by the Ctrl+S that hit the cap). Clear the
	// transient submit/cancel flags so the restored draft is a clean, submittable form —
	// otherwise handlePromptState checks IsCanceled before IsSubmitted, so every later
	// Enter/Ctrl+S on the restored form is misread as a cancel and the session is never
	// created.
	m.textInputOverlay.Canceled = false
	m.textInputOverlay.Submitted = false
	m.stashedDraft = m.textInputOverlay
	// Mirror the stash to disk so it outlives a crash/quit before the reopen.
	m.persistDraft(m.textInputOverlay)
}

// cancelPromptOverlay cancels the prompt overlay.
func (m *home) cancelPromptOverlay() tea.Cmd {
	m.stashDirtyCreateForm()
	m.textInputOverlay = nil
	m.state = stateDefault
	m.resetTitleCheck()
	return tea.Sequence(
		tea.RequestWindowSize,
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// killDataWarning returns a parenthetical suffix for the kill confirmation that
// names the local work a kill puts at stake, or "" when there is none.
//
// unpushed is the count of commits that exist nowhere but this branch: kill runs
// `git branch -D` and never touches origin, so a pushed commit survives the session
// and must not be warned about — a branch sitting in an open PR loses nothing.
// Pause folds its auto-WIP commit in via noteAutoPauseCommit, so a paused-then-dirty
// session reads Dirty=false with unpushed>=1.
//
// The consequence clause used to read "deleting discards this work", and that was
// true for as long as kill was the one unrecoverable thing Atrium did. It is not
// any more: the teardown commits what is uncommitted and pins the branch under a
// retention ref, so the work comes back with the session (#391). What stays
// off-screen at the moment of asking — and so what this still has to say — is that
// the work exists and that the window is finite. The horizon is read from
// undo.TTL rather than spelled out, so the sentence cannot outlive the constant.
func killDataWarning(dirty bool, unpushed int) string {
	switch {
	case dirty && unpushed > 0:
		return fmt.Sprintf(" (has uncommitted changes and %d unpushed commit%s — %s)",
			unpushed, plural(unpushed), undoWindowClause())
	case dirty:
		return fmt.Sprintf(" (has uncommitted changes — %s)", undoWindowClause())
	case unpushed > 0:
		return fmt.Sprintf(" (has %d unpushed commit%s — %s)", unpushed, plural(unpushed), undoWindowClause())
	}
	return ""
}

// undoWindowClause names the key and the horizon a kill can be taken back within.
// Both are derived — the key from the registry, the horizon from undo.TTL — so a
// rebinding or a retention change cannot leave this sentence lying.
func undoWindowClause() string {
	return fmt.Sprintf("%s restores it for %d days", undoKeyLabel(), int(undo.TTL.Hours()/24))
}

// sessionsWithUnpushedWork counts how many of insts carry uncommitted changes or
// unpushed commits, for the batch-kill confirmation's aggregate warning. It keys off
// the same at-risk measure as killDataWarning, so a fully pushed session is not
// counted: deleting it discards nothing.
func sessionsWithUnpushedWork(insts []*session.Instance) int {
	n := 0
	for _, inst := range insts {
		if s := inst.GetDiffStats(); s != nil && (s.Dirty || s.Unpushed > 0) {
			n++
		}
	}
	return n
}

// killIOCmd is the goroutine half of a kill: the branch-safety check and the
// teardown itself (tmux session, worktree, branch). It touches no model state, so
// it is safe off the update thread — which it must be, because a single kill is
// ~6 subprocesses plus a recursive worktree delete that routinely takes seconds on
// a fat tree, and it used to run inline with no progress row at all (#380). The
// model half is applyKillDone.
//
// Shared by the kill confirmation and the post-merge cleanup offer (#384) so both
// retire a session by exactly the same path — which is also why the undo journal
// is written here rather than at either call site: a session recoverable from one
// entry point and not the other would be the worse kind of surprise. It takes the
// model only to reach that journal (the data dir and the sweep's context); it
// touches nothing else on it, and must not, because it runs off the update thread.
func killIOCmd(m *home, inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
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
				return killDoneMsg{outcome: killOutcome{inst: inst}, refused: fmt.Errorf(
					"branch for %s is checked out in the main repo; switch it away before deleting",
					inst.DisplayName())}
			}
		}
		// Record what this kill is about to destroy, and pin the branch's commits so
		// `branch -D` cannot take them with it. It runs after the refusal above (a
		// kill that does not happen needs no record) and before the teardown it
		// describes — the only ordering that matters, since Kill is what destroys
		// them. Never fatal: an unrecordable kill is still a kill, and the notice
		// simply does not advertise a recovery that is not there.
		_, undoable := m.journalKill(inst, "")
		return killDoneMsg{outcome: killOutcome{inst: inst, err: inst.Kill()}, undoable: undoable}
	}
}

// armTeardown pairs a teardown's bookkeeping with the Cmd that performs it: the
// returned hook is staged with armOnConfirm and the Cmd with confirmAction, so both
// halves are decided in one place and neither can be given to one path and forgotten
// on the other. Single kill and batch kill both go through here.
//
// The hook does two things, and both must happen on the update thread:
//
//   - marks every instance retiring, which stops the metadata poll from "recovering"
//     a session that is deliberately being torn down. The row still exists for the
//     whole async window, so without the mark the poll sees the dying pane, trips
//     recoverLostInstances, and toasts "terminal exited — parked as paused" over the
//     busy row: a notice that both lies and hides the teardown's own label.
//   - joins startWG, so quitting midway cannot strand a half-removed worktree
//     (#282/#315). Add() on the update thread happens-before app.Run's wait; one
//     Add per dispatched goroutine, released by the wrapper below.
//
// Why a hook rather than doing it here: this runs while the dialog is being BUILT,
// and a declined confirmation drops its action without running it. Arming at build
// time therefore leaks a retiring mark that never clears (the session becomes exempt
// from lost-session recovery for the rest of the run) and a startWG count that never
// reaches zero (so every later quit burns the full drainStarts timeout) — on every
// cancel, which is a routine thing to do to a kill dialog.
func (m *home) armTeardown(insts []*session.Instance, io tea.Cmd) (arm func(), action tea.Cmd) {
	arm = func() {
		if m.retiring == nil {
			m.retiring = map[*session.Instance]bool{}
		}
		for _, inst := range insts {
			m.retiring[inst] = true
		}
		m.startWG.Add(1)
	}
	action = func() tea.Msg {
		defer m.startWG.Done()
		return io()
	}
	return arm, action
}

// endTeardown clears the retiring marks for a finished teardown. Main-thread only,
// and called for EVERY outcome — including a refusal, which leaves the session alive
// and so must leave it recoverable too.
func (m *home) endTeardown(insts []*session.Instance) {
	for _, inst := range insts {
		delete(m.retiring, inst)
	}
}

// killDoneMsg carries a single kill's teardown result back to the main loop, which
// applies the model half. refused marks a kill the goroutine declined before
// touching anything, so the handler leaves the session entirely alone.
type killDoneMsg struct {
	outcome killOutcome
	refused error
	// undoable reports that the journal recorded this kill, so the notice can only
	// advertise a recovery that exists. Decided in the I/O half, next to the
	// journalKill that settled it, and read by applyKillDone.
	undoable bool
}

// applyKillDone lands a single kill on the model: preview-terminal cleanup, the
// storage delete, the row, and the per-instance bookkeeping. Main thread only.
//
// Ordering note: the split tears tmux and the worktree down before storage is
// written, so a DeleteInstance failure can no longer abort the removal — the
// session is retired either way. The row goes regardless (a row pointing at a
// deleted worktree is worse than no row) and the failure is named, so a persisted
// ghost is reported rather than left silent.
func (m *home) applyKillDone(msg killDoneMsg) tea.Cmd {
	// Release the mark first, so every exit below is covered by one statement. A
	// refusal in particular leaves the session alive: it must leave it recoverable
	// too, and it used to return before reaching the clear.
	if inst := msg.outcome.inst; inst != nil {
		m.endTeardown([]*session.Instance{inst})
	}
	if msg.refused != nil {
		return m.handleError(msg.refused)
	}
	inst := msg.outcome.inst
	if inst == nil {
		return nil
	}
	m.tabbedWindow.CleanupTerminalForInstance(inst)
	storeErr := m.storage.DeleteInstance(inst.Title, inst.Path)
	m.list.RemoveInstance(inst)
	m.forgetInstance(inst)

	switch {
	case msg.outcome.err != nil:
		return m.handleError(fmt.Errorf("killed '%s' but teardown was incomplete: %w",
			inst.DisplayName(), msg.outcome.err))
	case storeErr != nil:
		return m.handleError(fmt.Errorf("removed '%s' but its saved state could not be cleared: %w",
			inst.DisplayName(), storeErr))
	}
	return func() tea.Msg {
		return instanceChangedMsg{notice: killedNotice(inst.DisplayName(), msg.undoable)}
	}
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

	message := fmt.Sprintf("Kill session '%s'?", inst.DisplayName())
	if stats := inst.GetDiffStats(); stats != nil {
		message += killDataWarning(stats.Dirty, stats.Unpushed)
	}
	// The label names its object because this dialog need not target the SELECTED
	// session: the in-session chord and the auto-open path both kill a specific one
	// (see the doc above). pausing…/resuming… stay object-less for the opposite
	// reason — they always act on the highlighted row. Don't "fix" that asymmetry.
	arm, action := m.armTeardown([]*session.Instance{inst}, killIOCmd(m, inst))
	cmd := m.confirmAction(message, busyLabel(fmt.Sprintf("killing '%s'…", inst.DisplayName())), action)
	m.armOnConfirm(arm)
	// Kill is the one destructive confirmation, so it alone wears the danger
	// border (the default is accent); confirmAction created m.confirmationOverlay
	// synchronously above.
	m.confirmationOverlay.SetDestructive()
	// Opt-in: a second press of the kill key confirms the dialog, so Ctrl+X Ctrl+X
	// kills in one motion. Scoped to the kill dialog (other confirmations still
	// require 'y').
	if m.appConfig.GetKillDoubleTapConfirm() {
		m.confirmationOverlay.SetConfirmAltKey(keys.KillKey())
	}
	return cmd
}

// offerCleanupAfterMerge presents a follow-up confirmation to tear down a
// just-merged session, reusing the kill teardown path and its risk-only data
// warning (killDataWarning, #384 — a question with a parenthetical that appears
// only when work is at risk, not a consequence-first dialog; see the
// confirmation-voice rule above hiddenNeighborNotice). Its message announces the
// merge, so it doubles as the merge acknowledgment. Declining leaves the session
// untouched — its row keeps rendering the merged (purple) PR chip. Returns false
// when there is no session to offer cleanup for, so the caller can fall back to a
// plain notice.
func (m *home) offerCleanupAfterMerge(inst *session.Instance, number int) bool {
	if inst == nil {
		return false
	}
	message := fmt.Sprintf("PR #%d merged. Clean up session '%s'?", number, inst.DisplayName())
	if stats := inst.GetDiffStats(); stats != nil {
		message += killDataWarning(stats.Dirty, stats.Unpushed)
	}
	arm, action := m.armTeardown([]*session.Instance{inst}, killIOCmd(m, inst))
	m.confirmAction(message, busyLabel(fmt.Sprintf("killing '%s'…", inst.DisplayName())), action)
	m.armOnConfirm(arm)
	return true
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

// busyLabel is a progress-row label: the specific, named promise an operation
// makes before it starts working. Its own type so a caller cannot forget it —
// see instantAction for the one legal way to pass nothing.
type busyLabel string

// instantAction declares that a confirmed action does no I/O and finishes well
// inside a frame, so it needs no progress row and runs inline. It is the ONLY
// legal empty label, and it is greppable, which is the point: the set of
// operations claiming to be instant should be auditable at a glance rather than
// inferred from an argument someone left off.
//
// Every current user returns a message and nothing else (tea.Quit, the
// proceed-anyway confirmations whose real work has its own affordance later).
const instantAction busyLabel = ""

// confirmAction shows a confirmation modal and stores the action to execute on
// confirm. The action is run (and its result dispatched) by the stateConfirm key
// handler, not here, so its returned message — including any error — flows through
// Update instead of being discarded.
//
// label is what the user sees while the action runs. A named action runs off the
// UI thread behind that label; instantAction runs inline (see handleConfirmState).
// The label is a required parameter rather than an optional one because "no
// feedback" was previously the default an author got by not thinking about it,
// and the operations that most needed a label — kill, batch kill — are exactly
// the ones that had none (#380).
func (m *home) confirmAction(message string, label busyLabel, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm
	m.pendingConfirmAction = action
	m.pendingConfirmBusyLabel = string(label)
	// Clear any hook the previous dialog staged. A caller that needs one sets it
	// AFTER this returns (armOnConfirm), so ordering cannot silently drop it.
	m.pendingConfirmArm = nil

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	m.confirmationOverlay.SetWidth(confirmWidth(m.windowWidth))

	return nil
}

// armOnConfirm stages bookkeeping to apply on the update thread if — and only if —
// the pending confirmation is accepted. Call it after confirmAction, which resets
// the slot.
//
// The distinction it exists for: a confirmation's action is stored, not run, and a
// declined dialog throws it away without ever invoking it. So anything paired with
// that action (a WaitGroup join, a "this is going away" mark) must be applied here,
// beside the dispatch, rather than where the dialog is built.
func (m *home) armOnConfirm(arm func()) {
	m.pendingConfirmArm = arm
}

// resolveSpawnPool returns the pool a plan rotates within: the picker's chosen
// pool if set, else the route-resolved pool.
func (m *home) resolveSpawnPool(plan spawnPlan) (string, []config.ClaudeAccount) {
	if plan.account != nil && plan.account.Pool != "" {
		return plan.account.Pool, m.appConfig.PoolMembers(plan.account.Pool)
	}
	remoteURL := ""
	if !plan.direct {
		remoteURL = git.GetRemoteURL(m.ctx, plan.path)
	}
	poolName, members, _ := m.appConfig.ResolveClaudePool(remoteURL, plan.path)
	return poolName, members
}

// gateAllExhausted returns (cmd, true) and stages a confirm when an unpinned
// multi-member pool has no currently-available member; (nil, false) to proceed.
// Evaluated once per batch, mirroring the soft-cap gate.
func (m *home) gateAllExhausted(plan spawnPlan) (tea.Cmd, bool) {
	if plan.account != nil && plan.account.Member != nil {
		return nil, false // a deliberate member pin bypasses availability
	}
	poolName, members := m.resolveSpawnPool(plan)
	if len(members) < 2 {
		return nil, false // singleton/empty pool: nothing to skip
	}
	avail := m.appState.GetAccountAvailability()
	// The len(members) < 2 guard above is deliberate and NOT redundant with allLimited:
	// SelectPoolMember reports allLimited for a singleton pool whose one account is
	// limited, but a pool of one has nothing to rotate, so it must not raise the confirm.
	if _, allLimited := config.SelectPoolMember(members, avail, m.appState.GetAccountRotation(poolName), time.Now()); !allLimited {
		return nil, false
	}
	return m.confirmAllExhausted(plan, poolName, members), true
}

// confirmAllExhausted stages a confirm for a fully-rate-limited pool. On accept
// it pins the soonest-to-reset member for the whole batch and spawns; on decline
// nothing is created (the dismissed form is stashed as a restorable draft, the
// same rollback contract as confirmOverCap).
func (m *home) confirmAllExhausted(plan spawnPlan, pool string, members []config.ClaudeAccount) tea.Cmd {
	avail := m.appState.GetAccountAvailability()
	soonest := config.SoonestResetMember(members, avail)
	pinned := members[soonest]
	plan.account = &overlay.AccountSelection{Pool: pool, Member: &pinned}
	m.pendingExhausted = &plan
	m.stashDirtyCreateForm()
	m.textInputOverlay = nil
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
	return m.confirmAction(
		allExhaustedMessage(pool, members, avail, pinned.Name),
		// The spawn this unblocks happens later, in the proceedExhaustedMsg handler,
		// and announces itself with its own per-row Loading spinner. A label here
		// would name an operation that is not running yet.
		instantAction,
		func() tea.Msg { return proceedExhaustedMsg{} })
}

// allExhaustedMessage renders the confirm body: the pool, each member's reset
// time (or "indefinitely"), and which member the batch will use.
func allExhaustedMessage(pool string, members []config.ClaudeAccount, avail map[string]config.AccountAvailability, pinned string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠ all %s accounts are rate-limited\n", pool)
	for _, mem := range members {
		when := "indefinitely"
		if u := avail[mem.Name].Until; u != "" {
			if t, err := time.Parse(time.RFC3339, u); err == nil {
				when = "resets " + t.Local().Format("15:04")
			}
		}
		fmt.Fprintf(&b, "  %s %s\n", mem.Name, when)
	}
	fmt.Fprintf(&b, "create anyway on %s?", pinned)
	return b.String()
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
	m.menu.SetBusy(ui.BusyAction, label)
	m.recomputeLayout() // the progress row now claims a line; shrink the panes to fit
	return func() tea.Msg {
		var result tea.Msg
		if cmd != nil {
			result = cmd()
		}
		return asyncActionDoneMsg{result: result}
	}
}

// beginBackgroundAction names an operation on the progress row WITHOUT gating
// keys. It is the counterpart to beginAsyncAction, whose actionInFlight freeze
// exists because its operations mutate the list or storage and must not interleave.
//
// Use it for work the user can safely act around: generating a name, opening a PR
// in the browser. Those take long enough to need naming — an LLM call runs for
// seconds — but freezing the whole keyboard for a read-only operation would be a
// heavier promise than the work deserves. A modal action arriving mid-flight wins
// the row and this one returns to it when that finishes.
func (m *home) beginBackgroundAction(label string, cmd tea.Cmd) tea.Cmd {
	m.menu.SetBusy(ui.BusyBackground, label)
	m.recomputeLayout()
	return func() tea.Msg {
		var result tea.Msg
		if cmd != nil {
			result = cmd()
		}
		return backgroundActionDoneMsg{result: result}
	}
}

// backgroundActionDoneMsg wraps a background action's result so Update can clear
// its progress line before the inner message is handled. Distinct from
// asyncActionDoneMsg because it must NOT touch actionInFlight — a modal action may
// be running at the same time, and clearing its gate would let mutating keys
// through mid-teardown.
type backgroundActionDoneMsg struct{ result tea.Msg }
