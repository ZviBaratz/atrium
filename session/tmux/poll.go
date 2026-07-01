package tmux

import (
	"bytes"
	"crypto/sha256"
	"regexp"
	"strings"

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
	// PaneDead means the tmux session no longer exists. Distinct from PaneUnknown
	// (a transient read failure of a live session): the metadata loop flags only a
	// PaneDead session for lost-session recovery. Runtime-only, never persisted.
	PaneDead
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
	// without forking a second has-session of its own.
	if !t.DoesSessionExist() {
		return PaneDead
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

	// A live busy marker is the one positive proof of work, and the only signal that raises
	// working. Confining it to the adapter's marker region keeps it reliable even under a
	// multi-agent team selector. Raising only on the marker is what kills the
	// flicker: a stuck state file or an idle repaint can never flip the indicator back to
	// working once it has settled to idle — only the marker returning can.
	hasMarker := len(t.adapter.BusyMarkers) > 0
	if hasMarker && t.markerWorking(content) {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "marker → working")
		return PaneWorking
	}

	if hasMarker {
		// The marker is absent. The hook state file is authoritative for *idle*: a
		// clean turn-end (Stop) or an API-error turn-end (StopFailure) latches "ready", so we
		// commit idle at once. Any other value — still "working", or no file yet — is NOT
		// trusted to hold working (that latch caused the oscillation); instead the
		// marker-absent grace below holds working only briefly, gated on how long the marker
		// has actually been gone, then commits idle and stays there.
		if hookState, ok := t.readHookState(); ok && hookState == hookStateReady {
			t.monitor.idleStreak = 0
			t.monitor.lastReported = PaneIdle
			t.monitor.logSignal(name, "hook ready → idle")
			return PaneIdle
		}
		t.monitor.idleStreak++
		if t.monitor.lastReported == PaneWorking && t.monitor.idleStreak < t.idleConfirmCap() {
			// A brief marker-absent gap after real work (auto-accept turn boundary, model
			// spin-up). Hold working. idleStreak grows monotonically while the marker is
			// gone, so once it caps we commit idle and the absence of a marker keeps us there
			// — no churn-driven re-raise.
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
	if !t.DoesSessionExist() {
		return PaneDead
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
	if hookState, ok := t.readHookState(); ok {
		if hookState == hookStateWorking {
			t.monitor.lastReported = PaneWorking
			t.monitor.logSignal(name, "refresh hook working → working")
			return PaneWorking
		}
		t.monitor.lastReported = PaneIdle
		t.monitor.logSignal(name, "hook ready → idle")
		return PaneIdle
	}
	if len(t.adapter.BusyMarkers) == 0 {
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
