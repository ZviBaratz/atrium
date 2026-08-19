//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendWarnsWhenNoTUIRunning: the send genuinely succeeded — the message is
// durable and will be delivered — so this exits 0, but silence would let an
// orchestrator believe an agent had been prompted when nothing was listening.
//
// Unix-only: acquireTUILock is a no-op on Windows, so tuiRunning reports
// "unknown" there and deliberately says nothing rather than guess.
func TestSendWarnsWhenNoTUIRunning(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "a deferred delivery is not a failure")
	assert.Contains(t, stderr, "no atrium TUI is running")
	assert.Len(t, spooled(t), 1)
}

// TestSendStaysQuietWhileATUIHoldsTheLock is the other side of that warning: the
// normal case, sending while watching the fleet, must not nag.
func TestSendStaysQuietWhileATUIHoldsTheLock(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	holdTUILock(t)

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err)
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestSendWarnsWhileAtriumIsParked is the prompt spool's half of #760. A queued prompt
// waiting for a detach is less surprising than a session that does not exist — the
// queue overlay lists it and `atrium ls` reports it as queued_prompts — but the same
// silence was there, and it is one predicate.
func TestSendWarnsWhileAtriumIsParked(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	holdParked(t, "fix-auth")

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "the prompt is durable; it lands on the detach")
	assert.Contains(t, stderr, "poll loop is parked")
	assert.Contains(t, stderr, "nothing is delivering this yet", "send's verb, not new's")
	assert.NotContains(t, stderr, "no atrium TUI is running")
}

// TestSendWaitNamesWhoIsNotDraining: the old deadline copy ended "will be delivered the
// next time one runs", which is plainly false while a TUI is up with its terminal handed
// away — it delivers on the detach. That was the one message in the pair that asserted
// something untrue rather than merely vague.
func TestSendWaitNamesWhoIsNotDraining(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	holdParked(t, "fix-auth")

	_, _, err := send(t, "fix-auth", "", "hello", 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still queued", "the message survives a timeout; say so")
	assert.Contains(t, err.Error(), "poll loop is parked")
	assert.NotContains(t, err.Error(), "the next time one runs")
}

// TestTUIRunningDetectsHeldLock pins the probe itself, independent of send.
func TestTUIRunningDetectsHeldLock(t *testing.T) {
	sandboxDataDir(t)

	running, known := tuiRunning()
	require.True(t, known)
	assert.False(t, running, "nothing holds the lock yet")

	path, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(path)
	require.NoError(t, err)

	running, known = tuiRunning()
	assert.True(t, known)
	assert.True(t, running, "a held lock means a TUI is up")

	release()
	running, _ = tuiRunning()
	assert.False(t, running, "releasing the lock frees the probe again")
}

// TestTUIRunningIgnoresAConcurrentProbe: the probe is shared, so two headless commands
// asking at the same instant do not read each other as a running TUI.
//
// This is the reading #760 made dangerous rather than merely wrong. drainState takes a
// running TUI with a free handover lock as drainLive, and drainerClause then tells a
// --wait caller the outbox "is being read" — a confident sentence about a TUI that does
// not exist, replacing an enumeration that was at least never false. Reverting tuiRunning
// to acquireTUILock's exclusive request is what this catches.
func TestTUIRunningIgnoresAConcurrentProbe(t *testing.T) {
	sandboxDataDir(t)
	path, err := tuiLockPath()
	require.NoError(t, err)
	other, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	require.NoError(t, syscall.Flock(int(other.Fd()), syscall.LOCK_SH|syscall.LOCK_NB),
		"the shared lock another headless command holds mid-probe")

	running, known := tuiRunning()
	assert.True(t, known)
	assert.False(t, running, "another probe is not a TUI")
}

// TestTUIRunningLeavesNoLockFile: a probe reads the data dir, so it must not write to it.
// The README's account of what a scripted `send`/`new` loop touches — the request it
// spools, and nothing else — is only true while this holds.
func TestTUIRunningLeavesNoLockFile(t *testing.T) {
	sandboxDataDir(t)
	running, known := tuiRunning()
	require.True(t, known, "a data dir with no lock file has answered the question")
	require.False(t, running)

	path, err := tuiLockPath()
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the probe created the lock file it only meant to read")
}

// TestStartingTUIOutlastsAProbesLock: a shared lock still refuses an exclusive one, so
// switching the probe to LOCK_SH does nothing for this direction — the retry does.
//
// Without it, acquireTUILockOrWarn maps the refusal to errTUIAlreadyRunning and the bare
// `atrium` exits with "atrium is already running for this data directory", naming an
// instance that does not exist. A user who launched Atrium while an agent's `atrium new`
// was mid-probe met that, and #760 made it likelier by probing on the --wait path too and
// again at every deadline.
func TestStartingTUIOutlastsAProbesLock(t *testing.T) {
	sandboxDataDir(t)
	path, err := tuiLockPath()
	require.NoError(t, err)
	probe, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = probe.Close() })
	fd := int(probe.Fd())
	require.NoError(t, syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB))

	// The fd is read here and joined below, not touched from the goroutine and left to
	// run. An unlock landing after t.Cleanup has closed the file would name whatever fd
	// the runtime handed out next — releasing a flock some other test in this process
	// holds, which is a failure that surfaces anywhere but here.
	unlocked := make(chan struct{})
	go func() {
		defer close(unlocked)
		time.Sleep(2 * lockRetryDelay)
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()

	release, err := acquireTUILock(path)
	<-unlocked
	require.NoError(t, err, "a TUI must not be refused its own lock by a passing probe")
	release()
}

// TestAcquireTUILockSpendsItsRetryBudget is the deterministic half of the above: with a
// reader's shared lock held for the whole call, acquireTUILock must spend its budget before
// refusing rather than give up on the first EWOULDBLOCK. The elapsed floor is what a single
// non-blocking attempt cannot clear, and it is computed from the two constants so the
// bound has one home.
//
// TestStartingTUIOutlastsAProbesLock covers the same mutation from the success side, but it
// depends on a goroutine being scheduled; this one does not.
func TestAcquireTUILockSpendsItsRetryBudget(t *testing.T) {
	sandboxDataDir(t)
	path, err := tuiLockPath()
	require.NoError(t, err)
	probe, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = probe.Close() })
	require.NoError(t, syscall.Flock(int(probe.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))

	start := time.Now()
	release, err := acquireTUILock(path)
	elapsed := time.Since(start)
	if err == nil {
		release()
		t.Fatal("a shared lock held for the whole call must refuse the exclusive one")
	}
	assert.ErrorIs(t, err, errTUIAlreadyRunning)
	assert.GreaterOrEqual(t, elapsed, time.Duration(lockAttempts-1)*lockRetryDelay,
		"acquireTUILock gave up without spending its retry budget, so a passing probe can refuse a starting TUI")
}
