//go:build !windows

package tmux

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Pty.Start's reaping goroutine is observable without mocking a pty or touching
// tmux: signal 0 succeeds for a zombie (the process table entry survives until a
// parent Waits) and fails with ESRCH once the child is reaped. So an alive child
// that is killed and then vanishes from the table is a direct assertion that
// something called Wait — which is the whole of #362.
//
// The child is a real `sleep` rather than a fast-exiting `true` so the
// still-alive precondition below cannot race the exit it is meant to precede.
func TestPtyStartReapsKilledChild(t *testing.T) {
	// t.Context so a `sleep` left behind by an early failure below cannot outlive
	// the test run; the kill and reap this test asserts on happen well before it.
	cmd := exec.CommandContext(t.Context(), "sleep", "30")
	ptmx, err := Pty{}.Start(cmd)
	require.NoError(t, err, "allocating a real pty")
	t.Cleanup(func() { _ = ptmx.Close() })

	pid := cmd.Process.Pid
	// The precondition that makes the ESRCH assertion below mean anything: the pid
	// is live now, so its later absence is a reap and not a child that never ran.
	require.NoError(t, syscall.Kill(pid, 0), "child should be running right after Start")

	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL), "killing the child")

	// Without the Wait goroutine in Pty.Start this pid parks as a zombie for the
	// lifetime of the process and signal 0 keeps succeeding, so this Eventually is
	// what fails if the goroutine is ever dropped.
	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, 5*time.Second, 10*time.Millisecond,
		"killed pty child was never reaped — it is a zombie (#362)")
}
