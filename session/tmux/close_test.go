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
// The last entry is the missing SOCKET rather than a missing session, and it is the one
// that was absent until #723: with no socket file, Close recorded a real error, so every
// caller that gates cleanup on a clean kill skipped it. TerminalPane.CloseForInstance
// releases the instance's owned shell name only when the reap succeeded, so the name was
// held forever — naming nothing, and reserving its title against every new session.
// Reachable whenever no tmux server is running: fresh boot, all sessions paused, after
// `atrium reap`.
func TestCloseTreatsDeadSessionAsSuccess(t *testing.T) {
	for _, msg := range []string{
		"can't find session: x",
		"no server running on /tmp/sock",
		"session not found",
		"error connecting to /tmp/sock (No such file or directory)",
	} {
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
// gone" too. None of them is: connect() never reached a server, so nothing was asked and
// no answer came back. Reporting a clean kill there is the one direction Close must never
// take — its caller then prunes state for an agent that may still be alive. The first case
// is that agent: EACCES is a socket that EXISTS, hosting a server this process cannot
// address, possibly running the very session being killed. The rest are a socket path that
// cannot be one at all, which is a misconfigured runtime rather than a dead session.
//
// Every message here was captured from tmux 3.6 rather than guessed, which matters because
// the obvious guess is wrong: ECONNREFUSED does NOT take this format. A bound-but-unlistened
// socket, a regular file and a directory at the socket path all report "no server running
// on …", which sessionAlreadyGone treats as gone — correctly, since tmux unlinks and moves
// on.
//
// Widen sessionAlreadyGone's socket case to `strings.Contains(hay, "error connecting to")`
// and this test is what goes red.
func TestCloseSurfacesAnUnreachableSocket(t *testing.T) {
	for _, msg := range []string{
		"error connecting to /tmp/sock (Permission denied)",
		"error connecting to /tmp/sock (Not a directory)",
		"error connecting to /tmp/sock (File name too long)",
	} {
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
