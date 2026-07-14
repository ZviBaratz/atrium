package tmux

import (
	"bytes"
	"crypto/sha256"
	"regexp"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// PaneState is the classification of a tmux pane derived from its content. Unlike a
// raw "did the content change" signal, these are *level* signals: each is decided by
// what the pane currently shows, so they are stable across ticks while the underlying
// situation is unchanged (no flicker).
type PaneState int

const (
	// PaneUnknown means the pane could not be read this tick; callers keep the prior status.
	PaneUnknown PaneState = iota
	// PaneWorking means the agent is actively processing.
	PaneWorking
	// PanePrompt means a yes/no prompt is on screen awaiting an answer.
	PanePrompt
	// PanePromptManual means a prompt is on screen whose auto-answer is destructive
	// (a matcher with NoAutoTap, e.g. claude's plan approval): autoyes must surface
	// it as needs-input rather than tapping Enter. Runtime-only, never persisted.
	PanePromptManual
	// PaneIdle means the agent has settled with nothing pending.
	PaneIdle
	// PanePending means the main turn ended (the hook latched "ready") but the agent still
	// has background sub-agents recorded in flight — it will resume on its own when they
	// report back. Distinct from PaneIdle so the row isn't mislabeled "done" during that
	// window (#290). Only ever raised by the structured hook record (ready + a non-empty
	// in-flight set); a live busy marker still outranks it as PaneWorking. Runtime-only,
	// never persisted.
	PanePending
	// PaneDead means the tmux session no longer exists. Distinct from PaneUnknown
	// (a transient read failure of a live session): the metadata loop flags only a
	// PaneDead session for lost-session recovery. Runtime-only, never persisted.
	PaneDead
	// PaneGate means a one-time startup/trust screen is up (claude's folder-trust or
	// new-MCP prompt, codex/gemini folder-trust, aider's first-run docs prompt). It
	// consumes keystrokes until a human dismisses it, so a queued first prompt must be
	// held; callers surface it as needs-input rather than tapping. Runtime-only, never
	// persisted.
	PaneGate
)

// markerWorking reports whether this session's agent shows its busy marker in the live
// marker region of content. The match is confined per the adapter's MarkerWindow (the
// footer below the input box for claude, a bottom window for agents whose status row
// renders above it) rather than the whole pane, which would also match the scrolled-back
// transcript. Returns false for programs without a known marker.
func (t *Session) markerWorking(content string) bool {
	return t.adapter.HasBusyMarker(content)
}

// ansiRegex matches ANSI/SGR escape sequences. The pane is captured with `-e` (the
// preview pane needs the colors), but for state detection we strip them so a cursor
// blink or color toggle no longer counts as a content change, and so marker/prompt
// substring matches are not split by SGR codes embedded mid-text.
var ansiRegex = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// cleanForDetection strips ANSI escapes and trailing whitespace per line, yielding the
// stable text used for hashing and substring matching in Poll.
func cleanForDetection(content string) string {
	content = ansiRegex.ReplaceAllString(content, "")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

type statusMonitor struct {
	program string
	// Store hashes to save memory.
	prevOutputHash []byte
	// lastReported is the last committed PaneState, used by the working→idle hysteresis.
	lastReported PaneState
	// idleStreak counts consecutive idle observations since the agent was last working.
	// It bounds the marker-absent hold (idleConfirmTicks) as a safety net.
	idleStreak int
	// stableStreak counts consecutive observations whose cleaned content is unchanged. A
	// quiet (settled) pane is what distinguishes genuine completion from a between-turns
	// gap, so it lets the working→idle commit fire fast when the pane stops repainting.
	stableStreak int
	// lastSignal is the last logged "which signal decided the state" label. Poll logs only
	// when this changes, so the log records transitions (hook vs marker vs fallback) rather
	// than one line per 500ms tick.
	lastSignal string
	// mode is the last permission mode detected from the live footer ("" until the
	// first confident detection). Sticky: an indeterminate footer (busy/startup)
	// leaves it untouched so the chip doesn't flicker. Read under monitorMu via
	// RuntimePermissionMode.
	mode string
	// effort is the reasoning-effort level the last read hook record reported ("" until a
	// resolved turn reports one — hooks only carry effort inside a tool-use context, so a
	// session that has never called a tool has none). Sticky like mode: a record with no
	// effort leaves it untouched rather than blanking the chip. Read under monitorMu via
	// RuntimeEffort.
	effort string
}

func newStatusMonitor(program string) *statusMonitor {
	return &statusMonitor{program: program}
}

// logSignal records which signal path decided the pane state, but only when it changes from
// the last decision — so a steady session emits one line, not one per tick. name is the tmux
// session name. Output goes to the atrium log (os.TempDir()/atrium.log).
func (m *statusMonitor) logSignal(name, signal string) {
	if m.lastSignal == signal {
		return
	}
	m.lastSignal = signal
	log.InfoLog.Printf("status %s: %s", name, signal)
}

// The working→idle commit is gated by two thresholds, whichever fires first.
//
// Background: a genuinely-idle Claude pane and a between-turns pane (auto-accept, between
// an accepted step and the next request's model spin-up) are indistinguishable in a single
// snapshot — same input box and footer, differing only by the "esc to interrupt" substring
// — so the marker alone can't tell "done" from "about to continue". The discriminator is
// motion: a finished pane freezes, whereas a between-turns pane keeps repainting (spinner
// elapsed ticking, output rendering, the next response streaming in).
//
//   - idleSettleTicks: once the marker is gone AND the cleaned content has been unchanged
//     for this many ticks, the pane has settled — commit to idle promptly (~1s). This is
//     the common path and keeps the "ready" indicator responsive on real completion.
//   - idleConfirmTicks: a safety cap. If the marker stays absent for this long even while
//     the pane keeps changing (an agent UI we don't model, or a missed marker), commit
//     anyway rather than holding "working" forever (~3s).
//
// A churning turn-boundary gap never satisfies idleSettleTicks (the pane is moving), so it
// holds "working" until the marker returns — no Ready→Running flicker. Prompts are surfaced
// instantly via detectPrompt regardless of either threshold. Both also govern the
// content-change fallback (aider/gemini): there "unchanged" is the same signal as "not
// working", so idleSettleTicks absorbs brief streaming pauses.
const (
	idleSettleTicks  = 2
	idleConfirmTicks = 6
)

// heartbeatTTL is how long a hook heartbeat (LastHeartbeat) is trusted to HOLD working
// after the last working edge fired (#311). It is deliberately short — not sized to span
// the longest legit tool call, because the animation-gated live spinner already covers a
// long silent tool (Claude's elapsed timer ticks throughout). It only needs to bridge the
// normal gap between consecutive tool events so an active turn reads Working even when both
// the below-box marker and the above-box spinner are absent/reworded; a hang then self-heals
// within roughly this window (heartbeat goes stale → the bounded grace commits idle).
const heartbeatTTL = 30 * time.Second

// heartbeatFresh reports whether rec's heartbeat is within heartbeatTTL of now. A zero
// timestamp (a Phase-1 bare-word file, or a record written before the field existed) is
// never fresh, so those degrade to the scrape fallback rather than falsely holding working.
func heartbeatFresh(rec hookRecord, now time.Time) bool {
	if rec.LastHeartbeat == 0 {
		return false
	}
	return now.Sub(time.Unix(rec.LastHeartbeat, 0)) < heartbeatTTL
}

// idleConfirmCap returns the working→idle safety cap for this session's agent:
// the adapter's IdleConfirmTicks when it sets one (> 0), otherwise the package
// default idleConfirmTicks. The override exists so a slow agent prone to long
// marker-absent gaps isn't reported falsely idle on a loaded host.
func (t *Session) idleConfirmCap() int {
	if t.adapter != nil && t.adapter.IdleConfirmTicks > 0 {
		return t.adapter.IdleConfirmTicks
	}
	return idleConfirmTicks
}

// hash hashes the string.
func (m *statusMonitor) hash(s string) []byte {
	h := sha256.New()
	// The []byte(s) conversion copies the (potentially several-KB) pane
	// content. io.WriteString does NOT avoid it: sha256's digest is not an
	// io.StringWriter, so it falls back to this same copy plus an extra alloc.
	// The only zero-copy option is unsafe.Slice(unsafe.StringData(s), len(s)),
	// not worth an unsafe import here — hash runs twice per session per 500ms
	// tick, behind tmux/git I/O that dwarfs the copy.
	h.Write([]byte(s))
	return h.Sum(nil)
}

// RuntimePermissionMode returns the permission mode last detected from the live
// pane footer ("" until the first confident detection, or for agents whose
// footer carries no mode indicator). Updated by Poll; read under monitorMu so it
// stays consistent with a concurrent poll.
func (t *Session) RuntimePermissionMode() string {
	t.monitorMu.Lock()
	defer t.monitorMu.Unlock()
	return t.monitor.mode
}

// RuntimeEffort returns the reasoning-effort level claude last reported through its hooks
// ("" until a resolved turn reports one, or for an agent without effort hooks). This is
// post-downgrade truth for the main session, so it outranks the --effort launch flag.
// Updated by Poll/PollNow; read under monitorMu so it stays consistent with a concurrent
// poll.
func (t *Session) RuntimeEffort() string {
	t.monitorMu.Lock()
	defer t.monitorMu.Unlock()
	return t.monitor.effort
}

// stashEffort lifts a hook record's effort onto the monitor. Sticky: an effort-less record
// (a session that hasn't run a tool yet, a model without effort support) leaves the last
// known level in place instead of blanking the chip. The record's own write rule already
// settled which turns may report an effort at all — by the time it lands on disk it is the
// main session's resolved level, so this is a plain lift, not a second gate.
//
// Caller must hold monitorMu (both call sites are inside Poll/PollNow, which hold it for
// their duration) — RuntimeEffort reads the field under that same lock.
func (t *Session) stashEffort(rec hookRecord) {
	if rec.Effort != "" {
		t.monitor.effort = rec.Effort
	}
}

// Poll classifies the current pane into a PaneState. It reads level signals (a prompt
// on screen, a busy marker, otherwise content stability) rather than treating any byte
// change as "working", which is what makes the result stable while the agent is idle.
func (t *Session) Poll() PaneState {
	// While the user is interactively attached, the live tmux client owns the
	// session: a capture-pane/has-session here would contend the shared socket,
	// and the detach's Restore swaps t.monitor out from under us. Skip before
	// taking the lock or spawning any subprocess. PaneUnknown is a no-op in
	// ApplyPaneState, and the post-detach attachFinishedMsg handler re-polls
	// fresh, so an in-flight tick loses nothing by skipping here.
	if t.attached.Load() {
		return PaneUnknown
	}
	// Serialize against a concurrent off-cadence poll from the UI (switch/detach) so the
	// two callers don't race on the monitor's hash/streak fields. The capture subprocess
	// runs under the lock, but it is brief and the lock is per-session.
	t.monitorMu.Lock()
	defer t.monitorMu.Unlock()
	// A dead/missing session can never be working; probing it would fail every tick
	// and flood the log. Report PaneDead (not PaneUnknown) so the metadata loop can
	// derive sessionLost from this single check and recover the instance to Paused,
	// without forking a second has-session of its own. An inconclusive probe (a
	// timeout kill or a fork/exec failure under load) is NOT a death — report
	// PaneUnknown so the strike counter never advances on transient failures (#270).
	switch t.liveness() {
	case sessionGone:
		return PaneDead
	case sessionIndeterminate:
		return PaneUnknown
	}
	raw, err := t.CapturePaneContent()
	if err != nil {
		// The session exists but capture failed transiently; throttle so a
		// persistent failure can't log hundreds of identical lines per second.
		if t.captureErrLog.ShouldLog() {
			log.ErrorLog.Printf("error capturing pane content in status monitor: %v", err)
		}
		return PaneUnknown
	}
	content := cleanForDetection(raw)
	name := t.snapshotName()

	// Live permission mode from the footer indicator. Sticky on an indeterminate
	// read so a busy/startup footer doesn't blank the chip; the Instance reads
	// t.monitor.mode via RuntimePermissionMode on the metadata tick.
	if mode, ok := t.adapter.DetectPermissionMode(content); ok {
		t.monitor.mode = mode
	}

	// Track content change. Used both by the no-marker fallback and by the settle check
	// below. Always update so the comparison is relative to the previous tick regardless of
	// which path decided the state.
	h := t.monitor.hash(content)
	changed := !bytes.Equal(h, t.monitor.prevOutputHash)
	t.monitor.prevOutputHash = h
	if changed {
		t.monitor.stableStreak = 0
	} else {
		t.monitor.stableStreak++
	}

	// A startup gate outranks every content state below: a trust/first-run screen has no
	// busy marker and matches no prompt matcher, so without this it would fall through to
	// idle and the row would lie as Ready while the session is actually blocked. GateUp
	// scans only the live dialog region (bottom chrome), like the prompt matchers, so a gate
	// literal quoted in the transcript body never wins over a genuinely-working pane. Setting
	// lastReported to PaneGate keeps the marker-absent grace below from reading the eventual
	// clear-out as a working→idle transition. We never dismiss the gate — a human must accept
	// it (or the trust_worktrees_root opt-in pre-accepts it), so ApplyPaneState maps this to
	// NeedsInput.
	if _, gated := t.adapter.GateUp(content); gated {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneGate
		t.monitor.logSignal(name, "gate → needs-input")
		return PaneGate
	}

	// A prompt awaiting an answer takes precedence over "working": when an agent stops to
	// ask, it is not processing, and this is the state a caller most needs to surface.
	// Matchers look only within the bottom chrome so the same strings in the scrolled-back
	// transcript (e.g. the agent discussing these UIs) don't false-trigger.
	if matcher, ok := t.adapter.DetectPrompt(content); ok {
		t.monitor.idleStreak = 0
		state := PanePrompt
		if matcher.NoAutoTap {
			state = PanePromptManual
		}
		t.monitor.lastReported = state
		t.monitor.logSignal(name, "prompt:"+matcher.Name+" → needs-input")
		return state
	}

	// A live busy marker is the strongest positive proof of work. Confining it to the adapter's
	// marker region keeps it reliable even under a multi-agent team selector. The marker is what
	// kills the #46 flicker: a stuck state file or an idle repaint can never flip the indicator
	// back to working once it has settled to idle. Two bounded signals below can also hold or
	// raise working without the marker — the animation-gated live spinner (#308) and a fresh
	// hook heartbeat (#311) — each guarded so it self-heals to idle instead of latching stuck.
	hasMarker := len(t.adapter.BusyMarkers) > 0 || t.adapter.LiveSpinner != nil
	if len(t.adapter.BusyMarkers) > 0 && t.markerWorking(content) {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "marker → working")
		return PaneWorking
	}

	if hasMarker {
		// The marker is absent. Read the structured hook record.
		//
		// The in-flight SET — not the working/ready latch — answers "is background work
		// pending". A non-empty set means sub-agents are still running, so the session is
		// busy regardless of whether the latch reads "ready" (the main turn ended and is
		// waiting on them) or "working" (a background sub-agent's OWN PreToolUse re-latched
		// it on the parent's file — confirmed to happen for run_in_background agents). Keying
		// off the latch alone mislabeled the latter as idle, because the busy children kept
		// the latch on "working" so the ready-gated pending check never fired (#290 follow-up).
		//
		// Trusting the set does NOT reintroduce the #46 oscillation the bare "working" latch
		// did: the set is bounded — SubagentStop drains it, the wall-clock watchdog clears a
		// stuck set after its cap, and tmux liveness recovers a dead pane — so an unmatched
		// start can never pin a row busy forever the way a stuck "working" file could.
		rec, haveRec := t.readHookRecord()
		if haveRec {
			// Lift the turn's effort onto the monitor before any classification returns below,
			// so the chip updates on the same tick that reads the record.
			t.stashEffort(rec)
		}

		// A live spinner status line above the box (2.1.207's footer reflow can crowd
		// "esc to interrupt" out of the below-box footer while the agent works — spinner.go).
		// It outranks the hook record like the esc-to-interrupt marker does: a spinning main
		// turn is Working, not Pending, even with sub-agents in flight. But the above-box band
		// is NOT structurally guaranteed live chrome (the transcript tail can quote the same
		// signature), so it carries two guards the below-box marker doesn't need:
		//   - Trust it only while the pane is ANIMATING (`changed`): a real spinner's per-second
		//     timer keeps the content changing, while a frozen scrollback match goes static and
		//     stops resetting idleStreak, self-healing to idle via the grace/cap below.
		//   - Never override a clean ready+empty hook — an authoritative turn-end that a stale
		//     spinner frame (or a scrollback quote on a finished pane) must not resurrect.
		cleanIdle := haveRec && rec.State == hookStateReady && len(rec.Inflight) == 0
		if changed && !cleanIdle && t.adapter.LiveSpinner != nil && t.adapter.LiveSpinner(content) {
			t.monitor.idleStreak = 0
			t.monitor.lastReported = PaneWorking
			t.monitor.logSignal(name, "spinner → working")
			return PaneWorking
		}

		if haveRec {
			if len(rec.Inflight) > 0 {
				t.monitor.idleStreak = 0
				t.monitor.lastReported = PanePending
				t.monitor.logSignal(name, "hook subagents in flight → pending")
				return PanePending
			}
			// Empty set. "ready" (a clean/errored turn-end) is a genuine idle; commit at once.
			// This is the sole "done" authority (Igor's rule: never inferred) and outranks the
			// heartbeat hold below, so an explicit turn-end always beats a heartbeat still fresh
			// from the PostToolUse just before Stop.
			if rec.State == hookStateReady {
				t.monitor.idleStreak = 0
				t.monitor.lastReported = PaneIdle
				t.monitor.logSignal(name, "hook ready → idle")
				return PaneIdle
			}
			// Version-independent corroborating freshness (#311). We are here with a record, an
			// empty in-flight set, and a non-"ready" latch (a working edge fired). A hook that
			// fired within heartbeatTTL proves the MAIN turn is live — read from Atrium's own
			// hook file, not scraped — so hold working even when the below-box marker is crowded
			// out AND the above-box spinner is absent/reworded. This is HOLD-ONLY and self-healing:
			//   - It never declares done/dead; ready+empty (above), tmux liveness (PaneDead, before
			//     the record is read), and the pending watchdog remain the only authorities on those.
			//   - A stale/zero heartbeat — a crashed writer, or a Phase-1 bare-word file — does
			//     nothing here and falls through to the bounded grace, so the #46 stuck-file guard
			//     (TestPollStuckWorkingFileDoesNotFlicker) still self-heals to idle.
			// Gated on the empty set (handled above) so a BACKGROUND sub-agent's own PreToolUse
			// bump can't mask Pending as Working (#290). No animation gate: unlike the scraped
			// spinner, the hook file can't be a stale scrollback quote, so freshness IS the heal.
			if heartbeatFresh(rec, time.Now()) {
				t.monitor.idleStreak = 0
				t.monitor.lastReported = PaneWorking
				t.monitor.logSignal(name, "hook heartbeat fresh → working")
				return PaneWorking
			}
		}
		t.monitor.idleStreak++
		if (t.monitor.lastReported == PaneWorking || t.monitor.lastReported == PanePending) &&
			t.monitor.idleStreak < t.idleConfirmCap() {
			// A brief marker-absent gap after real work (auto-accept turn boundary, model
			// spin-up) — or right after PanePending, when a session that was waiting on a
			// background sub-agent resumes: a working hook (UserPromptSubmit/PreToolUse) latches
			// "working" and the in-flight set drains a beat before the busy marker repaints, so
			// without holding here that sub-tick gap would commit PaneIdle → a false "done" (and
			// a false #289 "finished" ding) at every sub-agent resume. We only get here once the
			// hook is no longer "ready" (a working edge fired = the agent is doing something), so
			// holding working is honest. Still bounded by idleConfirmCap, so it can never relatch
			// working (#46) — once the cap is hit the absent marker keeps us idle.
			t.monitor.logSignal(name, "marker-absent grace → working")
			return PaneWorking
		}
		t.monitor.lastReported = PaneIdle
		t.monitor.logSignal(name, "marker-absent → idle")
		return PaneIdle
	}

	// No known marker for this program (aider, unknown agents): fall back to content-change detection
	// with the settle/cap hysteresis. A change reads as working; once the pane goes quiet it
	// commits idle after idleSettleTicks, or after the idleConfirmTicks cap if it keeps
	// churning without a marker we can model.
	if changed {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "content-change → working")
		return PaneWorking
	}
	if t.monitor.lastReported == PaneWorking {
		t.monitor.idleStreak++
		settled := t.monitor.stableStreak >= idleSettleTicks
		capped := t.monitor.idleStreak >= t.idleConfirmCap()
		if !settled && !capped {
			t.monitor.logSignal(name, "content-change → working (settling)")
			return PaneWorking
		}
	}
	t.monitor.lastReported = PaneIdle
	t.monitor.logSignal(name, "content-change → idle")
	return PaneIdle
}

// PollNow classifies the current pane at face value, skipping the working→idle hysteresis,
// and re-baselines the monitor to that result. It is for a one-shot refresh after the 500ms
// poll stream was interrupted — a detach, where the TUI handed the terminal to tmux and no
// ticks ran — so the accumulated smoothing state is stale and a single live snapshot is the
// most trustworthy signal. The resuming tick loop continues from the re-baselined state.
//
// Programs without a level marker (aider/gemini) can't be classified from one snapshot
// (their "working" signal is content change across ticks), so PollNow returns PaneUnknown
// for them — leaving the status untouched for the tick loop to resolve.
func (t *Session) PollNow() PaneState {
	t.monitorMu.Lock()
	defer t.monitorMu.Unlock()
	// As in Poll: only a definitive "gone" is PaneDead; an inconclusive probe stays
	// PaneUnknown so a transient failure doesn't re-baseline the monitor to dead.
	switch t.liveness() {
	case sessionGone:
		return PaneDead
	case sessionIndeterminate:
		return PaneUnknown
	}
	raw, err := t.CapturePaneContent()
	if err != nil {
		if t.captureErrLog.ShouldLog() {
			log.ErrorLog.Printf("error capturing pane content in status monitor: %v", err)
		}
		return PaneUnknown
	}
	content := cleanForDetection(raw)

	// Re-baseline the change tracker and streaks so the resuming tick loop compares against
	// this frame rather than a pre-attach one.
	t.monitor.prevOutputHash = t.monitor.hash(content)
	t.monitor.idleStreak = 0
	t.monitor.stableStreak = 0

	// Log via logSignal (transition-deduped, shared with Poll) so a detach that doesn't change
	// the state stays silent and only a real change emits one line.
	name := t.snapshotName()
	// A startup gate outranks the states below (see Poll for the full rationale): a
	// post-detach refresh must classify a trust/first-run screen as gated, not idle.
	if _, gated := t.adapter.GateUp(content); gated {
		t.monitor.lastReported = PaneGate
		t.monitor.logSignal(name, "gate → needs-input")
		return PaneGate
	}
	if matcher, ok := t.adapter.DetectPrompt(content); ok {
		state := PanePrompt
		if matcher.NoAutoTap {
			state = PanePromptManual
		}
		t.monitor.lastReported = state
		t.monitor.logSignal(name, "prompt:"+matcher.Name+" → needs-input")
		return state
	}
	// A present busy marker positively proves work; the hook state file is the next-best
	// authority (and is the only signal during a marker-absent between-turns gap).
	if t.markerWorking(content) {
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "marker → working")
		return PaneWorking
	}
	if rec, ok := t.readHookRecord(); ok {
		t.stashEffort(rec)
		switch {
		case len(rec.Inflight) > 0:
			// Background sub-agents in flight → pending, whatever the latch reads (see Poll).
			t.monitor.lastReported = PanePending
			t.monitor.logSignal(name, "refresh hook subagents in flight → pending")
			return PanePending
		case rec.State == hookStateWorking:
			t.monitor.lastReported = PaneWorking
			t.monitor.logSignal(name, "refresh hook working → working")
			return PaneWorking
		default:
			t.monitor.lastReported = PaneIdle
			t.monitor.logSignal(name, "hook ready → idle")
			return PaneIdle
		}
	}
	// No hook record. A live spinner above the box still proves work (the footer marker can
	// be crowded out on 2.1.207 — spinner.go); at face value it reads working. The resuming
	// tick loop applies the animation gate, so a one-shot scrollback match self-heals.
	if t.adapter.LiveSpinner != nil && t.adapter.LiveSpinner(content) {
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "refresh spinner → working")
		return PaneWorking
	}
	if len(t.adapter.BusyMarkers) == 0 && t.adapter.LiveSpinner == nil {
		// No level signal and no hook file; defer to the tick loop's content-change path.
		return PaneUnknown
	}
	// A marker-bearing agent with no hook file yet (e.g. before the first event): the
	// marker is absent here, so face value is idle.
	t.monitor.lastReported = PaneIdle
	return PaneIdle
}

// HasUpdated reports whether the agent is working and whether a prompt awaits an answer.
// It is a thin shim over Poll, kept for the daemon (which only consults hasPrompt) and
// for back-compat with existing callers.
func (t *Session) HasUpdated() (updated bool, hasPrompt bool) {
	s := t.Poll()
	return s == PaneWorking, s == PanePrompt
}
