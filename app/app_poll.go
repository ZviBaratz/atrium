package app

// Per-tick metadata poll loop, pane-state application, and prompt delivery.

import (
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/session/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}

	// Refresh the newly-selected session's status immediately rather than waiting for the
	// next 500ms metadata tick. instanceChanged also fires on every 100ms preview tick, so
	// gate on an actual selection change (a detach resets the tracker to nil to force a
	// refresh) to avoid polling 10×/s.
	if selected != m.lastStatusPollSelection {
		m.lastStatusPollSelection = selected
		m.selectedSince = time.Now()
		return pollSelectedCmd(selected, false)
	}
	return nil
}

// readDwell is how long a row must stay selected — and its unread state visible —
// before the selection counts as a read. Long enough that cursor travel and a
// just-landed result don't self-clear; short enough that glancing at the preview does.
const readDwell = 1500 * time.Millisecond

// markSeenAfterDwell clears the selected instance's unread state once the user has
// demonstrably seen it: the row has been selected for readDwell (the preview pane
// shows its live content) AND the unread flag itself is at least readDwell old (a
// reply landing on an already-selected row stays bright long enough to register).
// Gated on stateDefault because the 100ms preview tick fires in every UI state,
// including overlays that occlude the preview.
func (m *home) markSeenAfterDwell(now time.Time) {
	if m.state != stateDefault {
		return
	}
	sel := m.list.GetSelectedInstance()
	if sel == nil || !sel.Unread() {
		return
	}
	// Zero selectedSince means instanceChanged hasn't stamped a selection yet
	// (the first tick runs this before it): no dwell has been observed, and the
	// zero value must not read as "selected ~forever" — that would wipe a
	// restored unread bit (whose unreadAt is also zero) ~100ms after launch.
	if m.selectedSince.IsZero() {
		return
	}
	if now.Sub(m.selectedSince) < readDwell || now.Sub(sel.UnreadAt()) < readDwell {
		return
	}
	sel.MarkSeen()
}

// previewTickMsg implements tea.Msg and triggers a preview update
type previewTickMsg struct{}

type instanceChangedMsg struct{}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance       *session.Instance
	state          tmux.PaneState
	readyForPrompt bool
	// sessionLost is set when a started, non-paused instance's tmux pane no longer
	// exists. The main thread recovers it to Paused (see recoverLostInstances).
	sessionLost bool
	diffStats   *git.DiffStats
	prStatus    *git.PRStatus
	// model / modelStamp carry a transcript model extraction; modelOK marks a
	// result worth applying (ComputeModel returns ok=false for non-claude,
	// unavailable, or unchanged transcripts).
	model      string
	modelStamp transcript.Stamp
	modelOK    bool
	// mode carries the live permission mode detected from the footer; modeOK marks
	// a result worth applying (ComputeMode returns ok=false when unchanged or none).
	mode   string
	modeOK bool
}

// applyPaneState maps a polled pane state onto an instance's status. Prompt handling
// depends on AutoYes: with it on, auto-answer (TapEnter is a no-op otherwise); with it
// off the session is blocked on the user, so surface NeedsInput rather than a spinner.
// PanePromptManual surfaces NeedsInput even under AutoYes — its auto-answer is
// destructive (claude's plan approval: Enter accepts the plan AND enables auto-accept).
// PaneUnknown (an unreadable pane) leaves the status untouched.
func applyPaneState(inst *session.Instance, state tmux.PaneState) {
	switch state {
	case tmux.PaneWorking:
		inst.SetStatus(session.Running)
	case tmux.PanePrompt:
		if inst.AutoYes {
			inst.TapEnter()
		} else {
			inst.SetStatus(session.NeedsInput)
		}
	case tmux.PanePromptManual:
		inst.SetStatus(session.NeedsInput)
	case tmux.PaneIdle:
		inst.SetStatus(session.Ready)
	case tmux.PaneUnknown:
	}
}

// instancePolledMsg carries the result of an off-cadence poll of a single instance,
// triggered when the selection changes or a session is detached. It refreshes that one
// instance's status immediately instead of waiting up to a full 500ms metadata tick —
// which is why an idle session no longer lingers as "running" right after you switch to
// it or step out of it.
type instancePolledMsg struct {
	instance *session.Instance
	state    tmux.PaneState
}

// pollSelectedCmd polls a single instance off the UI thread for an immediate status
// refresh. Returns nil for a session that can't be polled; Poll itself also yields
// PaneUnknown for a dead session, which applyPaneState ignores.
//
// fresh selects PollNow over Poll: use it after a detach, where the tick stream was stalled
// while attached so the hysteresis state is stale and a face-value snapshot is correct. A
// live selection change uses the hysteresis-respecting Poll (the tick loop kept the monitor
// current).
func pollSelectedCmd(inst *session.Instance, fresh bool) tea.Cmd {
	if inst == nil || !inst.Started() || inst.Paused() {
		return nil
	}
	return func() tea.Msg {
		if fresh {
			return instancePolledMsg{instance: inst, state: inst.PollNow()}
		}
		return instancePolledMsg{instance: inst, state: inst.Poll()}
	}
}

// sendPromptCmd submits a queued initial prompt to an instance off the UI thread,
// so the SendKeys→Enter pause inside SendPrompt does not block rendering.
func sendPromptCmd(instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		if err := instance.SendPrompt(prompt); err != nil {
			log.ErrorLog.Printf("failed to send queued prompt: %v", err)
		}
		return nil
	}
}

// deliverReadyPrompts submits each ready instance's queued prompt and returns the
// commands that perform the sends. The prompt is cleared synchronously here so it
// is dispatched at most once, even if a later tick also reports the instance ready.
func deliverReadyPrompts(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range results {
		if r.readyForPrompt && r.instance.Prompt != "" {
			prompt := r.instance.Prompt
			r.instance.Prompt = ""
			r.instance.PromptQueuedAt = time.Time{}
			cmds = append(cmds, sendPromptCmd(r.instance, prompt))
		}
	}
	return cmds
}

// promptDeliveryTimeout bounds how long a queued startup prompt waits for the pane
// to fall idle before it is delivered anyway. It is comfortably longer than a typical
// agent boot (including slow MCP server init) yet short enough that a genuinely stalled
// boot does not feel hung. The clock starts when the prompt is queued (session creation),
// so it also covers worktree setup, not just the agent's own startup.
const promptDeliveryTimeout = 60 * time.Second

// promptDeliveryReady decides whether a queued startup prompt may be delivered now.
//
// gateReady is Instance.IsReadyForPrompt(): the agent has rendered and is past any
// one-time startup gate (claude's trust-folder / new-MCP-server screen, or the
// non-claude docs-url screen). This is a hard precondition the timeout never bypasses —
// keystrokes sent while a gate is up are consumed by the gate dialog, not the agent's
// input box, so the prompt would be lost.
//
// Normally we also wait for the pane to leave PaneWorking to avoid the post-trust
// "loading" transition window. But a chatty agent that writes continuously on boot can
// stay PaneWorking indefinitely and stall the first message forever; once the prompt has
// been queued longer than promptDeliveryTimeout we drop only that busy check. A zero
// queuedAt disables the timeout (the prompt was queued without a timestamp), falling back
// to the strict idle-pane requirement.
func promptDeliveryReady(state tmux.PaneState, gateReady bool, queuedAt, now time.Time) bool {
	if !gateReady {
		return false
	}
	if state != tmux.PaneWorking {
		return true
	}
	return !queuedAt.IsZero() && now.Sub(queuedAt) > promptDeliveryTimeout
}

// lostSessionRecoverThreshold is how many consecutive ticks an instance must be seen
// with a dead tmux session before it is recovered to Paused. Recovery commits any WIP
// and removes the worktree, so a single transient `tmux has-session` miss (server
// blip, load spike) must not trigger it — require confirmation across ticks.
const lostSessionRecoverThreshold = 2

// recoverLostInstances moves instances whose tmux session has died (flagged
// sessionLost by the metadata tick) into Paused, so they stop being polled and can be
// brought back with Resume. It debounces using strikes (a per-instance count of
// consecutive dead observations, owned by the caller): a session is only recovered
// after lostSessionRecoverThreshold consecutive misses; any live observation resets
// the count. Returns whether any instance was recovered so the caller can persist.
// Runs on the main thread — the only place model state may be mutated.
func recoverLostInstances(results []instanceMetaResult, strikes map[*session.Instance]int) (recovered bool) {
	for _, r := range results {
		if !r.sessionLost || r.instance.Paused() {
			delete(strikes, r.instance) // alive (or already paused): clear any prior strikes
			continue
		}
		strikes[r.instance]++
		if strikes[r.instance] < lostSessionRecoverThreshold {
			continue // not yet confirmed dead; re-check next tick
		}
		delete(strikes, r.instance)
		if err := r.instance.RecoverLostSession(); err != nil {
			log.ErrorLog.Printf("failed to recover lost session %q: %v", r.instance.Title, err)
		}
		recovered = true
	}
	return recovered
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

// snapshotActiveInstances returns the currently active (started, not paused)
// instances. Called on the main thread so the filtering doesn't race with
// state mutations.
func (m *home) snapshotActiveInstances() []*session.Instance {
	var out []*session.Instance
	for _, inst := range m.list.GetInstances() {
		if inst.Started() && !inst.Paused() {
			out = append(out, inst)
		}
	}
	return out
}

// metadataFullSweepEvery is how many 500ms ticks pass between full metadata sweeps of
// every active session. On the ticks in between, only the selected session and any
// session with a queued prompt are polled. This bounds the per-tick load on the single
// shared tmux server (its capture-pane calls serialize there): a full sweep over ~10
// streaming sessions costs hundreds of ms, so doing it every ~2s instead of every 500ms
// keeps the list responsive. Non-selected status chips can lag by at most this interval,
// which is imperceptible for a background session.
const metadataFullSweepEvery = 4

// pollTargets selects which active sessions to poll this tick. A full sweep polls all of
// them; a light tick polls only the selected session and any session with a queued
// prompt (whose delivery needs a responsive readiness probe). Sessions left out keep
// their last metadata until the next full sweep.
func pollTargets(active []*session.Instance, selected *session.Instance, fullSweep bool) []*session.Instance {
	if fullSweep {
		return active
	}
	var out []*session.Instance
	for _, inst := range active {
		if inst == selected || inst.Prompt != "" {
			out = append(out, inst)
		}
	}
	return out
}

// tickUpdateMetadataCmd returns a self-chaining Cmd that sleeps 500ms, then performs
// expensive metadata I/O (tmux capture, git diff) in parallel background goroutines.
// Because it only re-schedules after completing, overlapping ticks are impossible.
// The active instances slice should be snapshotted on the main thread via
// snapshotActiveInstances() before being passed here.
//
// fullSweep polls every active session; otherwise only the selected session and any
// session with a queued prompt are polled (the rest keep their last state until the next
// full sweep) — see metadataFullSweepEvery. Sessions left out of the returned results are
// simply not updated this tick.
//
// Only the selected instance gets a full diff (with Content); the rest get a
// lightweight numstat-only summary. This keeps per-instance memory bounded
// since the diff pane only ever renders the selected one.
func tickUpdateMetadataCmd(active []*session.Instance, selected *session.Instance, fullSweep bool) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)

		if len(active) == 0 {
			return metadataUpdateDoneMsg{}
		}

		poll := pollTargets(active, selected, fullSweep)
		if len(poll) == 0 {
			return metadataUpdateDoneMsg{}
		}

		results := make([]instanceMetaResult, len(poll))
		var wg sync.WaitGroup
		for idx, inst := range poll {
			wg.Add(1)
			go func(i int, instance *session.Instance) {
				defer wg.Done()
				r := &results[i]
				r.instance = instance
				// A started session whose tmux pane has died would fail every probe
				// (capture, diff) and flood the log/error box. Detect it once here
				// (read-only) and skip polling; the main thread recovers it to Paused.
				if instance.Started() && !instance.Paused() && !instance.TmuxAlive() {
					r.sessionLost = true
					return
				}
				r.state = instance.Poll()
				// Only probe readiness while a prompt is actually queued (a brief
				// window after a new session), so the extra pane capture is rare.
				if instance.Prompt != "" {
					r.readyForPrompt = promptDeliveryReady(
						r.state, instance.IsReadyForPrompt(),
						instance.PromptQueuedAt, time.Now())
				}
				if instance == selected {
					r.diffStats = instance.ComputeDiff()
				} else {
					r.diffStats = instance.ComputeDiffNumstat()
				}
				// PR status is network-bound but TTL-cached, so most ticks return
				// instantly with no I/O; the selected session refreshes eagerly.
				r.prStatus = instance.ComputePRStatus(instance == selected)
				// Transcript model is stamp-gated: an idle claude session costs one
				// ReadDir + Stat per tick, a streaming one a ≤128KB tail parse.
				r.model, r.modelStamp, r.modelOK = instance.ComputeModel()
				// Live permission mode reads the value Poll just detected from the
				// footer — no extra capture; only applied when it changed.
				r.mode, r.modeOK = instance.ComputeMode()
			}(idx, inst)
		}
		wg.Wait()

		return metadataUpdateDoneMsg{results: results}
	}
}
