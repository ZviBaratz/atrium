package memo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Enabled is an unsynchronized package global, so a test that flips it must not
// run concurrently with one that renders. Today nothing in app or ui calls
// t.Parallel(), which is what makes the seam safe — and a precondition nobody
// asserts is a precondition that decays.
//
// This walks the two packages that use the seam and fails on any FILE that both
// flips Enabled and calls t.Parallel(). Per-file rather than per-package on
// purpose: t.Parallel() elsewhere in a package is fine (it only interleaves with
// other parallel tests), and a package-wide ban would be a rule nobody could keep.
// What is not fine is the two appearing together, because a flipper marked
// parallel is exactly the race — and the author of that line is the one reading
// this file's name in the failure.
//
// An external test package (memo_test) so it can read sources without importing
// anything it is testing.
func TestEnabled_HasNoParallelFlippers(t *testing.T) {
	// Relative to internal/memo, where this test runs.
	roots := []string{
		filepath.Join("..", "..", "app"),
		filepath.Join("..", "..", "ui"),
	}

	checked := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		require.NoError(t, err, "the seam's users must stay findable from here")

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, e.Name())
			src, err := os.ReadFile(path)
			require.NoError(t, err)
			body := string(src)

			if !strings.Contains(body, "memo.SetEnabled(") {
				continue
			}
			checked++
			// A boolean, not require.NotContains: the haystack is a whole source
			// file, and NotContains prints it on failure — burying the one line that
			// matters under a few hundred.
			require.False(t, strings.Contains(body, "t.Parallel()"),
				"%s flips memo.Enabled, an unsynchronized global, and also calls t.Parallel() — "+
					"that is a data race on the render path and nondeterministic compose counts. "+
					"Drop the t.Parallel(), or move the flip to a test that has none.", path)
		}
	}

	// The walk has to have found the flippers, or it passes by looking at nothing —
	// which is what it would do after a rename of the seam or a move of these tests.
	require.NotZero(t, checked, "found no file flipping memo.SetEnabled; has the seam been renamed or moved?")
}
