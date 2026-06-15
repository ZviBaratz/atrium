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
// windowing helpers in chrome.go. Two exceptions take the RAW capture instead:
// GateUp, and SuggestionVisible — the latter because SGR dim styling is its
// entire signal (see suggestion.go).
package agent

import "strings"

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
	for _, s := range m.Any {
		if strings.Contains(flat, s) {
			return true
		}
	}
	return false
}

// DismissKey is the keystroke that dismisses a startup gate.
type DismissKey int

const (
	// DismissEnter accepts the gate's pre-highlighted option (trust screens).
	DismissEnter DismissKey = iota
	// DismissDAndEnter sends 'D' then Enter (aider's "(D)on't ask again").
	DismissDAndEnter
)

// Gate is a one-time setup/trust screen that consumes keystrokes until
// dismissed, so a queued first prompt must not be typed while one is up.
type Gate struct {
	// Contains marks the gate as up when any entry is present in the raw pane.
	Contains []string
	// Dismiss is the keystroke that clears the gate.
	Dismiss DismissKey
}

// Adapter is the declarative profile of one agent CLI. The zero value of every
// optional field means "no support": nil BusyMarkers falls back to the poller's
// content-change hysteresis, no Prompts means prompts are never surfaced, no
// Gates means nothing is auto-dismissed, nil Resume relaunches without history.
type Adapter struct {
	Key         Key
	DisplayName string

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

// GateUp returns the startup gate currently showing in the raw pane content.
func (a *Adapter) GateUp(content string) (Gate, bool) {
	for _, g := range a.Gates {
		for _, s := range g.Contains {
			if strings.Contains(content, s) {
				return g, true
			}
		}
	}
	return Gate{}, false
}
