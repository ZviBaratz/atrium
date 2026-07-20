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
)

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
// resolved this falls back to the session name, matching Session.paneTarget.
//
// The capture is read-only — it neither attaches to the session nor alters it.
func CapturePaneForSession(ctx context.Context, exec cmd.Executor, sessionName string, o CaptureOpts) (string, error) {
	if sessionName == "" {
		return "", errors.New("cannot capture a pane without a tmux session name")
	}

	target := sessionName
	listed, err := exec.Output(tmuxCommand(ctx, "list-panes", "-s", "-t", sessionName, "-F", "#{pane_id}"))
	if err == nil {
		if id, idErr := smallestPaneID(listed); idErr == nil {
			target = id
		}
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
