package app

import (
	"fmt"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// capOutcome is what a creation attempt should do under the effective session cap.
type capOutcome int

const (
	// capAllow: the creation fits (or the cap is unlimited) — proceed.
	capAllow capOutcome = iota
	// capConfirm: the creation exceeds the host-derived soft cap — ask once, then
	// allow on confirmation.
	capConfirm
	// capBlock: the creation exceeds an explicit hard cap — refuse.
	capBlock
)

// capVerdict decides what a creation that would bring the session count to
// count+adding should do under sc. adding is the batch size (≥1); count is the
// current population (live sessions for a soft cap, all sessions for a hard cap).
// An unlimited cap (Limit ≤ 0) always allows; within Limit always allows; over a
// soft cap confirms; over a hard cap blocks.
func capVerdict(sc config.SessionCap, count, adding int) capOutcome {
	if sc.Limit <= 0 || count+adding <= sc.Limit {
		return capAllow
	}
	if sc.Soft {
		return capConfirm
	}
	return capBlock
}

// sessionCap resolves the effective cap for the enforcement sites. An unset
// max_sessions yields the host-derived soft cap (Limit from m.hostCap, injectable
// in tests); an explicit value delegates to config (a positive hard cap, or an
// explicit "unlimited" for ≤ 0).
func (m *home) sessionCap() config.SessionCap {
	if m.appConfig == nil || m.appConfig.MaxSessions == nil {
		return config.SessionCap{Limit: m.hostCap, Soft: true}
	}
	return m.appConfig.SessionCap()
}

// capCount returns the population to compare against sc: live (non-Paused)
// sessions for the host-derived soft cap — a paused session imposes no load — and
// all sessions for an explicit hard cap, whose contract is a total-session limit.
func (m *home) capCount(sc config.SessionCap) int {
	if sc.Soft {
		return m.list.NumActiveInstances()
	}
	return m.list.NumInstances()
}

// overCapMessage is the host-capacity confirmation text: it names the derived cap
// and the live count so the tradeoff — more sessions queue rather than
// parallelize — is explicit at the moment the user crosses it.
func overCapMessage(limit, active, adding int) string {
	if adding > 1 {
		return fmt.Sprintf(
			"Host capacity is %d and %d sessions are already running.\n"+
				"Spawning %d more will queue, not parallelize, and drive up load.\n"+
				"Create them anyway? (set max_sessions in config.json to change this)",
			limit, active, adding)
	}
	return fmt.Sprintf(
		"Host capacity is %d and %d sessions are already running.\n"+
			"Another will queue, not parallelize, and drive up load.\n"+
			"Create it anyway? (set max_sessions in config.json to change this)",
		limit, active)
}

// spawnPlan is a fully-validated, ready-to-spawn creation: title conflicts,
// program flags, cap, and target have all passed. It is captured before a
// host-capacity confirmation so the post-confirm path spawns exactly what the
// user submitted, with no overlay to re-read.
type spawnPlan struct {
	titles   []string
	path     string
	direct   bool
	programs []string
	branch   string
	prompt   string
	account  *config.ClaudeAccount
}

// proceedOverCapMsg is emitted when the user confirms the host-capacity prompt; its
// Update handler spawns the staged pendingOverCap plan.
type proceedOverCapMsg struct{}

// confirmOverCap stages plan behind a host-capacity confirmation, dismisses the
// create form, and returns the confirm command. The dismissed form is stashed as a
// restorable draft first, so declining is non-destructive (the accept path clears it
// via closeCreateForm on commit). On acceptance the staged action emits
// proceedOverCapMsg (handled in Update → spawnVariants); on decline nothing is
// spawned and the stale plan is inert (overwritten by the next stage).
func (m *home) confirmOverCap(plan spawnPlan, limit, active int) tea.Cmd {
	m.pendingOverCap = &plan
	m.stashDirtyCreateForm()
	m.textInputOverlay = nil
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
	return m.confirmAction(
		overCapMessage(limit, active, len(plan.programs)),
		func() tea.Msg { return proceedOverCapMsg{} })
}

// spawnVariants boots every variant in plan, which has passed all pre-flight
// validation (titles, programs, cap). It is shared by the direct form submit
// (allow) and the post-confirm resume; the resume path runs with the form already
// closed, so every form touch is nil-guarded. On a first-variant failure the batch
// has spawned nothing, so the form (if still open) stays open to retry; a
// later-variant failure has committed live sessions, so the form closes and the
// error surfaces alongside them.
func (m *home) spawnVariants(plan spawnPlan) tea.Cmd {
	total := len(plan.programs)
	cmds := make([]tea.Cmd, 0, total)
	for i := range plan.programs {
		created, err := m.startNewSession(
			plan.titles[i], plan.path, plan.direct, plan.programs[i],
			plan.branch, plan.prompt, plan.account, total > 1)
		if err != nil {
			if i == 0 {
				if m.textInputOverlay != nil {
					m.textInputOverlay.Submitted = false // keep the form open to correct + retry
				}
				return m.handleError(err)
			}
			// Some variants are already live; a resubmit would double-spawn them.
			m.recordPrompt(plan.prompt)
			m.closeCreateForm()
			return tea.Batch(append(cmds, m.handleError(err))...)
		}
		cmds = append(cmds, created)
	}
	m.recordPrompt(plan.prompt)
	m.closeCreateForm()
	return tea.Batch(cmds...)
}

// closeCreateForm tears down the create form after a committed submit: it drops the
// overlay and any stashed/persisted draft, and returns the UI to the default state.
func (m *home) closeCreateForm() {
	m.textInputOverlay = nil
	m.stashedDraft = nil
	m.clearPersistedDraft()
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
}
