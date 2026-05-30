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

const ProgramClaude = "claude"

const ProgramAider = "aider"
const ProgramGemini = "gemini"

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
	if strings.HasSuffix(program, ProgramClaude) {
		return []string{"esc to interrupt"}
	}
	return nil
}

// continueProgram returns the launch command that resumes the prior conversation for
// claude, and the unchanged program for every other agent. Used only when resurrecting a
// session whose tmux pane has died, so the relaunched claude picks up where it left off
// instead of starting blank. tmux word-splits the trailing command string itself (the
// same reason "aider --model x" works), so appending " --continue" to the single program
// argv element is sufficient — no shell wrapping. The claude predicate matches the same
// HasSuffix check used by busyMarkers/detectPrompt, so an absolute path like
// /usr/local/bin/claude is recognized and the flag lands after it.
func continueProgram(program string) string {
	if strings.HasSuffix(program, ProgramClaude) {
		return program + " --continue"
	}
	return program
}

// detectPrompt reports whether region (the bottom chrome of the pane) shows a prompt that
// blocks on the user's answer. Claude has two shapes: the tool-permission dialog, and any
// interactive selection (AskUserQuestion, plan approval, etc.). The selection footer is
// matched by its co-occurring tokens on one line ("Esc to cancel" + navigate/select) so a
// sentence merely mentioning "Esc to cancel" cannot trip it.
func detectPrompt(program, region string) bool {
	switch {
	case strings.HasSuffix(program, ProgramClaude):
		if strings.Contains(region, "No, and tell Claude what to do differently") {
			return true
		}
		for _, line := range strings.Split(region, "\n") {
			if strings.Contains(line, "Esc to cancel") &&
				(strings.Contains(line, "to navigate") || strings.Contains(line, "to select")) {
				return true
			}
		}
		return false
	case strings.HasPrefix(program, ProgramAider):
		return strings.Contains(region, "(Y)es/(N)o/(D)on't ask again")
	case strings.HasPrefix(program, ProgramGemini):
		return strings.Contains(region, "Yes, allow once")
	}
	return false
}

// TmuxSession represents a managed tmux session
type TmuxSession struct {
	// mu guards sanitizedName/windowName against a deep Rename, which mutates them while
	// the metadata poll loop reads sanitizedName from a background goroutine. Rename holds
	// the write lock across its rename-session subprocess and the field swap, so a reader
	// never observes the brief window where the old session name no longer exists.
	mu sync.RWMutex
	// Initialized by NewTmuxSession
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

	// Initialized by Attach
	// Deinitilaized by Detach
	//
	// Channel to be closed at the very end of detaching. Used to signal callers.
	attachCh chan struct{}
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

// TmuxPrefix is the prefix applied to every Atrium-managed tmux session name. It
// derives from config.RuntimeName so legacy installs keep the "claudesquad_"
// prefix and can still find and clean up their pre-rebrand sessions.
func TmuxPrefix() string {
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
	return strings.Join(kept, "\n")
}

// Window sizes for marker detection within the bottom chrome. The working status bar is
// the last line, so a tight window is safest; a prompt block (question + options + footer,
// possibly with a todo tracker below) needs a taller one.
const (
	workChromeLines   = 3
	promptChromeLines = 15
)

func toClaudeSquadTmuxName(str string) string {
	str = whiteSpaceRegex.ReplaceAllString(str, "")
	str = strings.ReplaceAll(str, ".", "_") // tmux replaces all . with _
	return fmt.Sprintf("%s%s", TmuxPrefix(), str)
}

// NewTmuxSession creates a new TmuxSession with the given name and program.
func NewTmuxSession(name string, program string) *TmuxSession {
	return newTmuxSession(name, program, MakePtyFactory(), cmd.MakeExecutor())
}

// NewTmuxSessionWithDeps creates a new TmuxSession with provided dependencies for testing.
func NewTmuxSessionWithDeps(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *TmuxSession {
	return newTmuxSession(name, program, ptyFactory, cmdExec)
}

func newTmuxSession(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *TmuxSession {
	return &TmuxSession{
		sanitizedName: toClaudeSquadTmuxName(name),
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
func (t *TmuxSession) Start(workDir string) error {
	return t.start(workDir, t.program)
}

// StartContinue starts the session resuming the prior conversation when the program
// supports it (claude --continue). It is used only on resurrection — the agent process
// died and we are relaunching it — never on PTY reattach (Restore), where the process is
// still alive. The continue command is computed transiently; t.program, the value
// persisted via Instance, is never mutated.
func (t *TmuxSession) StartContinue(workDir string) error {
	return t.start(workDir, continueProgram(t.program))
}

// start creates a new detached tmux session running program in workDir, then attaches.
func (t *TmuxSession) start(workDir string, program string) error {
	// Check if the session already exists
	if t.DoesSessionExist() {
		return fmt.Errorf("tmux session already exists: %s", t.sanitizedName)
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
	if strings.HasSuffix(program, ProgramClaude) {
		return strings.Contains(content, "Do you trust the files in this folder?") ||
			strings.Contains(content, "new MCP server")
	}
	return strings.Contains(content, "Open documentation url for more info")
}

// CheckAndHandleTrustPrompt checks the pane content once for a trust prompt and dismisses it if found.
// Returns true if the prompt was found and handled.
func (t *TmuxSession) CheckAndHandleTrustPrompt() bool {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false
	}

	if !containsStartupGate(t.program, content) {
		return false
	}

	if strings.HasSuffix(t.program, ProgramClaude) {
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
func (t *TmuxSession) IsReadyForPrompt() bool {
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
func (t *TmuxSession) Restore() error {
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
	idleStreak int
}

func newStatusMonitor(program string) *statusMonitor {
	return &statusMonitor{program: program}
}

// idleConfirmTicks is how many consecutive idle observations are required before a
// working→idle transition is committed. At the 500ms metadata tick this is ~1s, which
// absorbs the brief output gap at the end of a turn (and streaming pauses for agents on
// the content-change fallback) without delaying the working indicator going up.
const idleConfirmTicks = 2

// hash hashes the string.
func (m *statusMonitor) hash(s string) []byte {
	h := sha256.New()
	// TODO: this allocation sucks since the string is probably large. Ideally, we hash the string directly.
	h.Write([]byte(s))
	return h.Sum(nil)
}

// TapEnter sends an enter keystroke to the tmux pane.
func (t *TmuxSession) TapEnter() error {
	_, err := t.ptmx.Write([]byte{0x0D})
	if err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

// TapDAndEnter sends 'D' followed by an enter keystroke to the tmux pane.
func (t *TmuxSession) TapDAndEnter() error {
	_, err := t.ptmx.Write([]byte{0x44, 0x0D})
	if err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

func (t *TmuxSession) SendKeys(keys string) error {
	_, err := t.ptmx.Write([]byte(keys))
	return err
}

// Poll classifies the current pane into a PaneState. It reads level signals (a prompt
// on screen, a busy marker, otherwise content stability) rather than treating any byte
// change as "working", which is what makes the result stable while the agent is idle.
func (t *TmuxSession) Poll() PaneState {
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

	// Track content change for the no-marker fallback. Always update so the comparison
	// is relative to the previous tick regardless of which path decided the state.
	h := t.monitor.hash(content)
	changed := !bytes.Equal(h, t.monitor.prevOutputHash)
	t.monitor.prevOutputHash = h

	// A prompt awaiting an answer takes precedence over "working": when an agent stops to
	// ask, it is not processing, and this is the state a caller most needs to surface.
	// Match only within the bottom chrome so the same strings in the scrolled-back
	// transcript (e.g. the agent discussing these UIs) don't false-trigger.
	if detectPrompt(t.program, liveChromeLines(content, promptChromeLines)) {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PanePrompt
		return PanePrompt
	}

	working := false
	if markers := busyMarkers(t.program); markers != nil {
		// The busy marker lives in the status bar (the last line), so confine the match
		// tightly to the bottom chrome rather than the whole pane.
		workRegion := liveChromeLines(content, workChromeLines)
		for _, m := range markers {
			if strings.Contains(workRegion, m) {
				working = true
				break
			}
		}
	} else {
		// No known marker for this program: fall back to content-change detection.
		working = changed
	}

	if working {
		t.monitor.idleStreak = 0
		t.monitor.lastReported = PaneWorking
		return PaneWorking
	}

	// Hysteresis: only the working→idle transition is debounced. Hold "working" for up to
	// idleConfirmTicks-1 idle observations to absorb the end-of-turn output gap; going up
	// to working stays immediate.
	if t.monitor.lastReported == PaneWorking {
		t.monitor.idleStreak++
		if t.monitor.idleStreak < idleConfirmTicks {
			return PaneWorking
		}
	}
	t.monitor.lastReported = PaneIdle
	return PaneIdle
}

// HasUpdated reports whether the agent is working and whether a prompt awaits an answer.
// It is a thin shim over Poll, kept for the daemon (which only consults hasPrompt) and
// for back-compat with existing callers.
func (t *TmuxSession) HasUpdated() (updated bool, hasPrompt bool) {
	s := t.Poll()
	return s == PaneWorking, s == PanePrompt
}

// attachInputAction is the outcome of classifying a chunk of attach stdin: keep
// forwarding it to the pty, detach, or detach-and-request-kill.
type attachInputAction int

const (
	attachForward attachInputAction = iota
	attachDetach
	attachKill
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
	return attachForward
}

// Attach connects the terminal to the tmux session and blocks (via the returned
// channel) until the user detaches. When allowKill is true, the in-session kill
// key (Ctrl+X) detaches and sets KillRequested so the caller can tear the session
// down; the Terminal-tab shell passes false so Ctrl+X stays a normal shell key.
func (t *TmuxSession) Attach(allowKill bool) (chan struct{}, error) {
	t.attachCh = make(chan struct{})
	t.killRequested = false

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
				t.Detach()
				return
			case attachKill:
				// Detach and request a kill; the caller reads KillRequested after
				// the attach returns and runs the teardown confirmation.
				t.killRequested = true
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
func (t *TmuxSession) KillRequested() bool {
	return t.killRequested
}

// DetachSafely disconnects from the current tmux session without panicking
func (t *TmuxSession) DetachSafely() error {
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
func (t *TmuxSession) Detach() {
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
		// This is a fatal error. Our invariant that a started TmuxSession always has a valid ptmx is violated.
		msg := fmt.Sprintf("error closing attach pty session: %v", err)
		log.ErrorLog.Println(msg)
		panic(msg)
	}

	// Cancel goroutines created by Attach.
	t.cancel()
	t.wg.Wait()
}

// Close terminates the tmux session and cleans up resources
func (t *TmuxSession) Close() error {
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
func (t *TmuxSession) SetDetachedSize(width, height int) error {
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
func (t *TmuxSession) updateWindowSize(cols, rows int) error {
	return pty.Setsize(t.ptmx, &pty.Winsize{
		Rows: clampUint16(rows),
		Cols: clampUint16(cols),
		X:    0,
		Y:    0,
	})
}

func (t *TmuxSession) DoesSessionExist() bool {
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := tmuxCommand("has-session", fmt.Sprintf("-t=%s", t.snapshotName()))
	return t.cmdExec.Run(existsCmd) == nil
}

// snapshotName reads sanitizedName under the read lock so background polling can't race
// the in-place field swap a deep Rename performs.
func (t *TmuxSession) snapshotName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sanitizedName
}

// CapturePaneContent captures the content of the tmux pane
func (t *TmuxSession) CapturePaneContent() (string, error) {
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
func (t *TmuxSession) CapturePaneContentWithOptions(start, end string) (string, error) {
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

	re := regexp.MustCompile(fmt.Sprintf(`%s.*:`, TmuxPrefix()))
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
