package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statePath returns the resolved state.json path for the sandboxed HOME.
func statePath(t *testing.T) string {
	t.Helper()
	dir, err := GetConfigDir()
	require.NoError(t, err)
	return filepath.Join(dir, StateFileName)
}

func TestLoadState_CorruptFileIsPreservedNotDiscarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Seed a real state with a recent path, then corrupt the file on disk.
	require.NoError(t, DefaultState().AddRecentPath("/keepme"))
	path := statePath(t)
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0644))

	loaded := LoadState()
	// Falls back to defaults (no crash) rather than the corrupt content.
	assert.Empty(t, loaded.GetRecentPaths())
	// The corrupt bytes are preserved alongside, recoverable.
	corrupt, err := os.ReadFile(path + ".corrupt")
	require.NoError(t, err)
	assert.Equal(t, "{not valid json", string(corrupt))
}

func TestLoadState_EmptyFileIsNotArchived(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path := statePath(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, nil, 0644))

	_ = LoadState()
	assert.NoFileExists(t, path+".corrupt")
}

func TestLoadState_SweepsStaleTempFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path := statePath(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	orphan := filepath.Join(filepath.Dir(path), "."+StateFileName+".tmp-987654")
	require.NoError(t, os.WriteFile(orphan, []byte("partial"), 0600))

	_ = LoadState()
	assert.NoFileExists(t, orphan)
}

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
