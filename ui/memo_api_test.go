package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// memoSeams are the four methods #565 exported purely so tests and benchmarks can
// see the memos: two counting seams and two invalidators.
//
// They are exported rather than hidden behind an export_test.go because the ones
// that matter most are called from package APP — resetRenderMemos keeps the cold
// benchmarks cold, and TestResetRenderMemos_ForcesEveryLayerToRecompose reads all
// three counters — and an export_test.go is visible only to its own package's test
// binary. The repo already carries this idiom (HasTerminalSession, "Exposed so a
// test can observe *when* creation happens").
//
// What that costs is a real hazard, and it is the reason for this test rather than
// a comment: nothing in the type system stops production code from calling
// ResetMemo in, say, a resize handler, which would silently reinstate the
// per-frame rebuild #565 removed and show up only as a benchmark nobody re-ran.
var memoSeams = []string{
	"ComposeRuns(",
	"PanelComposeRuns(",
	"ResetMemo(",
}

// The memo seams stay test-only.
//
// Scans every non-test .go file in the packages that could reach them and fails on
// a call. Declarations are skipped by matching a call site (`.Name(`) rather than
// the bare name, so the methods' own definitions do not trip it.
func TestMemoSeams_HaveNoProductionCallers(t *testing.T) {
	roots := []string{".", filepath.Join("..", "app")}

	scanned := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		require.NoError(t, err)

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, e.Name())
			src, err := os.ReadFile(path)
			require.NoError(t, err)
			scanned++

			for _, seam := range memoSeams {
				require.False(t, strings.Contains(string(src), "."+seam),
					"%s calls .%s — the memo seams are for tests and benchmarks only. "+
						"Invalidating a render memo from production reinstates the per-frame "+
						"rebuild #565 removed, and nothing but a benchmark would notice.",
					path, strings.TrimSuffix(seam, "("))
			}
		}
	}

	// Or the scan passes by reading nothing, which is what it would do if either
	// package moved.
	require.Greater(t, scanned, 20, "expected to scan both packages' sources")
}
