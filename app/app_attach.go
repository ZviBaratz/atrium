package app

// tmux attach plumbing for the home model.

import (
	"io"
	"os"
	"slices"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// isTerminal and makeRaw seam term's tty calls so Run's raw-mode-failure branch is
// testable: CI has no controlling TTY, so the real term.IsTerminal returns false and
// the branch is otherwise unreachable. term.Restore needs no seam — it is only
// reached on the success path, which the failure tests don't exercise.
var (
	isTerminal = term.IsTerminal
	makeRaw    = term.MakeRaw
)

// attachCommand adapts a blocking tmux attach into a tea.ExecCommand so Bubble
// Tea releases the terminal before the attach and restores+repaints it after —
// on the event loop, via execMsg, which is the framework's supported path for a
// blocking terminal takeover. (Calling ReleaseTerminal/RestoreTerminal directly
// from inside Update blocks the event loop for the whole attach and leaves the
// renderer/input reader wedged.) Run also puts stdin in raw mode for the
// duration: ReleaseTerminal restores cooked mode, where Ctrl+Q (ASCII 17 = XON)
// is swallowed by IXON flow control and never reaches the detach reader. The
// Set* methods are no-ops because the attach copies os.Stdin/os.Stdout directly
// rather than through the streams Bubble Tea would inject.
//
// Methods take a pointer receiver so Run's rawModeFailed write survives: tea.Exec
// holds the value as an interface and invokes Run on it after releasing the
// terminal, then hands our callback's message to a fresh goroutine (go p.Send in
// bubbletea's exec) — the go statement orders the callback's reads after Run's
// writes, so attachExec can pass a *attachCommand and read the flags back there.
type attachCommand struct {
	attach func() (chan struct{}, error)
	// keeper services non-attached sessions (prompt delivery, auto-yes taps) while
	// the event loop is suspended. Run starts it once the attach succeeds and joins
	// it before returning; nil is tolerated for tests that only exercise Run.
	keeper *attachKeeper
	// onAttached is called once the attach has succeeded, before the keeper starts.
	// Run executes on the suspended event-loop goroutine, so the callback may touch
	// main-loop state — attachExecCarry uses it to bump home.attachGen, retiring
	// pane-state captures taken before the keeper started rearranging panes. nil is
	// tolerated for tests that only exercise Run.
	onAttached func()
	// rawModeFailed records that raw mode couldn't be set, so the attach ran cooked
	// and Ctrl+Q detach was disabled. Read by attachExec's callback after Run returns.
	rawModeFailed bool
}

func (a *attachCommand) Run() error {
	if fd := int(os.Stdin.Fd()); isTerminal(fd) {
		if oldState, err := makeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		} else {
			// Stay in cooked mode where IXON swallows Ctrl+Q, so detach won't work and
			// the attach looks like a hang. Record it so attachFinishedMsg can surface a
			// modal on return, and log a breadcrumb (to the file, not the tmux-owned
			// terminal) for the case where the user kills Atrium instead of detaching.
			a.rawModeFailed = true
			log.WarningLog.Printf("failed to set raw mode for attach; Ctrl+Q detach may not work: %v", err)
		}
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
	cmd := &attachCommand{attach: attach, keeper: keeper,
		// Runs on the suspended event-loop goroutine (see attachCommand.onAttached),
		// so the bump is ordered before every parked message the resumed loop
		// processes — pre-attach captures always compare against the new generation.
		onAttached: func() { m.attachGen++ }}
	return tea.Exec(cmd, func(err error) tea.Msg {
		return attachFinishedMsg{
			err:             err,
			killTarget:      killTarget,
			rawModeFailed:   cmd.rawModeFailed,
			keeperDelivered: cmd.keeper.delivered,
			keeperErrs:      cmd.keeper.errs,
		}
	})
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
