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
	"runtime"
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
	// It also guards the paneID/paneIDTried cache and hookSessionName below.
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
	// hookSessionName is the session name this session's hook artifacts are keyed by:
	// the value of sanitizedName at the LAST launch, not the current one. start() bakes
	// an absolute --state-file into every hook command, so the launched agent's write
	// path is frozen for the life of its process and a later deep Rename cannot move it;
	// re-deriving the read path from the current name is what severed the channel in #492.
	// Persisted (InstanceData.HookName) because a TUI restart rebuilds the Session from the
	// post-rename name while the surviving agent still writes to the pre-rename directory,
	// and reattach never re-runs the bake. Empty means "neither launched nor rehydrated" —
	// only a Session freshly constructed in this process and not yet through start(), since
	// SetHookSessionName pins a name even when the persisted one is absent. hookName then
	// falls back to sanitizedName, the right answer for a session with no agent writing
	// anywhere yet.
	// Guarded by mu: written from start() on a background goroutine, read by the poller.
	hookSessionName string
	program         string
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
	// agyConfigDir, when non-empty, isolates the Antigravity CLI's configuration
	// directory using bwrap at session launch.
	agyConfigDir string
	// sessionEnv holds already-rendered NAME=VALUE pairs for this session (#389): the
	// repo's session_env and its managed ATRIUM_PORT, injected through the same
	// `new-session -e` mechanism as the dirs above.
	// It is the general form of what CLAUDE_CONFIG_DIR and GH_CONFIG_DIR are special
	// cases of: a value that must differ per session and that the tmux SERVER env
	// cannot carry, because that is frozen when the server starts. Set before each
	// launch (SetSessionEnv) rather than once at construction: unlike an account dir
	// these are rendered from config the user can edit while the session is parked,
	// and a resume relaunches through this same object.
	sessionEnv []string
	// sessionBriefFn yields the per-session facts the injected SessionStart context brief is
	// rendered from (#485). Unlike the creation-fixed values above this is a PROVIDER, not a
	// value: a Session object outlives its tmux session (pause→resume and recover-in-place
	// relaunch through start() on this same object) and a deep rename changes the title and
	// branch in between, so start() has to re-read them at each launch rather than trust
	// whatever was stamped at the last one. nil, or a provider yielding a zero brief, means
	// "say nothing" — what a direct (non-git) session and every non-claude pane get.
	sessionBriefFn func() SessionBrief
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
	// livenessErrLog throttles the same way for an unreachable socket, and is separate
	// from captureErrLog on purpose: sharing one window would let whichever failure
	// happened first suppress the other for a minute, and these two are reached on
	// mutually exclusive paths (a session whose socket cannot be opened never gets as
	// far as capture-pane).
	livenessErrLog *log.Every

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

// tmuxNameForbidRegex matches the characters tmux forbids in a target spec —
// colon (session:window) and dot (window.pane) — and is the single source of
// truth for both name-sanitizing paths below. tmux silently rewrites these to
// '_' inside a session name (so a name we pass with a colon lands on the socket
// as '_' and our later `has-session -t=` misses it), and tmux >= 3.7 hard-rejects
// them in a `-n` window name ("invalid window name: ...") where older tmux
// accepted them. Either way, leaving them in breaks a session's existence poll —
// the terminal pane fails to open on tmux 3.7+ (#305), and any colon-titled
// session desyncs from its on-socket name on every tmux version.
var tmuxNameForbidRegex = regexp.MustCompile(`[:.]`)

// SanitizeNameSegment normalizes one component of a managed tmux session name:
// whitespace runs stripped, then tmux's forbidden target-spec runes (: and .)
// replaced with underscores — matching exactly what tmux itself does to a
// session name, so the name we derive equals the one that lands on the socket.
// It is the per-segment half of toSanitizedName, exported so callers composing
// qualified names (and collision checks predicting them) share the exact rules
// the session layer applies.
func SanitizeNameSegment(s string) string {
	s = nameWhitespaceRegex.ReplaceAllString(s, "")
	return tmuxNameForbidRegex.ReplaceAllString(s, "_")
}

// sanitizeWindowName makes a human-readable label safe as a tmux window name by
// replacing each forbidden rune with '_' — exactly what tmux's own clean_name
// would do once check_name passed. Unlike SanitizeNameSegment it preserves
// whitespace, since spaces are legal in a window name (the label is cosmetic).
// The window name is cosmetic (the managed conf disables allow-rename/
// automatic-rename and blanks the window-status field), so this substitution is
// never user-visible; it only keeps session startup working across tmux versions.
// Applied at every -n boundary (new-session, rename-window).
func sanitizeWindowName(s string) string {
	return tmuxNameForbidRegex.ReplaceAllString(s, "_")
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

// SetAgyConfigDir sets the directory used to isolate the Antigravity CLI
// configuration at session launch. Must be called before Start.
func (t *Session) SetAgyConfigDir(dir string) {
	t.agyConfigDir = dir
}

// SetSessionEnv sets the already-rendered NAME=VALUE pairs from the repo's
// session_env, injected at the next launch. Call before Start/StartContinue; tmux can
// only set a session's environment when the session is born, so a call afterwards
// affects the next relaunch (a resume or an in-place recovery), not the live pane.
//
// The values are taken as given: rendering them, and refusing the names Atrium injects
// itself, are package repocfg's job.
func (t *Session) SetSessionEnv(env []string) {
	t.sessionEnv = env
}

// SetSessionBriefFunc binds the source of the facts the injected SessionStart context brief is
// rendered from (#485). It takes a provider rather than a value on purpose: every launch calls
// it afresh, so a session renamed between two launches describes itself correctly on the second
// one. Bind it once, before the first Start; a nil provider (or one yielding an incomplete
// brief) injects no SessionStart hook at all.
func (t *Session) SetSessionBriefFunc(fn func() SessionBrief) {
	t.sessionBriefFn = fn
}

// sessionBrief reads the current facts from the bound provider, or the zero brief when none is
// bound. Called at launch — see sessionBriefFn for why it is not read any earlier.
func (t *Session) sessionBrief() SessionBrief {
	if t.sessionBriefFn == nil {
		return SessionBrief{}
	}
	return t.sessionBriefFn()
}

// atriumMarkerEnv is injected into every session's env so external shell hooks
// (e.g. a per-repo gh/Claude account switcher in the user's zshrc) can detect an
// Atrium session and defer to the CLAUDE_CONFIG_DIR / GH_CONFIG_DIR / token env
// injected here, instead of re-deriving — and clobbering — it from the shell's
// current directory.
const atriumMarkerEnv = "ATRIUM=1"

func newSession(ctx context.Context, sessionName, windowName, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return &Session{
		baseCtx:        ctx,
		sanitizedName:  sessionName,
		windowName:     windowName,
		program:        program,
		adapter:        agent.Resolve(program),
		ptyFactory:     ptyFactory,
		cmdExec:        cmdExec,
		captureErrLog:  log.NewEvery(60 * time.Second),
		livenessErrLog: log.NewEvery(60 * time.Second),
		monitor:        newStatusMonitor(program),
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

// SetBaseContext rebinds the lifecycle context this session's tmux subprocesses
// derive from. baseCtx is normally creation-fixed and read lock-free, so this is
// ONLY safe on a quiescent session with no in-flight op — specifically shutdown
// reconciliation (#282), where app.Run swaps a cancelled ctx for one built with
// context.WithoutCancel so Close's `kill-session` isn't insta-killed by the
// cancellation and can actually terminate the tmux session.
func (t *Session) SetBaseContext(ctx context.Context) {
	t.baseCtx = ctx
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
// supports it (claude --continue, codex resume --last, gemini --resume latest, agy
// --continue). It is used only on resurrection — the agent process died and we are
// relaunching it — never on PTY reattach (Restore), where the process is still alive.
// The continue command is computed transiently; t.program, the value persisted via
// Instance, is never mutated.
//
// resuming reports whether the launch actually carried a rewrite. It is false whenever
// resumeCommand fell back to the plain program — no Resume, a probe that failed, an
// argv the adapter refuses to splice into — and a caller repairing a launch that died
// needs it: relaunching THAT command blank would re-run the identical command. It is
// returned rather than left to be re-derived so the answer describes this launch.
func (t *Session) StartContinue(workDir string) (resuming bool, err error) {
	command := t.resumeCommand()
	return command != t.program, t.start(workDir, command)
}

// resumeCommand returns the launch command that resumes the prior conversation, or the
// unchanged program when the agent has no resume support. tmux word-splits the trailing
// command string itself (the same reason "aider --model x" works), so the adapter's
// rewrite of the single program argv element is sufficient — no shell wrapping. When the
// adapter requires a capability probe (gemini's --resume is recent), an installed binary
// that predates the flag relaunches blank instead of failing on an unknown flag.
//
// ResumeProbe is a CAPABILITY check and nothing more: binHelpContains greps the binary's
// --help, so it answers "does this build support the flag" and never "is there a
// conversation to resume". It passes in an empty directory exactly as it does in a
// populated one. Conversation-EXISTENCE is asked separately, in Instance.startResuming
// via transcript.HasResumable, and only claude has an adapter there — so for agy, codex
// and gemini the flag is applied whenever the binary supports it, with nothing having
// looked for a conversation (#712).
//
// That is survivable because those CLIs tolerate it, which is a property of their code
// rather than of ours: driven and recorded beside each adapter's Resume, re-checkable
// with `just drive-agent resume <agent>`. Instance.RepairResumingLaunch is the belt to
// that brace — it relaunches blank, once, when a resuming launch dies at birth,
// whichever agent and whatever the reason.
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

	// Freeze the name this launch's hook artifacts are keyed by BEFORE ensureHookSettings
	// derives the state path from it and bakes that absolute path into every hook command.
	// The agent's write path is fixed the moment it is exec'd, so a later deep Rename can
	// only move the reader — and did, silently, until #492. Freezing here (rather than at
	// construction) is also what makes every relaunch re-key: pause→resume, recover-in-place
	// and a fresh create all route through start(), so a resumed session reads the directory
	// its NEW process writes to. See freezeHookName for why the superseded directory is swept
	// here too, and hookSessionName for why the value is persisted.
	hookName := t.freezeHookName()

	// Inject the authoritative status hooks for claude, plus the SessionStart context brief
	// (a no-op for other agents or when --settings is unsupported). The settings path is
	// appended to the launch command only; t.program (the persisted value) is never mutated.
	// A failure here just disables hooks — the launch still proceeds on the scrape classifier,
	// and without a brief.
	if settingsPath, err := ensureHookSettings(hookName, t.program, t.sessionBrief()); err != nil {
		log.ErrorLog.Printf("status hooks disabled for %s: %v", hookName, err)
	} else if settingsPath != "" {
		// tmux hands the launch command to `sh -c`, and the path embeds the session name,
		// which can carry shell metacharacters (a title like "Surya's comment"). Unquoted,
		// the apostrophe killed the window's shell at launch and start timed out.
		program = program + " --settings " + shellSingleQuote(settingsPath)
	}

	// Isolate a routed Antigravity (agy) account's config directory via bwrap. Keyed
	// off the resolved adapter — not a string match on program — so it also covers
	// the `antigravity` alias and the `--continue` resume command, and applied BEFORE
	// wrapOOMScore so the OOM snippet wraps the bwrap command rather than the check
	// running against an already-rewritten `…; exec agy` string (which never matches).
	// A no-op off Linux, without a routed dir, or when bwrap is not installed.
	if t.adapter.Key == agent.KeyAgy {
		program = wrapAgyBwrap(program, t.agyConfigDir, runtime.GOOS)
	}

	// Weight this agent against the shared tmux server for the kernel OOM killer:
	// prefix a shell snippet that raises the pane's oom_score_adj above the server's
	// before exec'ing the agent, so memory pressure sheds one recoverable session
	// rather than the server (every session). A no-op when disabled or off Linux.
	// The margin is read here, at each launch, so a session relaunched after a
	// mid-run Settings change (pause → resume, pane recreate) picks up the new value.
	program = wrapOOMScore(program, int(agentOOMMargin.Load()), runtime.GOOS)

	// Create a new detached tmux session and start claude in it. -n gives the
	// window the human-readable title (the conf disables auto-rename).
	// The pty client outlives this call, so it runs under the bare base context
	// (killed on app shutdown), never a per-op timeout.
	args := []string{"new-session", "-d", "-s", t.sanitizedName, "-c", workDir, "-n", sanitizeWindowName(t.windowName)}
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
	// The per-session environment (#389): the repo's own session_env, plus ATRIUM_PORT
	// when the session holds a managed port. Placed after Atrium's fixed names, though
	// the ordering is not what keeps them apart: repocfg refuses a session_env entry
	// named ATRIUM_*, CLAUDE_CONFIG_DIR or GH_CONFIG_DIR, so which of two assignments
	// tmux keeps never has to be reasoned about for those — including the port, whose
	// name is reserved by that same rule. The gh token names below are the
	// exception it cannot cover — they come from the user's own gh_accounts.token_env,
	// so a config that spells the same name in both sections gets whichever tmux
	// resolves. Values are single argv elements, so nothing here is re-parsed by a
	// shell.
	for _, pair := range t.sessionEnv {
		args = append(args, "-e", pair)
	}
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
// distinguish a menu-style gate from the composer: an agent draws its selector with its own
// prompt glyph (claude "❯ 1. …", agy "> 1. Yes", codex "› 1. Yes, continue"), so those
// gates are still excluded by GateUp / DetectPrompt above, not by the box check — and cannot
// be excluded by it, since a queued prompt that is a numbered list draws the same shape as a
// numbered menu (measured on live codex panes; see agent.promptSet). That is why codex widens
// its gate window rather than filtering the composer line: GateUp has to be right at every
// pane width, because nothing behind it is. Readiness is therefore the conjunction: no known
// gate or prompt AND a box on screen. It is a read-only check: it captures the pane once and
// never sends keystrokes.
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

// InputBoxCollapsedPaste reports whether the live composer is showing the agent's collapsed-paste
// placeholder chip (claude's "[Pasted text +N lines]") rather than literal text. It backs the
// closed-loop send: a ≥4-line prompt is delivered as a bracketed paste that claude collapses into
// this chip, so the first-line signature never appears — the chip is the only landing signal.
// False for agents without a PasteCollapsed predicate (they render pastes inline).
func (t *Session) InputBoxCollapsedPaste() bool {
	if t.adapter.PasteCollapsed == nil {
		return false
	}
	text, ok := t.InputBoxText()
	if !ok {
		return false
	}
	return t.IsCollapsedPaste(text)
}

// IsCollapsedPaste reports whether an already-captured input-box readback is the agent's
// collapsed-paste placeholder chip rather than literal text. It is the pure predicate behind
// InputBoxCollapsedPaste, exposed so a caller that already holds a readback (see the prompt
// delivery's boxHoldsPrompt) classifies it without re-capturing the pane. False for agents
// without a PasteCollapsed predicate (they render pastes inline).
func (t *Session) IsCollapsedPaste(text string) bool {
	return t.adapter.PasteCollapsed != nil && t.adapter.PasteCollapsed(text)
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
	// Keyed by the LAUNCHED name, not the current one: a killed session that had been deep-
	// renamed used to clean a directory that never existed and leave its real one behind
	// forever (#492), recoverable only by `atrium reset` — which wipes every session's hook
	// state, not just this one's. Cleaning the frozen name alone is sufficient because
	// freezeHookName already swept any superseded directory at the relaunch that superseded
	// it, so a session owns exactly one hook directory at any moment.
	cleanupHookSession(t.hookName())

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

// sessionAlreadyGone reports whether a failed tmux command means no session is left to
// act on, rather than a real failure. Two callers read it that way — Close, where "gone"
// is the teardown goal already met, and liveness, where it is a definitive "no" — so a
// case added here moves kill classification and poll classification together, so it needs
// adding to alreadyGoneMessages (close_test.go) — the table that holds BOTH callers to it:
// close_test.go drives the kill through it and tmux_test.go drives the poll through the same
// list. Those two lists are the authority on which messages land which way. The message can
// arrive on stderr (real tmux) or in the error itself (test fakes), so check both;
// anything unrecognized falls through as a real error for the caller to surface.
//
// The socket case is matched as a PAIR rather than on "error connecting to" alone, and
// that is the whole of its correctness. tmux formats it as `error connecting to %s (%s)`
// with strerror, so the prefix also covers "(Permission denied)" — a socket that exists,
// hosting a server this process cannot address, which may be running the very session
// being killed. socketUnreachable is that other half.
//
// Residual, accepted, and re-examined in #730 rather than merely tracked: a missing socket
// FILE is not proof the server is gone. Unlink a live server's socket and the server and
// its panes keep running while every tmux command aimed at that path reports ENOENT, so
// Close reports a clean kill and TerminalPane.CloseForInstance releases the shell's owned
// name. Holding the name instead recovers nothing — the path is unaddressable in both
// directions — and would reserve the instance's title forever in the far commoner case #723
// is about, no server running at all.
//
// What #730 added is the scope of that state, and it argues for accepting the residual more
// strongly than the paragraph above did. The socket a shell is killed on is not its own: a
// terminal shell is a session on the FLEET-WIDE ambient socket, because Close goes through
// tmuxCommand, which prepends `-L socketName()` (command.go). So ENOENT here cannot mean
// "this shell's server vanished" — it means the file every agent session is served through
// is gone, and every one of them is orphaned with it. Holding one shell's name protects
// nothing the unlink has not already taken, and `atrium reap` is what recovers the server,
// by the /proc scan in orphan.go.
//
// Note which no-server cases land on which message, because the residual is only one of
// three. tmux never unlinks a socket when its server dies (see StaleSocket in orphan.go),
// so a server that exited during this boot leaves its file behind and the probe reports
// "no server running". ENOENT means the file is genuinely absent, which is #723's common
// case — a boot that cleared /tmp, a deleted root — and there really is nothing to hold a
// name for. The residual is the third: unlinked out from under a server still running.
//
// Textual here, and the reason is the command rather than the guard: kill-session exits
// non-zero for plenty of reasons that are not "gone", so the messages have to be read.
// orphan.go's classifyPIDProbe is not the purely structural counterpart it once was — since
// #730 it reads the connect diagnostic too, through the same socketUnreachableMessage this
// predicate's other half uses, because on that path an unopenable socket must not be read as
// an empty one. What is still structural there is the rest: any other non-zero exit from
// display-message is taken as an answer.
func sessionAlreadyGone(err error, stderr string) bool {
	hay := strings.ToLower(err.Error() + " " + stderr)
	socketMissing := strings.Contains(hay, "error connecting to") &&
		strings.Contains(hay, "no such file or directory")
	return socketMissing ||
		strings.Contains(hay, "no server running") ||
		strings.Contains(hay, "session not found") ||
		strings.Contains(hay, "can't find session")
}

// socketUnreachable reports whether tmux could not open the socket at all for a reason
// other than the file's absence — "(Permission denied)" on a socket a live server may
// still be serving. That is neither "gone" nor an answer about the session: nothing was
// asked of any server, so liveness must keep the prior status rather than read it as a
// death. The ENOENT exclusion is what makes that split correct, and it is checked here
// rather than left to call order: sessionAlreadyGone owns the one connect failure that does
// mean gone, and at liveness's call site it has already matched and returned before this
// runs, so the conjunct is dead weight there.
//
// It stopped being dead weight when #730 arrived — the "next caller, whose order nothing
// here can promise" that this comment used to anticipate. classifyPIDProbe consults the
// rule through socketUnreachableMessage with no sessionAlreadyGone ahead of it, because on
// the reap path the two connect failures must land on OPPOSITE sides: the absent path is
// the #547 orphan the reaper exists to find, and the unopenable one is a live server it
// must not touch. Delete the conjunct and the two ENOENT tests in orphan_linux_test.go go
// red — TestOrphanedServerIsFoundAndKillableAfterItsSocketRootIsDeleted and
// TestAServerWhoseSocketFileIsGoneStillReportsItsAttachedClient — which is what
// "load-bearing" now means here rather than "depth". Named rather than counted because
// there are three tmux-backed orphan tests and the third, the unopenable-socket one, keeps
// passing under that mutant: widening the rule cannot change a verdict it already reaches.
//
// The errno tail is open-ended (tmux formats it with strerror) and, for a connect() failure,
// so is the set of errnos a given kernel picks — the messages in unreachableSocketMessages
// were captured on Linux, and CI's macOS job runs a different one. Hence the shape of the
// test: any errno but ENOENT, so an unlisted one is classified safely rather than reaching
// the ExitError branch and being read as a death. The cost of that default is below.
//
// The trade this makes, deliberately. sessionIndeterminate leaves the status untouched, and
// it does not merely fail to advance the lost-session strike counter — app_poll.go derives
// sessionLost from PaneDead alone and DELETES the strike entry for any other result, so an
// intermittent unreachable socket resets the count and never accumulates toward the recover
// threshold. While the condition persists the instance is therefore never parked as Paused:
// a permanently unopenable socket shows a frozen status instead of a recoverable Paused one,
// and the throttled log line in liveness is the only signal that anything is wrong. That is
// accepted because the reverse default is the #270 mass-pause shape, where every session
// behind one unopenable socket is torn down on the strength of a probe that never reached a
// server — the rare case degrades quietly here, rather than the common one destructively.
// (The cancelled-probe case is NOT an argument for this branch; it is caught by the ctx
// guard two cases earlier and never produces a connect diagnostic at all.) Neither outcome
// is honest, because "I cannot reach the socket" is not a session state. #730 did not change
// that and could not: what it fixed is the reap path, where the same confusion had a
// destructive consequence and a structural answer — the row leaves the kill set. Here there
// is no third status to move to, so the trade above stands as written.
func socketUnreachable(err error, stderr string) bool {
	return socketUnreachableMessage(err.Error() + " " + stderr)
}

// socketUnreachableMessage is the rule above over tmux's diagnostic alone, for the two
// callers that hold the text rather than an error: classifyPIDProbe, which has already
// unwrapped its *exec.ExitError to reach Stderr, and the precondition assertion in
// TestALiveServerBehindAnUnopenableSocketIsNotAReapTarget, which has captured a real
// diagnostic and no error at all.
//
// Convenience, not necessity, and the earlier version of this comment claimed otherwise:
// it said a bare &exec.ExitError{} panics on Error() because its ProcessState is nil, so
// classifyPIDProbe could not call socketUnreachable. That is false — (*os.ProcessState).String
// has a nil-receiver branch and returns "<nil>" — and it was measured, not reasoned, in
// review of #739. socketUnreachable(exitErr, string(exitErr.Stderr)) would work; it would
// just fold "exit status 1" into the haystack for nothing. One implementation, two entry
// points, no claim about panics.
//
// Callers pass whatever they have; the fold happens here so no caller can get the case
// handling wrong.
func socketUnreachableMessage(hay string) bool {
	hay = strings.ToLower(hay)
	return strings.Contains(hay, "error connecting to") &&
		!strings.Contains(hay, "no such file or directory")
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
	sessionAlive sessionLiveness = iota // has-session succeeded
	// sessionGone has two producers in liveness: a message sessionAlreadyGone recognizes,
	// and — for now — any other non-zero exit, via a fallthrough whose premise its own
	// guards disprove. Audit the second before trusting this state (see liveness).
	sessionGone
	sessionIndeterminate // probe never got a definitive answer (timeout, exec failure)
)

// liveness probes the tmux server for this session and classifies the result.
// A non-nil error is not automatically "gone": a context-killed probe, a fork/exec
// failure, or a socket tmux could not open means the probe never reached a definitive
// answer, so the caller must keep the prior status rather than tear the session down.
func (t *Session) liveness() sessionLiveness {
	ctx, cancel := t.opContext()
	defer cancel()
	// Capture stderr so a definitive answer can be recognized the same way Close's
	// sessionAlreadyGone does — the message is there, not in the error.
	var stderr bytes.Buffer
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := tmuxCommand(ctx, "has-session", fmt.Sprintf("-t=%s", t.snapshotName()))
	existsCmd.Stderr = &stderr
	err := t.cmdExec.Run(existsCmd)
	switch {
	case err == nil:
		return sessionAlive
	// A context-killed probe surfaces as an ExitError ("signal: killed"), so this must
	// be checked before the ExitError branch below — and on ctx.Err() rather than on
	// DeadlineExceeded alone, because a CANCELLED context (app shutdown, opContext's
	// parent going away) kills the process just the same while ctx.Err() reads
	// Canceled. Reading that as a death parks live sessions as Paused on the way out,
	// which is the #270 mass-pause shape. The error chain is checked too, for a fake
	// executor that reports the cause without a real context.
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return sessionIndeterminate
	// tmux gave a definitive "no live session" answer.
	case sessionAlreadyGone(err, stderr.String()):
		return sessionGone
	// tmux could not open the socket for a reason that is not its absence: nothing was
	// asked of any server, and one may be alive behind it (#730). Logged, throttled,
	// because this classification is otherwise completely silent — it leaves the status
	// alone and clears the strike count, so a socket stuck this way shows a stale status
	// forever with nothing anywhere to say why (see socketUnreachable's doc).
	case socketUnreachable(err, stderr.String()):
		if t.livenessErrLog.ShouldLog() {
			log.ErrorLog.Printf("tmux socket unreachable for session %q; its status is frozen "+
				"at the last known value until this clears: %v",
				t.snapshotName(), errWithStderr(err, stderr.String()))
		}
		return sessionIndeterminate
	// tmux actually ran and exited non-zero for some other reason. Read as a real "no" on
	// the grounds that has-session only fails when the session is absent — a premise the
	// two cases above are both counterexamples to, so this default is known incomplete
	// rather than sound. Every message it has not been taught lands here and is read as a
	// death: an unlisted connect errno, `unknown command: has-session` below the version
	// floor, a usage error from a malformed target. Inverting it (only a recognized message
	// is gone) is tracked in #734; it is a behaviour change on every tmux failure this
	// package has not enumerated, which is more than #723's blast radius can carry.
	case errors.As(err, new(*exec.ExitError)):
		return sessionGone
	// The probe never reached the server (fork/exec EMFILE/ENOMEM, a stalled-but-alive
	// server): inconclusive, keep the prior status.
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

// Gone reports that the session is DEFINITIVELY dead — tmux answered, and the answer
// was that there is no such session.
//
// It is not the negation of DoesSessionExist, and the difference is the whole reason it
// exists. That predicate reads every inconclusive probe — a socket that cannot be opened
// for any reason but its absence, a probe the context cancelled, a fork/exec failure
// under load — as not-alive, which is the safe way round for a caller asking "may I use
// this session?". A caller about to relaunch OVER the session is asking the opposite
// question, and for it an inconclusive answer must never authorise the relaunch: the
// server may be up with the session on it, and the second launch would either collide
// with a live agent or fail on the duplicate-name guard. Callers that must tell a
// transient failure from a death use this or liveness directly (Poll, startResuming).
func (t *Session) Gone() bool {
	return t.liveness() == sessionGone
}

// Attached reports whether an interactive tmux client currently owns this
// session. Background capturers use it the way Poll does (see its comment): a
// capture while attached contends the shared socket for a frame nobody is
// looking at, since the user is watching the real pane.
func (t *Session) Attached() bool {
	return t.attached.Load()
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

// CleanupSessions kills every tmux session on Atrium's socket whose name starts
// with Prefix() ("atrium_", or "claudesquad_" on a legacy install).
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
