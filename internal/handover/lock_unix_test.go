//go:build !windows

package handover

import (
	"encoding/json"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/flock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shareLock takes the same shared lock Held takes, standing in for a second headless
// command probing concurrently.
func shareLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
}

// TestHeldIsFalseWithNoHandover is the steady state for anyone who has never
// attached: an answer, not a failure to get one, so a caller can warn about a
// missing TUI rather than staying silent.
func TestHeldIsFalseWithNoHandover(t *testing.T) {
	sandbox(t)
	held, p, known := Held()
	assert.False(t, held)
	assert.True(t, known, "a data dir with no lock file has answered the question")
	assert.Equal(t, Payload{}, p)
}

// TestHoldIsVisibleToHeld is the whole point of the package: while the lock is held,
// another process reading the same data dir sees it and can name what it was handed to.
func TestHoldIsVisibleToHeld(t *testing.T) {
	sandbox(t)
	release, err := Hold(Payload{Kind: KindAttach, Label: "fix-auth"})
	require.NoError(t, err)

	held, p, known := Held()
	assert.True(t, held)
	assert.True(t, known)
	assert.Equal(t, Payload{Kind: KindAttach, Label: "fix-auth"}, p)
	assert.Equal(t, `attached to session "fix-auth"`, p.Describe())

	release()
	held, p, known = Held()
	assert.False(t, held, "the release drops the lock")
	assert.True(t, known)
	assert.Equal(t, Payload{}, p, "and clears the payload, so nothing later reads a session that has detached")
}

// TestReleaseClearsThePayloadOnDisk pins the half of the release that is not the
// flock. The clear is what makes the gap between one Hold's release and the next
// one's write read as "unknown" rather than as the previous holder's session.
func TestReleaseClearsThePayloadOnDisk(t *testing.T) {
	path := sandbox(t)
	release, err := Hold(Payload{Kind: KindAttach, Label: "fix-auth"})
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, b, "the payload is written into the flocked descriptor")

	release()
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, b)
}

// TestHoldTruncatesTheLastHoldersPayload guards the ordering inside Hold. Written
// without the leading truncate, a shorter payload would leave the previous holder's
// tail behind and decode as neither.
func TestHoldTruncatesTheLastHoldersPayload(t *testing.T) {
	path := sandbox(t)
	// Plant a long payload directly rather than via a Hold/release pair, because the
	// release clears it — this is the shape a process killed mid-handover leaves.
	long, err := json.Marshal(Payload{Kind: KindCommand, Label: "a-very-long-command-name-indeed"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, long, 0o644))

	release, err := Hold(Payload{Kind: KindAttach, Label: "b"})
	require.NoError(t, err)
	defer release()

	_, p, _ := Held()
	assert.Equal(t, Payload{Kind: KindAttach, Label: "b"}, p)
}

// TestHoldRefusesASecondHolder is what makes the lock a lock. Two TUIs per data dir
// are already refused by tui.lock, so this is defence in depth — but it is also what
// a Held probe relies on to answer at all.
func TestHoldRefusesASecondHolder(t *testing.T) {
	sandbox(t)
	release, err := Hold(Payload{Kind: KindAttach, Label: "first"})
	require.NoError(t, err)
	defer release()

	release2, err := Hold(Payload{Kind: KindAttach, Label: "second"})
	require.Error(t, err)
	assert.Nil(t, release2, "an error means the lock was not taken, so there is nothing to release")

	_, p, _ := Held()
	assert.Equal(t, "first", p.Label, "and the refused holder did not overwrite the payload")
}

// TestConcurrentProbesDoNotReadEachOther is why Held takes a shared lock. Two headless
// commands run at once — an agent's `atrium new` beside a scripted `atrium send` — and
// with an exclusive probe each would be refused by the other and report a handover
// that is not happening.
func TestConcurrentProbesDoNotReadEachOther(t *testing.T) {
	path := sandbox(t)
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o644))

	// Hold a shared lock the way a concurrent Held would, then probe.
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, shareLock(f))

	held, _, known := Held()
	assert.False(t, held, "another probe's shared lock is not a handover")
	assert.True(t, known)
}

// TestHeldReportsUnknownForAnUnreadableLock covers the third answer. A caller told
// "unknown" stays silent instead of asserting something wrong, which is the contract
// tuiRunning states for the same reason.
func TestHeldReportsUnknownForAnUnreadableLock(t *testing.T) {
	path := sandbox(t)
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := os.OpenFile(path, os.O_RDONLY, 0o644); err == nil {
		t.Skip("running as a user that ignores file modes (root), so the lock is readable anyway")
	}

	held, _, known := Held()
	assert.False(t, held)
	assert.False(t, known, "a lock that cannot be opened is not a lock that is free")
}

// TestHeldReadsAnUnparseablePayloadAsUnknown is the write-window case: a probe that
// lands between the truncate and the write sees an empty file under a held lock, and
// must report the handover without inventing a label for it.
func TestHeldReadsAnUnparseablePayloadAsUnknown(t *testing.T) {
	path := sandbox(t)
	release, err := Hold(Payload{Kind: KindAttach, Label: "fix-auth"})
	require.NoError(t, err)
	defer release()
	// Stand in for the window by emptying the file under the live lock.
	require.NoError(t, os.Truncate(path, 0))

	held, p, known := Held()
	assert.True(t, held, "the lock is what says a handover is in progress")
	assert.True(t, known)
	assert.Equal(t, Payload{}, p, "and the payload is not invented from the last holder's")
	assert.Equal(t, "has handed its terminal to another program", p.Describe(),
		"which costs the reader the session name and nothing else — see Describe")
}

// TestHoldRetriesPastASharedLock is the retry's own guard, and the one that is not a
// race: with a reader's shared lock held for the whole test, Hold must spend its budget
// before giving up rather than failing on the first refusal. The elapsed floor is what a
// single non-blocking attempt cannot clear.
//
// The mutation it exists for is a one-line revert of flock.LockExclusive to a bare
// Flock(LOCK_EX|LOCK_NB), which nothing that predates the retry can see: every earlier
// test in this package takes the lock with no reader in the way.
func TestHoldRetriesPastASharedLock(t *testing.T) {
	path := sandbox(t)
	reader, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	require.NoError(t, shareLock(reader), "the shared lock a concurrent Held would hold")

	start := time.Now()
	release, err := Hold(Payload{Kind: KindAttach, Label: "fix-auth"})
	elapsed := time.Since(start)
	if err == nil {
		release()
		t.Fatal("a shared lock held for the whole call must refuse the exclusive one")
	}
	assert.ErrorIs(t, err, syscall.EWOULDBLOCK)
	assert.GreaterOrEqual(t, elapsed, time.Duration(flock.Attempts-1)*flock.Delay,
		"Hold gave up without spending its retry budget, so a passing probe can cost an attach its whole handover record")
}

// TestHoldSucceedsOnceAProbeLetsGo is the same defect from the direction a user meets it:
// a scripted `send`/`new` loop — which the README blesses as safe to run alongside a live
// Atrium, and which is the only thing that reads this lock — must not be able to make an
// attach go unrecorded. Held takes its shared lock for two syscalls, so the reader here
// holds it far longer than the real one ever does and Hold still has most of its budget
// left.
func TestHoldSucceedsOnceAProbeLetsGo(t *testing.T) {
	path := sandbox(t)
	reader, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	require.NoError(t, shareLock(reader))

	// The fd is read here and joined below, not touched from the goroutine and left to
	// run. An unlock landing after t.Cleanup has closed the file would name whatever fd
	// the runtime handed out next — releasing a flock some other test in this process
	// holds, which is a failure that surfaces anywhere but here.
	fd := int(reader.Fd())
	unlocked := make(chan struct{})
	go func() {
		defer close(unlocked)
		time.Sleep(2 * flock.Delay)
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()

	release, err := Hold(Payload{Kind: KindAttach, Label: "fix-auth"})
	<-unlocked
	require.NoError(t, err, "the attach must still be recorded once the probe lets go")
	t.Cleanup(release)

	held, p, known := Held()
	assert.True(t, held)
	assert.True(t, known)
	assert.Equal(t, "fix-auth", p.Label)
}
