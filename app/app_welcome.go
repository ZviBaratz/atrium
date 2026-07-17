package app

import (
	"fmt"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// detectAgents is the agent-detection seam. A package var (matching the app's
// checkForUpdate/cleanupTerminalForInstance idiom) so tests inject a fake PATH.
var detectAgents = config.DetectAgentProfiles

// agentsDetectedMsg carries the result of async agent detection back to Update.
type agentsDetectedMsg struct {
	profiles []config.Profile
}

// detectAgentsCmd probes for installed agent CLIs off the main loop (detection
// shells out per agent) and delivers the result as an agentsDetectedMsg.
func (m *home) detectAgentsCmd() tea.Cmd {
	return func() tea.Msg {
		return agentsDetectedMsg{profiles: detectAgents()}
	}
}

// programCheckedMsg carries the result of the async effective-program PATH check
// (see checkProgramInstalledCmd) back to Update for the returning-user warning.
type programCheckedMsg struct {
	program   string
	installed bool
}

// checkProgramInstalledCmd resolves whether the effective program is installed,
// off the main loop. config.ProgramInstalled, for the "claude" token, shells out
// through the user's shell profile (up to a 10s timeout); running it here keeps
// that probe off the Bubble Tea update loop so the first frame is never blocked
// (mirrors detectAgentsCmd, which offloads the same per-agent probing).
func (m *home) checkProgramInstalledCmd() tea.Cmd {
	program := m.program
	return func() tea.Msg {
		return programCheckedMsg{program: program, installed: config.ProgramInstalled(program)}
	}
}

// handleWelcomeState routes a key to the welcome overlay. On confirm it merges
// the detected agents into the profiles, persists the picked default program,
// and retires the welcome; on skip it just closes (the seen-bit stays unset so
// the welcome re-shows until the user engages or creates a session).
func (m *home) handleWelcomeState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.welcomeOverlay.HandleKeyPress(msg)
	if !shouldClose {
		return m, nil
	}
	confirmed := m.welcomeOverlay.Confirmed()
	detected := m.welcomeOverlay.Detected()
	profile := m.welcomeOverlay.SelectedProfile()
	m.state = stateDefault
	m.welcomeOverlay = nil
	m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row

	if !confirmed {
		// Retire the welcome on skip, not only on confirm (#381): the modal
		// swallows keys until dismissed, so re-greeting a look-around-first user
		// every launch is a recurring toll on exactly the cautious newcomers it is
		// for. The recovery path is a one-line notice rather than the modal again,
		// and returning users are still covered by the missing-program check.
		m.markWelcomeSeen()
		return m, tea.Batch(
			tea.WindowSize(),
			m.flashNotice("Setup skipped — press , to pick a default agent, or n to start a session", ui.NoticeInfo),
		)
	}
	// Confirm: adopt the detected agents as profiles and persist the pick as the
	// profile *name* (matching seededDefaultConfig), so GetProgram keeps resolving
	// the default through the profile list — preserving a user's customized
	// same-named profile instead of overwriting it with the detected program.
	m.appConfig.MergeDetectedProfiles(detected)
	if profile.Name != "" {
		m.appConfig.DefaultProgram = profile.Name
		m.program = m.appConfig.GetProgram()
	}
	if err := config.SaveConfig(m.appConfig); err != nil {
		log.WarningLog.Printf("failed to persist welcome setup: %v", err)
	}
	m.markWelcomeSeen()
	return m, tea.WindowSize()
}

// markWelcomeSeen persists the one-time welcome's seen-bit when it is not already
// set. Best-effort: a failed persist is logged, not surfaced. Shared by the
// welcome-confirm path and the first-session-start chokepoint (handleInstanceStarted)
// so the two can't drift.
func (m *home) markWelcomeSeen() {
	seen := m.appState.GetHelpScreensSeen()
	if seen&(helpTypeWelcome{}.mask()) != 0 {
		return
	}
	if err := m.appState.SetHelpScreensSeen(seen | helpTypeWelcome{}.mask()); err != nil {
		log.WarningLog.Printf("failed to persist welcome-seen state: %v", err)
	}
}

// warnMissingProgram surfaces a one-shot, non-blocking notice that the effective
// program is not installed. Used on launches where the welcome does not show
// (returning users), driven off the async program check. It never opens a modal.
func (m *home) warnMissingProgram(program string) tea.Cmd {
	if m.pathWarned {
		return nil
	}
	m.pathWarned = true
	var text string
	if cmd := config.ProgramCommand(program); cmd != "" {
		text = fmt.Sprintf("%s not found on PATH — press , to change the default program", cmd)
	} else {
		text = "no default program set — press , to choose one"
	}
	return m.flashNotice(text, ui.NoticeError)
}
