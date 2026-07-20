package app

import (
	"context"
	"encoding/json"
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

	loaded, err := st.LoadInstances(context.Background())
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
