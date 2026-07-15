// Package agent centralizes per-agent CLI knowledge as declarative data: which
// pane strings prove the agent is working, which mark a blocking prompt, which
// startup gates intercept keystrokes, and how a dead session is relaunched so it
// resumes its conversation. The tmux poller consumes an Adapter instead of
// branching on the program string, so supporting a new agent (or fixing a stale
// heuristic after a third-party TUI changes its wording) is a table edit plus a
// fixture test — never a change to the poll logic itself.
//
// The package is pure data and string matching: no tmux, no subprocesses, no IO.
// Pane capture and capability probes stay in session/tmux; matchers receive the
// cleaned full pane and confine themselves to its bottom chrome via the
// windowing helpers in chrome.go. One exception takes the RAW capture instead:
// SuggestionVisible, because SGR dim styling is its entire signal (see
// suggestion.go).
//
// GateUp is NOT an exception — every call site passes cleaned content (session/tmux
// poll.go and tmux.go), and it must: its Match implementations anchor on box-drawing
// borders to tell the live dialog from a transcript quote, and an ANSI-prefixed line
// matches no border predicate. Handing it a raw capture would silently stop every gate
// from being detected, which is the fail-dangerous direction (a queued prompt typed into
// a folder-trust screen).
package agent

import (
	"strings"
	"time"
)

// Key is the canonical short identifier of a supported agent CLI. It is stable
// across releases (unlike DisplayName) and safe to key UI glyphs or config on.
type Key string

// The canonical keys, one per registered adapter plus the unknown-agent fallback.
const (
	KeyClaude  Key = "claude"
	KeyCodex   Key = "codex"
	KeyGemini  Key = "gemini"
	KeyAider   Key = "aider"
	KeyGeneric Key = "generic"
)

// Granularity is the smallest semver component whose increase past an adapter's
// VerifiedVersion counts as drift. Patch is the zero value and the conservative
// default: any installed version above the verified ceiling drifts.
type Granularity int

const (
	// GranularityPatch treats any version above the ceiling as drift.
	GranularityPatch Granularity = iota
	// GranularityMinor ignores patch bumps; a minor-or-higher increase is drift.
	GranularityMinor
	// GranularityMajor ignores minor and patch bumps; only a major increase is drift.
	GranularityMajor
)

// WindowPrompt is the chrome window size used by prompt matchers, in non-empty
// pane lines counted from the bottom. It mirrors the tmux poller's historical
// constant: a prompt block (question + options + footer, possibly with a todo
// tracker below) fits within it, and the structural segment scan
// (footerVisibleInSegments) uses it as its depth budget.
const WindowPrompt = 15

// PromptMatcher recognizes one shape of blocking prompt in the flattened bottom
// chrome (newlines collapsed to spaces, so hard-wrapped footers and sentences
// survive narrow pane widths). A matcher fires when every string in All is
// present and, if Any is non-empty, at least one of Any is present too.
//
// Match is the escape hatch for prompt shapes that a flat windowed substring
// match cannot express — e.g. claude's selection footer, which a custom
// multi-line statusLine can displace out of any fixed bottom-N window, so it
// needs the rule-delimited segment scan instead. When Match is set it receives
// the cleaned full pane and the declarative fields are ignored.
type PromptMatcher struct {
	// Name labels the matcher in status logs and tests.
	Name string
	// Window is how many non-empty bottom lines are flattened before matching.
	Window int
	// All must each be present in the flattened window.
	All []string
	// Any requires at least one entry present when non-empty.
	Any []string
	// Match, when set, replaces the All/Any/Window match entirely.
	Match func(content string) bool
	// NoAutoTap marks a prompt whose auto-answer is destructive (e.g. claude's
	// plan approval, where Enter accepts the plan AND enables auto-accept).
	// Autoyes must surface it as needs-input instead of tapping Enter.
	NoAutoTap bool
}

func (m PromptMatcher) matches(content string) bool {
	if m.Match != nil {
		return m.Match(content)
	}
	flat := flattenChrome(content, m.Window)
	for _, s := range m.All {
		if !strings.Contains(flat, s) {
			return false
		}
	}
	if len(m.Any) == 0 {
		return true
	}
	return containsAny(flat, m.Any)
}

// Gate is a one-time setup/trust screen that consumes keystrokes until a human
// dismisses it, so a queued first prompt must not be typed while one is up. Atrium
// never auto-dismisses a gate (surfacing it as needs-input is safer than blindly
// accepting a folder-trust or new-MCP screen); GateUp is a detection-only signal.
type Gate struct {
	// Contains marks the gate as up when any entry is present in the region GateUp
	// narrowed to — the flattened bottom WindowPrompt lines — not anywhere in the pane.
	// That window is a budget, not a liveness proof (see GateUp): a gate whose literals
	// also appear in the transcript needs Match instead.
	Contains []string
	// Match, when set, replaces the Contains scan entirely — the escape hatch for a
	// gate whose literals a flat bottom-N window cannot separate from the same words
	// quoted in the transcript (claude's; see claudeGateVisible). It receives the
	// cleaned full pane and does its own windowing, mirroring PromptMatcher.Match.
	Match func(content string) bool
}

// matches reports whether this gate is showing in flat, a region already narrowed and
// flattened by the caller. Any entry in Contains is sufficient (a pane shows one shape).
func (g Gate) matches(flat string) bool {
	return containsAny(flat, g.Contains)
}

// containsAny reports whether flat holds any of lits. Shared by Gate.matches and the
// adapters' own Match implementations, so a gate's literals are matched the same way
// whichever region the caller narrowed to.
func containsAny(flat string, lits []string) bool {
	for _, s := range lits {
		if strings.Contains(flat, s) {
			return true
		}
	}
	return false
}

// Adapter is the declarative profile of one agent CLI. The zero value of every
// optional field means "no support": nil BusyMarkers falls back to the poller's
// content-change hysteresis, no Prompts means prompts are never surfaced, no
// Gates means no startup screen is detected, nil Resume relaunches without history.
type Adapter struct {
	Key         Key
	DisplayName string

	// VerifiedVersion is the highest CLI version whose heuristic strings have
	// been confirmed against a live pane — a ceiling, not a frozen pin. An
	// installed version above it is unverified territory and triggers a drift
	// warning. Bump it (after re-checking) whenever a matcher string is edited,
	// and on a plain re-verification of a newer release. Empty = unversioned
	// (codex/aider): shown in `atrium doctor`, never triggers a hint.
	VerifiedVersion string
	// DriftGranularity is the smallest semver component whose increase past
	// VerifiedVersion counts as drift. Zero value (GranularityPatch) is the
	// conservative default.
	DriftGranularity Granularity

	// aliases are lowercased substrings matched against the basename of the
	// program's first token by Resolve.
	aliases []string

	// BusyMarkers are substrings whose presence in the marker region proves the
	// agent is actively working. A level signal: it stays on screen for the whole
	// turn, so presence — not content change — decides the state. The failure
	// mode of a stale marker is a visible "always idle", never flicker.
	BusyMarkers []string
	// MarkerWindow selects where BusyMarkers are searched. 0 anchors to the
	// footer below the input box's bottom border (claude renders its status
	// hints there, below a variable-height team selector). N > 0 searches the
	// last N non-empty lines instead — codex and gemini render their status row
	// *above* the input box, where the footer anchor never looks.
	MarkerWindow int

	// LiveSpinner is a structural busy signal complementing BusyMarkers: a func that
	// reports whether the pane shows an animating status spinner. It exists for a marker
	// that no fixed substring can express and that renders *above* the input box (outside
	// footerRegion) — claude's "<glyph> <Gerund>… (<elapsed> · …)" status line, whose
	// glyph and gerund vary. Unlike a below-box substring marker it is NOT structurally
	// guaranteed to be live chrome (the transcript tail can quote the same signature), so
	// the poller trusts it only while the pane is animating, never as a standalone latch.
	// nil for agents without such a spinner. Receives the full cleaned content (it does
	// its own windowing). See session/agent/spinner.go.
	LiveSpinner func(content string) bool

	// IdleConfirmTicks overrides the poller's working→idle safety cap for this
	// agent: the number of consecutive marker-absent (or, for markerless agents,
	// unchanged-while-churning) poll ticks after which the pane is committed to
	// idle even if it keeps moving. 0 means "use the package default" (session/
	// tmux's idleConfirmTicks). Raise it for an agent prone to long between-turns
	// gaps on a slow/loaded host, where the default cap can report a false idle.
	IdleConfirmTicks int

	// PendingWatchdog overrides the wall-clock cap a session may sit "pending" (main
	// turn ended, but sub-agents still recorded in flight) before the poller
	// force-reconciles it to done even though the pane is alive — the alive-but-stuck
	// backstop for a SubagentStop that never fired (#290). 0 means "use the package
	// default" (session.defaultPendingWatchdog). Generous by design: a background
	// sub-agent legitimately runs long, and tmux liveness carries the common failure.
	PendingWatchdog time.Duration

	// Prompts are tried in order; the first match classifies the pane as a
	// blocking prompt.
	Prompts []PromptMatcher

	// SuggestionVisible reports whether the agent's idle input box is showing
	// a ghost-text prompt suggestion that a Right keypress would accept.
	// Unlike every other matcher it receives the RAW capture (ANSI intact):
	// the SGR dim attribute is the only signal distinguishing a suggestion
	// from user-typed draft text, which Enter must never submit. nil means
	// the agent has no suggestion UI, and spares its panes the capture
	// entirely (session/tmux's AcceptSuggestion gates on it).
	SuggestionVisible func(raw string) bool

	// PasteCollapsed reports whether the input-box readback is the agent's collapsed-paste
	// placeholder chip rather than literal text: claude renders a ≥4-line bracketed paste as
	// "[Pasted text #N +L lines]" instead of the pasted content. Queued-prompt delivery treats
	// such a chip as the prompt having landed, since the chip carries no prompt text to match
	// against the first-line signature. nil means the agent renders pastes inline, so the
	// signature check alone suffices (codex/gemini/aider).
	PasteCollapsed func(boxText string) bool

	// PermissionMode reports the agent's current permission mode from the live
	// pane footer's mode indicator (mode "" / known false = indeterminate, keep
	// last value). It exists because the mode is runtime-mutable — a session
	// launched in one mode can be cycled to another — so the launch-time
	// --permission-mode flag goes stale. nil for agents that don't surface a
	// mode in their footer; claude wires claudePermissionMode.
	PermissionMode func(content string) (mode string, known bool)

	// Gates are the startup screens this agent can show.
	Gates []Gate

	// Resume rewrites the launch command so a relaunched session continues the
	// prior conversation. Used only on resurrection (the agent process died),
	// never on PTY reattach. nil means the agent has no resume support and the
	// relaunch starts blank.
	Resume func(program string) string
	// ResumeProbe, when non-empty, must appear in the agent binary's --help
	// output before Resume is applied — guarding against an older installed
	// binary that predates the flag. The probe itself runs in session/tmux,
	// against the configured program when it is the canonical binary itself
	// (even at an absolute path), else against the canonical name — never
	// against a wrapper, whose side effects must not run on a probe.
	ResumeProbe string

	// HookSupport marks agents with an authoritative status-hook integration
	// (claude's injected --settings). The injection mechanics live in
	// session/tmux/hooks.go.
	HookSupport bool

	// HeadlessNamer marks agents whose CLI supports a one-shot headless prompt
	// suitable for auto-naming sessions. Only the capability and its preference
	// order (registry order) live in the table: the invocation mechanics differ
	// per agent (claude prints a JSON envelope, gemini bare text), so each true
	// entry must have a matching branch in session/naming.go.
	HeadlessNamer bool
}

// NamerKeys returns the keys of the agents that support headless auto-naming,
// in registry (preference) order. session/naming.go consumes this to build its
// fallback chain instead of hardcoding the capable set.
func NamerKeys() []Key {
	var keys []Key
	for _, a := range registry {
		if a.HeadlessNamer {
			keys = append(keys, a.Key)
		}
	}
	return keys
}

// Adapters returns the recognized agent adapters (excludes the Generic
// fallback). The doctor package probes these for version drift.
func Adapters() []*Adapter {
	return registry
}

// HasBusyMarker reports whether a busy marker is present in the live marker
// region of content (the cleaned full pane). The region is confined per
// MarkerWindow so the same strings in the scrolled-back transcript don't count.
func (a *Adapter) HasBusyMarker(content string) bool {
	if len(a.BusyMarkers) == 0 {
		return false
	}
	region := footerRegion(content)
	if a.MarkerWindow > 0 {
		region = liveChromeLines(content, a.MarkerWindow)
	}
	for _, m := range a.BusyMarkers {
		if strings.Contains(region, m) {
			return true
		}
	}
	return false
}

// DetectPrompt reports whether the bottom chrome of content (the cleaned full
// pane) shows a blocking prompt, returning the matched matcher so callers can
// read its Name (status logging) and NoAutoTap (autoyes guard). Each matcher
// windows the pane itself (its own flattened window, or a structural scan via
// Match), so differently shaped matchers coexist without the caller
// pre-windowing.
func (a *Adapter) DetectPrompt(content string) (PromptMatcher, bool) {
	for _, m := range a.Prompts {
		if m.matches(content) {
			return m, true
		}
	}
	return PromptMatcher{}, false
}

// DetectPermissionMode reports the agent's current permission mode from the
// cleaned full pane, when the adapter has a detector (claude only). known=false
// means indeterminate or unsupported — the caller keeps its last known value.
func (a *Adapter) DetectPermissionMode(content string) (mode string, known bool) {
	if a.PermissionMode == nil {
		return "", false
	}
	return a.PermissionMode(content)
}

// GateUp returns the startup gate currently showing in the live dialog region of the
// cleaned pane. A gate with a Match runs it against the whole pane (it does its own
// windowing); otherwise the Contains entries are matched against the bottom WindowPrompt
// non-empty lines, flattened so a title wrapped across physical lines at a narrow pane
// width still reconstructs.
//
// That flat window is NOT by itself a liveness test, and this comment used to claim it was:
// the live chrome below the composer is only a few lines, so the window always also holds
// the tail of the transcript, and an agent that merely DISCUSSES a gate ("a session editing
// this registry, or discussing a 'New MCP server'") matched it. Only the adapters whose
// dialog shapes are pinned to captured panes can do better, via Match — see
// claudeGateVisible. The rest keep the flat window, whose failure is a false positive
// (needs-input on a working session), never a missed gate.
//
// The input must already be cleaned for detection (ANSI stripped; see tmux's
// cleanForDetection) — Match implementations anchor on box-drawing lines, which an
// ANSI-prefixed line would not match.
func (a *Adapter) GateUp(content string) (Gate, bool) {
	if len(a.Gates) == 0 {
		return Gate{}, false
	}
	// Flattened lazily: claude's gate is all Match, and this runs on every poll tick of
	// every session, so the windowing is only worth doing for an adapter that needs it.
	var flat string
	var flattened bool
	for _, g := range a.Gates {
		if g.Match != nil {
			if g.Match(content) {
				return g, true
			}
			continue
		}
		if !flattened {
			flat, flattened = flattenChrome(content, WindowPrompt), true
		}
		if g.matches(flat) {
			return g, true
		}
	}
	return Gate{}, false
}

// InputBoxText returns the text entered in the agent's live input box and whether a
// box is on screen, reading the cleaned full pane. found=false means no composer is
// rendered (a startup frame before the box, or an overlay covering it); found=true with
// an empty string means the box is present but blank. It is the positive readback used to
// confirm a queued prompt actually landed in the composer before it is submitted.
func (a *Adapter) InputBoxText(content string) (string, bool) {
	return inputBoxText(content)
}

// InputBoxVisible reports whether the agent's live input box is on screen in the cleaned
// full pane. It is the positive half of the prompt-delivery readiness check: a pre-box boot
// frame or an overlay that has replaced the composer has no box, so keystrokes would be
// lost. It does not by itself tell a composer from a menu-style gate — claude's trust/new-MCP
// screens render a "❯ 1. …" selector that also reads as a box line — so the caller must pair
// it with GateUp and DetectPrompt to exclude the screens that consume keystrokes.
func (a *Adapter) InputBoxVisible(content string) bool {
	_, ok := inputBoxText(content)
	return ok
}
