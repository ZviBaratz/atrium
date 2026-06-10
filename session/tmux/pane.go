package tmux

// pane.go — resolving the agent pane's immutable tmux id (%N).
//
// Pane-content reads (capture-pane) and keystroke writes (send-keys) must
// target the pane id, not the session name or the attach client's pty: tmux
// resolves both a session-name target and client input to the *active* pane
// of the session's current window, so any extra pane or window — a split the
// user opens while attached (the attach proxy forwards the tmux prefix), or a
// future overlay pane — silently redirects every capture and keystroke.
// Status detection, unread tracking, and the autoyes daemon all act on what
// capture shows, and the daemon answers with Enter taps — misdirect either
// side and the wrong pane gets read, or worse, typed into.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/log"
)

// paneTarget returns the tmux target for pane-scoped commands (capture-pane,
// send-keys): the agent pane's id when it can be resolved, otherwise the
// session name (tmux's active-pane resolution — the historical behavior — as
// a graceful fallback).
//
// Resolution runs at most once per tmux-session generation: pane ids are
// immutable for the pane's lifetime and survive session renames, so the
// cache is only reset where the session is created or killed (start, Close).
func (t *Session) paneTarget() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.paneIDTried {
		t.paneIDTried = true
		id, err := t.resolvePaneIDLocked()
		if err != nil {
			log.WarningLog.Printf("could not resolve pane id for %s (capture/send-keys fall back to the session name): %v", t.sanitizedName, err)
		} else {
			t.paneID = id
		}
	}
	if t.paneID != "" {
		return t.paneID
	}
	return t.sanitizedName
}

// resetPaneID clears the cached pane id so the next read re-resolves. Called
// where the pane can change identity: session creation and teardown.
func (t *Session) resetPaneID() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paneID = ""
	t.paneIDTried = false
}

// resolvePaneIDLocked lists every pane in the session and returns the one
// with the numerically smallest id. Ids are assigned in creation order, so
// the smallest id in the session is the pane new-session created for the
// agent. Pane *indexes* are no good as a handle: they are positional, and a
// `split-window -b` shifts them. Callers hold t.mu (so sanitizedName is read
// directly; snapshotName would self-deadlock).
func (t *Session) resolvePaneIDLocked() (string, error) {
	ctx, cancel := t.opContext()
	defer cancel()
	cmd := tmuxCommand(ctx, "list-panes", "-s", "-t", t.sanitizedName, "-F", "#{pane_id}")
	out, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", err
	}
	best, bestN := "", 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "%") {
			continue
		}
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			continue
		}
		if best == "" || n < bestN {
			best, bestN = line, n
		}
	}
	if best == "" {
		return "", fmt.Errorf("no pane id in list-panes output %q", strings.TrimSpace(string(out)))
	}
	return best, nil
}
