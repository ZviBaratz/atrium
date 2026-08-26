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
	KeyAgy     Key = "agy"
	KeyCopilot Key = "copilot"
	KeyGeneric Key = "generic"
)

// VerifiedGate is one remote feature gate and the value an adapter's heuristics
// were verified under. Name is the gate's key as the CLI itself resolves it.
type VerifiedGate struct {
	Name  string
	Value bool
}

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
	// Name identifies which gate this is, the way PromptMatcher.Name does — required on
	// every Gate, and the reason it exists is coverage rather than logic.
	//
	// Nothing in detection reads it: GateUp returns the Gate itself, and a caller wanting
	// to know which one fired can compare that. What could not be done without it is
	// saying which gate a piece of EVIDENCE covers. paneCoverage keys on
	// "<adapter>/gate/<name>", so an adapter with two gates has two keys, and a ladder
	// proving one of them cannot silently stand in for the other. Until #717 every adapter
	// declared exactly one gate and the key was "<adapter>/gate"; pane_width_test.go
	// carried a require.Len(a.Gates, 1) whose failure message was an instruction to add
	// this field before adding a second gate.
	Name string
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
// The convention holds because for each of those "no support" is a graceful
// degradation. InputBoxPrompts is the one exception — nil there means the package
// default, not the empty set, and its own doc says why.
type Adapter struct {
	Key         Key
	DisplayName string

	// VerifiedVersion is the highest CLI version whose heuristic strings have
	// been confirmed against a live pane — a ceiling, not a frozen pin. An
	// installed version above it is unverified territory and triggers a drift
	// warning. Bump it (after re-checking) whenever a matcher string is edited,
	// and on a plain re-verification of a newer release. Empty = unversioned:
	// shown in `atrium doctor`, never triggers a hint. Every registry adapter is
	// pinned; only the Generic fallback, which has no matchers, is unversioned.
	VerifiedVersion string
	// DriftGranularity is the smallest semver component whose increase past
	// VerifiedVersion counts as drift. Zero value (GranularityPatch) is the
	// conservative default.
	DriftGranularity Granularity
	// VerifiedGates pins the remote feature-gate values the heuristics were
	// verified under — a sibling to VerifiedVersion, not a substitute.
	// VerifiedVersion says WHICH build was driven; VerifiedGates says WHICH BRANCH
	// of that build rendered. Claude ships two footer implementations in one binary
	// and picks between them on a server-resolved gate, so a flip changes the UI
	// with no version change: the one drift no version pin can express, even in
	// principle. Values are bool because that is all a pin needs — the resolved map
	// also holds numbers and objects, and a non-bool reads as unknown, never as a
	// flip. Nil (every adapter but claude) = no gates pinned, nothing reported.
	VerifiedGates []VerifiedGate
	// VersionMarker is a substring the binary's `--version` output must contain for that
	// binary to be this agent's CLI. Empty (every adapter but copilot) means the name is not
	// contested and the probe is skipped.
	//
	// It exists because a binary NAME is not an identity. "copilot" is also the AWS Copilot
	// CLI's binary name, and detection is an exec.LookPath — so on a machine with that one
	// installed, Atrium seeds a profile whose program prints deployment help and exits, and
	// Resolve hands it this adapter's folder-trust gate and busy marker. The discriminator was
	// already driven and written into the spec ("GitHub Copilot CLI 1.0.80"); this is what
	// reads it. config.DetectAgentProfiles skips a binary that fails the check, and
	// doctor.Check reports it as a foreign CLI rather than as this agent having drifted.
	//
	// A MARKER, not a version parse: the question is whose CLI this is, which is answered by
	// the vendor string and not by the digits. It is deliberately not consulted by
	// agent.Resolve, which is a pure function over a program string and cannot exec anything —
	// so a hand-written profile pointing at the wrong copilot still resolves here, and what
	// tells the user is doctor.
	VersionMarker string

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

	// BackgroundWork reports whether the pane shows work the ENDED turn left running —
	// claude's "N shells"/"N monitors" footer chips. It answers a different question from
	// BusyMarkers and LiveSpinner, which both ask whether the turn itself is still going:
	// a background Bash or Monitor is not a sub-agent, so it never reaches the hook
	// record's in-flight set, and without this the poller reads the turn's end as done.
	// Consulted only once the busy signals have come up empty, so a running turn is still
	// Working rather than Pending. nil for agents with no such concept. Receives the full
	// cleaned content (it does its own windowing). See session/agent/background.go.
	BackgroundWork func(content string) bool

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
	// default" (session.DefaultPendingWatchdog). Generous by design: a background
	// sub-agent legitimately runs long, and tmux liveness carries the common failure.
	// Outranked by the user's own pending_watchdog_minutes when they set one (#799).
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

	// InputBoxPrompts REPLACES the default glyphs that open this agent's composer
	// interior line ("❯" for claude, the plain ASCII ">" aider and agy draw). Codex
	// draws "›" (U+203A). Not to be confused with Prompts above: those are
	// blocking-dialog matchers, this is the composer's own glyph.
	//
	// This is the one optional field whose zero value does not mean "no support". Nil
	// means "the package default" (defaultPrompts), because an empty set does not
	// degrade a feature, it kills one: InputBoxVisible goes permanently false, so
	// AwaitingInput does too, and a queued prompt is then neither delivered nor expired
	// (#510). The convention the rest of this struct follows is justified by its failure
	// mode — nil BusyMarkers falls back to content-change hysteresis, nil Prompts merely
	// leaves prompts unsurfaced — and that justification does not extend to a field whose
	// empty value silently breaks prompt delivery outright. Generic is a bare &Adapter{}
	// and is deliberately NOT in registry, so no table test over Adapters() can catch a
	// missing entry either; the default has to live in the resolver.
	//
	// It REPLACES rather than extends so that an agent with its own glyph fails CLOSED on
	// the others'. Codex proves the point twice: its banner line "│ >_ OpenAI Codex" and
	// its "> You are in <dir>" header both read as a composer under the default set.
	InputBoxPrompts []string

	// ModalVeto reports that the pane's bottom chrome is a MODAL the agent drew, not its
	// live composer, even though a composer glyph is on screen. When it fires,
	// InputBoxVisible is false and prompt delivery is held.
	//
	// It exists because the composer glyph is not always the composer's. Several agents draw
	// their dialog's selected row with the same glyph, which InputBoxVisible's own doc records
	// as unfixable ON THAT LINE'S SHAPE — and the pairing it prescribes instead (GateUp and
	// DetectPrompt) is a LITERAL match, so it holds only while the dialog's literals are on
	// screen. A pane shorter than the dialog scrolls the headline off while leaving the
	// selector, the option rows and the whole nav footer visible: the literals go, the glyph
	// stays, and AwaitingInput (!GateUp && !DetectPrompt && InputBoxVisible) goes TRUE on a
	// modal. A queued prompt is then typed into it and Enter selects the highlighted option.
	// Copilot's is "Yes, and add these directories to the allowed list", so the convenience
	// feature widens the agent's filesystem reach. NoAutoTap cannot help: it is consulted
	// only once DetectPrompt has already fired.
	//
	// So this is deliberately STRUCTURAL, never a literal: it must hold at a pane height where
	// no literal is left to read. copilotModalUp is the worked example — an anchored box whose
	// bottom border all but ends the pane, which copilot's borderless composer never is.
	// TestCopilotNeverDeliversAPromptIntoADialog measures the composed triple at every pane
	// height of every driven rung; TestCopilotBusyPanesStayDeliverable holds the other
	// direction, that the veto has not simply killed delivery.
	//
	// nil for every other adapter, and that is NOT a finding that the others are safe — it is
	// that nobody has driven the height axis for them. Their ladders are width ladders, so the
	// question has not been asked. The class is already documented here for one of them:
	// gemini's IdeIntegrationNudge is a screen where DetectPrompt and GateUp both measured
	// false while InputBoxVisible measured true, and what closed it was giving that dialog its
	// own Gate (#717) — a literal, which is the remedy this field exists to complement rather
	// than replace.
	//
	// It is NOT a substitute for Gates/Prompts either. A vetoed pane reports no needs-input, so
	// the prompt is held rather than surfaced — fail-closed, which is the direction GateUp's own
	// doc records as acceptable, and strictly better than delivery onto a dialog. Surfacing it
	// properly would need a state the poller does not have, and no driven capture of a
	// short-pane dialog to test it against.
	ModalVeto func(content string) bool

	// Gates are the startup screens this agent can show.
	Gates []Gate
	// GateWindow overrides how many non-empty lines from the bottom GateUp flattens
	// before matching a Gate's Contains literals. 0 means WindowPrompt, the same budget
	// the prompt matchers use.
	//
	// It exists because that budget is a LINE count and a wrapping TUI spends lines to
	// say the same thing: codex wraps its trust-gate body rather than truncating it, so
	// at 20 columns the dialog occupies 18 non-empty lines and its headline — the only
	// thing the gate is keyed on — is pushed out of a 15-line window while still plainly
	// on screen. Measured, not inferred, and the two halves are pinned by two tests:
	// TestCodexTrustGateHeadlineFallsOutsideTheDefaultWindowAtWidth20 asserts GateUp is
	// FALSE on the live width-20 capture under the default budget, and
	// TestCodexTrustGateDetectedAtEveryDrivenWidth asserts it is true at every rung with
	// this one.
	//
	// That miss is what makes this field load-bearing rather than cosmetic. GateUp is one
	// of the two guards standing between a queued prompt and codex's trust gate, whose
	// selector shares the composer's "›" glyph — and the box check cannot be the other
	// one, because a numbered-list prompt in the composer is the same shape as the menu
	// (see promptSet). A wider window trades a larger surface for a gate literal quoted
	// in the transcript, which fails CLOSED (a prompt is held, never mis-delivered) and
	// is the failure mode GateUp already documents as acceptable.
	GateWindow int

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
	//
	// It is a CAPABILITY check and only that. Grepping --help answers "does this
	// build support the flag" and can say nothing about whether a conversation
	// exists to resume: it passes in an empty directory exactly as it does in a
	// populated one. Existence is a separate question, asked in
	// Instance.startResuming through transcript.HasResumable, where only claude has
	// an adapter — so for every other agent the flag is applied with nothing having
	// looked. What each vendor does with it is driven and recorded beside that
	// agent's Resume, and repaired generically by Instance.RepairResumingLaunch (#712).
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

// BackgroundWorkVisible reports whether content (the cleaned full pane) shows work left
// running by a turn that has already ended. Adapters that declare no BackgroundWork report
// false, so an agent with no such concept degrades to the prior behaviour rather than
// needing an entry.
func (a *Adapter) BackgroundWorkVisible(content string) bool {
	if a.BackgroundWork == nil {
		return false
	}
	return a.BackgroundWork(content)
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
			flat, flattened = flattenChrome(content, a.gateWindow()), true
		}
		if g.matches(flat) {
			return g, true
		}
	}
	return Gate{}, false
}

// inputBoxPrompts resolves this adapter's composer glyph set. It never returns an empty
// set — see InputBoxPrompts for why nil must mean the default rather than "none".
func (a *Adapter) inputBoxPrompts() promptSet {
	if len(a.InputBoxPrompts) == 0 {
		return defaultPrompts
	}
	return a.InputBoxPrompts
}

// gateWindow resolves how many non-empty bottom lines GateUp flattens — see GateWindow.
func (a *Adapter) gateWindow() int {
	if a.GateWindow <= 0 {
		return WindowPrompt
	}
	return a.GateWindow
}

// InputBoxText returns the text entered in the agent's live input box and whether a
// box is on screen, reading the cleaned full pane. found=false means no composer is
// rendered (a startup frame before the box, or an overlay covering it); found=true with
// an empty string means the box is present but blank. It is the positive readback used to
// confirm a queued prompt actually landed in the composer before it is submitted. The
// agent's own prompt glyph is stripped, since the signature it is compared against does
// not carry one.
func (a *Adapter) InputBoxText(content string) (string, bool) {
	return inputBoxText(content, a.inputBoxPrompts())
}

// InputBoxVisible reports whether the agent's live input box is on screen in the cleaned
// full pane. It is the positive half of the prompt-delivery readiness check: a pre-box boot
// frame or an overlay that has replaced the composer has no box, so keystrokes would be
// lost.
//
// It does not by itself tell a composer from a screen that consumes keystrokes, and cannot
// be made to. A menu-style gate drawn with the agent's own prompt glyph reads as a box line
// (claude's "❯ 1. …" trust/new-MCP screens, agy's "> 1. Yes", codex's "› 1. Yes, continue"),
// and no rule on that line's shape can reject it without also rejecting a queued prompt that
// happens to be a numbered list — measured against live codex panes, see promptSet. An agent
// which echoes the user's own messages into its transcript with the composer glyph — codex
// does — additionally presents a box line on a frame whose composer an overlay has replaced.
// So the caller must pair this with GateUp and DetectPrompt, which is what excludes those
// screens; on codex's trust gate at narrow widths that pairing is what GateWindow protects.
//
// That pairing is a LITERAL match, so it covers only the pane heights where the dialog's
// literals are still on screen. ModalVeto is the structural half for an agent whose dialog
// outgrows the pane and takes its headline with it; see that field.
func (a *Adapter) InputBoxVisible(content string) bool {
	if a.ModalVeto != nil && a.ModalVeto(content) {
		return false
	}
	_, ok := inputBoxText(content, a.inputBoxPrompts())
	return ok
}
