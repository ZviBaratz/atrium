// Package tmux wraps a real tmux server on Atrium's dedicated socket. Each
// session runs its agent program in a pty; Poll captures pane content and
// classifies it into a PaneState (unknown, working, prompt, idle). All tmux
// subprocesses go through cmd.Executor so tests can fake them.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/teardown"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

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
	// ghConfigDir, when non-empty, is injected as GH_CONFIG_DIR via the same
	// `new-session -e` mechanism, selecting which GitHub CLI account the agent's
	// own `gh` (and any https git credential-helper) calls run under. Empty =
	// inherit. Set once before Start (SetGHConfigDir); fixed for the session life.
	ghConfigDir string
	// githubTokenEnv lists env var names (from config.GHAccount.TokenEnv, e.g.
	// GITHUB_PERSONAL_ACCESS_TOKEN) to inject the routed account's gh token under,
	// so tools that read a token from the env — notably the github MCP's
	// `Authorization: Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}` — use this session's
	// account rather than a stale value frozen into the tmux server env. The token
	// VALUE is resolved fresh in start() and never held on this struct, so it is
	// never persisted; only the names are creation-fixed (SetGitHubTokenEnv). Empty
	// = inject no token.
	githubTokenEnv []string
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
	// attachOut is the gated stdout pump for the current attach. Detach disables it so
	// the io.Copy goroutine — which can stay blocked in a pty read until the tmux client
	// exits — can be left to drain in the background without writing to the terminal
	// Bubble Tea has reclaimed.
	attachOut *gatedWriter
	// monitor monitors the tmux pane content and sends signals to the UI when it's status changes
	monitor *statusMonitor
	// monitorMu serializes Poll. The metadata tick polls each session once per cycle, but
	// the UI also polls the selected session off-cadence on switch/detach; without this
	// lock those two callers would race on the monitor's hash/streak fields.
	monitorMu sync.Mutex

	// Initialized by Attach
	// Deinitilaized by Detach
	//
	// detachMu serializes the teardown paths so they can't race each other on attachCh.
	// Before #236 the stdin reader was the only mid-attach detacher, so no lock was
	// needed; now the stdout pump also tears the attach down when the tmux client exits
	// on its own (detachOnClientExit), which can run concurrently with a keypress detach.
	// Whichever caller wins sets detachReason and closes attachCh under this lock; the
	// loser observes attachCh == nil and becomes a no-op instead of double-closing.
	detachMu sync.Mutex
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

	// attached is true from the end of Attach until the teardown (Detach /
	// DetachSafely) has finished reinstalling the detached ptmx + monitor. While
	// set, Poll early-returns so the in-flight metadata tick neither contends the
	// tmux socket with the live attach client nor races the monitor swap in
	// Restore. Atomic: written on the attach/detach goroutine, read on the
	// metadata-tick goroutines, with no companion state to guard under a mutex.
	attached atomic.Bool
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

// SetGHConfigDir sets the GH_CONFIG_DIR injected at session launch. It must be
// called before Start; once the session exists the env is frozen.
func (t *Session) SetGHConfigDir(dir string) {
	t.ghConfigDir = dir
}

// SetGitHubTokenEnv sets the env var names the routed account's gh token is
// injected under at launch (config.GHAccount.TokenEnv). Call before Start; the
// token value is resolved at session birth and never stored on the session.
func (t *Session) SetGitHubTokenEnv(names []string) {
	t.githubTokenEnv = names
}

// atriumMarkerEnv is injected into every session's env so external shell hooks
// (e.g. a per-repo gh/Claude account switcher in the user's zshrc) can detect an
// Atrium session and defer to the CLAUDE_CONFIG_DIR / GH_CONFIG_DIR / token env
// injected here, instead of re-deriving — and clobbering — it from the shell's
// current directory.
const atriumMarkerEnv = "ATRIUM=1"

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
	if t.ghConfigDir != "" {
		// Same mechanism for GH_CONFIG_DIR: pins the agent's `gh` (and https git
		// credential-helper) to the right GitHub account, per-session, with no
		// mutation of the global ~/.config/gh active account.
		args = append(args, "-e", "GH_CONFIG_DIR="+t.ghConfigDir)
	}
	// Marker for external shell hooks (see atriumMarkerEnv). Injected for every
	// session; -e values are single argv elements, so a sanitizedName is safe as a
	// value (only the trailing program word is handed to `sh -c`).
	args = append(args, "-e", atriumMarkerEnv, "-e", "ATRIUM_SESSION="+t.sanitizedName)
	// Resolve the routed account's gh token and inject it under each configured env
	// name (e.g. GITHUB_PERSONAL_ACCESS_TOKEN, which the github MCP reads). The
	// value is a start() local — never stored on the session nor persisted. A
	// failed resolution (no gh / not authenticated) injects nothing; launch still
	// proceeds, so a token hiccup can never block a session.
	//
	// Caveat: `-e NAME=<token>` puts the token in the spawned tmux client's argv,
	// readable by other local users via `ps`/`/proc/<pid>/cmdline` for that
	// process's brief lifetime. That's an accepted tradeoff on a single-user dev
	// host — and the only per-session env channel tmux offers — not a persisted or
	// logged exposure.
	if len(t.githubTokenEnv) > 0 {
		// Two short, local keyring/config reads (see resolveGitHubToken); they never
		// touch the network, so bound them with the same short budget as a tmux op.
		tokCtx, cancel := context.WithTimeout(t.baseContext(), tmuxOpTimeout)
		tok, err := resolveGitHubToken(tokCtx, t.ghConfigDir)
		cancel()
		if err != nil {
			log.InfoLog.Printf("gh token injection skipped for %s: %v", t.sanitizedName, err)
		} else {
			for _, name := range t.githubTokenEnv {
				args = append(args, "-e", name+"="+tok)
			}
		}
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

// IsReadyForPrompt reports whether the agent has rendered and is past any startup
// gate, so a queued first message can be submitted into its input box. It is a
// read-only check: it captures the pane once and never sends keystrokes.
func (t *Session) IsReadyForPrompt() bool {
	if !t.DoesSessionExist() {
		return false
	}
	raw, err := t.CapturePaneContent()
	if err != nil || strings.TrimSpace(raw) == "" {
		return false
	}
	_, gated := t.adapter.GateUp(cleanForDetection(raw))
	return !gated
}

// AwaitingInput reports whether keystrokes typed now would land in the agent's live
// input box. It is the positive readiness signal for delivering a queued initial prompt:
// the session exists, the pane has rendered, no startup gate (GateUp) and no
// blocking prompt (DetectPrompt) is up, and the composer's input box is actually on screen
// (InputBoxVisible).
//
// Requiring the box's presence — not merely the absence of a *known* gate, as
// IsReadyForPrompt does — closes the timing race this fix targets: a pre-box boot frame or
// a late-painting startup screen that is briefly idle-looking has no composer yet, so it can
// no longer be mistaken for readiness and swallow the prompt. It does not, on its own,
// distinguish a menu-style gate from the composer: claude renders its trust/new-MCP screens
// as a "❯ 1. …" selector, which reads as a box line, so those gates are still excluded by
// GateUp / DetectPrompt above, not by the box check. Readiness is therefore the conjunction:
// no known gate or prompt AND a box on screen. It is a read-only check: it captures the pane
// once and never sends keystrokes.
func (t *Session) AwaitingInput() bool {
	if !t.DoesSessionExist() {
		return false
	}
	raw, err := t.CapturePaneContent()
	if err != nil || strings.TrimSpace(raw) == "" {
		return false
	}
	content := cleanForDetection(raw)
	if _, gated := t.adapter.GateUp(content); gated {
		return false
	}
	if _, prompted := t.adapter.DetectPrompt(content); prompted {
		return false
	}
	return t.adapter.InputBoxVisible(content)
}

// InputBoxText returns the text currently shown in the agent's live input box and whether
// a box is on screen, from a fresh capture. It backs the closed-loop send: after typing a
// queued prompt the caller confirms the box now holds that text (it landed) and, after
// submitting, that the box no longer holds it (it was sent).
func (t *Session) InputBoxText() (string, bool) {
	raw, err := t.CapturePaneContent()
	if err != nil {
		return "", false
	}
	return t.adapter.InputBoxText(cleanForDetection(raw))
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
	// Serialize the monitor swap against Poll/RuntimePermissionMode, which read
	// t.monitor under this lock. Detach calls Restore on the detach goroutine
	// while an in-flight tick may still be inside Poll; the lock (around the
	// pointer write only, not the pty I/O above) closes the data race. No Restore
	// caller holds monitorMu, so this cannot deadlock.
	t.monitorMu.Lock()
	t.monitor = newStatusMonitor(t.program)
	t.monitorMu.Unlock()
	return nil
}

// TapEnter sends an enter keystroke to the agent pane.
func (t *Session) TapEnter() error {
	if err := t.sendKeysToPane("Enter"); err != nil {
		return fmt.Errorf("error sending enter keystroke to tmux pane: %w", err)
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

// SendPasted delivers text into the agent pane as a single bracketed-paste block via a
// tmux paste buffer, preserving embedded newlines without submitting on each one. Typing a
// multi-line prompt with send-keys -l feeds literal line feeds, and most agent TUIs submit
// on the first newline — dropping every line after it. Staging the text in a buffer and
// pasting with -p (bracketed paste) makes the agent receive the whole block as pasted text,
// exactly as if the user pasted it; the caller's subsequent single Enter submits it once.
// The buffer is named per session and deleted on paste (-d), so concurrent sessions sharing
// the tmux server do not collide and no buffer leaks.
func (t *Session) SendPasted(text string) error {
	if text == "" {
		return nil
	}
	ctx, cancel := t.opContext()
	defer cancel()
	buf := "atrium-prompt-" + t.snapshotName()
	// set-buffer passes the text as a single argv element (-- guards a leading dash), so no
	// stdin plumbing is needed and the staged value is verbatim — newlines included.
	if err := t.cmdExec.Run(tmuxCommand(ctx, "set-buffer", "-b", buf, "--", text)); err != nil {
		return fmt.Errorf("error staging tmux paste buffer: %w", err)
	}
	if err := t.cmdExec.Run(tmuxCommand(ctx, "paste-buffer", "-d", "-p", "-b", buf, "-t", t.paneTarget())); err != nil {
		return fmt.Errorf("error pasting buffer to tmux pane: %w", err)
	}
	return nil
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

// Close terminates the tmux session and cleans up resources
func (t *Session) Close() error {
	// Remove the per-session status-hook artifacts; harmless if the session never had any.
	cleanupHookSession(t.snapshotName())

	// The pane dies with the session; a resumed session must re-resolve.
	t.resetPaneID()

	var tc teardown.Errors

	if t.ptmx != nil {
		tc.Record("close PTY", t.ptmx.Close())
		t.ptmx = nil
	}

	ctx, cancel := t.opContext()
	defer cancel()
	// Capture stderr so a kill-session failure can be classified: an already-dead
	// session (external kill, crashed/absent server) is the teardown goal already
	// met, not a failure to report. Anything else — notably a hung server that
	// leaves the agent alive — must surface so the caller doesn't claim a clean
	// kill.
	var stderr bytes.Buffer
	// "-t=" forces an exact-name match. A bare "-t" is a tmux prefix match, so
	// killing an already-gone session could match and kill a *different* live
	// session whose name this one is a prefix of (e.g. "sess" vs "sess2").
	// DoesSessionExist uses "-t=" for exactly this reason.
	cmd := tmuxCommand(ctx, "kill-session", fmt.Sprintf("-t=%s", t.sanitizedName))
	cmd.Stderr = &stderr
	if err := t.cmdExec.Run(cmd); err != nil && !sessionAlreadyGone(err, stderr.String()) {
		// The error itself is usually just "exit status 1"; tmux's real diagnostic
		// lands on stderr, so fold it in so the surfaced failure is legible.
		tc.Record("kill tmux session", errWithStderr(err, stderr.String()))
	}

	return tc.Err()
}

// errWithStderr enriches a subprocess error with its stderr text when present.
// A failed `tmux` run reports only "exit status 1" in the error while the useful
// message ("server exited unexpectedly", …) is on stderr, so callers that surface
// the error would otherwise show nothing actionable.
func errWithStderr(err error, stderr string) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%w: %s", err, s)
	}
	return err
}

// sessionAlreadyGone reports whether a kill-session failure just means the session
// was already dead rather than a real teardown failure. tmux prints "can't find
// session"/"session not found" when the session is gone and "no server running on
// ..." when the whole server is down; both mean no live session remains, which is
// exactly what Close aims for. The message can arrive on stderr (real tmux) or in
// the error itself (test fakes), so check both. Anything unrecognized — a hung
// server, a timeout — falls through as a real error so the caller can surface it;
// tmux's messages are stable English, so the failure direction is the safe one.
func sessionAlreadyGone(err error, stderr string) bool {
	hay := strings.ToLower(err.Error() + " " + stderr)
	return strings.Contains(hay, "no server running") ||
		strings.Contains(hay, "session not found") ||
		strings.Contains(hay, "can't find session")
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

// sessionLiveness is the outcome of a has-session probe. It distinguishes a
// definitive "the session is gone" from an inconclusive probe (a timeout kill,
// a fork/exec failure under load) so the poll loop never treats a transient
// infrastructure hiccup as a dead session — the mass-pause bug in #270.
type sessionLiveness int

const (
	sessionAlive         sessionLiveness = iota // has-session succeeded
	sessionGone                                 // tmux answered "no such session"/"no server running"
	sessionIndeterminate                        // probe never got a definitive answer (timeout, exec failure)
)

// liveness probes the tmux server for this session and classifies the result.
// A non-nil error is not automatically "gone": a deadline-kill or a fork/exec
// failure means the probe never reached a definitive answer, so the caller must
// keep the prior status rather than tear the session down.
func (t *Session) liveness() sessionLiveness {
	ctx, cancel := t.opContext()
	defer cancel()
	// Capture stderr so a definitive "session not found"/"no server running" can
	// be recognized the same way Close's sessionAlreadyGone does.
	var stderr bytes.Buffer
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := tmuxCommand(ctx, "has-session", fmt.Sprintf("-t=%s", t.snapshotName()))
	existsCmd.Stderr = &stderr
	err := t.cmdExec.Run(existsCmd)
	switch {
	case err == nil:
		return sessionAlive
	// A deadline-kill can surface as an ExitError ("signal: killed"), so this must
	// be checked before the ExitError branch below. Inspect the context (a real
	// timeout in production) and the error chain (a fake executor in tests).
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return sessionIndeterminate
	// tmux gave a definitive "no live session" answer.
	case sessionAlreadyGone(err, stderr.String()):
		return sessionGone
	// tmux actually ran and exited non-zero for some other reason — has-session
	// only fails when the session is absent, so this is still a real "no".
	case errors.As(err, new(*exec.ExitError)):
		return sessionGone
	// The probe never reached the server (fork/exec EMFILE/ENOMEM, ctx canceled,
	// a stalled-but-alive server): inconclusive, keep the prior status.
	default:
		return sessionIndeterminate
	}
}

// DoesSessionExist asks the tmux server whether this session is currently
// alive (exact-name match). An inconclusive probe (timeout, exec failure) reads
// as not-alive here, matching the historical bool contract; callers that must
// distinguish transient failures from a real death use liveness directly (Poll).
func (t *Session) DoesSessionExist() bool {
	return t.liveness() == sessionAlive
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
