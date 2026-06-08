package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic_ContentsAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	require.NoError(t, writeFileAtomic(path, []byte(`{"hello":"world"}`), 0644))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"hello":"world"}`, string(got))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
}

func TestWriteFileAtomic_NoLeftoverTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	require.NoError(t, writeFileAtomic(path, []byte("a"), 0644))
	require.NoError(t, writeFileAtomic(path, []byte("bb"), 0644))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	// Only the target file should remain — no ".state.json.tmp-*" orphans.
	require.Len(t, entries, 1)
	assert.Equal(t, "state.json", entries[0].Name())
}

func TestWriteFileAtomic_OverwritePreservesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	require.NoError(t, writeFileAtomic(path, []byte("original"), 0644))
	require.NoError(t, writeFileAtomic(path, []byte("replacement"), 0644))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "replacement", string(got))
}

func TestSweepStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Simulate a crash between CreateTemp and Rename: an orphaned temp remains.
	orphan := filepath.Join(dir, ".state.json.tmp-123456")
	require.NoError(t, os.WriteFile(orphan, []byte("partial"), 0600))
	// A real state file and an unrelated file must be left untouched.
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0644))
	unrelated := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(unrelated, []byte("{}"), 0644))

	sweepStaleTempFiles(path)

	assert.NoFileExists(t, orphan)
	assert.FileExists(t, path)
	assert.FileExists(t, unrelated)
}
