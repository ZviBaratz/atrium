package app

// tmux attach plumbing for the home model.

import (
	"io"
	"os"
	"slices"

	"github.com/ZviBaratz/atrium/internal/handover"
	"github.com/ZviBaratz/atrium/internal/lifecycle"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// isTerminal, makeRaw and restoreTerm seam term's tty calls so Run's raw-mode branches
// are testable: CI has no controlling TTY, so the real term.IsTerminal returns false and
// the branches are otherwise unreachable.
//
// restoreTerm is seamed because #375 stage C added the first test to drive the raw-mode
// SUCCESS path (asserting a raw takeover does NOT borrow SIGINT), and a fake makeRaw has
// no real *term.State to hand back. Unseamed, that test either nil-derefs inside
// term.Restore or fabricates a zeroed State — and `go test` run from a terminal has that
// terminal on stdin, so the fabricated one applies a zeroed termios to the developer's
// own tty. The seam is the only spelling that is safe wherever the suite runs.
//
// suspendInterrupt is seamed for the same reason one rung up: the alternative is a test
// that raises a process-global SIGINT at the test binary.
var (
	isTerminal       = term.IsTerminal
	makeRaw          = term.MakeRaw
	restoreTerm      = term.Restore
	suspendInterrupt = lifecycle.SuspendTerminalSignals
)

// handoverHold is seamed for the reason the term calls above are: the tests that drive
// Run need to observe that the lock was taken and released around it, and a fake makes
// the failure branch — a data dir that cannot be written — reachable at all.
var handoverHold = handover.Hold

// attachCommand adapts a blocking terminal takeover into a tea.ExecCommand so Bubble
// Tea releases the terminal before it and restores+repaints it after —
// on the event loop, via execMsg, which is the framework's supported path for a
// blocking terminal takeover. (Calling ReleaseTerminal/RestoreTerminal directly
// from inside Update blocks the event loop for the whole attach and leaves the
// renderer/input reader wedged.) For a tmux attach Run also puts stdin in raw mode for
// the duration: ReleaseTerminal restores cooked mode, where Ctrl+Q (ASCII 17 = XON)
// is swallowed by IXON flow control and never reaches the detach reader. The
// Set* methods are no-ops because the takeover copies os.Stdin/os.Stdout directly
// rather than through the streams Bubble Tea would inject.
//
// It serves two callers, and "attach" in the names below is the older of the two rather
// than the whole set: a tmux attach (attachExecCarry) and a custom command in
// `output: terminal` mode (startTerminalCustomCommand, #375). What they share is the
// suspension and everything it owes — the keeper, the attachGen bump, the hard repaint
// on return — which is exactly why the second one reuses this rather than reaching for
// tea.ExecProcess.
//
// Methods take a pointer receiver so Run's rawModeFailed write survives: tea.Exec
// holds the value as an interface and invokes Run on it after releasing the
// terminal, then hands our callback's message to a fresh goroutine (go p.Send in
// bubbletea's exec) — the go statement orders the callback's reads after Run's
// writes, so attachExec can pass a *attachCommand and read the flags back there.
type attachCommand struct {
	attach func() (chan struct{}, error)
	// raw asks Run to put stdin in raw mode for the duration. True for a tmux attach,
	// whose Ctrl+Q detach reader needs single-byte reads with IXON off.
	//
	// FALSE for a custom command's `sh -c` child, and that is a decision rather than an
	// omission: term.MakeRaw also clears OPOST/ONLCR, so a command that prints newlines
	// would staircase its output down the screen, and a child that wants raw mode
	// (lazygit, an editor) sets its own termios. The positive test is
	// TestAttachCommandRun_CookedNeverAttemptsRawMode — a decision with no test is a
	// comment.
	raw bool
	// keeper services non-attached sessions (prompt delivery, auto-yes taps) while
	// the event loop is suspended. Run starts it once the attach succeeds and joins
	// it before returning; nil is tolerated for tests that only exercise Run.
	keeper *attachKeeper
	// onAttached is called once the attach has succeeded, before the keeper starts.
	// Run executes on the suspended event-loop goroutine, so the callback may touch
	// main-loop state — attachExecCommand uses it to bump home.attachGen, retiring
	// pane-state captures taken before the keeper started rearranging panes. nil is
	// tolerated for tests that only exercise Run.
	onAttached func()
	// rawModeFailed records that raw mode couldn't be set, so the attach ran cooked
	// and Ctrl+Q detach was disabled. Read by attachExec's callback after Run returns.
	rawModeFailed bool
	// handover names what the terminal is being handed to, so Run can publish it for
	// the headless commands. The zero value is honoured: a Run with nothing to say
	// still takes the lock, because whether the loop is parked is the fact that
	// matters and the label only decorates the message (internal/handover).
	handover handover.Payload
}

func (a *attachCommand) Run() error {
	// Publish the suspension for the whole of Run, which is the whole life of the child
	// holding the terminal and so the span in which neither outbox spool is drained.
	// bubbletea's Program.exec blocks its event loop for a little longer than this at each
	// end — it releases the terminal before calling Run and re-captures it after — so the
	// lock is the inner span, not the outer one. Registered as the first defer so it
	// releases LAST: the loop does not resume until Run returns, so the lock should
	// outlive the raw-mode restore, not the other way round.
	//
	// A failure to take it is logged and ignored: handing over the terminal must not
	// depend on a lock file. What that costs is the handover going unobserved for this
	// attach, which leaves `atrium new` unable to warn about it and — since drainState
	// reads a live TUI with a free handover lock as drainLive — able to tell a --wait
	// caller the outbox is being read when it is not.
	// (Nothing here reads or writes pane state or any home field, so the staleness
	// hazard the attach path carries — see home.attachGen — is not in play.)
	if release, err := handoverHold(a.handover); err == nil {
		defer release()
	} else {
		log.WarningLog.Printf("failed to record the terminal handover; `atrium new` cannot report it: %v", err)
	}
	if fd := int(os.Stdin.Fd()); a.raw && isTerminal(fd) {
		if oldState, err := makeRaw(fd); err == nil {
			defer func() { _ = restoreTerm(fd, oldState) }()
		} else {
			// Stay in cooked mode where IXON swallows Ctrl+Q, so detach won't work and
			// the attach looks like a hang. Record it so attachFinishedMsg can surface a
			// modal on return, and log a breadcrumb (to the file, not the tmux-owned
			// terminal) for the case where the user kills Atrium instead of detaching.
			a.rawModeFailed = true
			log.WarningLog.Printf("failed to set raw mode for attach; Ctrl+Q detach may not work: %v", err)
		}
	}
	// A child that asked for the terminal in cooked mode owns Ctrl+C. Cooked mode leaves
	// ISIG on and the child is in our process group, so the kernel delivers that SIGINT to
	// Atrium as well — where the root context is wired to it and passed to
	// tea.WithContext, so it would shut the app down. Borrowing the terminal's signals for
	// the duration leaves the interrupt to the child, which is what the user meant by it.
	// SIGTERM and SIGHUP are never borrowed.
	//
	// Keyed on the REQUEST (!a.raw), not on the outcome, and deliberately NOT extended to
	// `|| a.rawModeFailed`. An attach that asked for raw mode and could not get it is
	// running cooked too, so extending it looks strictly safer and is strictly worse:
	// tmux sets SIGINT to SIG_IGN in both client and server, so Ctrl+C would reach nobody
	// at all — and that attach is the one where Ctrl+Q is swallowed by IXON, keys are
	// line-buffered, and handleAttachFinished's own modal concedes tmux's prefix "may not
	// register on its own". Ctrl+C quitting Atrium is that user's last way out of a
	// session they cannot detach from, and it must stay.
	if cooked := !a.raw; cooked {
		defer suspendInterrupt()()
	}
	ch, err := a.attach()
	if err != nil {
		return err
	}
	if a.onAttached != nil {
		a.onAttached()
	}
	if a.keeper != nil {
		// Run executes on the suspended event-loop goroutine, so starting here gives
		// the keeper a happens-before edge from everything the main loop did, and the
		// deferred stop-and-join orders everything the keeper did before the loop
		// resumes. Do not move the keeper's lifetime out of Run: messages queued
		// mid-attach can be processed before attachFinishedMsg.
		a.keeper.start()
		defer a.keeper.stop()
	}
	<-ch
	return nil
}

func (a *attachCommand) SetStdin(io.Reader) {}

func (a *attachCommand) SetStdout(io.Writer) {}

func (a *attachCommand) SetStderr(io.Writer) {}

// attachExec hands the terminal to a tmux attach via tea.Exec and reports the
// outcome as an attachFinishedMsg once the user detaches. killTarget is the
// attached instance whose in-session Ctrl+X kill request the handler should honor
// on detach, or nil when the attach has no kill key (the terminal tab).
func (m *home) attachExec(attach func() (chan struct{}, error), killTarget *session.Instance) tea.Cmd {
	return m.attachExecCarry(attach, killTarget, nil)
}

// attachExecCarry is attachExec with keeper errors carried over from a previous
// attach in the same sibling-cycle chain: the cycle branch of handleAttachFinished
// re-attaches without reaching the error surfacing, so it seeds the next keeper
// with the losses and the chain's final plain detach surfaces all of them.
func (m *home) attachExecCarry(attach func() (chan struct{}, error), killTarget *session.Instance, carriedErrs []string) tea.Cmd {
	return tea.Exec(m.attachExecCommand(attach, killTarget, carriedErrs))
}

// attachExecCommand builds the suspension and the callback that reports it finished.
//
// Split out from the tea.Exec call for the reason terminalCustomCommandExec gives: tea.Exec
// wraps both of these in a message type bubbletea does not export, so a test holding the
// returned tea.Cmd can see nothing about what was wired. The handover payload is why this
// split arrived late — it is the one field here whose whole purpose is to be read by
// another process, and deleting the line that sets it changed nothing any test could see.
func (m *home) attachExecCommand(attach func() (chan struct{}, error), killTarget *session.Instance, carriedErrs []string) (*attachCommand, tea.ExecCallback) {
	// Attaching is the strongest form of visiting: clear the unread state before
	// handing the terminal over. killTarget is nil only for the terminal tab,
	// which the selection dwell covers instead.
	if killTarget != nil {
		killTarget.MarkSeen()
	}
	// Pass a pointer so Run's writes (rawModeFailed, the keeper's results) are
	// visible here: bubbletea runs Run to completion and only then spawns the
	// goroutine that evaluates this callback (go p.Send in exec), so the reads are
	// ordered after the writes (no race). The keeper gets a main-thread copy of the
	// instance list; membership can't change while the loop is suspended, and the
	// keeper re-checks Started/Paused per cycle.
	keeper := newAttachKeeper(m.ctx, slices.Clone(m.list.GetInstances()), killTarget)
	keeper.errs = slices.Clone(carriedErrs) // pre-start seed, ordered before the goroutine's appends
	// raw: true — tmux's in-session keys (Ctrl+Q detach, Ctrl+X kill, the sibling-cycle
	// keys) are read as single bytes, which cooked mode's line buffering and IXON cannot
	// deliver. A custom command passes false; see attachCommand.raw.
	cmd := &attachCommand{attach: attach, raw: true, keeper: keeper,
		// killTarget is nil only for the terminal tab, and that tab shows the selected
		// session — every terminal-tab site selects the row before attaching — so the
		// selection names the session either way. Read here rather than in Run, which
		// runs on the suspended goroutine and must not consult the list.
		handover: handover.Payload{Kind: handover.KindAttach, Label: attachLabel(killTarget, m.list.GetSelectedInstance())},
		// Runs on the suspended event-loop goroutine (see attachCommand.onAttached),
		// so the bump is ordered before every parked message the resumed loop
		// processes — pre-attach captures always compare against the new generation.
		//
		// The OS chrome is not cleared here. tea.Exec releases the terminal before it
		// starts this command at all, and releasing stops the renderer, which clears
		// the window title and taskbar progress it had set — strictly earlier than
		// this hook, which only runs once tmux is already attached and pumping. Doing
		// it here would race that pump.
		onAttached: func() {
			m.attachGen++
		}}
	return cmd, func(err error) tea.Msg {
		return attachFinishedMsg{
			err:             err,
			killTarget:      killTarget,
			rawModeFailed:   cmd.rawModeFailed,
			keeperDelivered: cmd.keeper.delivered,
			keeperErrs:      cmd.keeper.errs,
		}
	}
}

// attachLabel names the session an attach is handing the terminal to, for the
// handover payload. killTarget when there is one, else the selection — the terminal
// tab is the only attach without a kill target, and it belongs to the selected row.
// "" when neither is available, which handover.Payload.Describe renders as no label
// rather than as a guess.
func attachLabel(killTarget, selected *session.Instance) string {
	if killTarget != nil {
		return killTarget.Title
	}
	if selected != nil {
		return selected.Title
	}
	return ""
}

// attachFinishedMsg is delivered after a tea.Exec terminal attach returns (the
// user detached or the attach errored). It carries the attach error, if any, and
// the attached instance so the post-detach handler can surface an error and honor
// an in-session Ctrl+X kill request. killTarget is nil for the terminal tab, which
// has no kill key. rawModeFailed reports that raw mode couldn't be set, so the
// attach ran cooked and Ctrl+Q detach was disabled — the handler surfaces a modal.
// keeperDelivered and keeperErrs carry the attach keeper's results out of the
// suspended window: the handler persists the cleared prompts (the keeper cannot —
// persistence is main-loop-owned) and surfaces any lost prompts.
type attachFinishedMsg struct {
	err             error
	killTarget      *session.Instance
	rawModeFailed   bool
	keeperDelivered bool
	keeperErrs      []string
}
