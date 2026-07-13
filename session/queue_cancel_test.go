package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newQueueInstance(t *testing.T) *Instance {
	t.Helper()
	i, err := NewInstance(InstanceOptions{Title: "q", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	return i
}

func TestCancelQueuedPrompt(t *testing.T) {
	t.Run("removes a tail entry, preserving order", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		i.QueueFollowupPrompt("b")
		i.QueueFollowupPrompt("c")
		require.True(t, i.CancelQueuedPrompt(1, "b"))
		texts, _ := i.QueueView()
		require.Equal(t, []string{"a", "c"}, texts)
	})

	t.Run("removes the head when not in flight", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		i.QueueFollowupPrompt("b")
		require.True(t, i.CancelQueuedPrompt(0, "a"))
		texts, _ := i.QueueView()
		require.Equal(t, []string{"b"}, texts)
	})

	t.Run("refuses the in-flight head", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		_, ok := i.ClaimPrompt() // raises promptInFlight on the head
		require.True(t, ok)
		require.False(t, i.CancelQueuedPrompt(0, "a"))
		require.Equal(t, 1, i.QueueLen(), "the in-flight head stays")
	})

	t.Run("text mismatch is a no-op", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		require.False(t, i.CancelQueuedPrompt(0, "stale"))
		require.Equal(t, 1, i.QueueLen())
	})

	t.Run("out-of-range index is a no-op", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		require.False(t, i.CancelQueuedPrompt(-1, "a"))
		require.False(t, i.CancelQueuedPrompt(5, "a"))
		require.Equal(t, 1, i.QueueLen())
	})
}

func TestQueueView(t *testing.T) {
	i := newQueueInstance(t)
	texts, inFlight := i.QueueView()
	require.Empty(t, texts)
	require.False(t, inFlight)

	i.QueueFollowupPrompt("a")
	i.QueueFollowupPrompt("b")
	texts, inFlight = i.QueueView()
	require.Equal(t, []string{"a", "b"}, texts)
	require.False(t, inFlight)

	_, _ = i.ClaimPrompt()
	_, inFlight = i.QueueView()
	require.True(t, inFlight, "QueueView reports the head in flight after ClaimPrompt")
}
