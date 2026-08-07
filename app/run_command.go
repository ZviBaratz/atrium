package app

// run_command.go — the `d` key: start or stop the selected session's run_command
// (#389), the repo's dev server or watcher, hosted in a sibling tmux session.
//
// One key for both directions. The two are never both available — a session either has
// a server up or it does not — and the row already says which, so a second binding would
// buy nothing and cost a key.

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/session"
)

// runCommandDoneMsg carries a start/stop back to the update loop. started says which
// direction was attempted, so the notice can name what happened rather than what was
// asked for.
type runCommandDoneMsg struct {
	instance *session.Instance
	started  bool
	err      error
}

// toggleRunCommand starts the selected session's run command, or stops it when one is
// already running.
//
// It runs off the UI thread behind the modal busy gate, not the background one: starting
// forks git for the origin remote, reads the config, and waits on a `tmux new-session`
// and its existence poll. The gate is also what stops a double-press from minting two
// servers on one port before the first has come up.
func (m *home) toggleRunCommand() (tea.Model, tea.Cmd) {
	selected, cmd, ok := m.selectedActionable()
	if !ok {
		return m, cmd
	}
	if selected.RunLive() {
		return m, m.beginAsyncAction("stopping dev command…", func() tea.Msg {
			return runCommandDoneMsg{instance: selected, err: selected.StopRunCommand()}
		})
	}
	// Refused here rather than inside StartRunCommand's own paused check so the message
	// names the key that fixes it, the way the pause/resume refusals do.
	if selected.Paused() {
		return m, m.handleInfoNotice(pausedResumeNotice("before starting its dev command"))
	}
	// A direct session's directory is the user's own checkout, not an isolated worktree.
	// Refused for the same reason the setup script is, and stated as a notice rather than
	// an error because there is nothing here to fix.
	if selected.IsDirect() {
		return m, m.handleInfoNotice("direct sessions run in your own checkout — Atrium will not start a dev command there")
	}
	// The same predicate the palette dims this action on, so the two can never
	// disagree about whether pressing it would do anything (TestPaletteGatesAgreeWith
	// Dispatch pins that). It is deliberately "known to have none", not "not known to
	// have one": a session the poll has not looked at yet still resolves its own config
	// on the way through StartRunCommand.
	if selected.RunCommandUnavailable() {
		return m, m.handleInfoNotice("no run_command is configured for this repository — add one to a repo_scripts entry in config.json")
	}
	return m, m.beginAsyncAction("starting dev command…", func() tea.Msg {
		return runCommandDoneMsg{instance: selected, started: true, err: selected.StartRunCommand()}
	})
}

// applyRunCommandDone lands a start/stop on the model: the persisted wanted flag, and
// the notice. Main thread only — persistInstances reads m.list.
//
// A failure is reported through handleError rather than a notice, because the two things
// that fail here both have a reason worth reading in full: an unconfigured repo (which
// names the config section to add) and a command that died on launch (which quotes the
// command).
func (m *home) applyRunCommandDone(msg runCommandDoneMsg) tea.Cmd {
	if msg.instance == nil {
		return nil
	}
	if msg.err != nil {
		return m.handleError(msg.err)
	}
	// The started/stopped flag is persisted state (InstanceData.RunStarted), so a
	// restart knows whether to look for the server. Best-effort in the same sense the
	// other action handlers treat it: the tmux session is already up or already gone,
	// and a state.json that lags says so at the next save.
	if err := m.persistInstances(); err != nil {
		return m.handleError(err)
	}
	notice := fmt.Sprintf("dev command stopped for %s", msg.instance.DisplayName())
	if msg.started {
		notice = fmt.Sprintf("dev command running for %s", msg.instance.DisplayName())
		if port := msg.instance.PortText(); port != "" {
			notice += " on :" + port
		}
	}
	return func() tea.Msg { return instanceChangedMsg{notice: notice} }
}
