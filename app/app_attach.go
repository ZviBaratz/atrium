package app

// tmux attach plumbing for the home model.

import (
	"io"
	"os"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
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
type attachCommand struct {
	attach func() (chan struct{}, error)
}

func (a attachCommand) Run() error {
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if oldState, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		} else {
			// Stay in cooked mode where IXON swallows Ctrl+Q, so detach won't work and
			// the attach looks like a hang. Log a breadcrumb (to the file, not the
			// tmux-owned terminal) instead of failing silently.
			log.WarningLog.Printf("failed to set raw mode for attach; Ctrl+Q detach may not work: %v", err)
		}
	}
	ch, err := a.attach()
	if err != nil {
		return err
	}
	<-ch
	return nil
}

func (a attachCommand) SetStdin(io.Reader) {}

func (a attachCommand) SetStdout(io.Writer) {}

func (a attachCommand) SetStderr(io.Writer) {}

// attachExec hands the terminal to a tmux attach via tea.Exec and reports the
// outcome as an attachFinishedMsg once the user detaches. killTarget is the
// attached instance whose in-session Ctrl+X kill request the handler should honor
// on detach, or nil when the attach has no kill key (the terminal tab).
func (m *home) attachExec(attach func() (chan struct{}, error), killTarget *session.Instance) tea.Cmd {
	// Attaching is the strongest form of visiting: clear the unread state before
	// handing the terminal over. killTarget is nil only for the terminal tab,
	// which the selection dwell covers instead.
	if killTarget != nil {
		killTarget.MarkSeen()
	}
	return tea.Exec(attachCommand{attach: attach}, func(err error) tea.Msg {
		return attachFinishedMsg{err: err, killTarget: killTarget}
	})
}

// attachFinishedMsg is delivered after a tea.Exec terminal attach returns (the
// user detached or the attach errored). It carries the attach error, if any, and
// the attached instance so the post-detach handler can surface an error and honor
// an in-session Ctrl+X kill request. killTarget is nil for the terminal tab, which
// has no kill key.
type attachFinishedMsg struct {
	err        error
	killTarget *session.Instance
}
