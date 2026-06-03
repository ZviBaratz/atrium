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
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Names of known agent programs, used to select program-specific behavior
// (busy markers, prompt detection, trust-prompt handling).
const (
	ProgramClaude = "claude"
	ProgramAider  = "aider"
	ProgramGemini = "gemini"
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
	// PaneIdle means the agent has settled with nothing pending.
	PaneIdle
)

// busyMarkers returns substrings that, when present in the pane, prove the agent is
// actively working. The marker is a level signal: it stays on screen for the whole turn
// (including silent tool calls), and its own ticking elapsed-time counter no longer
// matters because we test for presence, not byte-equality. Programs without a known
// marker fall back to content-change detection in Poll. Extend the slice when an agent's
// UI changes; the failure mode of a stale marker is a visible "always idle", not flicker.
func busyMarkers(program string) []string {
	if isClaude(program) {
		return []string{"esc to interrupt"}
	}
	return nil
}

// markerWorking reports whether a busy marker for this program is present in the live footer
// of content. The match is confined to the footer (see footerRegion) rather than the whole
// pane, which would also match the scrolled-back transcript. Returns false for programs
// without a known marker.
func (t *Session) markerWorking(content string) bool {
	region := footerRegion(content)
	for _, m := range busyMarkers(t.program) {
		if strings.Contains(region, m) {
			return true
		}
	}
	return false
}

// isHorizontalRule reports whether line is a box-drawing horizontal border — the top or
// bottom edge of Claude's input box. Such a line is made only of horizontal dashes, box
// corners/sides, and padding, and contains a real run of dashes (so a prose line with a
// stray "│" doesn't qualify). It anchors the live footer in footerRegion.
func isHorizontalRule(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	dashes := 0
	for _, r := range line {
		switch r {
		case '─':
			dashes++
		case '╭', '╮', '╰', '╯', '│', '┌', '┐', '└', '┘', '├', '┤', ' ':
			// box corners/sides and interior padding are allowed
		default:
			return false
		}
	}
	return dashes >= 3
}

// footerRegion returns the live footer of the pane: the lines below the input box's bottom
// border. Claude renders its status hints and the variable-height agent-team selector (one
// line per teammate) there, and the busy marker sits among them — so anchoring to the box
// border, rather than a fixed bottom-N window, keeps the marker detectable no matter how many
// teammates the selector lists. Everything below the last box border is pure live chrome, so
// this still excludes the scrolled-back transcript above the box. When the pane has no border
// — a minimal footer, a non-claude agent, or a degenerate capture — it falls back to the last
// workChromeLines non-empty lines, preserving the previous behavior.
func footerRegion(content string) string {
	lines := strings.Split(content, "\n")
	lastRule := -1
	for i, line := range lines {
		if isHorizontalRule(line) {
			lastRule = i
		}
	}
	if lastRule < 0 {
		return liveChromeLines(content, workChromeLines)
	}
	return strings.Join(lines[lastRule+1:], "\n")
}

// continueProgram returns the launch command that resumes the prior conversation for
// claude, and the unchanged program for every other agent. Used only when resurrecting a
// session whose tmux pane has died, so the relaunched claude picks up where it left off
// instead of starting blank. tmux word-splits the trailing command string itself (the
// same reason "aider --model x" works), so appending " --continue" to the single program
// argv element is sufficient — no shell wrapping. The claude predicate is the same
// wrapper-aware isClaude check used by busyMarkers/detectPrompt, so an absolute path like
// /usr/local/bin/claude is recognized and the flag lands after it.
func continueProgram(program string) string {
	if isClaude(program) {
		return program + " --continue"
	}
	return program
}

// detectPrompt reports whether region (the bottom chrome of the pane) shows a prompt that
// blocks on the user's answer. Claude has two shapes: the tool-permission dialog, and any
// interactive selection (AskUserQuestion, plan approval, etc.). Matching is done against the
// flattened chrome (newlines collapsed to spaces) so a footer or sentence hard-wrapped at a
// narrow pane width is still recognized. The selection footer requires its co-occurring tokens
// ("Esc to cancel" + navigate/select) within a tight footer window, so prose merely mentioning
// "Esc to cancel" higher in the chrome cannot trip it.
//
// region is already the promptChromeLines window (see Poll); the inner flattenChrome calls
// re-window it, which stays correct only while footerChromeLines <= promptChromeLines so the
// footer tokens remain reachable within region.
func detectPrompt(program, region string) bool {
	switch {
	case isClaude(program):
		if strings.Contains(flattenChrome(region, promptChromeLines),
			"No, and tell Claude what to do differently") {
			return true
		}
		footer := flattenChrome(region, footerChromeLines)
		if strings.Contains(footer, "Esc to cancel") &&
			(strings.Contains(footer, "to navigate") || strings.Contains(footer, "to select")) {
			return true
		}
		return false
	case strings.HasPrefix(program, ProgramAider):
		return strings.Contains(flattenChrome(region, promptChromeLines), "(Y)es/(N)o/(D)on't ask again")
	case strings.HasPrefix(program, ProgramGemini):
		return strings.Contains(flattenChrome(region, promptChromeLines), "Yes, allow once")
	}
	return false
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
	// mu guards sanitizedName/windowName against a deep Rename, which mutates them while
	// the metadata poll loop reads sanitizedName from a background goroutine. Rename holds
	// the write lock across its rename-session subprocess and the field swap, so a reader
	// never observes the brief window where the old session name no longer exists.
	mu sync.RWMutex
	// Initialized by NewSession
	//
	// The name of the tmux session and the sanitized name used for tmux commands.
	sanitizedName string
	// windowName is the original, human-readable name, used as the tmux window
	// name (-n) so windows aren't shown under the sanitized session name or
	// auto-renamed to the running program.
	windowName string
	program    string
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

var whiteSpaceRegex = regexp.MustCompile(`\s+`)

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

// liveChromeLines returns the last n non-empty lines of the pane — the region where
// Claude renders its live status bar, prompt, and input box. Marker detection must be
// confined here: capture-pane returns the whole visible pane including the scrolled-back
// transcript, so the same strings ("esc to interrupt", a prompt footer) can appear in the
// conversation body, and only their presence in the bottom chrome reflects the live state.
func liveChromeLines(content string, n int) string {
	lines := strings.Split(content, "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append(kept, lines[i])
		}
	}
	// kept is collected bottom-up; reverse to natural top-to-bottom reading order so callers
	// that reconstruct wrapped multi-line text (flattenChrome) join the lines in the order
	// they were rendered. Substring callers (busy markers) are order-independent.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	return strings.Join(kept, "\n")
}

// flattenChrome collapses the last n non-empty lines into one whitespace-normalized line.
// A prompt's key-hint footer ("Enter to select · … · Esc to cancel") and the permission
// dialog's decline option wrap across physical lines at a narrow pane width; flattening
// (whiteSpaceRegex already spans newlines) reconstructs them so the substring/token matches
// survive the wrap instead of silently leaving a waiting session classified as idle.
func flattenChrome(content string, n int) string {
	return whiteSpaceRegex.ReplaceAllString(liveChromeLines(content, n), " ")
}

// Window sizes for marker detection within the bottom chrome. The working status bar is
// the last line, so a tight window is safest; a prompt block (question + options + footer,
// possibly with a todo tracker below) needs a taller one.
const (
	workChromeLines   = 3
	promptChromeLines = 15
	// footerChromeLines is the tight window for a prompt's key-hint footer. The footer wraps
	// across at most a couple of physical lines at a narrow pane width, so a small window
	// reconstructs it while keeping prose higher in the chrome from tripping detection.
	footerChromeLines = 3
)

// toSanitizedName converts an instance title into the managed tmux session name:
// whitespace stripped, dots replaced (tmux would do it anyway), and the active
// brand prefix (see Prefix) applied. It produces the value held in Session.sanitizedName.
func toSanitizedName(str string) string {
	str = whiteSpaceRegex.ReplaceAllString(str, "")
	str = strings.ReplaceAll(str, ".", "_") // tmux replaces all . with _
	return fmt.Sprintf("%s%s", Prefix(), str)
}

// NewSession creates a new Session with the given name and program.
func NewSession(name string, program string) *Session {
	return newSession(name, program, MakePtyFactory(), cmd.MakeExecutor())
}

// NewSessionWithDeps creates a new Session with provided dependencies for testing.
func NewSessionWithDeps(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return newSession(name, program, ptyFactory, cmdExec)
}

func newSession(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *Session {
	return &Session{
		sanitizedName: toSanitizedName(name),
		windowName:    name,
		program:       program,
		ptyFactory:    ptyFactory,
		cmdExec:       cmdExec,
		captureErrLog: log.NewEvery(60 * time.Second),
		monitor:       newStatusMonitor(program),
	}
}

// Start creates and starts a new tmux session, then attaches to it. Program is the command to run in
// the session (ex. claude). workdir is the git worktree directory.
func (t *Session) Start(workDir string) error {
	return t.start(workDir, t.program)
}

// StartContinue starts the session resuming the prior conversation when the program
// supports it (claude --continue). It is used only on resurrection — the agent process
// died and we are relaunching it — never on PTY reattach (Restore), where the process is
// still alive. The continue command is computed transiently; t.program, the value
// persisted via Instance, is never mutated.
func (t *Session) StartContinue(workDir string) error {
	return t.start(workDir, continueProgram(t.program))
}

// start creates a new detached tmux session running program in workDir, then attaches.
func (t *Session) start(workDir string, program string) error {
	// Check if the session already exists
	if t.DoesSessionExist() {
		return fmt.Errorf("tmux session already exists: %s", t.sanitizedName)
	}

	// Inject the authoritative status hooks for claude (a no-op for other agents or when
	// --settings is unsupported). The settings path is appended to the launch command only;
	// t.program (the persisted value) is never mutated. A failure here just disables hooks —
	// the launch still proceeds on the scrape classifier.
	if settingsPath, err := ensureHookSettings(t.sanitizedName, t.program); err != nil {
		log.ErrorLog.Printf("status hooks disabled for %s: %v", t.sanitizedName, err)
	} else if settingsPath != "" {
		program = program + " --settings " + settingsPath
	}

	// Create a new detached tmux session and start claude in it. -n gives the
	// window the human-readable title (the conf disables auto-rename).
	cmd := tmuxCommand("new-session", "-d", "-s", t.sanitizedName, "-c", workDir, "-n", t.windowName, program)

	ptmx, err := t.ptyFactory.Start(cmd)
	if err != nil {
		// Cleanup any partially created session if any exists.
		if t.DoesSessionExist() {
			cleanupCmd := tmuxCommand("kill-session", "-t", t.sanitizedName)
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
			if cleanupErr := t.Close(); cleanupErr != nil {
				err = fmt.Errorf("%w (cleanup error: %w)", err, cleanupErr)
			}
			return fmt.Errorf("timed out waiting for tmux session %s: %w", t.sanitizedName, err)
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

// containsStartupGate reports whether the pane is showing a one-time setup/trust
// gate that intercepts keystrokes (claude's trust-folder or new-MCP-server screen,
// or the non-claude documentation-url screen). Keystrokes sent while a gate is up
// are consumed by the gate rather than the agent's input box.
func containsStartupGate(program, content string) bool {
	if isClaude(program) {
		return strings.Contains(content, "Do you trust the files in this folder?") ||
			strings.Contains(content, "new MCP server")
	}
	return strings.Contains(content, "Open documentation url for more info")
}

// CheckAndHandleTrustPrompt checks the pane content once for a trust prompt and dismisses it if found.
// Returns true if the prompt was found and handled.
func (t *Session) CheckAndHandleTrustPrompt() bool {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false
	}

	if !containsStartupGate(t.program, content) {
		return false
	}

	if isClaude(t.program) {
		if err := t.TapEnter(); err != nil {
			log.ErrorLog.Printf("could not tap enter on trust/MCP screen: %v", err)
		}
	} else {
		if err := t.TapDAndEnter(); err != nil {
			log.ErrorLog.Printf("could not tap enter on trust screen: %v", err)
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
	return !containsStartupGate(t.program, content)
}

// Restore attaches to an existing session and restores the window size
func (t *Session) Restore() error {
	ptmx, err := t.ptyFactory.Start(tmuxCommand("attach-session", "-t", t.sanitizedName))
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
	// TODO: this allocation sucks since the string is probably large. Ideally, we hash the string directly.
	h.Write([]byte(s))
	return h.Sum(nil)
}

// TapEnter sends an enter keystroke to the tmux pane.
func (t *Session) TapEnter() error {
	_, err := t.ptmx.Write([]byte{0x0D})
	if err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

// TapDAndEnter sends 'D' followed by an enter keystroke to the tmux pane.
func (t *Session) TapDAndEnter() error {
	_, err := t.ptmx.Write([]byte{0x44, 0x0D})
	if err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

// SendKeys writes raw bytes to the session's pty, as if the user typed them.
func (t *Session) SendKeys(keys string) error {
	_, err := t.ptmx.Write([]byte(keys))
	return err
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
	// Match only within the bottom chrome so the same strings in the scrolled-back
	// transcript (e.g. the agent discussing these UIs) don't false-trigger.
	if detectPrompt(t.program, liveChromeLines(content, promptChromeLines)) {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PanePrompt
		t.monitor.logSignal(name, "prompt → needs-input")
		return PanePrompt
	}

	// A live busy marker is the one positive proof of work, and the only signal that raises
	// working. Anchoring it to the footer (markerWorking → footerRegion) keeps it reliable
	// even under a multi-agent team selector. Raising only on the marker is what kills the
	// flicker: a stuck state file or an idle repaint can never flip the indicator back to
	// working once it has settled to idle — only the marker returning can.
	hasMarker := busyMarkers(t.program) != nil
	if hasMarker && t.markerWorking(content) {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneWorking
		t.monitor.logSignal(name, "marker → working")
		return PaneWorking
	}

	if hasMarker {
		// Claude: the marker is absent. The hook state file is authoritative for *idle*: a
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

	// No known marker for this program (aider/gemini): fall back to content-change detection
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
	if detectPrompt(t.program, liveChromeLines(content, promptChromeLines)) {
		t.monitor.lastReported = PanePrompt
		t.monitor.logSignal(name, "prompt → needs-input")
		return PanePrompt
	}
	// A present busy marker positively proves work; the hook state file is the next-best
	// authority (and is the only signal during a marker-absent between-turns gap).
	if busyMarkers(t.program) != nil && t.markerWorking(content) {
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
	if busyMarkers(t.program) == nil {
		// No level signal and no hook file; defer to the tick loop's content-change path.
		return PaneUnknown
	}
	// Claude with no hook file yet (e.g. before the first event): the marker is absent here,
	// so face value is idle.
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
	t.attachCh = make(chan struct{})
	t.killRequested = false
	t.detachReason = DetachQuit

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

// DetachSafely disconnects from the current tmux session without panicking
func (t *Session) DetachSafely() error {
	// Only detach if we're actually attached
	if t.attachCh == nil {
		return nil // Already detached
	}

	var errs []error

	// Close the attached pty session.
	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing attach pty session: %w", err))
		}
		t.ptmx = nil
	}

	// Clean up attach state
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
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

	if len(errs) > 0 {
		return fmt.Errorf("errors during detach: %v", errs)
	}
	return nil
}

// Detach disconnects from the current tmux session. It panics if detaching fails. At the moment, there's no
// way to recover from a failed detach.
func (t *Session) Detach() {
	// TODO: control flow is a bit messy here. If there's an error,
	// I'm not sure if we get into a bad state. Needs testing.
	defer func() {
		close(t.attachCh)
		t.attachCh = nil
		t.cancel = nil
		t.ctx = nil
		t.wg = nil
	}()

	// Close the attached pty session.
	err := t.ptmx.Close()
	if err != nil {
		// This is a fatal error. We can't detach if we can't close the PTY. It's better to just panic and have the
		// user re-invoke the program than to ruin their terminal pane.
		msg := fmt.Sprintf("error closing attach pty session: %v", err)
		log.ErrorLog.Println(msg)
		panic(msg)
	}
	// Attach goroutines should die on EOF due to the ptmx closing. Call
	// t.Restore to set a new t.ptmx.
	if err = t.Restore(); err != nil {
		// This is a fatal error. Our invariant that a started Session always has a valid ptmx is violated.
		msg := fmt.Sprintf("error closing attach pty session: %v", err)
		log.ErrorLog.Println(msg)
		panic(msg)
	}

	// Cancel goroutines created by Attach.
	t.cancel()
	t.wg.Wait()
}

// Close terminates the tmux session and cleans up resources
func (t *Session) Close() error {
	// Remove the per-session status-hook artifacts; harmless if the session never had any.
	cleanupHookSession(t.snapshotName())

	var errs []error

	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing PTY: %w", err))
		}
		t.ptmx = nil
	}

	cmd := tmuxCommand("kill-session", "-t", t.sanitizedName)
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

// updateWindowSize updates the window size of the PTY.
func (t *Session) updateWindowSize(cols, rows int) error {
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
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := tmuxCommand("has-session", fmt.Sprintf("-t=%s", t.snapshotName()))
	return t.cmdExec.Run(existsCmd) == nil
}

// snapshotName reads sanitizedName under the read lock so background polling can't race
// the in-place field swap a deep Rename performs.
func (t *Session) snapshotName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sanitizedName
}

// CapturePaneContent captures the content of the tmux pane
func (t *Session) CapturePaneContent() (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := tmuxCommand("capture-pane", "-p", "-e", "-J", "-t", t.snapshotName())
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("error capturing pane content: %w", err)
	}
	return string(output), nil
}

// CapturePaneContentWithOptions captures the pane content with additional options
// start and end specify the starting and ending line numbers (use "-" for the start/end of history)
func (t *Session) CapturePaneContentWithOptions(start, end string) (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := tmuxCommand("capture-pane", "-p", "-e", "-J", "-S", start, "-E", end, "-t", t.snapshotName())
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane content with options: %w", err)
	}
	return string(output), nil
}

// CleanupSessions kills all tmux sessions that start with "session-"
func CleanupSessions(cmdExec cmd.Executor) error {
	// This is the `reset` path: wipe the entire status-hooks tree alongside the sessions.
	cleanupAllHookSessions()

	// First try to list sessions
	cmd := tmuxCommand("ls")
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
		if err := cmdExec.Run(tmuxCommand("kill-session", "-t", match)); err != nil {
			return fmt.Errorf("failed to kill tmux session %s: %w", match, err)
		}
	}
	return nil
}
