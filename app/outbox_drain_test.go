package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainHome builds a home with storage wired up (the drain persists) and HOME
// pointed at a fresh temp dir so each test gets its own spool.
func drainHome(t *testing.T) *home {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	return h
}

// addInstance registers an instance at an explicit path, so tests can build the
// same-title-different-repo case the (Title, Path) match exists for.
func addInstance(t *testing.T, h *home, title, path string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: path, Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	return inst
}

// addPersistedInstance seeds a paused instance into state.json and loads it back
// through storage, which is the only route to an Instance whose unexported
// "started" flag is set — and SaveInstances persists nothing else. reattach
// short-circuits for a paused instance, so this touches no tmux.
func addPersistedInstance(t *testing.T, h *home, title, path string) *session.Instance {
	t.Helper()
	data, err := json.Marshal([]session.InstanceData{{
		Title: title, Path: path, Program: "echo", Status: session.Paused,
		Worktree: session.GitWorktreeData{RepoPath: path},
	}})
	require.NoError(t, err)
	require.NoError(t, config.LoadState().SaveInstances(data))

	st, err := session.NewStorage(config.LoadState())
	require.NoError(t, err)
	h.storage = st

	loaded, _, err := st.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.True(t, loaded[0].Started(), "only a started instance is persisted")
	h.list.AddInstance(loaded[0])
	return loaded[0]
}

func spool(t *testing.T, m outbox.Message) string {
	t.Helper()
	path, err := outbox.Write(m)
	require.NoError(t, err)
	return path
}

func spoolCount(t *testing.T) int {
	t.Helper()
	entries, err := outbox.List()
	require.NoError(t, err)
	return len(entries)
}

// TestDrainQueuesPromptAndRemovesFile is the end-to-end contract.
func TestDrainQueuesPromptAndRemovesFile(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "rebase on main"})

	cmd := h.drainOutbox()

	assert.Equal(t, 1, inst.QueueLen())
	view, _ := inst.QueueView()
	require.Len(t, view, 1)
	assert.Equal(t, "rebase on main", view[0])
	assert.Zero(t, spoolCount(t), "a delivered message must not be delivered twice")
	assert.NotNil(t, cmd, "a delivered prompt is surfaced to the user")
}

// TestDrainQueuesAsIdleOnlyFollowup is the guard on the delivery guarantee
// itself: a spooled prompt must reach the agent on exactly quick-send's terms,
// strictly when it next goes idle, never injected mid-turn.
//
// That guarantee lives in *which* queue call is used, and the two are one word
// apart. QueueFollowupPrompt stores a zero queue clock, which disables the 60s
// delivery-timeout valve in promptDeliveryReady; QueuePrompt (the boot-prompt
// path) stamps a live clock and so can force-inject once that valve opens. The
// clock is the only externally visible difference between them.
func TestDrainQueuesAsIdleOnlyFollowup(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "hello"})

	h.drainOutbox()

	require.Equal(t, 1, inst.QueueLen())
	assert.True(t, inst.PromptQueuedAt().IsZero(),
		"a zero queue clock is what makes delivery idle-only; a live one re-enables the timeout valve")
}

// TestDrainMatchesOnTitleAndPath is the guard for the identity rule. Titles are
// unique only within a repo group, so a same-titled session in another repo must
// never receive a message meant for this one — matching on title alone would
// send the prompt to whichever instance happened to be listed first.
func TestDrainMatchesOnTitleAndPath(t *testing.T) {
	h := drainHome(t)
	web := addInstance(t, h, "api", t.TempDir())
	svc := addInstance(t, h, "api", t.TempDir())

	spool(t, outbox.Message{Title: "api", Path: svc.Path, Text: "for svc"})
	h.drainOutbox()

	assert.Zero(t, web.QueueLen(), "the wrong repo's session must not receive it")
	require.Equal(t, 1, svc.QueueLen())
	view, _ := svc.QueueView()
	assert.Equal(t, "for svc", view[0])
}

// TestDrainDiscardsUnknownTarget: the session was killed between send and drain.
// The file must go, or it would be re-read every tick forever.
func TestDrainDiscardsUnknownTarget(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "gone", Path: "/repo/nowhere", Text: "hello"})

	cmd := h.drainOutbox()

	assert.Zero(t, inst.QueueLen(), "an unmatched message must not land on some other session")
	assert.Zero(t, spoolCount(t))
	assert.Nil(t, cmd, "nothing was delivered, so say nothing")
}

// TestDrainDiscardsExpired: a prompt spooled a day ago describes a tree that has
// moved on, so delivering it is worse than dropping it.
func TestDrainDiscardsExpired(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{
		Title: "fix-auth", Path: inst.Path, Text: "stale",
		CreatedAt: time.Now().Add(-outbox.TTL - time.Minute),
	})

	h.drainOutbox()

	assert.Zero(t, inst.QueueLen())
	assert.Zero(t, spoolCount(t))
}

// TestDrainDeliversMessageJustInsideTTL is the other side of that boundary.
func TestDrainDeliversMessageJustInsideTTL(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{
		Title: "fix-auth", Path: inst.Path, Text: "still good",
		CreatedAt: time.Now().Add(-outbox.TTL + time.Minute),
	})

	h.drainOutbox()
	assert.Equal(t, 1, inst.QueueLen())
}

// TestDrainDiscardsUndecodable: a corrupt file nobody can read and nobody
// deletes would be re-read on every tick for the life of the process.
func TestDrainDiscardsUndecodable(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())

	dir, err := outbox.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0000000000000000001-aaaaaaaa.json"), []byte("{broken"), 0o644))

	h.drainOutbox()

	assert.Zero(t, inst.QueueLen())
	assert.Zero(t, spoolCount(t))
}

// TestDrainPreservesOrder: two prompts for one session must arrive in the order
// they were sent, or a "do X then Y" pair silently inverts.
func TestDrainPreservesOrder(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())

	base := time.Now().Add(-time.Hour)
	for i, text := range []string{"first", "second", "third"} {
		spool(t, outbox.Message{
			Title: "fix-auth", Path: inst.Path, Text: text,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	h.drainOutbox()

	view, _ := inst.QueueView()
	assert.Equal(t, []string{"first", "second", "third"}, view)
}

// TestDrainHonorsBudget keeps one tick from blocking the UI goroutine on a large
// backlog; the rest arrives on the following ticks.
func TestDrainHonorsBudget(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())

	base := time.Now().Add(-time.Hour)
	total := outboxDrainBudget + 10
	for i := range total {
		spool(t, outbox.Message{
			Title: "fix-auth", Path: inst.Path, Text: "m",
			CreatedAt: base.Add(time.Duration(i) * time.Millisecond),
		})
	}

	h.drainOutbox()
	assert.Equal(t, outboxDrainBudget, inst.QueueLen())
	assert.Equal(t, total-outboxDrainBudget, spoolCount(t))

	h.drainOutbox()
	assert.Equal(t, total, inst.QueueLen())
	assert.Zero(t, spoolCount(t))
}

// TestDrainPersistsQueuedPrompts: the queue only survives a restart if it was
// written, and the drain is the one place that knows a prompt just arrived.
func TestDrainPersistsQueuedPrompts(t *testing.T) {
	h := drainHome(t)
	inst := addPersistedInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "persist me"})

	h.drainOutbox()

	var stored []session.InstanceData
	require.NoError(t, json.Unmarshal(config.LoadState().GetInstances(), &stored))
	require.NotEmpty(t, stored, "the drain must write the queue through to state.json")
	require.Len(t, stored[0].PromptQueue, 1)
	assert.Equal(t, "persist me", stored[0].PromptQueue[0].Text)
}

// TestDrainPoisonsUndeletableFile is the guard against infinite duplicate
// delivery: if the unlink keeps failing, re-reading the file every tick would
// re-queue the same prompt forever.
func TestDrainPoisonsUndeletableFile(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "hello"})

	dir, err := outbox.Dir()
	require.NoError(t, err)
	// A read-only directory makes unlink fail while the file stays readable.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	h.drainOutbox()
	require.Equal(t, 1, inst.QueueLen())
	require.Equal(t, 1, spoolCount(t), "the file could not be removed")

	h.drainOutbox()
	assert.Equal(t, 1, inst.QueueLen(), "a message that cannot be unlinked must not be re-delivered")

	h.drainOutbox()
	assert.Equal(t, 1, inst.QueueLen())
}

// TestDrainEmptySpoolIsQuiet: the steady state is an empty (or absent) spool, so
// it must cost nothing and say nothing on every one of the ~2 ticks per second.
func TestDrainEmptySpoolIsQuiet(t *testing.T) {
	h := drainHome(t)
	addInstance(t, h, "fix-auth", t.TempDir())

	dir, err := outbox.Dir()
	require.NoError(t, err)
	require.NoDirExists(t, dir, "nothing should create the spool until a send does")

	assert.Nil(t, h.drainOutbox())
}

// TestDrainNoticeCountsPrompts covers the singular/plural wording.
func TestDrainNoticeCountsPrompts(t *testing.T) {
	assert.Equal(t, "queued 1 prompt from atrium send", queuedPromptsNotice(1))
	assert.Equal(t, "queued 2 prompts from atrium send", queuedPromptsNotice(2))
}

// TestDrainRunsOnStaleAttachTick: the drain sits outside the attachGen guard,
// because a spooled prompt is not a pane observation and an attach gives no
// reason to drop it.
func TestDrainRunsOnStaleAttachTick(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "hello"})

	h.attachGen = 7
	h.Update(metadataUpdateDoneMsg{attachGen: 3}) // stale: captured before an attach

	assert.Equal(t, 1, inst.QueueLen())
	assert.Zero(t, spoolCount(t))
}

// TestDrainLeavesRejectionReceiptForUnknownTarget is what makes `send --wait`
// truthful. The drain unlinks the message whether it queued the prompt or threw
// it away, so without a receipt a discard is indistinguishable from a delivery —
// and the realistic discard is a session killed between resolve and drain, which
// is exactly when a sender must not be told "delivered".
func TestDrainLeavesRejectionReceiptForUnknownTarget(t *testing.T) {
	h := drainHome(t)
	addInstance(t, h, "fix-auth", t.TempDir())
	path := spool(t, outbox.Message{Title: "gone", Path: "/repo/nowhere", Text: "hello"})

	h.drainOutbox()

	reason, ok := outbox.Rejection(path)
	require.True(t, ok, "a discarded message must leave a receipt")
	assert.Contains(t, reason, "gone", "the reason should name the session")
	assert.Zero(t, spoolCount(t), "the message itself is still cleared")
}

// TestDrainLeavesRejectionReceiptForExpired covers the other discard path.
func TestDrainLeavesRejectionReceiptForExpired(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	path := spool(t, outbox.Message{
		Title: "fix-auth", Path: inst.Path, Text: "stale",
		CreatedAt: time.Now().Add(-outbox.TTL - time.Minute),
	})

	h.drainOutbox()

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "horizon")
}

// TestDrainLeavesNoReceiptOnDelivery: a receipt means failure, so a delivered
// message must not leave one or --wait would report a false negative.
func TestDrainLeavesNoReceiptOnDelivery(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	path := spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "hello"})

	h.drainOutbox()

	_, ok := outbox.Rejection(path)
	assert.False(t, ok, "a delivered message must leave no rejection receipt")
}

// TestDrainSweepsStaleRejections: a receipt is only read by a sender still
// blocked in --wait, so one past the horizon has no reader left and would
// otherwise accumulate for the life of the data dir.
func TestDrainSweepsStaleRejections(t *testing.T) {
	h := drainHome(t)
	addInstance(t, h, "fix-auth", t.TempDir())

	dir, err := outbox.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stale := filepath.Join(dir, fmt.Sprintf("%019d-aaaaaaaa.json.rejected", time.Now().UnixNano()))
	fresh := filepath.Join(dir, fmt.Sprintf("%019d-bbbbbbbb.json.rejected", time.Now().UnixNano()))
	require.NoError(t, os.WriteFile(stale, []byte("old reason"), 0o644))
	require.NoError(t, os.WriteFile(fresh, []byte("new reason"), 0o644))
	// A receipt ages by its own mtime, not the timestamp in its name — the two
	// diverge for an expired-message rejection, whose name carries the message's
	// long-past CreatedAt. Backdate the file itself to put it past the horizon.
	old := time.Now().Add(-outbox.TTL - time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	h.drainOutbox()

	assert.NoFileExists(t, stale, "a receipt whose mtime is past the horizon has no reader left")
	assert.FileExists(t, fresh, "a fresh receipt may still be collected by a waiting sender")
}

// TestDrainClearsSpoolEvenIfPersistFails: the prompt is already live in the
// session's queue by this point, and the TUI persists on every later mutation
// anyway — so leaving the file would only re-queue a duplicate on the next tick.
// A persist failure must therefore be logged, not retried.
func TestDrainClearsSpoolEvenIfPersistFails(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", t.TempDir())
	spool(t, outbox.Message{Title: "fix-auth", Path: inst.Path, Text: "hello"})

	// Make state.json unwritable while leaving the spool dir writable: the
	// atomic write needs to create a temp file in the data dir itself.
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	// Without this the test would pass just as happily if the chmod had no effect
	// and the persist actually succeeded.
	require.Error(t, h.persistInstances(), "fixture must genuinely make persisting fail")

	h.drainOutbox()

	assert.Equal(t, 1, inst.QueueLen(), "the prompt is still queued in memory")
	assert.Zero(t, spoolCount(t), "and the spool file is cleared rather than left to re-queue")
}

// TestRejectionSweepIsThrottled: the receipt GC enforces a 24-hour horizon and used to
// run on every ~500ms metadata tick, walking both spool directories each time — ~170k
// directory reads a day to collect a file that can wait. It must still run once per
// launch, because a receipt left by a previous run for a producer that never came back
// is exactly what it exists to collect.
func TestRejectionSweepIsThrottled(t *testing.T) {
	h := drainHome(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "gone", Path: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, outbox.Reject(path, "a reason nobody read"))
	stale := path + ".rejected"
	require.NoError(t, os.Chtimes(stale, time.Now().Add(-2*outbox.TTL), time.Now().Add(-2*outbox.TTL)))

	// A second receipt, aged the same way, to prove the second sweep is what is
	// skipped rather than the collection being a one-shot.
	other, err := outbox.WriteCreate(outbox.Request{Title: "gone-too", Path: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, outbox.Reject(other, "another"))

	h.drainOutbox() // the first tick of the run sweeps
	assert.NoFileExists(t, stale, "a receipt past the horizon is collected on the first tick")

	require.NoError(t, os.Chtimes(other+".rejected",
		time.Now().Add(-2*outbox.TTL), time.Now().Add(-2*outbox.TTL)))
	h.drainOutbox()
	assert.FileExists(t, other+".rejected", "and the next tick does not walk the spools again")

	// The control: past the interval it sweeps again, so the throttle is a delay and
	// not a one-shot.
	h.lastRejectionSweep = time.Now().Add(-2 * rejectionSweepInterval)
	h.drainOutbox()
	assert.NoFileExists(t, other+".rejected")
}

// TestRejectionSweepRunsEvenWhenThePromptSpoolIsUnreadable pins the ordering. The sweep
// collects receipts from BOTH spools and the create spool has no other collector —
// drainCreateRequests deliberately does not sweep, on the grounds that this runs first
// on the same tick. Below drainOutbox's early return that stops being true: one
// unreadable prompt directory strands the sweep for the life of the process, and every
// refused `atrium new` then leaks a receipt with nothing left to collect it.
//
// Asserted on lastRejectionSweep rather than on a collected file, because making the
// parent directory unreadable necessarily takes the nested create spool with it — the
// question here is only whether the sweep was reached.
func TestRejectionSweepRunsEvenWhenThePromptSpoolIsUnreadable(t *testing.T) {
	h := drainHome(t)
	dir, err := outbox.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// A real precondition, not drainOutbox's nil: it returns nil for an empty spool too,
	// so asserting that would pass whether or not the read actually failed — and the
	// mutation this test exists to catch would survive.
	_, listErr := outbox.List()
	require.Error(t, listErr, "precondition: the prompt spool must be unreadable")

	require.Nil(t, h.drainOutbox(), "the drain bails on the unreadable spool")
	assert.False(t, h.lastRejectionSweep.IsZero(),
		"the receipt GC must not be gated behind the prompt spool being readable")
}
