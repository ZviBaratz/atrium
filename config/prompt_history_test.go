package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// texts projects the history entries to their texts for order assertions.
func histTexts(s *State) []string {
	out := make([]string, len(s.PromptHistory))
	for i, e := range s.PromptHistory {
		out[i] = e.Text
	}
	return out
}

func TestPromptHistory_OrderDedupeCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := DefaultState()

	require.NoError(t, s.AddPromptHistory("first"))
	require.NoError(t, s.AddPromptHistory("second"))
	// Most-recent-first.
	require.Equal(t, []string{"second", "first"}, histTexts(s))

	// A consecutive repeat of the head is dropped (no pile-up).
	require.NoError(t, s.AddPromptHistory("second"))
	require.Equal(t, []string{"second", "first"}, histTexts(s), "consecutive repeat must not be added")

	// A non-consecutive repeat is genuinely recent again, so it is added.
	require.NoError(t, s.AddPromptHistory("first"))
	require.Equal(t, []string{"first", "second", "first"}, histTexts(s),
		"a non-consecutive repeat is re-added at the front")

	// Blank / whitespace-only prompts are never recorded.
	require.NoError(t, s.AddPromptHistory("   "))
	require.NoError(t, s.AddPromptHistory(""))
	require.Equal(t, []string{"first", "second", "first"}, histTexts(s))
}

func TestPromptHistory_Capped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := DefaultState()
	for i := 0; i < maxPromptHistory+10; i++ {
		require.NoError(t, s.AddPromptHistory(fmt.Sprintf("prompt-%03d", i)))
	}
	require.Len(t, s.PromptHistory, maxPromptHistory, "history is capped at maxPromptHistory")
	// The newest survives at the front; the oldest (prompt-000) is evicted.
	require.Equal(t, fmt.Sprintf("prompt-%03d", maxPromptHistory+9), s.PromptHistory[0].Text)
	for _, e := range s.PromptHistory {
		require.NotEqual(t, "prompt-000", e.Text, "the oldest entry must be evicted past the cap")
	}
}

func TestPromptHistory_PersistRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, DefaultState().AddPromptHistory("remembered prompt"))

	loaded := LoadState()
	require.Equal(t, []string{"remembered prompt"}, histTexts(loaded), "prompt history survives a restart")
	require.NotZero(t, loaded.PromptHistory[0].AtUnix, "the entry carries a timestamp")
}

func TestPromptHistory_Clear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := DefaultState()
	require.NoError(t, s.AddPromptHistory("a"))
	require.NoError(t, s.AddPromptHistory("b"))
	require.NoError(t, s.ClearPromptHistory())
	require.Empty(t, s.PromptHistory)

	// Clearing an already-empty history is a no-op and survives a reload as empty.
	require.NoError(t, s.ClearPromptHistory())
	require.Empty(t, LoadState().PromptHistory)
}
