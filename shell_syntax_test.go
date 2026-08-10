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
// No Go tool in the gate can see a shell mistake: `go build`, `go vet`, gofmt and
// golangci-lint are all blind to these files. Since #652 the lint workflow carries a
// `shellcheck` job over the same set, which is where style and correctness findings
// now come from; the inline `# shellcheck disable` directives in test/smoke/run.sh and
// scripts/drive-agent.sh are narrow suppressions inside that run rather than, as
// before, references to a checker nothing ever invoked. The scripts are also the
// least-exercised code here: govulncheck.sh runs only in its own workflow, run.sh and
// render.sh need vhs/ttyd/ffmpeg, and drive-agent.sh is driven by hand when someone
// re-verifies an agent's heuristics — which may be months apart. A syntax break in any
// of them would sit undetected until the one moment it is needed.
//
// This stays syntax-only, and that is what it adds over the shellcheck job rather than
// a weaker copy of it. shellcheck parses with its own parser and does not promise
// parse-equivalence with the bash that will actually run these scripts; `bash -n` is
// the only check here that asks the interpreter itself. It is also the only one inside
// `just test`, so it still answers "does this file parse" on a machine with no
// shellcheck installed — `just ci` does not require the tool.
func TestShellScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	root := moduleRoot(t)

	// The script list comes from git, not from a directory walk, because "committed"
	// is exactly what this test means and a walk cannot express it. A walk pulls in
	// whatever is lying in the tree — a half-written repro.sh left in the root
	// mid-debug turns `just ci` red for a file that is not part of the repo — and it
	// needs a skip list to keep out vendored trees, which then has to be maintained
	// against guesses about what might appear under them.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--", "*.sh").Output()
	require.NoError(t, err, "git ls-files failed; this test reads the index, not the working tree")

	var scripts []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		// A file can be in the index and absent from the tree mid-rebase; nothing is
		// proved by trying to parse one that is not there.
		abs := filepath.Join(root, rel)
		if _, statErr := os.Stat(abs); statErr != nil {
			continue
		}
		scripts = append(scripts, abs)
	}

	// A guard that silently matches nothing is worse than no guard: if the pathspec
	// ever stops matching the scripts, this test would pass while checking zero files.
	require.NotEmpty(t, scripts, "git ls-files matched no *.sh — the query is broken, not the tree")

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
