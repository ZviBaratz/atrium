//go:build !windows

package flock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// open makes a lock file and a second, independent descriptor on it. Two descriptors are
// the point: flock is per open-file-description, so a single *os.File cannot contend with
// itself and a test that reused one would prove nothing.
func open2(t *testing.T) (acquirer, reader *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.lock")
	for _, f := range []**os.File{&acquirer, &reader} {
		h, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		require.NoError(t, err)
		t.Cleanup(func() { _ = h.Close() })
		*f = h
	}
	return acquirer, reader
}

func TestLockExclusiveTakesAFreeLock(t *testing.T) {
	f, _ := open2(t)
	require.NoError(t, LockExclusive(f, Attempts, Delay))
}

// TestLockExclusiveRetriesPastASharedLock is the whole reason this package exists: a
// reader's shared lock refuses an exclusive request, so an acquirer that tries once can be
// refused by a probe that is mid-question.
func TestLockExclusiveRetriesPastASharedLock(t *testing.T) {
	f, reader := open2(t)
	require.NoError(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))

	fd := int(reader.Fd())
	unlocked := make(chan struct{})
	go func() {
		defer close(unlocked)
		time.Sleep(2 * Delay)
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()

	err := LockExclusive(f, Attempts, Delay)
	<-unlocked
	require.NoError(t, err, "a passing shared lock must not defeat the acquirer")
}

// TestLockExclusiveSpendsItsBudget pins the retry deterministically, where the test above
// depends on a goroutine being scheduled.
func TestLockExclusiveSpendsItsBudget(t *testing.T) {
	f, reader := open2(t)
	require.NoError(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))

	start := time.Now()
	err := LockExclusive(f, Attempts, Delay)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, syscall.EWOULDBLOCK)
	assert.GreaterOrEqual(t, elapsed, time.Duration(Attempts-1)*Delay,
		"gave up without spending the budget, so a passing probe can refuse an acquirer")
}

// TestLockExclusiveNeverReportsSuccessWithoutTheLock is the trap the attempts guard exists
// for. `var err error` before the loop and `return err` after it means a budget of zero
// would return nil having taken nothing — silently converting a single-instance guard into
// a no-op, with #230 reopened and no symptom. Every non-positive budget must still make one
// real attempt and report what it found.
func TestLockExclusiveNeverReportsSuccessWithoutTheLock(t *testing.T) {
	for _, attempts := range []int{0, -1} {
		f, reader := open2(t)
		require.NoError(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_EX|syscall.LOCK_NB),
			"an exclusive holder nobody may be told they displaced")

		err := LockExclusive(f, attempts, Delay)
		assert.ErrorIs(t, err, syscall.EWOULDBLOCK, "attempts=%d reported success on a held lock", attempts)
	}
}

// TestLockExclusiveDoesNotRetryAOtherError: contention is worth waiting out, a bad
// descriptor is not. Retrying a permanent failure would spend the whole budget on every
// call for no chance of success.
func TestLockExclusiveDoesNotRetryAOtherError(t *testing.T) {
	f, _ := open2(t)
	require.NoError(t, f.Close()) // a closed descriptor gives EBADF, not EWOULDBLOCK

	start := time.Now()
	err := LockExclusive(f, Attempts, Delay)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.NotErrorIs(t, err, syscall.EWOULDBLOCK)
	assert.Less(t, elapsed, time.Duration(Attempts-1)*Delay, "a non-contention error must return at once")
}
