package overlay

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkdirs creates the named subdirectories under root and returns root.
func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, os.MkdirAll(filepath.Join(root, n), 0o755))
	}
}

func TestDirectoryPicker_BrowsesOnDiskSubdirs(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "alpha", "beta", "gamma")
	// A plain file in the same dir must not appear as a candidate.
	require.NoError(t, os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0o644))

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/"))
	items := dp.visibleItems()

	assert.Contains(t, items, filepath.Join(root, "alpha"))
	assert.Contains(t, items, filepath.Join(root, "beta"))
	assert.Contains(t, items, filepath.Join(root, "gamma"))
	assert.NotContains(t, items, filepath.Join(root, "afile"), "files are not selectable directories")
}

func TestDirectoryPicker_BaseFuzzyFiltersOnDisk(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "alpha", "beta", "gamma")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/al"))
	items := dp.visibleItems()

	assert.Contains(t, items, filepath.Join(root, "alpha"))
	assert.NotContains(t, items, filepath.Join(root, "beta"))
}

func TestDirectoryPicker_CompletePrefix_UniqueCompletesWithoutSlash(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "alpha")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/al"))

	grew := dp.CompletePrefix()
	assert.True(t, grew, "Tab should complete the unique prefix")
	assert.Equal(t, root+"/alpha", dp.filter, "completes to the dir name, with NO trailing slash")
	// And the completed path is still selectable (not skipped past).
	assert.Equal(t, filepath.Join(root, "alpha"), dp.GetSelectedPath())
}

func TestDirectoryPicker_CompletePrefix_CommonPrefixOfSeveral(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "repo-foo", "repo-fox", "other")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/repo-"))

	grew := dp.CompletePrefix()
	assert.True(t, grew)
	assert.Equal(t, root+"/repo-fo", dp.filter, "extends to the common prefix of repo-foo/repo-fox")
}

func TestDirectoryPicker_CompletePrefix_NoOpWhenNothingToExtend(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "repo-foo", "repo-fox")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/repo-fo")) // already at the common prefix

	assert.False(t, dp.CompletePrefix(), "nothing left to complete → false so Tab advances focus")
}

func TestDirectoryPicker_CompletePrefix_NonPathReturnsFalse(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a"})
	dp.HandleKeyPress(runes("re")) // not path-like
	assert.False(t, dp.CompletePrefix())
}

func TestDirectoryPicker_CompletePrefix_FuzzyOnlyMatchDoesNotComplete(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "gamma")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/gm")) // fuzzy-matches gamma but is not a literal prefix
	// gamma shows in the list...
	assert.Contains(t, dp.visibleItems(), filepath.Join(root, "gamma"))
	// ...but Tab does not complete a non-prefix, so it falls through to advance.
	assert.False(t, dp.CompletePrefix())
}

func TestDirectoryPicker_CompletePrefix_RecasesTypedInput(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "RepoA")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/re")) // lower-case; on-disk is RepoA

	assert.True(t, dp.CompletePrefix())
	assert.Equal(t, root+"/RepoA", dp.filter, "completion emits the on-disk casing")
}

func TestDirectoryPicker_MissingParentFallsBackToLiteral(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nope", "xyz")

	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(missing))
	// No crash, and the fully-typed path remains selectable as the literal fallback.
	assert.Equal(t, missing, dp.GetSelectedPath())
}

func TestDirectoryPicker_LargeDirIsBounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 600; i++ {
		require.NoError(t, os.Mkdir(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755))
	}
	dp := NewDirectoryPicker(nil)
	dp.HandleKeyPress(runes(root + "/"))
	items := dp.visibleItems()
	// Bounded read (cap + the literal fallback), so it never lists all 600 and never hangs.
	assert.LessOrEqual(t, len(items), maxDirEntries+1)
}

func TestDirectoryPicker_NonPathFilterFuzzyRanksCandidates(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/home/me/atrium", "/home/me/archive"})
	dp.HandleKeyPress(runes("ar"))
	items := dp.visibleItems()
	require.Len(t, items, 2)
	assert.Equal(t, "/home/me/archive", items[0], "contiguous 'ar' ranks archive first")
}
