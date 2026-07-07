package session

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueuePrompt(t *testing.T) {
	i := &Instance{Title: "queue"}

	i.QueuePrompt("do the thing")
	require.Equal(t, "do the thing", i.Prompt())
	require.Equal(t, 1, i.QueueLen())
	require.False(t, i.PromptQueuedAt().IsZero(), "queuing a boot prompt must start its delivery clock")

	i.QueuePrompt("")
	require.Equal(t, "do the thing", i.Prompt(), "an empty prompt is a no-op and must not disturb the queue")
	require.Equal(t, 1, i.QueueLen())
}

// TestQueuePromptAppendsFIFO pins the FIFO contract: appends go to the tail, the head
// is the next to deliver, and ClearPrompt pops the head to promote the successor.
func TestQueuePromptAppendsFIFO(t *testing.T) {
	i := &Instance{Title: "fifo"}
	i.QueuePrompt("first")
	i.QueueFollowupPrompt("second")

	require.Equal(t, 2, i.QueueLen())
	require.Equal(t, "first", i.Prompt(), "the head is the first-queued prompt")

	prompt, ok := i.ClaimPrompt()
	require.True(t, ok)
	require.Equal(t, "first", prompt)

	i.ClearPrompt("first") // delivery confirmed: pop the head
	require.Equal(t, 1, i.QueueLen())
	require.Equal(t, "second", i.Prompt(), "popping the head promotes the successor")
	require.False(t, i.PromptSending(), "a settled delivery lowers the in-flight guard")
}

// TestQueueFollowupHasZeroClock pins the boot-vs-follow-up distinction: a follow-up
// carries a zero clock (strict idle-only, so promptDeliveryReady never force-injects it
// mid-turn), while a boot prompt carries a live clock (the 60s valve).
func TestQueueFollowupHasZeroClock(t *testing.T) {
	boot := &Instance{Title: "boot"}
	boot.QueuePrompt("boot prompt")
	require.False(t, boot.PromptQueuedAt().IsZero(), "a boot prompt keeps a live delivery clock")

	followup := &Instance{Title: "followup"}
	followup.QueueFollowupPrompt("quick send")
	require.True(t, followup.PromptQueuedAt().IsZero(), "a follow-up must have a zero clock (strict idle-only)")
}

// TestClearPromptMatchedDequeue pins the double-settle guard: a settle whose text no
// longer heads the queue leaves the head (a newer prompt) intact rather than eating it,
// while still lowering the in-flight guard.
func TestClearPromptMatchedDequeue(t *testing.T) {
	i := &Instance{Title: "matched"}
	i.QueuePrompt("keep me")
	_, ok := i.ClaimPrompt()
	require.True(t, ok)

	i.ClearPrompt("stale text") // a mismatched settle must not pop the head
	require.Equal(t, "keep me", i.Prompt(), "a mismatched settle must not eat the current head")
	require.Equal(t, 1, i.QueueLen())
	require.False(t, i.PromptSending(), "a mismatched settle still lowers the in-flight guard")
}

// TestClearPromptSendingKeepsClock pins that a soft deferral is a retry, not a promotion:
// it must leave the head's timeout clock untouched so a chatty boot keeps accumulating
// toward its 60s valve.
func TestClearPromptSendingKeepsClock(t *testing.T) {
	i := &Instance{Title: "defer"}
	i.QueuePrompt("do the thing")
	queuedAt := i.PromptQueuedAt()
	require.False(t, queuedAt.IsZero())

	_, ok := i.ClaimPrompt()
	require.True(t, ok)
	i.ClearPromptSending()

	require.Equal(t, queuedAt, i.PromptQueuedAt(), "a deferral must not reset the head's delivery clock")
}

func TestClaimPrompt(t *testing.T) {
	t.Run("nothing queued refuses the claim", func(t *testing.T) {
		i := &Instance{Title: "empty"}
		_, ok := i.ClaimPrompt()
		require.False(t, ok)
		require.False(t, i.PromptSending(), "a refused claim must not raise the in-flight guard")
	})

	t.Run("queued prompt is claimed exactly once", func(t *testing.T) {
		i := &Instance{Title: "claim"}
		i.QueuePrompt("do the thing")

		prompt, ok := i.ClaimPrompt()
		require.True(t, ok)
		require.Equal(t, "do the thing", prompt)
		require.True(t, i.PromptSending(), "a claim must raise the in-flight guard")

		_, ok = i.ClaimPrompt()
		require.False(t, ok, "a second claim while a send is in flight must be refused")
	})

	t.Run("deferred send can be reclaimed", func(t *testing.T) {
		i := &Instance{Title: "reclaim"}
		i.QueuePrompt("do the thing")
		_, ok := i.ClaimPrompt()
		require.True(t, ok)

		i.ClearPromptSending() // soft outcome: prompt stays queued for the next tick
		prompt, ok := i.ClaimPrompt()
		require.True(t, ok, "after a deferred send the prompt must be claimable again")
		require.Equal(t, "do the thing", prompt)
	})

	t.Run("delivered prompt cannot be reclaimed", func(t *testing.T) {
		i := &Instance{Title: "done"}
		i.QueuePrompt("do the thing")
		_, ok := i.ClaimPrompt()
		require.True(t, ok)

		i.ClearPrompt("do the thing") // delivery confirmed
		_, ok = i.ClaimPrompt()
		require.False(t, ok)
		require.False(t, i.PromptSending())
	})
}

// TestPromptStateConcurrentAccess pins the promptMu contract under the race detector:
// the metadata tick's cmd goroutines read the queued-prompt state off-thread
// (pollTargets, collectMetadata) — and during an attach the keeper both reads and
// writes it — while the main loop queues, claims, and settles prompts.
func TestPromptStateConcurrentAccess(t *testing.T) {
	i := &Instance{Title: "race"}
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // the tick fan-out's off-thread readers
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = i.Prompt()
				_ = i.PromptQueuedAt()
				_ = i.PromptSending()
			}
		}
	}()

	for range 200 { // writer: queue → claim → settle, as the main loop / keeper would
		i.QueuePrompt("do the thing")
		if _, ok := i.ClaimPrompt(); ok {
			i.ClearPromptSending()
		}
		i.ClearPrompt("do the thing")
	}
	close(stop)
	wg.Wait()
}
