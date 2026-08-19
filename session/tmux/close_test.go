package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// alreadyGoneMessages and unreachableSocketMessages are the message tables both callers of
// the classification are held to: Close (below) and probeLiveness (tmux_test.go). They live
// here as values rather than as prose in the predicates' doc comments so that adding a
// message moves every assertion that depends on it, and so neither side can drift into
// covering a message the other does not. A message added to sessionAlreadyGone belongs in
// one of the first two lists — which half it is decides whether Session.Gone may act on it
// — and one added to socketUnreachable belongs in the third.
//
// Every string was captured from a real `tmux -S <path>` run (tmux 3.6, Linux) rather than
// guessed, which matters because the obvious guess is wrong twice over. ECONNREFUSED does
// NOT take the "error connecting to" format: a bound-but-unlistened socket, a regular file
// and a directory at the socket path all report "no server running on …", which is why the
// gone list carries that message and no "(Connection refused)" appears anywhere here.
//
// The errno tail is open-ended, and where it comes from a connect() failure it is also
// platform-dependent: these were captured on Linux, and the kernel that produces them is
// not the one CI's macOS job runs. socketUnreachable is written as "any errno but ENOENT"
// precisely so an errno absent from this list still classifies safely rather than falling
// through to a death verdict — see its doc for what that costs.
var (
	// confirmedGoneMessages are the gone messages a SERVER's own answer produces: tmux
	// reached the socket, or found nothing listening on it, and reported that the session
	// is not there. Nothing is running behind them to collide with, which is why these —
	// and only these — also satisfy Session.Gone (noLiveSessionMessage).
	confirmedGoneMessages = []string{
		"can't find session: x",
		"no server running on /tmp/sock",
		"session not found",
	}
	// socketMissingGoneMessages is the other half: ENOENT on the socket path, the case
	// that was absent until #723. It classifies as gone for Close and for the poll, but a
	// LIVE server whose socket was unlinked reports it identically, so Session.Gone must
	// refuse it — see socketMissingMessage.
	socketMissingGoneMessages = []string{
		"error connecting to /tmp/sock (No such file or directory)",
	}
	// alreadyGoneMessages is the union, and it is what sessionAlreadyGone recognizes:
	// every message that must classify as gone — a clean teardown for Close, PaneDead for
	// the poll. A message added to that predicate belongs in whichever half above it
	// actually is; putting it in the wrong one either weakens Session.Gone's safety guard
	// or blinds it (TestGoneAcceptsOnlyAServersOwnAnswer holds both halves).
	alreadyGoneMessages = append(append([]string{}, confirmedGoneMessages...), socketMissingGoneMessages...)
	// unreachableSocketMessages must NOT classify as gone: a surfaced error for Close,
	// PaneUnknown for the poll. connect() never reached a server, so nothing was asked and
	// no answer came back. The first is a live agent's socket — EACCES is a socket that
	// EXISTS, hosting a server this process cannot address, possibly running the very
	// session being killed. The rest are a socket path that cannot be one at all, which is
	// a misconfigured runtime rather than a dead session.
	unreachableSocketMessages = []string{
		"error connecting to /tmp/sock (Permission denied)",
		"error connecting to /tmp/sock (Not a directory)",
		"error connecting to /tmp/sock (File name too long)",
	}
)

// TestCloseTargetsSessionByExactName guards the kill target form. A bare "-t" is a
// tmux prefix match, so killing an already-gone session could match and kill a live
// sibling whose name this one is a prefix of ("sess" vs "sess2"). Close must target
// by exact name with "-t=".
func TestCloseTargetsSessionByExactName(t *testing.T) {
	var killArgs []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "kill-session") {
				killArgs = slices.Clone(cmd.Args)
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	s := NewSessionWithDeps(context.Background(), "sess", "claude", NewMockPtyFactory(t), cmdExec)

	require.NoError(t, s.Close())
	require.NotEmpty(t, killArgs, "Close must issue kill-session")
	require.NotContains(t, killArgs, "-t", "bare -t is a tmux prefix match and can kill the wrong session")
	require.True(t, slices.ContainsFunc(killArgs, func(a string) bool { return strings.HasPrefix(a, "-t=") }),
		"kill-session must target by exact name (-t=<name>)")
}

// TestCloseTreatsDeadSessionAsSuccess verifies Close does not report a spurious
// error when kill-session fails because the session was already gone (external
// kill, crashed/absent server). It still attempts the kill — the classification is
// on the failure, not a pre-check that could skip a live session.
//
// The missing-socket entry is the one that was absent until #723: with no socket file,
// Close recorded a real error, so every caller that gates cleanup on a clean kill skipped
// it. TerminalPane.CloseForInstance releases the instance's owned shell name only when the
// reap succeeded, so the name was held forever — naming nothing, and reserving its title
// against every new session. Reachable whenever no tmux server is running: fresh boot, all
// sessions paused, after `atrium reap`.
func TestCloseTreatsDeadSessionAsSuccess(t *testing.T) {
	for _, msg := range alreadyGoneMessages {
		t.Run(msg, func(t *testing.T) {
			var attempted bool
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "kill-session") {
						attempted = true
						// Model real tmux: the diagnostic lands on stderr and the
						// process exits non-zero. This exercises Close's stderr
						// classification (its production path), not just the
						// error-string fallback that test fakes would otherwise hit.
						if cmd.Stderr != nil {
							_, _ = fmt.Fprintln(cmd.Stderr, msg)
						}
						return fmt.Errorf("exit status 1")
					}
					return nil
				},
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte(""), nil },
			}
			s := NewSessionWithDeps(context.Background(), "dead", "claude", NewMockPtyFactory(t), cmdExec)

			require.NoError(t, s.Close(), "an already-dead session must not surface a spurious teardown error")
			require.True(t, attempted, "Close should still attempt kill-session, not silently skip it")
		})
	}
}

// TestCloseSurfacesAnUnreachableSocket is the negative half of the case above, and it is
// what makes that one's PAIR match load-bearing rather than decorative.
//
// tmux formats the message as `error connecting to %s (%s)` with strerror, so the errno
// tail is open-ended and matching the prefix alone would classify all of these as "already
// gone" too. None of them is (see unreachableSocketMessages for what each one is), and
// reporting a clean kill there is the one direction Close must never take — its caller then
// prunes state for an agent that may still be alive.
//
// Widen sessionAlreadyGone's socket case to `strings.Contains(hay, "error connecting to")`
// and this test is what goes red.
func TestCloseSurfacesAnUnreachableSocket(t *testing.T) {
	for _, msg := range unreachableSocketMessages {
		t.Run(msg, func(t *testing.T) {
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "kill-session") {
						if cmd.Stderr != nil {
							_, _ = fmt.Fprintln(cmd.Stderr, msg)
						}
						return fmt.Errorf("exit status 1")
					}
					return nil
				},
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte(""), nil },
			}
			s := NewSessionWithDeps(context.Background(), "live", "claude", NewMockPtyFactory(t), cmdExec)

			err := s.Close()
			require.Error(t, err,
				"a socket that exists but cannot be addressed is not evidence the session is gone")
			require.Contains(t, err.Error(), "kill tmux session",
				"the failure must be surfaced as a teardown error")
			require.Contains(t, err.Error(), msg,
				"tmux's own diagnostic must be folded in — the bare exit status names nothing actionable")
		})
	}
}

// TestCloseSurfacesRealTeardownFailure verifies a kill-session failure that is NOT
// an already-dead session (a hung/unresponsive server) is surfaced, so the caller
// never reports a clean kill while a live agent keeps running.
func TestCloseSurfacesRealTeardownFailure(t *testing.T) {
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "kill-session") {
				// Real tmux: diagnostic on stderr, generic non-zero exit. Close must
				// fold the stderr text into the surfaced error.
				if cmd.Stderr != nil {
					_, _ = fmt.Fprintln(cmd.Stderr, "server is wedged")
				}
				return fmt.Errorf("exit status 1")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	s := NewSessionWithDeps(context.Background(), "hung", "claude", NewMockPtyFactory(t), cmdExec)

	err := s.Close()
	require.Error(t, err, "a real kill-session failure must surface")
	require.Contains(t, err.Error(), "wedged")
}
