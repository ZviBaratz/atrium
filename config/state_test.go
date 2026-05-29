package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddRecentPath_OrderDedupCap(t *testing.T) {
	// Isolate SaveState writes from the real ~/.claude-squad/state.json.
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	require.NoError(t, s.AddRecentPath("/a"))
	require.NoError(t, s.AddRecentPath("/b"))
	require.NoError(t, s.AddRecentPath("/a")) // re-add moves to front and dedups
	assert.Equal(t, []string{"/a", "/b"}, s.GetRecentPaths())

	// Empty path is a no-op.
	require.NoError(t, s.AddRecentPath(""))
	assert.Equal(t, []string{"/a", "/b"}, s.GetRecentPaths())
}

func TestAddRecentPath_Capped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	for i := 0; i < maxRecentPaths+5; i++ {
		require.NoError(t, s.AddRecentPath(fmt.Sprintf("/p%d", i)))
	}
	got := s.GetRecentPaths()
	assert.Len(t, got, maxRecentPaths)
	// Most-recent-first: the last path added is at the front.
	assert.Equal(t, fmt.Sprintf("/p%d", maxRecentPaths+4), got[0])
}

func TestState_RecentPathsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	require.NoError(t, s.AddRecentPath("/x"))
	require.NoError(t, s.AddRecentPath("/y"))

	loaded := LoadState()
	assert.Equal(t, []string{"/y", "/x"}, loaded.GetRecentPaths())
}

func TestState_CollapsedReposRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	require.NoError(t, s.SetCollapsedRepos([]string{"repoA", "repoB"}))

	loaded := LoadState()
	assert.Equal(t, []string{"repoA", "repoB"}, loaded.GetCollapsedRepos())
}
