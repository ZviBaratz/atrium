// Package agent centralizes per-agent CLI knowledge as declarative data: which
// pane strings prove the agent is working, which mark a blocking prompt, which
// startup gates intercept keystrokes, and how a dead session is relaunched so it
// resumes its conversation. The tmux poller consumes an Adapter instead of
// branching on the program string, so supporting a new agent (or fixing a stale
// heuristic after a third-party TUI changes its wording) is a table edit plus a
// fixture test — never a change to the poll logic itself.
//
// The package is pure data and string matching: no tmux, no subprocesses, no IO.
// Pane capture, windowing (footer anchoring, chrome flattening), and capability
// probes stay in session/tmux; matchers receive already-windowed text.
package agent

import "strings"

// Key is the canonical short identifier of a supported agent CLI. It is stable
// across releases (unlike DisplayName) and safe to key UI glyphs or config on.
type Key string

const (
	KeyClaude  Key = "claude"
	KeyCodex   Key = "codex"
	KeyGemini  Key = "gemini"
	KeyAider   Key = "aider"
	KeyGeneric Key = "generic"
)

// Chrome window sizes used by prompt matchers, in non-empty pane lines counted
// from the bottom. They mirror the tmux poller's historical constants: a prompt
// block (question + options + footer, possibly with a todo tracker below) needs
// the tall window, while a key-hint footer that wraps across a couple of
// physical lines needs a tight one so prose higher in the chrome can't trip a
// token co-occurrence match.
const (
	WindowPrompt = 15
	WindowFooter = 3
)

// PromptMatcher recognizes one shape of blocking prompt in the flattened bottom
// chrome (newlines collapsed to spaces, so hard-wrapped footers and sentences
// survive narrow pane widths). A matcher fires when every string in All is
// present and, if Any is non-empty, at least one of Any is present too.
type PromptMatcher struct {
	// Name labels the matcher in status logs and tests.
	Name string
	// Window is how many non-empty bottom lines are flattened before matching.
	Window int
	// All must each be present in the flattened window.
	All []string
	// Any requires at least one entry present when non-empty.
	Any []string
}

func (m PromptMatcher) matches(flat string) bool {
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

	// Gates are the startup screens this agent can show.
	Gates []Gate

	// Resume rewrites the launch command so a relaunched session continues the
	// prior conversation. Used only on resurrection (the agent process died),
	// never on PTY reattach. nil means the agent has no resume support and the
	// relaunch starts blank.
	Resume func(program string) string
	// ResumeProbe, when non-empty, must appear in the agent binary's --help
	// output before Resume is applied — guarding against an older installed
	// binary that predates the flag. The probe itself runs in session/tmux.
	ResumeProbe string

	// HookSupport marks agents with an authoritative status-hook integration
	// (claude's injected --settings). The injection mechanics live in
	// session/tmux/hooks.go.
	HookSupport bool
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
// pane) shows a blocking prompt, returning the matcher's name for status
// logging. Each matcher flattens its own window, so a tight-footer matcher and
// a tall-dialog matcher coexist without the caller pre-windowing.
func (a *Adapter) DetectPrompt(content string) (string, bool) {
	for _, m := range a.Prompts {
		if m.matches(flattenChrome(content, m.Window)) {
			return m.Name, true
		}
	}
	return "", false
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
