package app

// Session lifecycle actions: create, kill, resume, rename, auto-name.

import (
	"context"
	"errors"
	"fmt"
	"os"
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
// session to value, then clears the cosmetic label so the list shows the corrected name. It
// rejects an empty title or one already used in the instance's repo group — comparing derived
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
	return m.storage.SaveInstances(m.list.GetInstances())
}

type instanceStartedMsg struct {
	instance *session.Instance
	err      error
}

// shouldAutoOpen reports whether a freshly started session should be attached
// automatically. It is gated by the auto_attach config flag and skipped when the
// instance carries an initial prompt (delivered asynchronously by the metadata tick,
// which is paused while attached). The Started/TmuxAlive guards avoid attaching a
// session that did not come up — and, because Started() short-circuits before
// TmuxAlive() (which dereferences tmuxSession), keep unstarted instances (e.g. in
// tests) off both the panic and the attach path.
func (m *home) shouldAutoOpen(inst *session.Instance) bool {
	return m.appConfig.GetAutoAttach() && inst.Prompt == "" && inst.Started() && inst.TmuxAlive()
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

// resumeSelected resumes a paused instance and persists the new running state
// (Resume itself only mutates in-memory status, so without this a crash before
// the next save would leave the session stamped Paused). When resume is blocked
// because the session branch is checked out in the BASE repo — the common result
// of the Checkout action — it offers to detach the base repo and retry. When the
// branch is held by a sibling worktree it surfaces a dismissible modal naming the
// holder rather than auto-touching another live worktree.
func (m *home) resumeSelected(selected *session.Instance) tea.Cmd {
	err := selected.Resume()
	if err == nil {
		if serr := m.storage.SaveInstances(m.list.GetInstances()); serr != nil {
			log.WarningLog.Printf("failed to persist resumed instance %s: %v", selected.Title, serr)
		}
		return tea.WindowSize()
	}

	// Only a branch-busy failure is recoverable; surface anything else as-is.
	var busy *git.BranchCheckedOutError
	if !errors.As(err, &busy) {
		return m.handleError(err)
	}

	wt, gerr := selected.GetGitWorktree()
	if gerr != nil {
		return m.handleError(err)
	}
	heldByBase, herr := wt.IsBranchHeldByBaseRepo()
	if herr != nil || !heldByBase {
		// Held by a sibling worktree (or undeterminable): report where it lives in
		// a dismissible modal; never auto-detach another live worktree.
		return m.showInfo(err.Error())
	}

	message := fmt.Sprintf("Branch '%s' is checked out in the main repo. Detach it and resume?", wt.GetBranchName())
	action := func() tea.Msg {
		if derr := wt.DetachBranchInBaseRepo(); derr != nil {
			// e.g. the dirty-repo refusal — show it in a modal the user can read.
			return infoMsg(derr.Error())
		}
		if rerr := selected.Resume(); rerr != nil {
			return rerr
		}
		if serr := m.storage.SaveInstances(m.list.GetInstances()); serr != nil {
			log.WarningLog.Printf("failed to persist resumed instance %s: %v", selected.Title, serr)
		}
		return instanceChangedMsg{}
	}
	return m.confirmAction(message, action)
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

// openCreateForm opens the unified new-session form — the single creation flow
// behind both `n` (focusTitle, for "type a name and go") and `N` (project picker
// first). The session itself is not created (and no list row appears) until the
// form is submitted. The contextual target is derived up front and, when it is a
// git repo, a background fetch kicked off so branches are current by the time the
// user reaches the branch field.
func (m *home) openCreateForm(focusTitle bool) tea.Cmd {
	if limit := m.appConfig.GetMaxSessions(); limit > 0 && m.list.NumInstances() >= limit {
		return m.handleError(
			fmt.Errorf("you can't create more than %d sessions (max_sessions in config.json)", limit))
	}

	m.newSessionPath = m.defaultNewSessionPath()
	target := m.newSessionPath
	// Scope the duplicate-title check to the target's repo group from the first
	// keystroke (one sync git call, in line with the open-time plumbing below);
	// the async validity check re-points it as the picker moves.
	m.newSessionGroup = git.RepoGroupKey(m.ctx, target)
	m.resetTitleCheck()

	m.state = statePrompt
	ov, isGit := m.newSessionFormOverlay()
	m.textInputOverlay = ov
	if focusTitle {
		m.textInputOverlay.FocusTitle()
	}
	// Open the account picker on the auto-routed account for the contextual target,
	// so the preselected choice matches what creating without touching it would do.
	// A non-git target has no remote and routes by path; no-op when the form has no
	// account picker (≤1 account configured).
	remoteURL := ""
	if isGit {
		remoteURL = git.GetRemoteURL(m.ctx, target)
	}
	if name, _, _ := m.appConfig.ResolveClaudeAccount(remoteURL, target); name != "" {
		m.textInputOverlay.PreselectAccount(name)
	}

	// Branch plumbing only applies to a git target: seed the fetched-once set and kick
	// the background fetch plus the initial (undebounced) branch search. A non-git
	// target's branch section is inert, so there is nothing to fetch or list — and a
	// later path change onto a git repo triggers its own verdict-driven fetch (every
	// other candidate is fetched when, and if, it is confirmed as git while selected).
	m.fetchedPaths = map[string]bool{}
	cmds := []tea.Cmd{tea.WindowSize()}
	if isGit {
		m.fetchedPaths[target] = true
		cmds = append(cmds,
			m.runBranchFetch(target),
			m.runBranchSearch("", m.textInputOverlay.BranchFilterVersion()))
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
	// Compose the model override into the persisted program string, so launch,
	// pause/resume, and the daemon all see it with no extra plumbing. The Resolve
	// check is a belt-and-braces guard behind the form's own gating (the field is
	// inert for non-claude programs), and the validation is a backstop behind its
	// keystroke filtering.
	if model := ov.GetModel(); model != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidModelName(model) {
			ov.Submitted = false
			return m.handleError(fmt.Errorf("invalid model name %q (letters, digits, . _ : / - only)", model))
		}
		program = agent.WithModelFlag(program, model)
	}
	// Compose the permission-mode override the same way (behavioral details —
	// resume semantics, autoyes safety — live on ModeField's doc comment). The
	// chips are a closed set today, so the validation backstop exists for drift:
	// claude rejects an unknown mode at argv parse time, and a pre-launch error
	// beats a dead session if the chip list and the enum ever diverge.
	if mode := ov.GetPermissionMode(); mode != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidPermissionMode(mode) {
			ov.Submitted = false
			return m.handleError(fmt.Errorf("invalid permission mode %q", mode))
		}
		program = agent.WithPermissionModeFlag(program, mode)
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    path,
		Program: program,
		Direct:  direct,
	})
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
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
	if acct, ok := ov.GetSelectedAccount(); ok && acct.Name != "" {
		// An explicit picker choice wins over auto-routing. Picking the catch-all
		// (no-rule) account stays dim; a routed account shows accented — the same
		// rule the resolver applies.
		accName, accDir, accIsDefault = acct.Name, acct.ResolvedConfigDir(), acct.IsCatchAll()
	}
	instance.SetClaudeAccount(accName, accDir, accIsDefault)

	// Create the list row only now, on submit. AddInstance may insert it mid-list under its
	// repo group, so select it by identity.
	finalizer := m.list.AddInstance(instance)
	m.list.SelectInstance(instance)
	if branch := ov.GetSelectedBranch(); branch != "" {
		instance.SetBaseBranch(branch)
	}
	instance.Prompt = prompt
	instance.PromptQueuedAt = time.Now()
	instance.SetStatus(session.Loading)
	finalizer()

	m.textInputOverlay = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()

	startCmd := func() tea.Msg {
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err}
	}
	return tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd)
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
// picker: the current target first, then existing sessions' repos, then recently-used
// project directories, then cwd.
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

// cancelPromptOverlay cancels the prompt overlay.
func (m *home) cancelPromptOverlay() tea.Cmd {
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

		// Then kill the instance
		m.list.KillInstance(inst)
		return instanceChangedMsg{}
	}

	message := fmt.Sprintf("Kill session '%s'?", inst.DisplayName())
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
