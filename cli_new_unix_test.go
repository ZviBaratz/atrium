//go:build !windows

package main

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/handover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdTUILock stands in for a running TUI by holding tui.lock for the test.
func holdTUILock(t *testing.T) {
	t.Helper()
	path, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(path)
	require.NoError(t, err)
	t.Cleanup(release)
}

// holdParked stands in for a TUI whose terminal is handed to a session: BOTH locks,
// because that is what parked means. tui.lock alone is the ordinary running case, and
// handover.lock alone cannot happen — only a live TUI takes it.
func holdParked(t *testing.T, label string) {
	t.Helper()
	holdTUILock(t)
	release, err := handover.Hold(handover.Payload{Kind: handover.KindAttach, Label: label})
	require.NoError(t, err)
	t.Cleanup(release)
}

// TestNewWarnsWhenNoTUIRunning: the request genuinely succeeded — it is durable
// and will be created — so this exits 0, but silence would let an orchestrator
// believe a session existed when nothing was listening. That matters more here
// than for `send`: a caller that goes on to `atrium send` the session it thinks it
// just made would get "no session named …" with no clue why.
//
// Unix-only: acquireTUILock is a no-op on Windows, so tuiRunning reports
// "unknown" there and deliberately says nothing rather than guess.
func TestNewWarnsWhenNoTUIRunning(t *testing.T) {
	sandboxDataDir(t)

	stdout, stderr, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err, "a queued request is a success, not a failure")
	assert.Contains(t, stdout, "fix-auth")
	assert.Contains(t, stderr, "no atrium TUI is running")
	assert.Len(t, spooledCreates(t), 1, "and it is durable")
}

// TestNewStaysQuietWhileATUIHoldsTheLock is the other side of that warning: the
// normal case, creating a session while watching the fleet, must not nag.
//
// tui.lock held and handover.lock free is what "running and polling" looks like, and
// keeping the second half unheld here is what makes this the quiet case rather than
// the parked one below.
func TestNewStaysQuietWhileATUIHoldsTheLock(t *testing.T) {
	sandboxDataDir(t)
	holdTUILock(t)

	_, stderr, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)
	assert.Empty(t, stderr, "a live, polling atrium is the case with nothing to say")
}

// TestNewWarnsWhileAtriumIsParked is #760. A TUI that has handed its terminal to a
// session has a blocked event loop, so the request waits for the detach — and tui.lock
// is held either way, which is why silence here was indistinguishable from "it will be
// along in a second".
//
// The session is named because the reader is usually the person attached to it: an
// agent's `atrium new` runs inside a session, so this warning lands in the pane they
// are watching, and "detach from fix-auth" is the action.
func TestNewWarnsWhileAtriumIsParked(t *testing.T) {
	sandboxDataDir(t)
	holdParked(t, "fix-auth")

	_, stderr, err := newSession(t, newRequest{title: "next-issue", path: tempRepo(t)})
	require.NoError(t, err, "the request is durable; it lands on the detach")
	assert.Contains(t, stderr, "poll loop is parked")
	assert.Contains(t, stderr, "when you detach")
	assert.Contains(t, stderr, `attached to session "fix-auth"`, "name the session to detach from")
	assert.NotContains(t, stderr, "no atrium TUI is running", "one is running; that is the whole problem")
	assert.Len(t, spooledCreates(t), 1)
}

// TestNewWarnsWhileParkedWithoutALabel: the lock is what proves the loop is parked and
// the label is decoration written beside it, so a payload that could not be recorded
// must cost the caller the session name and nothing else.
func TestNewWarnsWhileParkedWithoutALabel(t *testing.T) {
	sandboxDataDir(t)
	holdParked(t, "")

	_, stderr, err := newSession(t, newRequest{title: "next-issue", path: tempRepo(t)})
	require.NoError(t, err)
	assert.Contains(t, stderr, "poll loop is parked")
	assert.Contains(t, stderr, "has handed its terminal to a session")
}

// TestNewWaitStillWarnsWhileAtriumIsParked is the deliberate exception to
// TestNewWaitSkipsTheNoTUIWarning's rule, and the two must not be collapsed.
//
// That rule holds for the no-TUI warning because it predicts an outcome the wait is
// about to report for real. The parked warning is not addressed to the caller at all —
// it is addressed to the person at the keyboard, who is the only party that can unblock
// it, and it is actionable when printed rather than at the deadline. Suppressed, a
// `--wait 60s` would leave them a minute of silence in front of a session that is not
// going to appear.
func TestNewWaitStillWarnsWhileAtriumIsParked(t *testing.T) {
	sandboxDataDir(t)
	holdParked(t, "fix-auth")

	_, stderr, err := newSession(t, newRequest{
		title: "next-issue", path: tempRepo(t), wait: 150 * time.Millisecond,
	})
	require.Error(t, err, "nothing drains while the loop is parked, so this times out")
	assert.Contains(t, stderr, "poll loop is parked", "printed at once, not held to the deadline")
}

// TestNewWaitNamesWhoIsNotDraining pins criterion 2 of #760: the deadline names which
// of the three cases held, instead of enumerating all of them and leaving the caller to
// guess whether to wait longer or go and detach.
func TestNewWaitNamesWhoIsNotDraining(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T)
		want   string
		absent string
	}{
		{
			name:  "no TUI",
			setup: func(*testing.T) {},
			want:  "No atrium TUI is running",
			// The old wording said this unconditionally, including when it was false.
			absent: "on detach",
		},
		{
			name:   "parked",
			setup:  func(t *testing.T) { holdParked(t, "fix-auth") },
			want:   "poll loop is parked",
			absent: "No atrium TUI is running",
		},
		{
			name:   "running and polling",
			setup:  holdTUILock,
			want:   "poll loop live",
			absent: "poll loop is parked",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxDataDir(t)
			tc.setup(t)
			_, _, err := newSession(t, newRequest{
				title: "next-issue", path: tempRepo(t), wait: 150 * time.Millisecond,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "still in the outbox", "the file half stays")
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), tc.absent)
		})
	}
}

// TestNewWaitSkipsTheNoTUIWarning: --wait already reports the outcome, including
// a timeout that says the request is still queued, so the warning would only
// duplicate it — and it must not be printed before the wait even begins.
func TestNewWaitSkipsTheNoTUIWarning(t *testing.T) {
	sandboxDataDir(t)

	_, stderr, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), wait: 150 * time.Millisecond,
	})
	require.Error(t, err, "nothing is draining, so this times out")
	assert.NotContains(t, stderr, "no atrium TUI is running")
}
