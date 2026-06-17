package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// attachedSession builds a Session in the "currently attached" state without going
// through the real-stdin Attach goroutines: it Restores a (mock) pty, then sets the
// per-attach fields directly, mirroring what Attach establishes. The returned
// MockPtyFactory lets a test flip StartErr to simulate a Restore failure.
func attachedSession(t *testing.T) (*Session, *MockPtyFactory) {
	t.Helper()
	ptyFactory := NewMockPtyFactory(t)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil }, // session exists / is alive
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}
	s := NewSessionWithDeps(context.Background(), "detach-test", "claude", ptyFactory, cmdExec)
	require.NoError(t, s.Restore()) // populate s.ptmx via the mock factory
	s.attachCh = make(chan struct{})
	s.wg = &sync.WaitGroup{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.attached.Store(true) // mirror Attach's poll guard
	return s, ptyFactory
}

// assertClosed fails unless ch is already closed (a receive that does not block).
func assertClosed(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal("attachCh not closed")
	}
}

// T1: a clean detach re-establishes the pty, records no error, and tears down the
// per-attach state.
func TestDetachCleanByDefault(t *testing.T) {
	s, _ := attachedSession(t)
	ch := s.attachCh

	require.NotPanics(t, s.Detach)

	require.NoError(t, s.AttachExitError())
	require.NotNil(t, s.ptmx, "Detach should re-Restore the pty")
	require.Nil(t, s.attachCh)
	require.Nil(t, s.cancel)
	require.Nil(t, s.wg)
	require.Nil(t, s.ctx)
	assertClosed(t, ch)
}

// T2: a failing pty close is recorded, not fatal; Restore still succeeds so ptmx is
// re-established.
func TestDetachPtyCloseErrorDoesNotPanic(t *testing.T) {
	s, _ := attachedSession(t)
	ch := s.attachCh
	require.NoError(t, s.ptmx.Close()) // the next Close inside Detach now errors

	require.NotPanics(t, s.Detach)

	require.Error(t, s.AttachExitError())
	require.Contains(t, s.AttachExitError().Error(), "closing attach pty")
	require.NotNil(t, s.ptmx, "Restore should still have succeeded")
	require.Nil(t, s.attachCh)
	assertClosed(t, ch)
}

// T3: a failing Restore is recorded and leaves ptmx nil instead of panicking; the nil
// ptmx does not break polling (which goes through cmdExec subprocesses).
func TestDetachRestoreFailureLeavesNilPty(t *testing.T) {
	s, ptyFactory := attachedSession(t)
	ch := s.attachCh
	ptyFactory.StartErr = fmt.Errorf("attach-session: server not found")

	require.NotPanics(t, s.Detach)

	require.Error(t, s.AttachExitError())
	require.Contains(t, s.AttachExitError().Error(), "restoring attach pty")
	require.Nil(t, s.ptmx, "a failed Restore must leave ptmx nil")
	require.Nil(t, s.attachCh)
	assertClosed(t, ch)

	// A nil ptmx must not break polling — Poll never reads ptmx, only cmdExec.
	require.NotPanics(t, func() { _ = s.Poll() })
}

// T4: Restore (the self-heal building block Attach uses when ptmx is nil) re-allocates
// the pty on success and leaves it nil on failure, propagating the error.
func TestRestoreReallocatesOrPropagates(t *testing.T) {
	s, ptyFactory := attachedSession(t)

	s.ptmx = nil
	require.NoError(t, s.Restore())
	require.NotNil(t, s.ptmx)

	ptyFactory.StartErr = fmt.Errorf("attach-session failed")
	s.ptmx = nil
	require.Error(t, s.Restore())
	require.Nil(t, s.ptmx)
}

// T5: DetachSafely is unchanged — it tears down without re-Restoring (leaving ptmx
// nil), and a second call is a no-op (no double-close panic).
func TestDetachSafelyUnchangedAndIdempotent(t *testing.T) {
	s, _ := attachedSession(t)
	ch := s.attachCh

	require.NoError(t, s.DetachSafely())
	require.Nil(t, s.ptmx, "DetachSafely does not re-Restore")
	require.Nil(t, s.attachCh)
	assertClosed(t, ch)

	require.NoError(t, s.DetachSafely(), "second DetachSafely must be a no-op")
}

// T6: mixing the two detach paths in either order never panics or double-closes.
func TestDoubleDetachNoPanic(t *testing.T) {
	s, _ := attachedSession(t)
	require.NotPanics(t, s.Detach)
	require.NotPanics(t, func() { _ = s.DetachSafely() })

	s2, _ := attachedSession(t)
	require.NotPanics(t, func() { _ = s2.DetachSafely() })
	require.NotPanics(t, s2.Detach)
}
