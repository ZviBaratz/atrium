package tmux

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// While the user is interactively attached, an in-flight metadata tick must not
// poll the session: a capture-pane/has-session there contends the shared tmux
// socket with the live attach client and races the monitor swap in Restore. Poll
// must short-circuit to PaneUnknown (a no-op in ApplyPaneState) without running a
// single subprocess.
func TestPollSkipsWhileAttached(t *testing.T) {
	var runs, outputs int
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { runs++; return nil }, // has-session: alive
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			outputs++
			return []byte("hello"), nil // fresh content => working for a marker-less agent
		},
	}
	// aider has no busy marker, so a first capture of new content classifies as working.
	s := NewSessionWithDeps(context.Background(), "attach-poll", "aider", NewMockPtyFactory(t), cmdExec)

	// Detached: Poll probes the pane and returns a real (non-Unknown) state.
	require.NotEqual(t, PaneUnknown, s.Poll(), "a live, detached session should classify normally")
	require.Positive(t, runs, "detached Poll should run has-session")
	require.Positive(t, outputs, "detached Poll should run capture-pane")

	// Attached: Poll is a no-op and spawns no subprocess.
	runsBefore, outputsBefore := runs, outputs
	s.attached.Store(true)
	require.Equal(t, PaneUnknown, s.Poll(), "an attached session must not be polled")
	require.Equal(t, runsBefore, runs, "attached Poll must not run has-session")
	require.Equal(t, outputsBefore, outputs, "attached Poll must not run capture-pane")
}

// Poll reads t.monitor under monitorMu; Restore (on the detach goroutine) swaps
// t.monitor. The swap is now locked, so the two interleave without a data race.
// This test only fails the race detector if the lock in Restore is removed.
func TestPollRestoreNoRace(t *testing.T) {
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("hello"), nil },
	}
	s := NewSessionWithDeps(context.Background(), "race", "aider", NewMockPtyFactory(t), cmdExec)
	require.NoError(t, s.Restore()) // seed an initial ptmx/monitor

	const iters = 500
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iters {
			s.Poll() // detached: takes monitorMu, reads/writes t.monitor fields
		}
	}()
	go func() {
		defer wg.Done()
		for range iters {
			require.NoError(t, s.Restore()) // swaps t.monitor under the lock
		}
	}()
	wg.Wait()
}

// The teardown clears the poll guard so the session is polled again once detached:
// Detach clears it after re-Restoring; DetachSafely clears it without re-Restoring.
func TestDetachClearsAttachedGuard(t *testing.T) {
	s, _ := attachedSession(t)
	require.True(t, s.attached.Load(), "helper mirrors Attach: attached is set")

	s.Detach()
	require.False(t, s.attached.Load(), "Detach must clear the poll guard")

	s2, _ := attachedSession(t)
	require.True(t, s2.attached.Load())
	require.NoError(t, s2.DetachSafely())
	require.False(t, s2.attached.Load(), "DetachSafely must clear the poll guard")
}
