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
	require.False(t, i.PromptQueuedAt().IsZero(), "queuing a prompt must start its delivery clock")

	i.QueuePrompt("")
	require.Equal(t, "", i.Prompt())
	require.True(t, i.PromptQueuedAt().IsZero(), "an empty prompt must stop the delivery clock, matching FromInstanceData")
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

		i.ClearPrompt() // delivery confirmed
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
		i.ClearPrompt()
	}
	close(stop)
	wg.Wait()
}
