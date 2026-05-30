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

func TestState_ListRatioDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A fresh state uses the default split.
	assert.Equal(t, defaultListRatio, DefaultState().GetListRatio())

	// A zero value (e.g. an older state.json with no list_ratio key) also reads
	// as the default rather than collapsing the list to nothing.
	assert.Equal(t, defaultListRatio, (&State{}).GetListRatio())
}

func TestState_ListRatioClampAndRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()

	// A valid ratio is stored and survives a reload.
	require.NoError(t, s.SetListRatio(0.45))
	assert.Equal(t, 0.45, LoadState().GetListRatio())

	// Out-of-range values clamp to the bounds.
	require.NoError(t, s.SetListRatio(0.9))
	assert.Equal(t, maxListRatio, s.GetListRatio())
	require.NoError(t, s.SetListRatio(0.01))
	assert.Equal(t, minListRatio, s.GetListRatio())
}
