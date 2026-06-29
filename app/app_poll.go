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
// PaneDead for a dead session, which ApplyPaneState ignores.
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

// promptSendErrorMsg reports that a queued initial prompt failed to deliver after the
// bounded retries, so the failure surfaces in the UI instead of only reaching the log.
// instance identifies which session's prompt was lost.
type promptSendErrorMsg struct {
	instance *session.Instance
	err      error
}

// promptDeliveredMsg reports that a queued initial prompt was confirmed delivered (typed
// into the composer and submitted). The main loop clears the queued prompt on receipt, so
// it stops being a poll target and is never re-sent.
type promptDeliveredMsg struct {
	instance *session.Instance
}

// promptDeferredMsg reports that a delivery attempt could not yet confirm (the pane was not
// awaiting input, or the text had not landed/submitted) — a soft, expected outcome during
// boot. The main loop only clears the in-flight guard so the next tick retries; the prompt
// stays queued. SendPrompt is idempotent, so the retry re-submits rather than re-types.
type promptDeferredMsg struct {
	instance *session.Instance
}

// promptSendAttempts bounds how many times a queued initial prompt's delivery is retried
// before the failure is surfaced. The readiness gate already confirmed the pane was live
// and idle, so a failure is usually a dead pane that retrying cannot revive; the extra
// attempts exist only to ride out a transient tmux hiccup (e.g. a send-keys that times
// out during a window resize) where the pane is still alive.
const promptSendAttempts = 3

// promptSendRetryDelay spaces the retry attempts so momentary tmux contention can clear.
// A var, not a const, so tests can zero it out and stay fast.
var promptSendRetryDelay = 250 * time.Millisecond

// sendWithRetry calls send up to promptSendAttempts times, spacing attempts by
// promptSendRetryDelay, to ride out a transient *hard* tmux failure. It returns nil on the
// first success and stops immediately on a soft outcome (session.IsSoftPromptError) —
// "pane not ready / not yet confirmed", which must defer to the next tick rather than burn
// the retry budget — returning that soft error for the caller to route. Only a hard error
// is retried; the last error is returned once every attempt has failed.
//
// SendPrompt is idempotent across the soft-failure paths (it re-submits an already-staged
// prompt instead of re-typing it), so a retry after a partial send does not double the
// prompt — bar the one narrow window noted on SendPrompt where a submit succeeds but its
// confirmation times out before the box repaints.
func sendWithRetry(send func() error) error {
	var err error
	for attempt := range promptSendAttempts {
		err = send()
		if err == nil || session.IsSoftPromptError(err) {
			return err
		}
		if attempt < promptSendAttempts-1 {
			time.Sleep(promptSendRetryDelay) // ride out a transient tmux hiccup
		}
	}
	return err
}

// sendPromptCmd delivers a queued initial prompt to an instance off the UI thread, so the
// verify pauses inside SendPrompt do not block rendering. It returns:
//   - promptDeliveredMsg on confirmed delivery (the main loop then clears the prompt);
//   - promptDeferredMsg on a soft outcome (pane not ready / unconfirmed) so the next tick
//     retries with the prompt still queued;
//   - promptSendErrorMsg on a hard failure after the bounded retries, so the loss surfaces
//     in the UI rather than being swallowed.
func sendPromptCmd(instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		err := sendWithRetry(func() error { return instance.SendPrompt(prompt) })
		switch {
		case err == nil:
			log.InfoLog.Printf("delivered queued prompt to %q", instance.Title)
			return promptDeliveredMsg{instance: instance}
		case session.IsSoftPromptError(err):
			return promptDeferredMsg{instance: instance}
		default:
			log.ErrorLog.Printf("failed to send queued prompt to %q after %d attempts: %v",
				instance.Title, promptSendAttempts, err)
			return promptSendErrorMsg{instance: instance, err: err}
		}
	}
}

// deliverReadyPrompts dispatches a send for each ready instance with a queued prompt and
// returns the commands that perform them. The prompt is NOT cleared here — it stays queued
// until delivery is confirmed (promptDeliveredMsg), so a failed or unconfirmed send is
// retried by a later tick rather than lost. An in-flight guard ensures only one send is
// outstanding per instance, so overlapping ticks cannot dispatch the same prompt twice.
func deliverReadyPrompts(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range results {
		if r.readyForPrompt && r.instance.Prompt != "" && !r.instance.PromptSending() {
			r.instance.MarkPromptSending()
			cmds = append(cmds, sendPromptCmd(r.instance, r.instance.Prompt))
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
// awaitingInput is Instance.AwaitingInput(): the agent has rendered, no startup gate
// (claude's trust-folder / new-MCP-server screen, the non-claude docs-url screen) and no
// blocking prompt is up, AND its live input box is on screen. This is a hard precondition
// the timeout never bypasses — keystrokes sent while anything but the box is up are consumed
// by that screen, not the agent's input box, so the prompt would be lost. Requiring the
// box's *presence* (not merely the absence of a known gate) closes the race where a startup
// screen that has not painted yet — no box on screen — is briefly mistaken for readiness.
// (A menu-style gate that has painted still reads as a box, so it is the gate/prompt checks
// inside AwaitingInput, not the box check, that keep its "❯ 1. …" selector out.)
//
// Normally we also wait for the pane to leave PaneWorking to avoid the post-trust
// "loading" transition window. But a chatty agent that writes continuously on boot can
// stay PaneWorking indefinitely and stall the first message forever; once the prompt has
// been queued longer than promptDeliveryTimeout we drop only that busy check. A zero
// queuedAt disables the timeout (the prompt was queued without a timestamp), falling back
// to the strict idle-pane requirement.
func promptDeliveryReady(state tmux.PaneState, awaitingInput bool, queuedAt, now time.Time) bool {
	if !awaitingInput {
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
				// (capture, diff) and flood the log/error box. Poll reports a dead
				// session as PaneDead from its own (single) has-session check, so derive
				// sessionLost from that rather than forking a second has-session here.
				// The main thread recovers it to Paused, debounced by recoverLostInstances
				// (which also re-guards Paused, so a raced-paused instance is ignored).
				r.state = instance.Poll()
				if r.state == tmux.PaneDead {
					r.sessionLost = true
					return
				}
				// Only probe readiness while a prompt is actually queued (a brief
				// window after a new session), so the extra pane capture is rare.
				if instance.Prompt != "" {
					r.readyForPrompt = promptDeliveryReady(
						r.state, instance.AwaitingInput(),
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
