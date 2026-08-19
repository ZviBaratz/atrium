package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/internal/handover"
	"github.com/ZviBaratz/atrium/log"

	tea "charm.land/bubbletea/v2"
)

// `output: terminal` — a custom command that owns the terminal for as long as it runs
// (#375 stage C), for the verbs a captured buffer would be useless for: lazygit, an
// editor, a pager, a build the user means to watch.
//
// It is NOT a tea.ExecProcess. Suspending the event loop is the same act a tmux attach
// performs, and it carries the same obligations, none of them local to the code
// that suspends:
//
//  1. m.attachGen++ — pane-capture commands stamp the generation they were created
//     under and are dropped if it has moved. Without the bump, a metadataUpdateDoneMsg
//     captured BEFORE a three-minute command is applied AFTER it, replaying its
//     PanePrompt as a TapEnter against whatever dialog is on screen now — silently
//     accepting a plan-approval screen that auto-yes deliberately never answers.
//  2. An attachKeeper — a suspended loop stops deliverReadyPrompts and ApplyPaneState
//     FLEET-WIDE, and the autoyes daemon cannot cover the gap: it runs only while no TUI
//     is alive, and a suspended TUI is alive. Without the keeper, every autoyes session
//     sits on an unanswered permission dialog for the whole command. That is #264
//     reintroduced, for a duration the user configures in config.json.
//  3. repaintAfterAttach on return — tea.Exec's RestoreTerminal does only a soft
//     diff-cache repaint, and a child that drew full-screen leaves it stale.
//
// The keeper's excluded instance is nil: unlike an attach, no session is holding the
// terminal, so every one of them is the keeper's business.

// customCommandTerminalDoneMsg reports a finished terminal-mode run.
//
// tea.Exec always invokes its callback — on a failure to start as well as on a clean
// exit — so unlike the background path this message needs no fallback to guarantee the
// single-flight slot is released. It carries the keeper's results out of the suspended
// window for the same reason attachFinishedMsg does: the keeper cannot persist, because
// persistence is main-loop-owned.
type customCommandTerminalDoneMsg struct {
	key  string
	desc string
	err  error
	// restoreErr is bubbletea's failure to reclaim the terminal after the command, which
	// is a fact about the terminal rather than about the command — see restoreErrOf.
	restoreErr      error
	keeperDelivered bool
	keeperErrs      []string
}

// runTerminalCustomCommand is the exec leg of a terminal-mode custom command.
//
// A package var for the same reason runCustomCommand is one: a gate that stopped the
// notice but not the subprocess would satisfy every assertion about the screen while
// still running the user's shell command, so the tests need to see the spawn itself.
var runTerminalCustomCommand = execTerminalCustomCommand

// startTerminalCustomCommand hands the terminal to a resolved command via the attach
// path's suspend-the-loop seam.
func (m *home) startTerminalCustomCommand(spec customCommandSpec) tea.Cmd {
	return tea.Exec(m.terminalCustomCommandExec(spec))
}

// terminalCustomCommandExec builds the suspension and the callback that reports it
// finished.
//
// Split out from the tea.Exec call for one reason: tea.Exec wraps both of these in a
// message type bubbletea does not export, so a test holding the returned tea.Cmd can see
// nothing about what was wired. The obligations listed at the top of this file are the
// whole substance of terminal mode, and every one of them lives in the value this
// returns — an untestable constructor would leave them provable only by hand.
// TestCustomCommandTerminalWiresEveryAttachObligation enumerates them and is the count's
// one home; #760 added a fourth (the handover payload) and this doc undercounted it until
// the numbers came out of the prose.
//
// Everything the child needs is already a string in spec, resolved on the update thread.
// Nothing here closes over an *Instance: the keeper takes a main-thread copy of the
// instance list, and membership cannot change while the loop is suspended.
func (m *home) terminalCustomCommandExec(spec customCommandSpec) (*attachCommand, tea.ExecCallback) {
	ctx := m.ctx
	keeper := newAttachKeeper(ctx, slices.Clone(m.list.GetInstances()), nil)
	// outcome is written inside attach (on the suspended event-loop goroutine) and read
	// in the callback, which bubbletea evaluates in a goroutine it spawns only after Run
	// returns — the same ordering rawModeFailed relies on, so neither needs a lock.
	var outcome func() error
	cmd := &attachCommand{
		attach: func() (chan struct{}, error) {
			done, out, err := runTerminalCustomCommand(ctx, spec)
			outcome = out
			return done, err
		},
		// raw: false is the load-bearing half of this. See attachCommand.raw: a cooked
		// child gets ONLCR, so its output does not staircase, and it gets ISIG, so
		// Ctrl+C reaches it — which is why Run borrows SIGINT for the duration.
		raw:    false,
		keeper: keeper,
		// The description rather than the key, because that is what the user named the
		// command; the key is the keystroke that reached it. Empty desc falls back to the
		// key, and an empty label renders as no label at all (handover.Payload.Describe).
		handover: handover.Payload{Kind: handover.KindCommand, Label: cmp.Or(spec.desc, spec.key)},
		// Runs on the suspended event-loop goroutine, so the bump is ordered before
		// every parked message the resumed loop processes.
		onAttached: func() { m.attachGen++ },
	}
	return cmd, func(teaErr error) tea.Msg {
		// teaErr is NOT simply "the error Run returned". bubbletea's Program.exec ends its
		// success path with `err := p.RestoreTerminal(); go p.Send(fn(err))` — so on a
		// command that ran to completion this argument carries the RESTORE error, and it
		// can also arrive from a releaseTerminal failure before Run was ever called.
		//
		// So `outcome`, not teaErr, is what discriminates. It is set only once the child
		// was launched, which makes its nilness the exact test for "did this ever start":
		//
		//   outcome != nil → it ran; its own result is the command's outcome, whatever
		//                    the terminal did afterwards.
		//   outcome == nil → it never started; teaErr is the launch failure.
		//
		// Reading teaErr as authoritative discarded a real exit status every time restore
		// reported a problem — reporting "did not finish" for a build that exited 4, which
		// is the one thing this message exists to say.
		var err error
		if outcome != nil {
			err = outcome()
		} else {
			err = teaErr
		}
		return customCommandTerminalDoneMsg{
			key: spec.key, desc: spec.desc, err: err,
			// Carried separately because it is a fact about the TERMINAL, not about the
			// command: a failed restore can leave the frame wedged in a way the hard
			// repaint on return may not fix, and attributing it to the user's command
			// would be a lie in both directions.
			restoreErr:      restoreErrOf(teaErr, outcome),
			keeperDelivered: keeper.delivered,
			keeperErrs:      keeper.errs,
		}
	}
}

// restoreErrOf isolates the terminal-restore error from the callback's single argument.
// It is teaErr exactly when the command ran (so teaErr cannot be a launch failure) and
// nil otherwise — the launch-failure case reports through err instead.
func restoreErrOf(teaErr error, outcome func() error) error {
	if outcome == nil {
		return nil
	}
	return teaErr
}

// execTerminalCustomCommand starts the script on the terminal Bubble Tea just released.
//
// It returns a channel closed once the process has exited AND been recorded, plus a
// function reporting the outcome — valid only after that channel closes. The split is
// what the seam is for: attachCommand.Run must start the keeper BETWEEN the spawn and
// the wait, or the fleet goes unserviced for exactly the window the keeper exists to
// cover.
//
// os.Stdin/Stdout/Stderr directly, not the streams Bubble Tea offers through the Set*
// methods, matching the tmux attach — the child is talking to the real terminal, and a
// pipe in the middle would break every full-screen program this mode exists for.
//
// Bound to ctx, which is the app's, so quitting cancels a runaway. No wall-clock
// timeout, on purpose: `just ci` legitimately runs for minutes.
func execTerminalCustomCommand(ctx context.Context, spec customCommandSpec) (chan struct{}, func() error, error) {
	c := exec.CommandContext(ctx, "sh", "-c", spec.script)
	c.Dir = spec.dir
	c.Env = append(os.Environ(), spec.env...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr

	start := time.Now()
	if err := c.Start(); err != nil {
		return nil, nil, err
	}

	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = c.Wait()
		// Record the synthetic argv, NEVER the rendered script — cmdlog.Redact models
		// one NAME=VALUE per argv token and a whole shell script in a single token
		// defeats it in both directions. See customcmd.Command.LogArgv.
		c.Args = spec.argv
		// nil output, which is the honest record for this mode: the command wrote to the
		// terminal the user was watching, and there is no buffer to store. The exit code
		// still lands, which is what makes "every execution is recorded" true here.
		cmdlog.RecordCmd(c, spec.session, start, nil, runErr)
		if runErr != nil {
			log.ErrorLog.Printf("custom command %q failed: %v", spec.key, runErr)
		}
	}()
	// The close/receive pair in attachCommand.Run orders this write before every read of
	// it, so the outcome needs no lock.
	return done, func() error { return runErr }, nil
}

// handleCustomCommandTerminalDone resumes the app after a terminal-mode command.
//
// It owes everything handleAttachFinished owes for the same reason — the loop was
// suspended, so the whole fleet is stale and the frame is whatever the child left on the
// screen — which is why the shared tail is resumeAfterSuspendedLoop rather than a second
// copy of it.
func (m *home) handleCustomCommandTerminalDone(msg customCommandTerminalDoneMsg) (tea.Model, tea.Cmd) {
	m.runningCustomCommand = ""
	// The renderer restored the title and progress bar it cleared for the takeover; this
	// refreshes the counts, which may have moved while the loop was suspended.
	m.refreshOSChrome(false)
	// The keeper cleared prompts while the loop was suspended — delivered, or abandoned
	// with their budget spent — but it cannot persist. Mirror the attach path.
	if msg.keeperDelivered || len(msg.keeperErrs) > 0 {
		if err := m.persistInstances(); err != nil {
			log.ErrorLog.Printf("failed to persist after keeper prompt delivery: %v", err)
		}
	}
	var cmds []tea.Cmd
	if surfaced := customCommandTerminalError(msg); surfaced != nil {
		cmds = append(cmds, m.handleError(surfaced))
	}
	return m, m.resumeAfterSuspendedLoop(cmds...)
}

// customCommandTerminalError joins everything the return owes the user into ONE error.
//
// One, because handleError writes a single notice: two calls in the same batch would
// leave the second silently overwriting the first. Joined in this order because the exit
// status is the thing the user just watched happen and the lost prompts are the thing
// they cannot see. errors.Join returns nil for an empty set, which is the quiet
// success path.
func customCommandTerminalError(msg customCommandTerminalDoneMsg) error {
	var parts []error
	if msg.err != nil {
		// The full error, with everything the bounded row cannot carry, still reaches
		// the log.
		log.ErrorLog.Printf("custom command %q (%s) failed: %v", msg.key, msg.desc, msg.err)
		parts = append(parts, errors.New(customCommandExitNotice(msg.desc, msg.err)))
	}
	if msg.restoreErr != nil {
		// Surfaced rather than logged only, because the repaint on return may not be
		// enough: if Bubble Tea could not reclaim stdin, the app is drawing into a
		// terminal it no longer controls and no amount of redrawing fixes that. It is
		// its own sentence because it is not the command's fault.
		log.ErrorLog.Printf("failed to reclaim the terminal after custom command %q: %v",
			msg.key, msg.restoreErr)
		// customCommandLabel already quotes and bounds the description.
		parts = append(parts, fmt.Errorf("the terminal could not be fully reclaimed after "+
			"%s: %w", customCommandLabel(msg.desc), msg.restoreErr))
	}
	if len(msg.keeperErrs) > 0 {
		parts = append(parts, errors.New(strings.Join(msg.keeperErrs, "\n")))
	}
	return errors.Join(parts...)
}

// customCommandExitNotice is the terminal-mode failure toast, bounded exactly as the
// background one is: through customCommandLabel, with the reason LAST.
//
// It exists because terminal mode's stderr went to the tty but its exit code did not: a
// non-zero exit is an *exec.ExitError visible to us and invisible to a user who watched a
// screenful of output scroll past and cannot now tell whether it ended well.
//
// Note where that error comes from, because the obvious answer is wrong and shipped a bug:
// NOT from the ExecCallback's argument, which on a command that ran to completion carries
// bubbletea's terminal-RESTORE error instead. It comes from the run's own recorded outcome
// — see terminalCustomCommandExec.
func customCommandExitNotice(desc string, err error) string {
	return customCommandLabel(desc) + customCommandExitTail(err)
}

// customCommandExitTail names the outcome in a bounded way.
//
// Three cases, because the two that ExitCode() cannot number are not the same event and
// reading as though they were is what makes a message unhelpful:
//
//   - a status: " exited 2".
//   - a signal: ExitCode() is -1, and the ORDINARY way to get here is the user's own
//     Ctrl+C. " exited -1" reads as a bug in Atrium, and "see the log" sends them to a
//     record holding exit -1 and — by this mode's design — no output at all. So it names
//     the interruption and points nowhere.
//   - anything else: the command never ran, and the log genuinely has the reason.
func customCommandExitTail(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return fmt.Sprintf(customCommandExitedTailFmt, code)
		}
		return customCommandInterruptedTail
	}
	return customCommandDiedTail
}

const (
	// customCommandExitedTailFmt is the tail for a command that exited with a status.
	// A wait status is 0-255, so three digits is the widest instantiation — which is
	// what TestCustomCommandRefusalsFitARow measures, never the format string.
	customCommandExitedTailFmt = " exited %d"
	// customCommandInterruptedTail covers a signal death — overwhelmingly the user's own
	// Ctrl+C. It deliberately points at no log record: this mode captures no output, so the
	// record it would name holds an exit of -1 and nothing to read.
	customCommandInterruptedTail = " was interrupted"
	// customCommandDiedTail covers a command that never ran at all, where the log does
	// carry the reason.
	customCommandDiedTail = " could not run — see the log"
)
