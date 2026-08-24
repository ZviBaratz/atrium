package session

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// tmuxWithRun returns a hermetic tmux session whose every subprocess result is
// scripted by run, so a test can make a specific tmux verb (e.g. kill-session)
// fail on demand without a real server.
func tmuxWithRun(name string, run func(*exec.Cmd) error) *tmux.Session {
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    run,
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	return tmux.NewSessionWithDeps(context.Background(), name, "claude", tmux.MakePtyFactory(), mockExec)
}

// TestNoteAutoPauseCommit verifies pause's auto-WIP commit is folded into the
// cached commit count (so the kill dialog can warn), including the never-polled
// case where no stats exist yet, and that the later clearCachedDirty pause makes
// leaves the bumped count intact.
func TestNoteAutoPauseCommit(t *testing.T) {
	// Never polled: creates the stats and counts the one WIP commit.
	fresh := &Instance{}
	fresh.noteAutoPauseCommit()
	require.NotNil(t, fresh.GetDiffStats())
	require.Equal(t, 1, fresh.GetDiffStats().Commits)

	// Already polled: increments the existing count, and clearCachedDirty (which
	// pause runs afterwards) clears Dirty without disturbing Commits.
	polled := &Instance{}
	polled.SetDiffStats(&git.DiffStats{Commits: 2, Dirty: true})
	polled.noteAutoPauseCommit()
	require.Equal(t, 3, polled.GetDiffStats().Commits)
	polled.clearCachedDirty()
	require.Equal(t, 3, polled.GetDiffStats().Commits)
	require.False(t, polled.GetDiffStats().Dirty)
}

// TestKillPropagatesTeardownFailure verifies a real tmux teardown failure (a hung
// server, not an already-dead session) is returned from Kill rather than swallowed,
// so the UI can tell the user a live session leaked.
func TestKillPropagatesTeardownFailure(t *testing.T) {
	ts := tmuxWithRun("boom", func(c *exec.Cmd) error {
		if slices.Contains(c.Args, "kill-session") {
			// Real tmux: diagnostic on stderr, generic non-zero exit.
			if c.Stderr != nil {
				_, _ = fmt.Fprintln(c.Stderr, "server is wedged")
			}
			return fmt.Errorf("exit status 1")
		}
		return nil
	})
	inst := &Instance{ident: identity{title: "boom"}, tmuxSession: ts}

	err := inst.Kill()
	require.Error(t, err, "a failed tmux teardown must surface from Kill")
	require.Contains(t, err.Error(), "wedged")
}

// TestKillTreatsDeadSessionAsClean verifies killing an already-dead session (tmux
// reports it gone) returns no error, so the kill flow doesn't report a spurious
// failure for a session that was the teardown goal already.
func TestKillTreatsDeadSessionAsClean(t *testing.T) {
	ts := tmuxWithRun("gone", func(c *exec.Cmd) error {
		if slices.Contains(c.Args, "kill-session") {
			// Real tmux delivers "can't find session" on stderr and exits non-zero;
			// Close must classify it as already-gone from the stderr text.
			if c.Stderr != nil {
				_, _ = fmt.Fprintln(c.Stderr, "can't find session: gone")
			}
			return fmt.Errorf("exit status 1")
		}
		return nil
	})
	inst := &Instance{ident: identity{title: "gone"}, tmuxSession: ts}

	require.NoError(t, inst.Kill(), "an already-dead session must not surface a spurious error")
}
