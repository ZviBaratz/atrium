package tmux

import (
	"errors"
	"os/exec"
)

// ErrNotInstalled is the user-facing sentinel returned by Available when tmux is
// not on PATH. Atrium runs every session inside tmux, so its absence is fatal to
// session creation; the message names the dependency and the fix rather than
// leaking a raw exec-not-found error. It is intentionally multi-clause so the
// TUI's error surfacing routes it to the persistent info modal (see the app's
// handleError) instead of a truncated toast.
var ErrNotInstalled = errors.New(
	"tmux is not installed — Atrium runs each session inside tmux. " +
		"Install it and retry (macOS: brew install tmux; Debian/Ubuntu: sudo apt install tmux). " +
		"Run `atrium doctor` to check dependencies")

// lookPath is the exec.LookPath seam so tests can simulate tmux present/absent
// without a real binary on PATH (mirrors notify.Notifier's lookPath field).
var lookPath = exec.LookPath

// Available reports whether tmux is usable: ErrNotInstalled when the binary is not on
// PATH, an ErrTooOld error when it is present but older than MinVersion, nil otherwise.
// It is the pre-flight gate the new-session flow (the create form and smart dispatch)
// consults before building a worktree, so an unusable tmux surfaces one clean,
// actionable message up front instead of the raw "exec: \"tmux\": executable file not
// found" at pty launch, or a bare poll timeout naming nothing.
//
// It gates session *creation* only. Resume, recover-in-place and every other path into
// start() still reach the raw failure — the same scope the presence check has always had.
//
// Cheap by construction: no subprocess runs here. Both call sites are reached
// synchronously from handleKeyPress (app_session.go, on every n/N and on smart
// dispatch), so an exec on this path would stall the update thread. The version verdict
// is probed once in Init and read from an atomic.
//
// Fails open: only a confidently-parsed, definitely-below-floor tmux is refused. An
// unreadable version, a failed probe, or a probe that never ran all pass through — an
// unreadable version is not evidence of an unusable binary, and it bounds the blast
// radius if MinVersion is ever wrong.
func Available() error {
	if _, err := lookPath("tmux"); err != nil {
		return ErrNotInstalled
	}
	if v := tooOldVersion.Load(); v != nil {
		return ErrTooOldFor(*v)
	}
	return nil
}
