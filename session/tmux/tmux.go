// Package tmux wraps a real tmux server on Atrium's dedicated socket. Each
// session runs its agent program in a pty; Poll captures pane content and
// classifies it into a PaneState (unknown, working, prompt, idle). All tmux
// subprocesses go through cmd.Executor so tests can fake them.
package tmux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
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
)

// markerWorking reports whether this session's agent shows its busy marker in the live
// marker region of content. The match is confined per the adapter's MarkerWindow (the
// footer below the input box for claude, a bottom window for agents whose status row
// renders above it) rather than the whole pane, which would also match the scrolled-back
// transcript. Returns false for programs without a known marker.
func (t *Session) markerWorking(content string) bool {
	return t.adapter.HasBusyMarker(content)
}

// DetachReason explains why an Attach loop ended so the caller (app.go's
// attachLoop) can decide whether to return to the list or re-attach a sibling. It
// is set by the stdin interceptor just before Detach and read via AttachExitReason
// after the attach channel closes — the close provides the happens-before, and the
// write+close happen on the same goroutine, so no extra synchronization is needed.
type DetachReason int

const (
	// DetachQuit is the default: a normal Ctrl+Q detach (or any non-nav exit).
	DetachQuit DetachReason = iota
	// DetachNext requests cycling to the next sibling session in the repo group.
	DetachNext
	// DetachPrev requests cycling to the previous sibling session in the repo group.
	DetachPrev
)

// navReason maps a raw stdin chunk to a sibling-navigation detach reason. The keys
// are Ctrl+PageUp (previous) and Ctrl+PageDown (next); their standard xterm
// encodings are matched exactly so pasted content can't trigger an accidental jump.
// Terminals that emit different sequences for these chords simply won't navigate —
// that is the documented terminal-dependency caveat; log buf[:nr] to discover the
// actual bytes before changing this.
func navReason(b []byte) (DetachReason, bool) {
	switch string(b) {
	case "\x1b[5;5~": // Ctrl+PageUp
		return DetachPrev, true
	case "\x1b[6;5~": // Ctrl+PageDown
		return DetachNext, true
	}
	return DetachQuit, false
}

// Session represents a managed tmux session
type Session struct {
	// baseCtx is the lifecycle context every tmux subprocess derives from: short
	// operations cap it with tmuxOpTimeout (opContext), long-lived pty clients
	// (new-session, attach-session) use it bare so app shutdown — not a timeout —
	// tears them down. Set once at construction, before any background goroutine
	// can reach this session; nil means Background. Distinct from ctx below,
	// which is attach-scoped and nil'd on detach.
	baseCtx context.Context
	// mu guards sanitizedName/windowName against a deep Rename, which mutates them while
	// the metadata poll loop reads sanitizedName from a background goroutine. Rename holds
	// the write lock across its rename-session subprocess and the field swap, so a reader
	// never observes the brief window where the old session name no longer exists.
	// It also guards the paneID/paneIDTried cache below.
	mu sync.RWMutex
	// paneID caches the agent pane's immutable tmux id (%N) so pane reads
	// (capture-pane) and keystroke writes (send-keys) target the agent's pane,
	// never whatever pane happens to be active. Empty after a failed resolution
	// — paneTarget then falls back to the session name. paneIDTried makes
	// resolution once-per-generation; both are reset by resetPaneID where the
	// session is created or killed.
	paneID      string
	paneIDTried bool
	// Initialized by NewSession
	//
	// The name of the tmux session and the sanitized name used for tmux commands.
	sanitizedName string
	// windowName is the original, human-readable name, used as the tmux window
	// name (-n) so windows aren't shown under the sanitized session name or
	// auto-renamed to the running program.
	windowName string
	program    string
	// configDir, when non-empty, is injected into the session's environment as
	// CLAUDE_CONFIG_DIR via `new-session -e` at launch, selecting which Claude
	// Code account the agent runs under. Empty = inherit the inherited env. Set
	// once before Start (SetClaudeConfigDir); like program it is fixed for the
	// life of the tmux session, since the env can only be set at session birth.
	configDir string
	// adapter holds the per-agent heuristics resolved once from program at
	// construction; never nil (unknown programs get agent.Generic).
	adapter *agent.Adapter
	// ptyFactory is used to create a PTY for the tmux session.
	ptyFactory PtyFactory
	// cmdExec is used to execute commands in the tmux session.
	cmdExec cmd.Executor
	// captureErrLog throttles capture-pane error logging so a persistent failure
	// can't flood the log with hundreds of identical lines per second.
	captureErrLog *log.Every

	// Initialized by Start or Restore
	//
	// ptmx is a PTY is running the tmux attach command. This can be resized to change the
	// stdout dimensions of the tmux pane. On detach, we close it and set a new one.
	// This should never be nil.
	ptmx *os.File
	// monitor monitors the tmux pane content and sends signals to the UI when it's status changes
	monitor *statusMonitor
	// monitorMu serializes Poll. The metadata tick polls each session once per cycle, but
	// the UI also polls the selected session off-cadence on switch/detach; without this
	// lock those two callers would race on the monitor's hash/streak fields.
	monitorMu sync.Mutex

	// Initialized by Attach
	// Deinitilaized by Detach
	//
	// Channel to be closed at the very end of detaching. Used to signal callers.
	attachCh chan struct{}
	// detachReason records why the current attach ended (normal Ctrl+Q vs a
	// sibling-navigation request). Reset at Attach, set by the stdin interceptor
	// before Detach, read via AttachExitReason once attachCh has closed.
	detachReason DetachReason
	// detachErr records any error encountered while tearing down the current
	// attach (a failed pty close or a failed Restore). Reset at Attach, written by
	// Detach before attachCh is closed, read via AttachExitError once attachCh has
	// closed — sharing detachReason's happens-before edge. nil means a clean detach.
	detachErr error

	// ctx{Name,Left} cache the last context-bar payload pushed via SetContext so an
	// unchanged metadata tick skips the tmux subprocess. ctxSet guards the first push
	// (when both are still the empty string). Accessed only from the main update
	// loop, like the other Set* paths.
	ctxName, ctxLeft string
	ctxSet           bool
	// While attached, we use some goroutines to manage the window size and stdin/stdout. This stuff
	// is used to terminate them on Detach. We don't want them to outlive the attached window.
	ctx    context.Context
	cancel func()
	wg     *sync.WaitGroup

	// killRequested is set by the attach stdin reader when the user presses the
	// in-session kill key (Ctrl+X). It is reset at the start of every Attach and
	// read once after the attach returns; the channel close in Detach provides the
	// happens-before edge to the reader.
	killRequested bool
}

// Prefix is the prefix applied to every Atrium-managed tmux session name. It
// derives from config.RuntimeName so legacy installs keep the "claudesquad_"
// prefix and can still find and clean up their pre-rebrand sessions.
func Prefix() string {
	return config.RuntimeName() + "_"
}

// nameWhitespaceRegex strips whitespace runs from instance titles in
// toSanitizedName. (The chrome-flattening whitespace regex moved to
// session/agent with the windowing helpers; this one is name sanitizing only.)
var nameWhitespaceRegex = regexp.MustCompile(`\s+`)

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

// SanitizeNameSegment normalizes one component of a managed tmux session name:
// whitespace runs stripped, dots replaced with underscores (tmux would do it
// anyway). It is the per-segment half of toSanitizedName, exported so callers
// composing qualified names (and collision checks predicting them) share the
// exact rules the session layer applies.
func SanitizeNameSegment(s string) string {
	s = nameWhitespaceRegex.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, ".", "_") // tmux replaces all . with _
}

// toSanitizedName converts an instance title into the legacy (unqualified)
// managed tmux session name: the sanitized title with the active brand prefix
// (see Prefix) applied. New sessions get repo-qualified names via
// QualifiedSessionName; this derivation must stay byte-for-byte stable because
// sessions persisted before names were stored are still found on the socket by
// exactly this name.
func toSanitizedName(str string) string {
	return fmt.Sprintf("%s%s", Prefix(), SanitizeNameSegment(str))
}

// QualifiedSessionName builds the managed tmux session name for a session
// titled title in repo group group: <prefix><group>_<title>, each segment
// sanitized. The result is an opaque unique handle — it is not parseable back
// into its parts (segments may themselves contain underscores); uniqueness is
// enforced per group at creation/rename time, not by the name's shape.
func QualifiedSessionName(group, title string) string {
	return fmt.Sprintf("%s%s_%s", Prefix(), SanitizeNameSegment(group), SanitizeNameSegment(title))
}

// NewSession creates a new Session with the given name and program.
// ctx is the lifecycle context tmux subprocesses derive from; cancelling it
// (app/daemon shutdown) kills in-flight subprocesses.
func NewSession(ctx context.Context, name string, program string) *Session {
	return newSession(ctx, toSanitizedName(name), name, program, MakePtyFactory(), cmd.MakeExecutor())
}

// NewSessionWithName creates a Session whose tmux session name is sessionName
// verbatim — no derivation. It is the constructor for sessions whose name is
// owned by the caller (minted at creation as a qualified name, or restored from
// persisted state); windowName stays the human-readable title shown in the
// window list.
func NewSessionWithName(ctx context.Context, sessionName, windowName, program string) *Session {
	return newSession(ctx, sessionName, windowName, program, MakePtyFactory(), cmd.MakeExecutor())
}

// NewSessionWithDeps creates a new Session with provided dependencies for testing.
func NewSessionWithDeps(ctx context.Context, name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return newSession(ctx, toSanitizedName(name), name, program, ptyFactory, cmdExec)
}

// NewSessionWithNameAndDeps is NewSessionWithName with injected dependencies for testing.
func NewSessionWithNameAndDeps(ctx context.Context, sessionName, windowName, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return newSession(ctx, sessionName, windowName, program, ptyFactory, cmdExec)
}

// SetClaudeConfigDir sets the CLAUDE_CONFIG_DIR injected at session launch. It
// must be called before Start; once the session exists the env is frozen.
func (t *Session) SetClaudeConfigDir(dir string) {
	t.configDir = dir
}

func newSession(ctx context.Context, sessionName, windowName, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return &Session{
		baseCtx:       ctx,
		sanitizedName: sessionName,
		windowName:    windowName,
		program:       program,
		adapter:       agent.Resolve(program),
		ptyFactory:    ptyFactory,
		cmdExec:       cmdExec,
		captureErrLog: log.NewEvery(60 * time.Second),
		monitor:       newStatusMonitor(program),
	}
}

// baseContext returns the lifecycle context subprocesses derive from,
// defaulting to Background for sessions constructed without one.
func (t *Session) baseContext() context.Context {
	if t.baseCtx != nil {
		return t.baseCtx
	}
	return context.Background()
}

// opContext returns a tmuxOpTimeout-capped context for a short tmux operation
// (capture-pane, send-keys, kill-session, has-session). Callers must invoke the
// returned cancel once the subprocess has finished.
func (t *Session) opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(t.baseContext(), tmuxOpTimeout)
}

// Start creates and starts a new tmux session, then attaches to it. Program is the command to run in
// the session (ex. claude). workdir is the git worktree directory.
func (t *Session) Start(workDir string) error {
	return t.start(workDir, t.program)
}

// StartContinue starts the session resuming the prior conversation when the program
// supports it (claude --continue, codex resume --last, gemini --resume latest). It is
// used only on resurrection — the agent process died and we are relaunching it — never
// on PTY reattach (Restore), where the process is still alive. The continue command is
// computed transiently; t.program, the value persisted via Instance, is never mutated.
func (t *Session) StartContinue(workDir string) error {
	return t.start(workDir, t.resumeCommand())
}

// resumeCommand returns the launch command that resumes the prior conversation, or the
// unchanged program when the agent has no resume support. tmux word-splits the trailing
// command string itself (the same reason "aider --model x" works), so the adapter's
// rewrite of the single program argv element is sufficient — no shell wrapping. When the
// adapter requires a capability probe (gemini's --resume is recent), an installed binary
// that predates the flag relaunches blank instead of failing on an unknown flag.
func (t *Session) resumeCommand() string {
	a := t.adapter
	if a.Resume == nil {
		return t.program
	}
	if a.ResumeProbe != "" {
		bin := probeTarget(t.program, a.Key)
		if !binHelpContains(bin, a.ResumeProbe) {
			log.InfoLog.Printf("resume disabled for %s: %q not in %q --help", t.sanitizedName, a.ResumeProbe, bin)
			return t.program
		}
	}
	return a.Resume(t.program)
}

// probeTarget returns the binary whose --help is probed for a resume capability. The
// program's first token is preferred when it *is* the canonical agent binary — possibly at
// an absolute path outside PATH, where probing the bare name would fail and silently
// disable resume for the very binary the session runs. Anything whose basename is not
// exactly the canonical name (a launcher wrapper, a same-agent alias script) is never
// probed — a wrapper's side effects must not run on a probe — so the canonical name is
// probed instead, accepting the PATH-miss degradation for that case.
func probeTarget(program string, key agent.Key) string {
	bin := program
	if i := strings.IndexByte(bin, ' '); i >= 0 {
		bin = bin[:i]
	}
	if filepath.Base(bin) == string(key) {
		return bin
	}
	return string(key)
}

// start creates a new detached tmux session running program in workDir, then attaches.
func (t *Session) start(workDir string, program string) error {
	// Check if the session already exists
	if t.DoesSessionExist() {
		return fmt.Errorf("tmux session already exists: %s", t.sanitizedName)
	}

	// A fresh tmux session means a fresh agent pane; drop any id cached from a
	// previous generation (pause → resume reuses this Session object).
	t.resetPaneID()

	// Inject the authoritative status hooks for claude (a no-op for other agents or when
	// --settings is unsupported). The settings path is appended to the launch command only;
	// t.program (the persisted value) is never mutated. A failure here just disables hooks —
	// the launch still proceeds on the scrape classifier.
	if settingsPath, err := ensureHookSettings(t.sanitizedName, t.program); err != nil {
		log.ErrorLog.Printf("status hooks disabled for %s: %v", t.sanitizedName, err)
	} else if settingsPath != "" {
		// tmux hands the launch command to `sh -c`, and the path embeds the session name,
		// which can carry shell metacharacters (a title like "Surya's comment"). Unquoted,
		// the apostrophe killed the window's shell at launch and start timed out.
		program = program + " --settings " + shellSingleQuote(settingsPath)
	}

	// Create a new detached tmux session and start claude in it. -n gives the
	// window the human-readable title (the conf disables auto-rename).
	// The pty client outlives this call, so it runs under the bare base context
	// (killed on app shutdown), never a per-op timeout.
	args := []string{"new-session", "-d", "-s", t.sanitizedName, "-c", workDir, "-n", t.windowName}
	if t.configDir != "" {
		// -e sets a session-scoped env var independent of the persistent server
		// env (which froze CLAUDE_CONFIG_DIR unset at server start). It must
		// precede the program word.
		args = append(args, "-e", "CLAUDE_CONFIG_DIR="+t.configDir)
	}
	args = append(args, program)
	cmd := tmuxCommand(t.baseContext(), args...)

	ptmx, err := t.ptyFactory.Start(cmd)
	if err != nil {
		// Cleanup any partially created session if any exists.
		if t.DoesSessionExist() {
			cleanupCtx, cancel := t.opContext()
			defer cancel()
			cleanupCmd := tmuxCommand(cleanupCtx, "kill-session", "-t", t.sanitizedName)
			if cleanupErr := t.cmdExec.Run(cleanupCmd); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
			}
		}
		return fmt.Errorf("error starting tmux session: %w", err)
	}

	// Poll for session existence with exponential backoff
	timeout := time.After(2 * time.Second)
	sleepDuration := 5 * time.Millisecond
	for !t.DoesSessionExist() {
		select {
		case <-timeout:
			// err is nil on this path (a failed pty start returned above), so build the
			// timeout error first — wrapping nil with %w renders as "%!w(<nil>)".
			err := fmt.Errorf("timed out waiting for tmux session %s", t.sanitizedName)
			if cleanupErr := t.Close(); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
			}
			return err
		default:
			time.Sleep(sleepDuration)
			// Exponential backoff up to 50ms max
			if sleepDuration < 50*time.Millisecond {
				sleepDuration *= 2
			}
		}
	}
	_ = ptmx.Close()

	// history-limit and mouse are set server-globally by the bundled managed
	// config, so no per-session set-option is needed here.

	err = t.Restore()
	if err != nil {
		if cleanupErr := t.Close(); cleanupErr != nil {
			err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
		}
		return fmt.Errorf("error restoring tmux session: %w", err)
	}

	return nil
}

// CheckAndHandleTrustPrompt checks the pane content once for a startup gate (a trust or
// setup screen that consumes keystrokes) and dismisses it with the adapter's keystroke if
// found. Returns true if a gate was found and handled.
func (t *Session) CheckAndHandleTrustPrompt() bool {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false
	}

	gate, ok := t.adapter.GateUp(content)
	if !ok {
		return false
	}

	switch gate.Dismiss {
	case agent.DismissDAndEnter:
		if err := t.TapDAndEnter(); err != nil {
			log.ErrorLog.Printf("could not tap D+enter on startup gate: %v", err)
		}
	default:
		if err := t.TapEnter(); err != nil {
			log.ErrorLog.Printf("could not tap enter on startup gate: %v", err)
		}
	}
	return true
}

// IsReadyForPrompt reports whether the agent has rendered and is past any startup
// gate, so a queued first message can be submitted into its input box. It is a
// read-only check: it captures the pane once and never sends keystrokes.
func (t *Session) IsReadyForPrompt() bool {
	if !t.DoesSessionExist() {
		return false
	}
	content, err := t.CapturePaneContent()
	if err != nil || strings.TrimSpace(content) == "" {
		return false
	}
	_, gated := t.adapter.GateUp(content)
	return !gated
}

// Restore attaches to an existing session and restores the window size
func (t *Session) Restore() error {
	// The attach client lives until detach/close, so it runs under the bare base
	// context (killed on app shutdown), never a per-op timeout.
	ptmx, err := t.ptyFactory.Start(tmuxCommand(t.baseContext(), "attach-session", "-t", t.sanitizedName))
	if err != nil {
		return fmt.Errorf("error opening PTY: %w", err)
	}
	t.ptmx = ptmx
	t.monitor = newStatusMonitor(t.program)
	return nil
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

// TapEnter sends an enter keystroke to the agent pane.
func (t *Session) TapEnter() error {
	if err := t.sendKeysToPane("Enter"); err != nil {
		return fmt.Errorf("error sending enter keystroke to tmux pane: %w", err)
	}
	return nil
}

// TapDAndEnter sends 'D' followed by an enter keystroke to the agent pane.
func (t *Session) TapDAndEnter() error {
	if err := t.sendKeysToPane("D", "Enter"); err != nil {
		return fmt.Errorf("error sending D+enter keystrokes to tmux pane: %w", err)
	}
	return nil
}

// Tunables for AcceptSuggestion's accept→submit handshake. claude commits an
// accepted suggestion via an async render, so the submit Enter polls for that
// to land; overridable in tests to avoid real delays.
var (
	suggestionAcceptPollInterval = 20 * time.Millisecond
	suggestionAcceptTimeout      = 1 * time.Second
)

// AcceptSuggestion captures the pane fresh and, when the adapter recognizes a
// ghost-text prompt suggestion in an otherwise-empty input box, accepts it
// (Right), waits for the accept to commit, then submits it (Enter), reporting
// whether keys were sent. Agents without a suggestion UI (nil SuggestionVisible)
// return false without capturing.
//
// The capture must be fresh — never the last poll tick's content: the dim
// gate (agent/suggestion.go) is what keeps the trailing Enter from submitting
// user-typed draft text, and it is only as good as the capture is current.
// The keys are claude-semantics, verified against the 2.1.175 binary: Right
// accepts only while a suggestion is showing on an empty input (a cursor
// no-op otherwise; Tab was rejected for its completion fall-throughs), and
// Enter on an empty input does nothing — so if the suggestion vanishes between
// capture and send, the keys degrade to no-ops. Right and Enter cannot be one
// batch: Right's accept is an async React state update, so an Enter in the
// same breath hits the still-empty input (see waitSuggestionCommitted).
func (t *Session) AcceptSuggestion() (bool, error) {
	if t.adapter.SuggestionVisible == nil {
		return false, nil
	}
	raw, err := t.CapturePaneContent()
	if err != nil {
		return false, fmt.Errorf("error capturing pane for suggestion: %w", err)
	}
	if !t.adapter.SuggestionVisible(raw) {
		return false, nil
	}
	// Accept the ghost text. Right (not Tab) fills the input without Tab's
	// completion-menu fall-throughs.
	if err := t.sendKeysToPane("Right"); err != nil {
		return false, fmt.Errorf("error sending right keystroke to tmux pane: %w", err)
	}
	// Submit only once the accept has committed. claude's input is a React
	// component: Right schedules an *async* state update (the binary's accept
	// handler runs `dH(L$)` — set input := suggestion — guarded on the input
	// currently being empty), so an Enter sent in the same breath is read
	// against the still-empty input, where since claude 2.1.136 Enter is a
	// deliberate no-op. That left the suggestion inserted but unsent. Waiting
	// for the dim ghost to give way to committed (non-dim) text closes the race
	// before submitting.
	t.waitSuggestionCommitted()
	if err := t.sendKeysToPane("Enter"); err != nil {
		return false, fmt.Errorf("error sending enter keystroke to tmux pane: %w", err)
	}
	return true, nil
}

// waitSuggestionCommitted blocks until a fresh capture no longer shows a dim
// ghost suggestion — i.e. Right's accept has rendered the committed text — or
// until suggestionAcceptTimeout elapses. The timeout is a bounded fallback, not
// a guess: by the time it expires far more than a render frame has passed, so
// submitting is safe regardless (and an Enter into a still-empty box is itself
// a harmless no-op). Only reached after the nil-adapter gate, so
// SuggestionVisible is non-nil here.
func (t *Session) waitSuggestionCommitted() {
	deadline := time.Now().Add(suggestionAcceptTimeout)
	for {
		raw, err := t.CapturePaneContent()
		if err == nil && !t.adapter.SuggestionVisible(raw) {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(suggestionAcceptPollInterval)
	}
}

// SendKeys types text into the agent pane, as if the user typed it. -l sends
// the bytes literally (never interpreted as tmux key names); -- guards text
// that starts with a dash.
func (t *Session) SendKeys(keys string) error {
	if keys == "" {
		return nil
	}
	return t.sendKeysToPane("-l", "--", keys)
}

// sendKeysToPane runs send-keys against the agent pane (paneTarget), never by
// writing to the attach client's pty: tmux routes client input to the *active*
// pane of the session's current window — the same resolution that made
// session-name captures unsafe — so a split opened while attached would
// swallow autoyes Enter taps and queued prompts. An explicit pane target also
// works without any attach client.
func (t *Session) sendKeysToPane(keys ...string) error {
	ctx, cancel := t.opContext()
	defer cancel()
	args := append([]string{"send-keys", "-t", t.paneTarget()}, keys...)
	return t.cmdExec.Run(tmuxCommand(ctx, args...))
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
	// Serialize against a concurrent off-cadence poll from the UI (switch/detach) so the
	// two callers don't race on the monitor's hash/streak fields. The capture subprocess
	// runs under the lock, but it is brief and the lock is per-session.
	t.monitorMu.Lock()
	defer t.monitorMu.Unlock()
	// A dead/missing session can never be working; probing it would fail every tick
	// and flood the log. The caller (metadata loop) detects the dead session
	// separately and recovers the instance to Paused.
	if !t.DoesSessionExist() {
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
		if t.monitor.lastReported == PaneWorking && t.monitor.idleStreak < idleConfirmTicks {
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
		capped := t.monitor.idleStreak >= idleConfirmTicks
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

// attachInputAction is the outcome of classifying a chunk of attach stdin: keep
// forwarding it to the pty, detach, detach-and-request-kill, or detach to cycle to
// the next/previous sibling session.
type attachInputAction int

const (
	attachForward attachInputAction = iota
	attachDetach
	attachKill
	attachNext
	attachPrev
)

// Control bytes intercepted while attached.
const (
	ctrlQ = 17 // detach
	ctrlX = 24 // kill (detach + request teardown)
)

// classifyAttachInput decides what a single stdin read means while attached.
// Ctrl+Q detaches; Ctrl+X requests a kill but only when allowKill is set (agent
// sessions, not the Terminal-tab shell, where Ctrl+X is a normal editing key).
// A control byte is only honored when it arrives alone (a single-byte read) so
// it isn't mistaken for part of a longer escape sequence or paste. Everything
// else is forwarded to the pty unchanged.
func classifyAttachInput(in []byte, allowKill bool) attachInputAction {
	if len(in) == 1 {
		switch in[0] {
		case ctrlQ:
			return attachDetach
		case ctrlX:
			if allowKill {
				return attachKill
			}
		}
	}
	// Sibling navigation (Ctrl+PageUp/PageDown) arrives as a multi-byte escape
	// sequence, so it is matched separately from the single-byte control keys.
	switch reason, ok := navReason(in); {
	case ok && reason == DetachNext:
		return attachNext
	case ok && reason == DetachPrev:
		return attachPrev
	}
	return attachForward
}

// Attach connects the terminal to the tmux session and blocks (via the returned
// channel) until the user detaches. When allowKill is true, the in-session kill
// key (Ctrl+X) detaches and sets KillRequested so the caller can tear the session
// down; the Terminal-tab shell passes false so Ctrl+X stays a normal shell key.
func (t *Session) Attach(allowKill bool) (chan struct{}, error) {
	// A prior Detach whose Restore failed leaves ptmx nil rather than panicking;
	// re-establish the attach pty here so the degraded session heals transparently
	// on the next attach. If Restore fails again, propagate it to the caller.
	if t.ptmx == nil {
		if err := t.Restore(); err != nil {
			return nil, fmt.Errorf("cannot attach: pty unavailable and restore failed: %w", err)
		}
	}

	t.attachCh = make(chan struct{})
	t.killRequested = false
	t.detachReason = DetachQuit
	t.detachErr = nil

	t.wg = &sync.WaitGroup{}
	t.wg.Add(1)
	t.ctx, t.cancel = context.WithCancel(context.Background())

	// The first goroutine should terminate when the ptmx is closed. We use the
	// waitgroup to wait for it to finish.
	// The 2nd one returns when you press escape to Detach. It doesn't need to be
	// in the waitgroup because is the goroutine doing the Detaching; it waits for
	// all the other ones.
	go func() {
		defer t.wg.Done()
		_, _ = io.Copy(os.Stdout, t.ptmx)
		// When io.Copy returns, it means the connection was closed
		// This could be due to normal detach or Ctrl-D
		// Check if the context is done to determine if it was a normal detach
		select {
		case <-t.ctx.Done():
			// Normal detach, do nothing
		default:
			// If context is not done, it was likely an abnormal termination (Ctrl-D)
			// Print warning message
			fmt.Fprintf(os.Stderr, "\n\033[31mError: Session terminated without detaching. Use Ctrl-Q to properly detach from tmux sessions.\033[0m\n")
		}
	}()

	// Snapshot ptmx before the loop so the goroutine writes through a local copy instead
	// of re-reading the shared t.ptmx field on every keypress. DetachSafely (called by
	// lost-session recovery) can set t.ptmx = nil from another goroutine while this one is
	// blocked on os.Stdin.Read; reading the field in the loop would be a data race on that
	// pointer. (os.File.Write is nil-safe, so the original code raced rather than panicked.)
	attachedPtmx := t.ptmx
	go func() {
		// Close the channel after 50ms
		timeoutCh := make(chan struct{})
		go func() {
			time.Sleep(50 * time.Millisecond)
			close(timeoutCh)
		}()

		// Read input from stdin and check for Ctrl+q
		buf := make([]byte, 32)
		for {
			nr, err := os.Stdin.Read(buf)
			if err != nil {
				if err == io.EOF {
					break
				}
				continue
			}

			// Nuke the first bytes of stdin, up to 64, to prevent tmux from reading it.
			// When we attach, there tends to be terminal control sequences like ?[?62c0;95;0c or
			// ]10;rgb:f8f8f8. The control sequences depend on the terminal (warp vs iterm). We should use regex ideally
			// but this works well for now. Log this for debugging.
			//
			// There seems to always be control characters, but I think it's possible for there not to be. The heuristic
			// here can be: if there's characters within 50ms, then assume they are control characters and nuke them.
			select {
			case <-timeoutCh:
			default:
				log.InfoLog.Printf("nuked first stdin: %s", buf[:nr])
				continue
			}

			switch classifyAttachInput(buf[:nr], allowKill) {
			case attachDetach:
				t.detachReason = DetachQuit
				t.Detach()
				return
			case attachKill:
				// Detach and request a kill; the caller reads KillRequested after
				// the attach returns and runs the teardown confirmation.
				t.killRequested = true
				t.Detach()
				return
			case attachNext:
				t.detachReason = DetachNext
				t.Detach()
				return
			case attachPrev:
				t.detachReason = DetachPrev
				t.Detach()
				return
			default:
				// Forward other input to tmux. If DetachSafely closed the pty, this
				// write returns a "file already closed" error (discarded) rather than
				// racing on t.ptmx. attachedPtmx is captured live at Attach time, so
				// it is never nil.
				_, _ = attachedPtmx.Write(buf[:nr])
			}
		}
	}()

	t.monitorWindowSize()
	return t.attachCh, nil
}

// KillRequested reports whether the most recent attach ended with the user
// pressing the in-session kill key (Ctrl+X). It is reset at the start of Attach.
func (t *Session) KillRequested() bool {
	return t.killRequested
}

// AttachExitReason reports why the most recent attach ended. It is meaningful only
// after the attach channel returned by Attach has closed.
func (t *Session) AttachExitReason() DetachReason {
	return t.detachReason
}

// AttachExitError reports any error encountered while tearing down the most recent
// attach (a failed pty close or Restore). It is meaningful only after the attach
// channel returned by Attach has closed, and is nil for a clean detach.
func (t *Session) AttachExitError() error {
	return t.detachErr
}

// detachCleanup tears down the goroutines and pty backing the current attach,
// returning any errors instead of panicking. It deliberately does NOT close
// attachCh: the caller closes it last, after writing per-detach state
// (detachReason / detachErr), so the channel close provides the happens-before
// edge that makes that state visible to the reader. Returns nil when there is no
// active attach.
func (t *Session) detachCleanup() []error {
	if t.attachCh == nil {
		return nil // already detached / never attached
	}

	var errs []error

	// Close the attached pty session. Closing unblocks the io.Copy goroutine via
	// EOF; nil the field so a stale pointer is never reused.
	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing attach pty session: %w", err))
		}
		t.ptmx = nil
	}

	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}

	if t.wg != nil {
		t.wg.Wait()
		t.wg = nil
	}

	t.ctx = nil

	return errs
}

// DetachSafely disconnects from the current tmux session without panicking. It does
// not re-establish the attach pty; the next Attach self-heals a nil ptmx. Used by
// the programmatic lifecycle paths (pause, lost-session recovery).
func (t *Session) DetachSafely() error {
	if t.attachCh == nil {
		return nil // Already detached
	}

	errs := t.detachCleanup()

	// attachCh closed last; nothing reads detachErr on this path, but keep ordering
	// consistent with Detach.
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during detach: %v", errs)
	}
	return nil
}

// Detach disconnects from the current tmux session and re-establishes the attach pty
// for the next Attach. It degrades rather than panicking: a failed pty close or
// Restore is recorded in detachErr (surfaced via AttachExitError) and logged, leaving
// the session recoverable — polling tolerates a nil ptmx, and the next Attach
// re-Restores it. The caller has already set detachReason / killRequested.
func (t *Session) Detach() {
	if t.attachCh == nil {
		return // already detached / never attached
	}

	errs := t.detachCleanup()

	// Re-establish the attach pty for the next Attach. On failure leave ptmx nil
	// (detachCleanup already did) and record it; Attach/Resume will re-Restore.
	if err := t.Restore(); err != nil {
		errs = append(errs, fmt.Errorf("error restoring attach pty after detach: %w", err))
	}

	if len(errs) > 0 {
		t.detachErr = fmt.Errorf("errors during detach: %v", errs)
		log.ErrorLog.Println(t.detachErr)
	} else {
		t.detachErr = nil
	}

	// attachCh closed LAST, after detachErr is written, so the reader observes it.
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
	}
}

// Close terminates the tmux session and cleans up resources
func (t *Session) Close() error {
	// Remove the per-session status-hook artifacts; harmless if the session never had any.
	cleanupHookSession(t.snapshotName())

	// The pane dies with the session; a resumed session must re-resolve.
	t.resetPaneID()

	var errs []error

	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing PTY: %w", err))
		}
		t.ptmx = nil
	}

	ctx, cancel := t.opContext()
	defer cancel()
	cmd := tmuxCommand(ctx, "kill-session", "-t", t.sanitizedName)
	if err := t.cmdExec.Run(cmd); err != nil {
		errs = append(errs, fmt.Errorf("error killing tmux session: %w", err))
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple errors occurred during cleanup:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return errors.New(errMsg)
}

// SetDetachedSize set the width and height of the session while detached. This makes the
// tmux output conform to the specified shape.
func (t *Session) SetDetachedSize(width, height int) error {
	return t.updateWindowSize(width, height)
}

// clampUint16 bounds an int into the uint16 range. PTY winsize fields are
// uint16; terminal dimensions are always small and positive in practice, but
// clamping makes the conversion provably safe (and satisfies gosec G115).
func clampUint16(n int) uint16 {
	if n < 0 {
		return 0
	}
	if n > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(n)
}

// updateWindowSize updates the window size of the PTY. A nil ptmx (e.g. during the
// degraded window after a failed Restore) makes it a no-op rather than a crash.
func (t *Session) updateWindowSize(cols, rows int) error {
	if t.ptmx == nil {
		return nil
	}
	return pty.Setsize(t.ptmx, &pty.Winsize{
		Rows: clampUint16(rows),
		Cols: clampUint16(cols),
		X:    0,
		Y:    0,
	})
}

// DoesSessionExist asks the tmux server whether this session is currently
// alive (exact-name match).
func (t *Session) DoesSessionExist() bool {
	ctx, cancel := t.opContext()
	defer cancel()
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := tmuxCommand(ctx, "has-session", fmt.Sprintf("-t=%s", t.snapshotName()))
	return t.cmdExec.Run(existsCmd) == nil
}

// snapshotName reads sanitizedName under the read lock so background polling can't race
// the in-place field swap a deep Rename performs.
func (t *Session) snapshotName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sanitizedName
}

// Name returns the tmux session name this Session targets. It is the value the
// instance layer persists, so a session created before names were stored
// records its derived (legacy) name on first load.
func (t *Session) Name() string {
	return t.snapshotName()
}

// CapturePaneContent captures the content of the tmux pane
func (t *Session) CapturePaneContent() (string, error) {
	ctx, cancel := t.opContext()
	defer cancel()
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := tmuxCommand(ctx, "capture-pane", "-p", "-e", "-J", "-t", t.paneTarget())
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("error capturing pane content: %w", err)
	}
	return string(output), nil
}

// CapturePaneContentWithOptions captures the pane content with additional options
// start and end specify the starting and ending line numbers (use "-" for the start/end of history)
func (t *Session) CapturePaneContentWithOptions(start, end string) (string, error) {
	ctx, cancel := t.opContext()
	defer cancel()
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := tmuxCommand(ctx, "capture-pane", "-p", "-e", "-J", "-S", start, "-E", end, "-t", t.paneTarget())
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane content with options: %w", err)
	}
	return string(output), nil
}

// CleanupSessions kills all tmux sessions that start with "session-"
func CleanupSessions(ctx context.Context, cmdExec cmd.Executor) error {
	// This is the `reset` path: wipe the entire status-hooks tree alongside the sessions.
	cleanupAllHookSessions()

	// First try to list sessions
	listCtx, cancel := context.WithTimeout(ctx, tmuxOpTimeout)
	defer cancel()
	cmd := tmuxCommand(listCtx, "ls")
	output, err := cmdExec.Output(cmd)

	// If there's an error and it's because no server is running, that's fine
	// Exit code 1 typically means no sessions exist
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil // No sessions to clean up
		}
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	re := regexp.MustCompile(fmt.Sprintf(`%s.*:`, Prefix()))
	matches := re.FindAllString(string(output), -1)
	for i, match := range matches {
		matches[i] = match[:strings.Index(match, ":")]
	}

	for _, match := range matches {
		log.InfoLog.Printf("cleaning up session: %s", match)
		killCtx, killCancel := context.WithTimeout(ctx, tmuxOpTimeout)
		err := cmdExec.Run(tmuxCommand(killCtx, "kill-session", "-t", match))
		killCancel()
		if err != nil {
			return fmt.Errorf("failed to kill tmux session %s: %w", match, err)
		}
	}
	return nil
}
