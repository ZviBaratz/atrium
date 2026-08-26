package tmux

// capture.go — reading a session's pane from outside the TUI.
//
// The TUI captures through Session.CapturePaneContent, which needs a live
// *Session (and so a whole Instance behind it). The headless `atrium peek`
// command has neither: it knows only a tmux session name read out of state.json.
// This file gives it a way in that still routes through tmuxCommand, so the
// socket and managed config path stay derived from config.RuntimeName() rather
// than hardcoded at the call site.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
)

// SessionConfirmedAbsent reports whether the tmux SERVER answered that this session is
// not there — as opposed to a probe that never got an answer.
//
// It exists for the headless callers this file is about, which hold a session name and no
// *Session, and it draws exactly the distinction probeLiveness draws for the poll: the
// server's own answer is evidence, while a timeout, a cancelled context or a socket tmux
// could not open is not. Only the strong half returns true, so a caller may act on it.
//
// What acts on it is the retire gate. A capture that fails is not an idle pane, and
// refusing on one is deliberate — but a session whose agent has exited is not an
// unreadable pane, it is an absent one, and there is no turn in flight in a session that
// does not exist. Without this the two are the same error, and the ordinary end of an
// agent's life left its session with no headless way to retire it at all.
//
// It reads noLiveSessionMessage, the half of sessionAlreadyGone a server produces, rather
// than restating it — so a diagnostic this package learns is learned here too. The other
// half, socketMissingMessage, is deliberately left out: a live server whose socket was
// unlinked answers exactly like an absent one, which is an inference and not evidence.
func SessionConfirmedAbsent(ctx context.Context, exec cmd.Executor, sessionName string) bool {
	if sessionName == "" {
		return false
	}
	// `-t=` is an exact match; plain `-t` is a prefix match, which would answer for a
	// neighbouring session whose name this one is a prefix of.
	var stderr strings.Builder
	probe := tmuxCommand(ctx, "has-session", fmt.Sprintf("-t=%s", sessionName))
	probe.Stderr = &stderr
	err := exec.Run(probe)
	switch {
	case err == nil:
		return false
	// A context-killed probe surfaces as an ExitError ("signal: killed"), so it is
	// checked first and on ctx.Err() rather than DeadlineExceeded alone: a cancelled
	// context kills the process just the same. Neither is an answer about the session.
	case ctx.Err() != nil, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return false
	}
	return noLiveSessionMessage(goneHaystack(err, stderr.String()))
}

// CaptureOpts tunes a headless pane capture.
type CaptureOpts struct {
	// Lines, when positive, is how many lines of output to return, reaching back
	// into the scrollback history when the visible pane holds fewer. Zero returns
	// the visible pane.
	Lines int
	// Color keeps tmux's ANSI escape sequences in the output. Off by default: the
	// usual consumer is a script or an agent, for which escapes are noise.
	Color bool
}

// CapturePaneForSession returns the contents of a tmux session's agent pane.
//
// It targets the pane id, not the session name, for the reason pane.go
// documents at length: tmux resolves a session-name target to the *active* pane
// of the current window, so a split the user opened while attached would
// silently redirect the capture to the wrong pane. When the id cannot be
// resolved this falls back to the session name, matching Session.paneTarget —
// and logs it the same way, so a capture quietly redirected to the wrong pane is
// at least diagnosable rather than an unexplained wrong answer.
//
// The capture is read-only — it neither attaches to the session nor alters it.
func CapturePaneForSession(ctx context.Context, exec cmd.Executor, sessionName string, o CaptureOpts) (string, error) {
	if sessionName == "" {
		return "", errors.New("cannot capture a pane without a tmux session name")
	}

	target := sessionName
	if id, err := resolvePaneID(ctx, exec, sessionName); err != nil {
		log.WarningLog.Printf("could not resolve pane id for %s (peek falls back to the session name): %v", sessionName, err)
	} else {
		target = id
	}

	args := []string{"capture-pane", "-p", "-J"}
	if o.Color {
		args = append(args, "-e")
	}
	if o.Lines > 0 {
		// Negative start-line reaches into the history. Ask for the requested
		// count of history on top of the visible pane, then trim to exactly
		// Lines below, so the caller gets what it asked for rather than "N plus
		// however tall the pane happens to be".
		args = append(args, "-S", strconv.Itoa(-o.Lines))
	}
	args = append(args, "-t", target)

	out, err := exec.Output(tmuxCommand(ctx, args...))
	if err != nil {
		return "", fmt.Errorf("failed to capture pane for tmux session %q: %w", sessionName, err)
	}
	return trimCapture(string(out), o.Lines), nil
}

// resolvePaneID returns the agent pane's id for a session, or an error when the
// listing fails or yields no usable id. It is the headless counterpart to
// Session.resolvePaneIDLocked; callers fall back to the session name on error.
func resolvePaneID(ctx context.Context, exec cmd.Executor, sessionName string) (string, error) {
	listed, err := exec.Output(tmuxCommand(ctx, "list-panes", "-s", "-t", sessionName, "-F", "#{pane_id}"))
	if err != nil {
		return "", err
	}
	return smallestPaneID(listed)
}

// PaneIDsForSession returns every pane id in a tmux session, for a caller that has to
// recognise a pane rather than read one.
//
// A pane id is the identity a NAME is not: tmux mints it once and never reissues it, so
// it survives a rename of the session, the window or the pane. That is what makes it the
// only sound way to answer "am I inside this session?" — the name a process was launched
// with is frozen in its environment, and a session renamed since then answers to a name
// nothing in that process knows.
//
// Every pane, not the agent's alone: the question is whether the CALLER's pane belongs to
// this session, and the caller may be a shell the user split off rather than the agent.
func PaneIDsForSession(ctx context.Context, exec cmd.Executor, sessionName string) ([]string, error) {
	if sessionName == "" {
		return nil, errors.New("cannot list panes without a tmux session name")
	}
	listed, err := exec.Output(tmuxCommand(ctx, "list-panes", "-s", "-t", sessionName, "-F", "#{pane_id}"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// trimCapture drops the blank rows tmux pads a partly-filled pane with, then
// keeps at most the last n lines. Padding is an artifact of the pane's height,
// never of what the agent printed, so returning it would make every capture end
// in a variable run of empty lines.
func trimCapture(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
