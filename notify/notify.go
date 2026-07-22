// Package notify emits an out-of-band signal — a terminal bell, an OSC 9 escape, or
// an external desktop-notification command — when a background session finishes a
// turn or blocks on a prompt. Agents run inside Atrium's dedicated tmux server and
// the TUI shows capture-pane content, so an agent's own bell never reaches the user's
// terminal; Atrium emits its own here, on the TUI's real stdout (bell, osc) or via a
// spawned process (desktop). The two stdout modes cost nothing on a remote host: the
// user's own terminal renders them, so they traverse SSH with no local notifier.
package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/charmbracelet/x/ansi"
)

// notifyCommandTimeout bounds a single desktop-notification command. A notification
// is ephemeral and runs detached from the TUI, so the bound exists only to stop a
// wedged notifier (a stuck dialog, a dead webhook socket) from leaking a process for
// the app's lifetime — it is generous enough never to cut off a real notifier.
const notifyCommandTimeout = 30 * time.Second

// Event is the session transition that triggered a notification.
type Event int

const (
	// EventFinished is the non-Ready→Ready edge: the agent finished a turn.
	EventFinished Event = iota
	// EventNeedsInput is the transition into NeedsInput: the agent is blocked on a
	// prompt or sitting on a startup/trust gate.
	EventNeedsInput
)

// status is the ATRIUM_STATUS value handed to a notify command.
func (e Event) status() string {
	if e == EventNeedsInput {
		return "NeedsInput"
	}
	return "Ready"
}

// token is the ATRIUM_EVENT value handed to a notify command.
func (e Event) token() string {
	if e == EventNeedsInput {
		return "needs_input"
	}
	return "finished"
}

// headline is the human-readable body of a built-in desktop notification.
func (e Event) headline(session string) string {
	if e == EventNeedsInput {
		return fmt.Sprintf("%s needs input", session)
	}
	return fmt.Sprintf("%s finished", session)
}

// Notifier emits notifications. It writes the bell to out (the TUI's own stdout)
// and runs the desktop command through runner (the fakeable cmd.Executor seam), so
// tests never ring a real bell or spawn a real process. lookPath resolves built-in
// per-OS notifiers and is overridable in tests.
type Notifier struct {
	out      io.Writer
	runner   cmd.Executor
	lookPath func(string) (string, error)

	warnOnce sync.Once // dedupes the "no desktop notifier found" log
}

// New builds a Notifier that writes the bell to out and runs desktop commands via
// runner. out is normally os.Stdout (the TUI's real terminal).
func New(out io.Writer, runner cmd.Executor) *Notifier {
	return &Notifier{out: out, runner: runner, lookPath: osexec.LookPath}
}

// Emit delivers a notification for the given mode. It never blocks the caller: the
// bell is a single write, and the desktop command runs on its own goroutine so a
// slow notifier can't stall the poll tick. mode/command are read live by the caller
// from config, so a settings change takes effect on the next edge with no restart.
// off and unrecognized modes do nothing.
func (n *Notifier) Emit(mode, command, session string, ev Event) {
	switch mode {
	case config.NotificationsBell:
		n.bell()
	case config.NotificationsOSC:
		n.osc(session, ev)
	case config.NotificationsDesktop:
		go n.desktop(command, session, ev)
	}
}

// bell writes a single BEL to the TUI's stdout. BEL is screen-buffer-independent
// (fine under the alt-screen) and a lone C0 control, so it can't corrupt the
// renderer's cell output.
func (n *Notifier) bell() {
	if _, err := io.WriteString(n.out, "\a"); err != nil {
		log.WarningLog.Printf("notify: bell write failed: %v", err)
	}
}

// osc writes an OSC 9 desktop notification to the TUI's stdout, naming the session in
// the body. Like the bell it is a single synchronous write of an out-of-band control
// string — it draws nothing in the cell grid, so it's alt-screen-safe — and because
// the escape is emitted by the user's own terminal it reaches them over SSH with no
// local notifier. Control characters in the body (a user-editable display name) are
// folded so an embedded BEL/ST can't truncate the sequence.
func (n *Notifier) osc(session string, ev Event) {
	if _, err := io.WriteString(n.out, ansi.Notify(foldControls(ev.headline(session)))); err != nil {
		log.WarningLog.Printf("notify: osc write failed: %v", err)
	}
}

// desktop runs the resolved desktop-notification command, logging (never fatal) on
// failure. A nil command means nothing to run (no command configured and no default
// notifier on PATH — already logged).
func (n *Notifier) desktop(command, session string, ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCommandTimeout)
	defer cancel()
	c := n.desktopCommand(ctx, command, session, ev)
	if c == nil {
		return
	}
	if err := n.runner.Run(c); err != nil {
		log.WarningLog.Printf("notify: desktop command %q failed: %v", cmd.ToString(c), err)
	}
}

// desktopCommand builds the command for a desktop notification: the user's
// NotifyCommand via `sh -c` when set, otherwise a built-in per-OS default. The
// session name rides in the environment (never interpolated into the argv), so it
// can't break argument parsing or inject shell. Returns nil when no command is
// configured and no default notifier is on PATH.
func (n *Notifier) desktopCommand(ctx context.Context, command, session string, ev Event) *osexec.Cmd {
	if command != "" {
		c := osexec.CommandContext(ctx, "sh", "-c", command)
		c.Env = append(os.Environ(),
			"ATRIUM_SESSION="+session,
			"ATRIUM_STATUS="+ev.status(),
			"ATRIUM_EVENT="+ev.token(),
		)
		return c
	}
	return n.defaultCommand(ctx, runtime.GOOS, session, ev)
}

// defaultCommand resolves a built-in desktop notifier for the given OS: notify-send
// on Linux/BSD, terminal-notifier then osascript on macOS. goos is a parameter (not
// runtime.GOOS directly) so the selection is testable on any host. Returns nil and
// logs once when nothing suitable is on PATH.
func (n *Notifier) defaultCommand(ctx context.Context, goos, session string, ev Event) *osexec.Cmd {
	const title = "Atrium"
	body := ev.headline(session)
	switch goos {
	case "darwin":
		if path, err := n.lookPath("terminal-notifier"); err == nil {
			return osexec.CommandContext(ctx, path, "-title", title, "-message", body)
		}
		if path, err := n.lookPath("osascript"); err == nil {
			script := fmt.Sprintf("display notification %s with title %s", osaQuote(body), osaQuote(title))
			return osexec.CommandContext(ctx, path, "-e", script)
		}
	default: // linux and other freedesktop-notifier platforms
		if path, err := n.lookPath("notify-send"); err == nil {
			return osexec.CommandContext(ctx, path, title, body)
		}
	}
	n.warnOnce.Do(func() {
		log.WarningLog.Printf("notify: no desktop notifier found on PATH; set notify_command to customize")
	})
	return nil
}

// osaQuote renders s as an AppleScript double-quoted string literal. Backslash and
// double-quote are escaped; any control character (newline, CR, tab, …) — which an
// AppleScript string literal cannot contain and which a user-editable display name might
// hold — is folded to a space so the generated one-line -e script stays valid.
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + foldControls(s) + "\""
}

// foldControls replaces every C0 control character and DEL with a space. A
// display name is user-editable, so it may hold control bytes that would break a
// single-line AppleScript literal or truncate an OSC control string at an embedded
// BEL/ST; folding them keeps the generated sequence well-formed.
func foldControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
