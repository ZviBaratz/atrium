package app

// Per-tick metadata poll loop, pane-state application, and prompt delivery.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
)

func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// Render the panes from cached state. The preview's frame comes from the capture
	// chain in app_frames.go, never from a capture here: this function runs on every
	// 100ms tick AND from ~60 key handlers, so a tmux round trip in it was a latency
	// floor under every keystroke, and an unresponsive server froze the app (#380).
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
		// The new selection's frame is whatever was last captured for it, which may be
		// nothing at all — either way it is new, not stale, so restamp freshness (which
		// also drops the quiet run, so the new pane is watched at full rate until it
		// settles on its own). The capture chain notices the changed target when its
		// in-flight capture lands and re-arms without waiting out another interval (see
		// handlePaneFrame); if it had already ended, the preview tick revives it.
		m.noteFrameTargetChange()
		return pollSelectedCmd(selected, m.attachGen)
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

// contextPushFailedMsg carries the sessions whose context-bar push failed, so the
// main thread can un-arm their caches and the next tick retries. Arming is
// optimistic (see tmux.ArmContext) precisely so the common unchanged tick costs
// nothing; this message is the other half of that bargain.
type contextPushFailedMsg struct {
	instances []*session.Instance
}

// instanceChangedMsg asks Update to refresh after a confirmed action changed the
// list. notice, when set, is flashed alongside the refresh — it exists so the kill
// teardown can tell the user their session is recoverable without needing a second
// message type for what is one event.
type instanceChangedMsg struct{ notice string }

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
	// diffContentSkipped marks a diffStats computed by ComputeRepoStats: its
	// branch-level counters are fresh but its line counts and patch text are zero
	// because they were never asked for. The main thread carries the previous ones
	// forward rather than storing the zeroes (see applyMetadataResults).
	diffContentSkipped bool
	prStatus           *git.PRStatus
	// model / modelStamp carry a transcript model extraction; modelOK marks a
	// result worth applying (ComputeModel returns ok=false for non-claude,
	// unavailable, or unchanged transcripts).
	model      string
	modelStamp transcript.Stamp
	modelOK    bool
	// usage / usageStamp carry a transcript context-window extraction (#596);
	// usageOK marks a result worth applying (ComputeUsage returns ok=false for
	// non-claude, unavailable, or unchanged transcripts). Its own stamp, beside
	// modelStamp rather than sharing it: the two readers scan independently, so
	// one memo must not gate the other.
	usage      transcript.Usage
	usageStamp transcript.Stamp
	usageOK    bool
	// usageClear is the other verdict usagePolicy can reach: this session must
	// not hold a reading at all (an ambiguous transcript source, or the chip
	// switched off). It is not the same as usageOK == false, which only means
	// "nothing new" — the main thread drops the stored value for it.
	usageClear bool
	// cost / costCursor carry a transcript spend estimate (#392), with costOK and
	// costClear as the exact analogues of the pair above. Its own cursor rather
	// than a stamp, because it resumes from a byte offset per file instead of
	// re-reading a window: see transcript.CostCursor.
	//
	// Exactly one of the usage/cost pairs is ever populated on a tick — the chip
	// mode decides which read is taken — so the other arrives clear. That is why
	// they are two independent quartets and not one tagged union: the CLEAR verdict
	// has to be expressible for whichever reading is not being taken.
	cost       transcript.Cost
	costCursor transcript.CostCursor
	costOK     bool
	costClear  bool
	// asked / askedStamp carry the #571 question check — whether the turn that just
	// ended did so by asking the user something; askedOK marks a result worth applying
	// (ComputeAsked returns ok=false for non-claude, unavailable, or unchanged
	// transcripts). Only re-read on a pane that has actually settled.
	asked      bool
	askedStamp transcript.Stamp
	askedOK    bool
	// mode carries the live permission mode detected from the footer; modeOK marks
	// a result worth applying (ComputeMode returns ok=false when unchanged or none).
	mode   string
	modeOK bool
	// effort carries the reasoning-effort level claude's hooks reported; effortOK
	// marks a result worth applying (ComputeEffort returns ok=false when unchanged
	// or none reported yet).
	effort   string
	effortOK bool
	// paneFrame / paneFrameAt carry the pane capture Poll already paid for, so every
	// polled session's preview cache stays warm without a second capture-pane;
	// paneFrameOK is false for a session that has never polled successfully.
	paneFrame   string
	paneFrameAt time.Time
	paneFrameOK bool
	// runState carries what this tick observed about the session's dev command (#389).
	// It has its own *Known flags rather than an OK beside it, because it answers two
	// questions on independent schedules — see session.RunState.
	runState session.RunState
}

// instancePolledMsg carries the result of an off-cadence status poll of a single instance,
// triggered when the selection changes. It refreshes that one instance's status immediately
// instead of waiting up to a full 500ms metadata tick — which is why an idle session no
// longer lingers as "running" right after you switch to it. (A detach refreshes the whole
// list at once via sweepMetadataNowCmd, not this message.)
type instancePolledMsg struct {
	instance *session.Instance
	state    tmux.PaneState
	// attachGen stamps home.attachGen at cmd creation, like metadataUpdateDoneMsg.
	attachGen uint64
}

// pollSelectedCmd polls a single instance off the UI thread for an immediate status refresh
// when the selection changes, so an idle session no longer lingers as "running" right after
// you switch to it. It uses the hysteresis-respecting Poll — the tick loop kept the monitor
// current for a live selection change. Returns nil for a session that can't be polled; Poll
// itself also yields PaneDead for a dead session, which ApplyPaneState ignores.
//
// A detach is different — the tick stream was stalled while attached, so every row is stale
// — and is handled by sweepMetadataNowCmd (a face-value PollNow for the selected row plus a
// full background sweep), not here.
func pollSelectedCmd(inst *session.Instance, attachGen uint64) tea.Cmd {
	if inst == nil || !inst.Started() || inst.Paused() {
		return nil
	}
	return func() tea.Msg {
		return instancePolledMsg{instance: inst, state: inst.Poll(), attachGen: attachGen}
	}
}

// promptSendErrorMsg reports that a queued initial prompt failed to deliver after the
// bounded retries, so the failure surfaces in the UI instead of only reaching the log.
// instance identifies which session's prompt was lost.
type promptSendErrorMsg struct {
	instance *session.Instance
	// prompt is the claimed head text this attempt carried, so the handler pops exactly
	// that entry (matched dequeue) rather than blindly clearing whatever now heads the queue.
	prompt string
	err    error
}

// promptDeliveredMsg reports that a queued initial prompt was confirmed delivered (typed
// into the composer and submitted). The main loop clears the queued prompt on receipt, so
// it stops being a poll target and is never re-sent.
type promptDeliveredMsg struct {
	instance *session.Instance
	// prompt is the claimed head text that was delivered, so the handler pops exactly that
	// entry (matched dequeue) and a stale confirmation can never wipe a newer prompt.
	prompt string
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
			log.InfoLog.Printf("delivered queued prompt to %q", instance.Title())
			return promptDeliveredMsg{instance: instance, prompt: prompt}
		case session.IsSoftPromptError(err):
			return promptDeferredMsg{instance: instance}
		default:
			log.ErrorLog.Printf("failed to send queued prompt to %q after %d attempts: %v",
				instance.Title(), promptSendAttempts, err)
			return promptSendErrorMsg{instance: instance, prompt: prompt, err: err}
		}
	}
}

// deliverReadyPrompts dispatches a send for each ready instance with a queued prompt and
// returns the commands that perform them. The prompt is NOT cleared here — it stays queued
// until delivery is confirmed (promptDeliveredMsg), so a failed or unconfirmed send is
// retried by a later tick rather than lost. ClaimPrompt's atomic in-flight guard ensures
// only one send is outstanding per instance, so overlapping dispatchers (a later tick, or
// the attach keeper) cannot send the same prompt twice.
//
// The #571 question hold is applied HERE rather than folded into r.readyForPrompt, and the
// placement is the whole point: applyMetadataResults calls this after its ApplyPaneState
// loop, so questionHoldsPrompt reads an Unread() that already reflects the turn that just
// ended. Evaluated in collectMetadata's goroutine it would instead read the value from
// before this tick — false for a row the user was watching — and open the gate on the one
// tick a question ever gets delivered into. r.asked carries the verdict from that goroutine
// (the transcript read is what had to happen off-thread); only the cheap Unread() re-read
// belongs on the main thread.
func deliverReadyPrompts(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range results {
		if !r.readyForPrompt || questionHoldsPrompt(r.instance, r.asked) {
			continue
		}
		if prompt, ok := r.instance.ClaimPrompt(); ok {
			cmds = append(cmds, sendPromptCmd(r.instance, prompt))
		}
	}
	return cmds
}

// endedAskingNow resolves the #571 question flag for an instance whose pane currently
// reads state: did its last turn end by asking the user something?
//
// The transcript is re-read only on a SETTLED pane, so this costs about one read per
// turn-end rather than one per tick; every other state, and an unchanged transcript,
// answers from the instance's memo. A mid-turn pane cannot have a question to answer yet,
// and re-reading one would only race the turn it is still writing.
//
// The (value, stamp, ok) triple is returned rather than stored so the CALLER decides
// whether to memoize. The metadata tick does, on the main thread. The attach keeper
// deliberately does not: askedStamp carries the same "written on the main thread only"
// contract as modelStamp, whose own comment says a second extraction call site would need
// a lock. The keeper pays a re-read per cycle instead — bounded, and only for a session
// that has a prompt queued while the user is attached elsewhere.
func endedAskingNow(inst *session.Instance, state tmux.PaneState) (bool, transcript.Stamp, bool) {
	// PaneBackground counts as settled here, and must: it is only ever raised on a pane
	// whose TURN has ended (poll.go demotes a would-be idle, never a working state), so the
	// transcript is exactly as final as it is under PaneIdle. Without it, a turn that ends
	// by asking a question while a background shell is live would never run ComputeAsked —
	// silently dropping #571's question hold and the EventAsked notification for precisely
	// the sessions this feature exists to keep visible.
	//
	// Necessary but NOT sufficient, and the other half is in another package: the hold is
	// `asked && Unread()`, so it also needs the turn-end into a chip-held Pending to raise
	// the unread bit (session.setStatusTurnEnded). With only this half, `asked` is computed
	// for a conjunction that is always false — the hold reads as present and does nothing.
	if state != tmux.PaneIdle && state != tmux.PaneBackground {
		return inst.EndedAsking(), transcript.Stamp{}, false
	}
	asked, stamp, ok := inst.ComputeAsked()
	if !ok {
		return inst.EndedAsking(), transcript.Stamp{}, false
	}
	return asked, stamp, true
}

// questionHoldsPrompt turns the #571 verdict into the delivery gate: a question holds a
// queued prompt only until the user has demonstrably SEEN it. markSeenAfterDwell clears
// Unread once the row has been selected long enough for the preview to show the question,
// which is the whole release valve — it needs no new key and no new UI state, and it
// makes a misfired classification cost a delay rather than a prompt stuck forever.
//
// It exists as a function rather than an `asked && inst.Unread()` at each call site
// because there are TWO dispatchers (the metadata tick and the attach keeper) and a hold
// applied by only one of them is not a hold.
//
// Both must also call it AFTER the ApplyPaneState that ends the turn, because Unread() is
// what that call raises: attachKeeper.service applies the pane state on the line above its
// check, and the tick path defers to deliverReadyPrompts for the same reason. Sharing the
// predicate is not enough — it has a precondition, and reading it one step too early
// silently disables the hold rather than failing.
func questionHoldsPrompt(inst *session.Instance, asked bool) bool {
	return asked && inst.Unread()
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
// (A menu-style gate that has painted still reads as a box for every agent that draws its
// selector with the composer glyph — "❯ 1. …" for claude, "> 1. Yes" for agy, "› 1. Yes,
// continue" for codex — so it is the gate/prompt checks inside AwaitingInput, never the box
// check, that keep the selector out. Deliberately so: a queued prompt that is itself a
// numbered list draws the same shape, so a box check strict enough to reject the menu would
// reject the prompt too.)
//
// unansweredQuestion is the #571 gate: the last turn ended by asking the user something
// and they have not seen it yet. A pane that stopped to ask satisfies every other clause
// here — the turn ended, the box is up, nothing is working — so without this the queue
// answers a question the user never read, and the agent proceeds on an answer to
// something else. It sits beside awaitingInput as a hard precondition rather than below
// the busy check ON PURPOSE: the timeout must never bypass it.
//
// Only attachKeeper.service passes it live, because it has already applied the pane state
// that raises Unread(). The metadata tick passes false here and applies the same hold in
// deliverReadyPrompts instead — see questionHoldsPrompt for why the ordering, not the
// predicate, is what makes it work.
//
// It applies to EVERY queued prompt, not only zero-clock follow-ups, even though a boot
// prompt is conceptually the first message of a session and has no prior turn to answer.
// Keying it on queuedAt.IsZero() looks equivalent and is not: FromInstanceData
// re-stamps the restored head with a LIVE clock (session/instance.go), so after a TUI
// restart a prompt that was queued as a follow-up would arm the 60s valve and be
// delivered into the question anyway. Holding unconditionally closes that, and costs a
// genuine boot prompt nothing — a session with no prior turn has no assistant message, so
// the flag is false. It is also right across pause→resume, where `claude --continue`
// keeps the transcript and the pre-pause question is still unanswered.
//
// Normally we also wait for the pane to leave PaneWorking to avoid the post-trust
// "loading" transition window. PanePending is held the same as PaneWorking: the main turn
// has ended but a background sub-agent is still in flight (#290), so although the input box
// is idle and typable, delivering now would interleave a new turn with the still-running one
// — a zero-clock follow-up must wait for the sub-agent to finish.
//
// PaneBackground is deliberately NOT held, and the asymmetry is the point: a sub-agent's
// turn interleaves with a new one, whereas a detached background shell or monitor does not.
// Holding it would strand the follow-up outright rather than merely delay it — a quick-send
// carries a ZERO queuedAt, which disables the timeout valve below, so a session-length
// Monitor would keep every later message queued for the rest of the session with no
// release. The case that DOES need holding — the turn ended by asking something — is held
// by unansweredQuestion instead, which reaches a chip-held row because the turn-end raises
// unread there too (session.setStatusTurnEnded).
//
// But a chatty agent that writes continuously on boot can stay PaneWorking indefinitely and
// stall the first message forever; once the prompt has been queued longer than
// promptDeliveryTimeout we drop only that busy check. A zero queuedAt disables the timeout
// (the prompt was queued without a timestamp), falling back to the strict idle-pane
// requirement.
func promptDeliveryReady(state tmux.PaneState, awaitingInput, unansweredQuestion bool, queuedAt, now time.Time) bool {
	if !awaitingInput {
		return false
	}
	if unansweredQuestion {
		return false
	}
	if state != tmux.PaneWorking && state != tmux.PanePending {
		return true
	}
	return !queuedAt.IsZero() && now.Sub(queuedAt) > promptDeliveryTimeout
}

// lostSessionRecoverThreshold is how many consecutive ticks an instance must be seen
// with a dead tmux session before it is recovered to Paused. Recovery commits any WIP
// and removes the worktree, so a single transient `tmux has-session` miss (server
// blip, load spike) must not trigger it — require confirmation across ticks.
const lostSessionRecoverThreshold = 2

// lostSessionLaunchCrashWindow is how soon after (re)launch a lost-session
// recovery counts as a crash-at-launch (a bad program/profile) rather than a
// long-lived session that later died, so the notice can name the command.
//
// It bounds the blank relaunch too (Instance.RepairResumingLaunch), and deliberately
// the same number: both ask "did this session die OF its launch?". It has to be
// comfortably more than the death it repairs takes to arrive — `claude --continue`
// with nothing to resume was driven at 3.3s from launch to exit — and comfortably less
// than a session's working life, which is what keeps a long-lived session that dies
// from being resurrected instead of parked.
const lostSessionLaunchCrashWindow = 15 * time.Second

// lostRecovery is the outcome of recovering one lost session, returned so the
// caller can surface it — a silent Running→Paused transition is indistinguishable
// from a user pause (#270). err is non-nil when RecoverLostSession failed (the
// instance is still parked Paused by pause()); launchCmd is set only for a
// crash-at-launch, naming the command that likely caused it.
//
// instance and worktreeGone are here so the caller can reap the session's cached
// terminal shell (#707). A recovery removes the worktree on its success path as well
// as most of its failing ones, and unlike the two pause handlers this one had no reap
// at all — recoverLostInstances is a free function with no m, so the reap has to
// happen in the caller and the facts it needs have to travel.
//
// worktreeGone is measured, not inferred from the kind of session: a recovered direct
// session normally reports false, because it has no worktree to free and its working
// directory is the user's own checkout, but one whose directory the user has since
// deleted reports true and its shell is stranded exactly like a git session's.
//
// relaunchedBlank marks the one outcome that is NOT a park: a session whose resuming
// launch died at birth and was relaunched without its resume flag (#712). It is still
// reported, because it is the recovery a user cannot see — the row keeps its title, its
// status and its place, and only the agent's memory of the conversation is gone.
type lostRecovery struct {
	instance        *session.Instance
	title           string
	err             error
	worktreeGone    bool
	launchCmd       string
	relaunchedBlank bool
}

// recoverLostInstances acts on instances whose tmux session has died (flagged
// sessionLost by the metadata tick). Nearly always that means parking them as Paused, so
// they stop being polled and can be brought back with Resume; the one exception is a
// resuming launch that died at birth, which RepairResumingLaunch relaunches blank and
// leaves RUNNING (see lostRecovery.relaunchedBlank). Everything below that says "recovery"
// covers both.
//
// It debounces using strikes (a per-instance count of consecutive dead observations, owned
// by the caller): a session is only recovered after lostSessionRecoverThreshold
// consecutive misses; any live observation resets the count. Recovery is attempted exactly
// once (at the threshold strike): a failed recovery pins the strike above the threshold so
// it never retries in a tight loop (the #270 dead-end), and pause() guarantees the instance
// ends Paused regardless, so the next tick's Paused check clears the strike. A repair
// clears the strike itself, because its instance ends live rather than Paused and so is
// never caught by that check. Returns one lostRecovery per instance acted on so the caller
// can persist, surface, and reap them. Runs on the main thread — the only place model state
// may be mutated.
//
// busy suspends the whole sweep for a tick, and it is the guard against acting on a pane
// some off-thread action is in the middle of killing. A pause runs through
// beginAsyncAction and writes SetStatus(Paused) only AFTER closeParkedSession, the WIP
// commit and the worktree removal — several seconds during which the pane is already dead,
// the instance still reads Running, and nothing marks it retiring (that mark covers kills).
// Two 500ms ticks fit inside that window easily. Parking it a second time was merely
// redundant; RepairResumingLaunch would launch a fresh agent into a worktree being
// unlinked, and pause() would then write Paused over a row whose tmux session is live —
// leaving an agent nothing re-parks. Strikes are left untouched rather than cleared, so
// the debounce resumes where it was once the action finishes.
//
// Both outcomes do blocking I/O here, on the update thread, and the repair is the CHEAPER
// of the two: a handful of tmux calls, the short ones capped at tmuxOpTimeout, plus
// start's bounded existence backoff (2s worst case, single-digit ms in practice). The
// park it replaces runs pause() — kill-session, two git queries, possibly a commit, a
// recursive worktree removal and a prune — and always has. Moving one without the other
// would leave the heavier path inline, so if this ever needs to go off-thread (#380's
// shape) both go together.
//
// The reap is the caller's because this is a free function with no m; each
// lostRecovery therefore carries the instance and whether the recovery freed its
// working directory (see the type).
func recoverLostInstances(results []instanceMetaResult, strikes map[*session.Instance]int, retiring map[*session.Instance]bool, busy bool) []lostRecovery {
	if busy {
		return nil
	}
	var recovered []lostRecovery
	for _, r := range results {
		// A retiring session's pane is SUPPOSED to die: its kill is in flight and
		// its row still exists for the length of that window. Recovering it would
		// park it as Paused and toast "terminal exited" — a notice that is both
		// false and painted over the kill's own progress row (#380).
		if !r.sessionLost || r.instance.Paused() || retiring[r.instance] {
			delete(strikes, r.instance) // alive, paused, or retiring: clear any prior strikes
			continue
		}
		strikes[r.instance]++
		// Attempt recovery only on the exact threshold strike. Below it we are still
		// debouncing; above it a prior attempt already ran and (if it failed) must not
		// be retried every tick — surface once, then leave it be.
		if strikes[r.instance] != lostSessionRecoverThreshold {
			continue
		}
		// Repair before park. A session whose RESUMING launch died at birth is one a
		// blank relaunch can save, and parking it instead would remove the worktree
		// Atrium had just rebuilt — the #699 consequence, for the agents #712 leaves
		// exposed. The strike goes with the repair: the session is live again, so the
		// next tick starts counting from zero like any other live observation.
		if r.instance.RepairResumingLaunch(lostSessionLaunchCrashWindow) {
			delete(strikes, r.instance)
			recovered = append(recovered, lostRecovery{
				instance:        r.instance,
				title:           r.instance.Title(),
				relaunchedBlank: true,
			})
			continue
		}
		err := r.instance.RecoverLostSession()
		if err != nil {
			log.ErrorLog.Printf("failed to recover lost session %q: %v", r.instance.Title(), err)
		} else {
			delete(strikes, r.instance) // clean success; drop the strike
		}
		var launchCmd string
		if r.instance.DiedAtLaunch(lostSessionLaunchCrashWindow) {
			launchCmd = r.instance.Program
		}
		recovered = append(recovered, lostRecovery{
			instance: r.instance,
			title:    r.instance.Title(),
			err:      err,
			// Sampled right after the recovery, before anything else can run: this
			// loop and the caller's reap are the same synchronous update turn.
			worktreeGone: r.instance.WorkingDirGone(),
			launchCmd:    launchCmd,
		})
	}
	return recovered
}

// finishBlankRelaunches does for a repaired session what the resume path does for a
// resumed one, and it is needed because a repair is invisible to everything that normally
// notices a relaunch — the selection did not move and no user action ran.
//
// The size, because ts.Start creates the replacement with `new-session -d` and no -x/-y,
// so the pane keeps tmux's 80x24 default until something gives it the preview's geometry.
// The only thing that does that on its own is the layout recompute
// (SetSessionPreviewSize), and no part of this path asks for one. A background create has
// the same gap for the same reason and closes it the same way, through the same seam —
// see handleInstanceStarted, the other site sizing one pane by hand. The window this
// would otherwise leave unsized is the BOOT window, whose captures feed GateUp,
// DetectPrompt and the busy-marker search — every one of them width-sensitive (#512,
// #648, #665).
//
// The frame note, because a repair swaps the pane underneath the same *Instance, which is
// precisely the case instanceChanged's pointer comparison cannot see. Without it the quiet
// run measured against the DEAD pane goes on deciding whether the new one is captured, and
// the preview reports a real frame age for a pane that no longer exists (noteFrameTargetChange).
//
// Runs on the main thread, from the metadata handler, with the recoveries that handler
// just produced.
func (m *home) finishBlankRelaunches(recoveries []lostRecovery) {
	selected := m.list.GetSelectedInstance()
	width, height := m.tabbedWindow.GetPreviewSize()
	for _, rec := range recoveries {
		if !rec.relaunchedBlank || rec.instance == nil {
			continue
		}
		if err := sizeStartedPane(rec.instance, width, height); err != nil {
			log.ErrorLog.Printf("could not size the relaunched pane for %q: %v", rec.title, err)
		}
		if rec.instance == selected {
			m.noteFrameTargetChange()
		}
	}
}

// countLostDeaths is how many of recoveries actually cost the fleet a session. A blank
// relaunch did not: it is reported so the user hears about the lost conversation, but the
// agent is running, so the OS chrome must not paint the taskbar error state for it.
func countLostDeaths(recoveries []lostRecovery) int {
	deaths := 0
	for _, rec := range recoveries {
		if !rec.relaunchedBlank {
			deaths++
		}
	}
	return deaths
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
// attachGen records home.attachGen at cmd creation; the handler drops results from
// an older generation — a terminal attach ran in between, and the keeper may have
// advanced the very panes the capture observed (see home.attachGen).
type metadataUpdateDoneMsg struct {
	results   []instanceMetaResult
	attachGen uint64
}

// metadataSweepDoneMsg carries the result of a one-shot, off-cadence metadata refresh
// (see sweepMetadataNowCmd). Unlike metadataUpdateDoneMsg, its handler applies the
// results without re-arming the periodic tick. attachGen guards staleness the same way.
type metadataSweepDoneMsg struct {
	results   []instanceMetaResult
	attachGen uint64
}

// sweepMetadataNowCmd refreshes every active session immediately (no 500ms sleep, no
// metadataFullSweepEvery throttle), for use right after a detach where the event loop was
// suspended and every row is stale. It brings the next full sweep forward to now and does
// NOT re-arm the periodic tick. The selected row is polled face-value (PollNow, fresh=true)
// so a stale "running" on a now-idle agent doesn't linger; background rows keep the
// hysteresis Poll so a mid-turn agent isn't falsely flagged done (see collectMetadata's
// fresh argument). Returns nil when there are no active sessions to refresh.
func sweepMetadataNowCmd(ctx context.Context, active []*session.Instance, selected *session.Instance, attachGen uint64, usage usagePolicy) tea.Cmd {
	if len(active) == 0 {
		return nil
	}
	return func() tea.Msg {
		return metadataSweepDoneMsg{results: collectMetadata(ctx, active, selected, true, usage), attachGen: attachGen}
	}
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

// diffContentFloor bounds how stale a background row's +/- chip may get. It is the
// backstop for every writer the agent's own status cannot see: the terminal tab
// runs a shell inside the worktree, the agent may run `git commit` in its own pane,
// the user may edit the worktree from an editor, and a sibling session sharing a
// link_path mutates the same tree — none of which moves a status. A var so tests
// can shrink it.
var diffContentFloor = 15 * time.Second

// diffContentDue reports whether a non-selected session needs its diff CONTENT —
// the line counts and patch text — recomputed this sweep.
//
// The branch-level counters are never gated: the caller refreshes those every
// sweep either way, so the numbers a kill confirmation reads (Dirty, Unpushed) stay
// exactly as fresh as before. Only the row's +/- chip can age here.
//
// Content is due when the agent could plausibly have written since it was last
// computed:
//
//   - never computed — there is no number to preserve;
//   - a writing status — Running, Loading and Pending all mean the agent may have
//     the tree open right now. Pending is deliberately in that set: the main turn
//     ended but background work — a sub-agent (#290), or a shell/monitor the turn left
//     running — may still be writing;
//   - the status changed since the last computation — this is the Running→Ready
//     edge, the moment the agent's final write lands and the chip matters most;
//   - the floor has lapsed, covering the writers no status can see.
//
// The zero StatusChangedAt is treated as "changed just now", not as 2000 years
// ago: a restored instance assigns its status directly rather than through
// SetStatus, so the stamp stays zero until the first poll, and a naive comparison
// would read every freshly launched session as indefinitely idle.
func diffContentDue(status session.Status, statusChangedAt, contentAt, now time.Time) bool {
	if contentAt.IsZero() {
		return true
	}
	switch status {
	case session.Running, session.Loading, session.Pending:
		return true
	}
	if statusChangedAt.IsZero() || statusChangedAt.After(contentAt) {
		return true
	}
	return now.Sub(contentAt) >= diffContentFloor
}

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
		if inst == selected || inst.Prompt() != "" {
			out = append(out, inst)
		}
	}
	return out
}

// collectMetadata polls each instance in poll on its own background goroutine and returns
// the per-instance results, to be applied on the main thread by applyMetadataResults. The
// diff work splits three ways (see the switch below): the selected instance gets a full
// diff (with Content) for the diff pane; a background instance whose tree may have moved
// gets a lightweight numstat-only summary, keeping per-instance memory bounded; and one
// diffContentDue rules out gets the branch-level counters alone, skipping the diff
// entirely. Shared by the periodic metadata tick and the one-shot detach sweep.
//
// fresh takes a face-value PollNow for the selected instance instead of the hysteresis
// Poll: the detach sweep sets it because the tick stream was stalled while attached, so the
// selected row's smoothing state is stale and a snapshot is correct (ArmReadySuppression,
// armed by the detach handler, absorbs the synthetic Running→Ready). Background rows always
// use the hysteresis Poll — they carry no ready-suppression, so a single marker-absent
// sample of a mid-turn agent must not be allowed to flag a false completion. The periodic
// tick passes fresh=false, so every row uses the hysteresis Poll there.
func collectMetadata(ctx context.Context, poll []*session.Instance, selected *session.Instance, fresh bool, usage usagePolicy) []instanceMetaResult {
	results := make([]instanceMetaResult, len(poll))
	var wg sync.WaitGroup
	for idx, inst := range poll {
		wg.Add(1)
		go func(i int, instance *session.Instance) {
			defer wg.Done()
			r := &results[i]
			r.instance = instance
			// Bail before firing any subprocess once the app context is cancelled
			// (shutdown): each probe below would only fail fast against a torn-down
			// instance. r.instance is already set, so applyMetadataResults — which
			// leaves a zero PaneUnknown state untouched — never derefs a nil here. The
			// zero diff/PR stats it does apply are harmless: this fires only on the
			// shutdown tick, and a cancelled probe would have nilled them anyway.
			if ctx.Err() != nil {
				return
			}
			// A started session whose tmux pane has died would fail every probe
			// (capture, diff) and flood the log/error box. Poll reports a dead
			// session as PaneDead from its own (single) has-session check, so derive
			// sessionLost from that rather than forking a second has-session here.
			// The main thread recovers it to Paused, debounced by recoverLostInstances
			// (which also re-guards Paused, so a raced-paused instance is ignored).
			if fresh && instance == selected {
				r.state = instance.PollNow()
			} else {
				r.state = instance.Poll()
			}
			if r.state == tmux.PaneDead {
				r.sessionLost = true
				return
			}
			// Re-derive the #571 question flag. It must come BEFORE the delivery probe
			// below, which reads the value it produces — otherwise the first tick after
			// a question would still deliver into it. The main thread memoizes the
			// result (applyMetadataResults), which is what keeps this to about one
			// transcript read per turn-end rather than one per tick.
			var asked bool
			asked, r.askedStamp, r.askedOK = endedAskingNow(instance, r.state)
			r.asked = asked
			// Only probe readiness while a prompt is actually queued (a brief
			// window after a new session), so the extra pane capture is rare.
			//
			// The #571 hold is deliberately NOT folded in here: questionHoldsPrompt
			// reads Unread(), and the Running→Ready edge that raises it for a finished
			// turn is applied by THIS tick's ApplyPaneState — which runs later, on the
			// main thread. Asking here reads the pre-turn value, so the hold would miss
			// exactly the tick it exists for. deliverReadyPrompts applies it after that
			// loop; r.asked, set just above, is what carries the verdict there.
			if instance.Prompt() != "" {
				r.readyForPrompt = promptDeliveryReady(
					r.state, instance.AwaitingInput(), false,
					instance.PromptQueuedAt(), time.Now())
			}
			switch {
			case instance == selected:
				r.diffStats = instance.ComputeDiff()
			case diffContentDue(instance.GetStatus(), instance.StatusChangedAt(),
				instance.DiffContentAt(), time.Now()):
				r.diffStats = instance.ComputeDiffNumstat()
			default:
				// The tree cannot have changed: skip the untracked-file walk and the
				// diff itself, but still refresh the branch-level counters — Dirty and
				// Unpushed among them, which are what a kill confirmation reads.
				r.diffStats = instance.ComputeRepoStats()
				r.diffContentSkipped = true
			}
			// PR status is network-bound but TTL-cached, so most ticks return
			// instantly with no I/O; the selected session refreshes eagerly.
			r.prStatus = instance.ComputePRStatus(instance == selected)
			// Transcript model is stamp-gated: an idle claude session costs one
			// ReadDir + Stat per tick, a streaming one a ≤128KB tail parse.
			r.model, r.modelStamp, r.modelOK = instance.ComputeModel()
			// Context occupancy is stamp-gated the same way, over the same file: an
			// idle claude session costs one more ReadDir + Stat per tick and no file
			// open, a streaming one a second ≤128KB tail parse. usagePolicy decides
			// whether it is read AT ALL — a chip switched off does no work, and an
			// ambiguous transcript source is not merely hidden but never stored.
			if usage.allowsContext(instance) {
				r.usage, r.usageStamp, r.usageOK = instance.ComputeUsage()
			} else {
				r.usageClear = true
			}
			// Spend is cursor-gated rather than stamp-gated, because it sums the
			// whole project directory instead of reading one file's tail: an idle
			// session costs one ReadDir plus one Stat per transcript and opens
			// nothing, and a growing one reads only the bytes that were appended.
			// Same policy gate, and it is mutually exclusive with the reading above
			// — the two chips share a column, so only one of them is ever paid for.
			if usage.allowsCost(instance) {
				r.cost, r.costCursor, r.costOK = instance.ComputeCost()
			} else {
				r.costClear = true
			}
			// Live permission mode reads the value Poll just detected from the
			// footer — no extra capture; only applied when it changed.
			r.mode, r.modeOK = instance.ComputeMode()
			// Effort reads the value Poll just lifted off the hook record — no extra
			// I/O; only applied when it changed.
			r.effort, r.effortOK = instance.ComputeEffort()
			// The dev-server sibling session (#389): whether this repo declares a run
			// command, and whether one is up. Priced to cost nothing for a session whose
			// repo declares none — see ComputeRunState.
			r.runState = instance.ComputeRunState()
			// Take the frame Poll just captured. It costs nothing — Poll captured the
			// pane to classify it and used to throw the bytes away — and it means a
			// session the user has not selected this run still has a frame to paint
			// the moment they do, instead of the setup splash (#380).
			r.paneFrame, r.paneFrameAt, r.paneFrameOK = instance.HarvestPaneFrame()
		}(idx, inst)
	}
	wg.Wait()
	return results
}

// applyMetadataResults applies a batch of metadata results to their instances on the main
// thread (pane state, diff, PR, model, mode, effort), re-floats urgent rows, refreshes the session
// context bars, writes state.json when any session's status has moved (see
// flushStatusPersist), and returns any queued-prompt delivery commands. Shared by the periodic
// metadata tick and the one-shot detach sweep. It deliberately does NOT recover lost
// sessions or reschedule the tick — those stay with the periodic handler (recovery's
// strike debounce must not be shortened by a same-resume double observation).
func (m *home) applyMetadataResults(results []instanceMetaResult, emit bool) []tea.Cmd {
	// Read the notification mode once per batch: when off (the default) the per-instance
	// status snapshots below are skipped entirely, so a disabled feature adds no work.
	// The detach sweep passes emit=false, so returning to the list never replays a burst
	// of edges that fired silently while the event loop was suspended by an attach.
	mode := config.NotificationsOff
	if emit {
		mode = m.notificationsMode()
	}
	notifyOn := mode != config.NotificationsOff
	// The quiet-pane gate only ever asks about the selected session's preview, so
	// only its harvest is worth folding into the run below.
	selected := m.list.GetSelectedInstance()
	for _, r := range results {
		// Skip instances that were paused while metadata was being computed, or that
		// were just recovered to Paused because their session died.
		if r.sessionLost || r.instance.Paused() {
			continue
		}
		// Snapshot the status and unread stamp just before ApplyPaneState so maybeNotify
		// can detect the transition it drives (ApplyPaneState calls SetStatus, which
		// overwrites both). Only taken when notifications are on. The persist below
		// deliberately does NOT read it: it asks the instances what changed, so a
		// transition applied by some other observer is not missed here.
		var old session.Status
		var prevUnreadAt time.Time
		if notifyOn {
			old = r.instance.GetStatus()
			prevUnreadAt = r.instance.UnreadAt()
		}
		r.instance.ApplyPaneState(r.state)
		if notifyOn {
			// r.asked is the verdict endedAskingNow resolved for this tick (fresh read
			// or memo), and it is the SAME value deliverReadyPrompts gates the hold on
			// below — so the notification and the hold can never disagree about whether
			// the turn ended asking. They act at different points in the tick (here,
			// inside the loop; the hold after it, once ApplyPaneState has raised
			// Unread), but both read this one immutable field.
			//
			// Passing a literal false here instead is not a compile error and not a test
			// failure — every notify test drives notifyEventFor/maybeNotify directly — it
			// just silently retires EventAsked in production. TestTickPathNotifiesAsked
			// exists because that is not a hypothetical.
			m.maybeNotify(r.instance, old, prevUnreadAt, r.asked, mode)
		}
		applyDiffStats(r.instance, r.diffStats, r.diffContentSkipped)
		r.instance.SetPRStatus(r.prStatus)
		if r.modelOK {
			r.instance.SetModelMeta(r.model, r.modelStamp)
		}
		switch {
		case r.usageClear:
			r.instance.ClearUsage()
		case r.usageOK:
			r.instance.SetUsageMeta(r.usage, r.usageStamp)
		}
		switch {
		case r.costClear:
			r.instance.ClearCost()
		case r.costOK:
			r.instance.SetCostMeta(r.cost, r.costCursor)
		}
		if r.askedOK {
			r.instance.SetAskedMeta(r.asked, r.askedStamp)
		}
		if r.modeOK {
			r.instance.SetModeMeta(r.mode)
		}
		if r.effortOK {
			r.instance.SetEffortMeta(r.effort)
		}
		r.instance.ApplyRunState(r.runState)
		// Record the liveness this poll already established, so the context-bar push
		// below reads a memo instead of forking its own has-session per session. A
		// PaneUnknown result is inconclusive (attached, unstarted, a transient probe
		// failure) and deliberately leaves the memo alone rather than claiming death.
		if r.state != tmux.PaneUnknown {
			r.instance.SetPaneLive(r.state != tmux.PaneDead)
		}
		// Note the harvest into the quiet run only when it was actually stored. While
		// the capture chain is alive its frames are always fresher, so the harvest is
		// dropped and contributes nothing — which is right, because the chain is
		// already feeding the run at 10Hz. Once the gate closes the chain stops, the
		// harvest becomes the newest frame every sweep, and this is the only thing
		// still watching the pane for a change.
		if text, stored := applyHarvestedFrame(r); stored && r.instance == selected {
			m.noteFrameSeen(frameTarget{instance: r.instance}, text)
		}
	}
	// Called every sweep, not only when this sweep moved something, so a write deferred
	// by the interval — or by a failure, or applied by an observer other than this one
	// — still lands on a later quiet tick.
	m.flushStatusPersist(time.Now())
	// Re-apply the status sort now that pane states are fresh, so urgent sessions keep
	// floating to the top of their group. No-op in creation mode; the selected session
	// stays under the cursor (preserved by identity).
	m.list.ApplySort()
	cmds := deliverReadyPrompts(results)
	// Appended only when there is something to push, so a quiet tick returns an
	// empty slice rather than one holding a nil Cmd.
	if push := m.pushSessionContexts(); push != nil {
		cmds = append(cmds, push)
	}
	// This sweep is where a row's status flips to Running or Loading most often, so
	// reviving the spinner here makes it immediate. It is not the only writer —
	// the unconditional 100ms tick is what covers the rest — just the one worth
	// not waiting a beat for. No-op while one is already running or nothing spins.
	if spin := m.armSpinnerTick(); spin != nil {
		cmds = append(cmds, spin)
	}
	return cmds
}

// statusPersistInterval is the shortest gap between two saves triggered by a status
// change. A var, not a const, so tests can shrink it (see
// TestStatusPersistHonoursTheInterval).
//
// Transitions arrive in bursts — a fleet resuming after a lull logs a dozen inside one
// second — and each save marshals and atomically rewrites the whole state file (~100KB
// for a 15-session fleet). One write per second bounds that at a cost far below the
// per-tick pane captures the same sweep already pays for, while keeping the stored
// snapshot current to within a second, which is an order of magnitude finer than any
// consumer of `atrium ls` samples at.
var statusPersistInterval = time.Second

// flushStatusPersist writes state.json when any session's status has changed since the
// last successful write, at most once per statusPersistInterval.
//
// WHY THIS EXISTS AT ALL. Until it did, state.json was written only by user and
// lifecycle events — roughly twenty handlers, none of them a status change — so the
// stored status was a byproduct of the last unrelated save. Measured on a live fleet:
// gaps of 5 and 26 minutes during active use, and one of about eight hours overnight
// that swallowed a session's entire needs-input spell. `atrium ls` reads that file, so
// every consumer of it — the TUI's own restart path included — was being served a
// status that had been true at an arbitrary past moment, with no way to tell which.
//
// WHY IT ASKS THE INSTANCES. The dirty bit is set by recordStatusChange, so whichever
// observer sees a transition marks it: the metadata sweep, the off-cadence selection
// poll, and the attach keeper all apply pane states, on different cadences and (for the
// keeper) a different goroutine. Comparing each instance's status before and after this
// sweep's own ApplyPaneState would catch only the transitions this sweep applied — a
// change the selection poll had already applied in memory reads as no change here,
// because the "before" it snapshots is already the new value. That is the original
// staleness bug in miniature, so the detection belongs with the writer.
//
// A suppressed change stays dirty, which is the part worth not losing. A burst's LAST
// transition is usually the interesting one (running→needs-input is what a fleet settles
// on), and it is exactly the one the interval suppresses; leaving the bit set means the
// next sweep to clear the interval writes it, change or not. A failed write leaves the
// bits set for the same reason — the next sweep retries instead of dropping the change
// — while lastStatusPersist still advances, so a failing disk is retried once a second
// rather than on every tick.
//
// A failure is logged rather than surfaced: this runs on the metadata tick, twice a
// second, and a modal for a transient write error would be worse than the staleness it
// reports. Silence is affordable here only because the dirty bits make the failure
// self-healing — a save that never succeeded is never marked done.
func (m *home) flushStatusPersist(now time.Time) {
	// Snapshot before the write, and clear only these: the attach keeper marks
	// instances from its own goroutine, so one that goes dirty while the save is in
	// flight must stay dirty rather than be cleared by a write that predates it.
	dirty := m.dirtyStatusInstances()
	if len(dirty) == 0 {
		return
	}
	if !m.lastStatusPersist.IsZero() && now.Sub(m.lastStatusPersist) < statusPersistInterval {
		return
	}
	m.lastStatusPersist = now
	if err := m.persistInstances(); err != nil {
		log.ErrorLog.Printf("failed to persist instances after status change: %v", err)
		return
	}
	for _, inst := range dirty {
		inst.ClearStatusDirty()
	}
}

// dirtyStatusInstances returns the sessions whose status has moved since the last save
// that carried it.
//
// Unstarted sessions count too. SaveInstances filters them out of the payload, so the
// write does not actually carry them — but they are cleared along with it, because a
// bit no save can ever discharge would re-arm this write every interval for the life
// of the process.
func (m *home) dirtyStatusInstances() []*session.Instance {
	var dirty []*session.Instance
	for _, inst := range m.list.GetInstances() {
		if inst.StatusDirty() {
			dirty = append(dirty, inst)
		}
	}
	return dirty
}

// applyHarvestedFrame stores the pane capture the poll already paid for, but only
// when it is newer than the cached one, and reports the text it stored. The order
// matters: the 100ms capture chain and this 500ms sweep both feed the same cache,
// and the chain's frames are fresher for the selected session — an unconditional
// write here would rewind the watched pane by up to half a second, ten times a
// second. Main thread only, like the Set*Meta calls it sits beside.
//
// It returns the stored text rather than letting the caller re-derive it because
// the quiet-pane gate compares frames byte for byte, and sanitizing the same
// capture twice to feed two consumers is both wasted work and a chance for the
// two copies to drift.
func applyHarvestedFrame(r instanceMetaResult) (string, bool) {
	if !r.paneFrameOK {
		return "", false
	}
	if _, cachedAt, cached := r.instance.PaneFrame(); cached && !r.paneFrameAt.After(cachedAt) {
		return "", false
	}
	text := theme.SanitizeWidth(r.paneFrame)
	r.instance.SetPaneFrame(text, r.paneFrameAt)
	return text, true
}

// applyDiffStats stores freshly computed diff stats on an instance (main thread only),
// dropping the result to nil on a real error so the row shows no stale numbers. The
// "base commit SHA not set" case is an expected pre-baseline state, not worth logging.
//
// contentSkipped marks a repo-stats-only result. Storing one verbatim would blank
// the row's +/- chip to zero on every gated tick — the numbers are absent because
// they were never asked for, not because the tree is clean — so the previous ones
// are carried forward and the content clock is left where it was. A result that DID
// carry content stamps that clock, which is what the gate reads next sweep.
func applyDiffStats(inst *session.Instance, stats *git.DiffStats, contentSkipped bool) {
	if stats != nil && stats.Error != nil {
		if !strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			log.WarningLog.Printf("could not update diff stats: %v", stats.Error)
		}
		inst.SetDiffStats(nil)
		return
	}
	if contentSkipped {
		inst.CarryDiffContent(stats)
	} else if stats != nil {
		inst.NoteDiffContentComputed(time.Now())
	}
	inst.SetDiffStats(stats)
}

// Context contract for poll tea.Cmd closures: tickUpdateMetadataCmd and
// sweepMetadataNowCmd capture the app context (home.ctx) and must honor it so app
// shutdown unwinds in-flight poll work instead of running it to completion. The tick's
// 500ms wait selects on ctx.Done(); collectMetadata's per-instance fan-out
// short-circuits when ctx is cancelled; and the underlying I/O derives its kill signal
// from each instance's baseCtx (= the app ctx): tmux capture and git diff cancel their
// subprocesses (exec.CommandContext), and ComputeModel's transcript read honors
// ctx via Instance.baseContext(). The metadataUpdateDoneMsg handler also stops re-arming
// once ctx is cancelled, so the tick chain ends on shutdown.
//
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
// Only the selected instance gets a full diff (with Content), which keeps per-instance
// memory bounded since the diff pane only ever renders the selected one. A background
// instance gets a lightweight numstat-only summary, or — once diffContentDue rules out
// that its tree moved — the branch-level counters alone. See collectMetadata.
func tickUpdateMetadataCmd(ctx context.Context, active []*session.Instance, selected *session.Instance, fullSweep bool, attachGen uint64, usage usagePolicy) tea.Cmd {
	return func() tea.Msg {
		// Honor ctx during the inter-tick wait so a shutdown mid-sleep doesn't leave
		// this goroutine parked for up to 500ms.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return metadataUpdateDoneMsg{attachGen: attachGen}
		}

		if len(active) == 0 {
			return metadataUpdateDoneMsg{attachGen: attachGen}
		}

		poll := pollTargets(active, selected, fullSweep)
		if len(poll) == 0 {
			return metadataUpdateDoneMsg{attachGen: attachGen}
		}

		return metadataUpdateDoneMsg{results: collectMetadata(ctx, poll, selected, false, usage), attachGen: attachGen}
	}
}
