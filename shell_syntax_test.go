package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestShellScriptsParse runs `bash -n` over every committed shell script.
//
// Nothing else in the gate can see a shell mistake: `go build`, `go vet`, gofmt and
// golangci-lint are all blind to these files, and CI has no shellcheck job. What the
// repo does carry is inline `# shellcheck disable` directives — in test/smoke/run.sh
// and scripts/drive-agent.sh — and those SUPPRESS a checker rather than run one, so
// they leave the gap exactly where it was (#652 tracks closing it). The
// scripts are also the least-exercised code here: govulncheck.sh runs only in its own
// workflow, run.sh and render.sh need vhs/ttyd/ffmpeg, and drive-agent.sh is driven by
// hand when someone re-verifies an agent's heuristics — which may be months apart. A
// syntax break in any of them would sit undetected until the one moment it is needed.
//
// This is deliberately syntax-only. It cannot start failing on style the way a
// shellcheck gate could, so adding it does not change what an unrelated PR is judged
// on; it only answers "does this file still parse".
func TestShellScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	root := moduleRoot(t)
	var scripts []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored JS and Go fixtures hold shell-shaped files that are not ours
			// to parse, and .git holds sample hooks shipped by git itself.
			switch d.Name() {
			case ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".sh") {
			scripts = append(scripts, path)
		}
		return nil
	}))

	// A guard that silently matches nothing is worse than no guard: if the walk ever
	// stops finding the scripts, this test would pass while checking zero files.
	require.NotEmpty(t, scripts, "found no *.sh to check — the walk is broken, not the tree")

	for _, script := range scripts {
		rel, err := filepath.Rel(root, script)
		require.NoError(t, err)
		t.Run(rel, func(t *testing.T) {
			// `bash -n` parses without executing, so it always terminates; the
			// timeout is only so a wedged fork cannot hang the package's run.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bash, "-n", script).CombinedOutput()
			require.NoError(t, err, "bash -n %s:\n%s", rel, out)
		})
	}
}
