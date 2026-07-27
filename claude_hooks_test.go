package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two hooks in .claude/hooks are dev tooling, not part of the binary, but they
// are exactly the kind of thing that reads as coverage while doing nothing: a hook
// whose payload parsing breaks is discarded silently, with no error in the pane or
// the log. These tests are the negative controls that make that visible, and they
// run under `just ci` like everything else.
//
// They also pin the two bugs the matching logic actually had: a substring match
// cannot tell `golangci-lint run` from `which golangci-lint`, and a bypass tested
// against the whole command string lets `just lint && golangci-lint run` through.

// runHook feeds payload to a hook script and returns its exit code.
func runHook(t *testing.T, script string, payload any) int {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stdout, cmd.Stderr = nil, nil

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		t.Fatalf("running %s: %v", script, err)
	}
	return 0
}

// requireBash skips on a host without bash rather than failing: the hooks are
// developer tooling, and the Windows CI job has no business running them.
func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

const (
	lintHook     = ".claude/hooks/lint-through-just.sh"
	advisoryHook = ".claude/hooks/drift-sites-advisory.sh"

	hookAllow = 0 // exit 0: permit the call, stay silent
	hookBlock = 2 // exit 2: surface stderr to the agent
)

// TestLintThroughJustHook pins which commands the #486 guard blocks. The two
// directions matter equally: a bare run that slips through defeats the hook, and a
// blocked `which golangci-lint` breaks the PATH diagnosis CLAUDE.md prescribes.
func TestLintThroughJustHook(t *testing.T) {
	requireBash(t)

	cases := []struct {
		name string
		cmd  string
		want int
	}{
		// Real invocations, by every spelling that reaches the global cache.
		{"bare run", "golangci-lint run", hookBlock},
		{"run with packages", "golangci-lint run ./ui/...", hookBlock},
		{"dot-slash path", "./golangci-lint run", hookBlock},
		{"relative path", "bin/golangci-lint run", hookBlock},
		{"tilde path", "~/go/bin/golangci-lint run", hookBlock},
		{"absolute path", "/usr/local/bin/golangci-lint run", hookBlock},
		{"leading env assignment", "GOFLAGS=-mod=mod golangci-lint run", hookBlock},
		{"inside a subshell", "(cd ui && golangci-lint run)", hookBlock},

		// Chained after something allowed. The bypass must be judged per segment;
		// testing it against the whole string lets every one of these through.
		{"chained after just lint", "just lint && golangci-lint run", hookBlock},
		{"chained before just lint", "golangci-lint run && just lint", hookBlock},
		{"semicolon after just lint", "just lint ; golangci-lint run", hookBlock},
		{"after an echo naming the recipe", `echo "see just lint docs"; golangci-lint run`, hookBlock},
		{"after a cd", "cd /tmp && golangci-lint run --fix", hookBlock},

		// The recipe itself, which is the whole point of the redirect.
		{"just lint", "just lint", hookAllow},
		{"just lint scoped", "just lint ./ui/...", hookAllow},
		{"just ci", "just ci", hookAllow},
		{"unrelated recipes", "just fmt && just vet", hookAllow},

		// Mentions, not invocations. A substring match blocks all of these.
		{"which", "which golangci-lint", hookAllow},
		{"command -v", "command -v golangci-lint", hookAllow},
		{"grep for the name", "grep -rn golangci-lint .claude", hookAllow},
		{"echo the name", "echo golangci-lint run", hookAllow},
		{"shell comment", "# golangci-lint run", hookAllow},
		{"go install by module path", "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest", hookAllow},
		{"ls the binary", "ls ~/go/bin/golangci-lint", hookAllow},
		{"cat the hook itself", "cat " + lintHook, hookAllow},

		// Subcommands that read no cache, so #486 cannot apply to them.
		{"version", "golangci-lint version", hookAllow},
		{"--version", "golangci-lint --version", hookAllow},
		{"help", "golangci-lint help", hookAllow},
		{"no subcommand", "golangci-lint", hookAllow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHook(t, lintHook, map[string]any{
				"tool_input": map[string]any{"command": tc.cmd},
			})
			require.Equalf(t, tc.want, got, "command %q", tc.cmd)
		})
	}
}

// TestDriftAdvisoryHookFiresPerSite pins which edits draw an advisory. Both the
// repo-relative and absolute forms must fire: two branches once matched only
// absolute paths and were dead for the relative ones the Edit tool actually sends.
func TestDriftAdvisoryHookFiresPerSite(t *testing.T) {
	requireBash(t)

	fires := []string{
		"keys/registry.go",
		"config/types.go",
		"ui/theme/theme.go",
		"ui/theme/registry.go",
		"ui/theme/agent.go",
	}
	for _, rel := range fires {
		t.Run(rel, func(t *testing.T) {
			for _, path := range []string{rel, filepath.Join("/home/someone/atrium", rel)} {
				got := runHook(t, advisoryHook, map[string]any{
					"tool_input": map[string]any{"file_path": path},
				})
				require.Equalf(t, hookBlock, got, "path %q should draw an advisory", path)
			}
		})
	}

	quiet := []string{
		"app/app.go",
		"README.md",
		"ui/theme/current.go", // selection, not a glyph table
		"ui/theme/panel.go",
	}
	for _, rel := range quiet {
		t.Run("quiet/"+rel, func(t *testing.T) {
			got := runHook(t, advisoryHook, map[string]any{
				"tool_input": map[string]any{"file_path": rel},
			})
			require.Equalf(t, hookAllow, got, "path %q should stay silent", rel)
		})
	}
}

// TestHooksFailOpenOnBadPayload is the negative control. A hook that dies on a
// payload it cannot parse must degrade to "allow", because the alternative is
// blocking every Bash call in the session with no visible reason.
func TestHooksFailOpenOnBadPayload(t *testing.T) {
	requireBash(t)

	for _, script := range []string{lintHook, advisoryHook} {
		t.Run(filepath.Base(script), func(t *testing.T) {
			for _, raw := range []string{"", "not json at all", "{}", `{"tool_input":{}}`, `{"tool_input":null}`} {
				cmd := exec.CommandContext(t.Context(), "bash", script)
				cmd.Stdin = strings.NewReader(raw)
				require.NoErrorf(t, cmd.Run(), "payload %q must exit 0", raw)
			}
		})
	}
}

// TestDriftAdvisoryNamesOnlyRealFiles walks the paths the advisory prints and
// asserts each one exists. An advisory is a map, and a map naming a file that was
// renamed or never existed sends the reader looking for nothing — this pattern set
// shipped with a branch for ui/theme/glyphs.go, which is not a file.
func TestDriftAdvisoryNamesOnlyRealFiles(t *testing.T) {
	body, err := os.ReadFile(advisoryHook)
	require.NoError(t, err)

	// Comment lines are excluded: they explain the matching with deliberately
	// fictional examples ("/abs/path/keys/registry.go"), which are illustration
	// rather than a site the reader is being sent to.
	var live []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		live = append(live, line)
	}

	// Go-ish paths the advisory names, in its case patterns and its printed site
	// lists alike: a/b.go and a/b/c.go.
	paths := regexp.MustCompile(`[a-z_]+(?:/[a-z_]+)*\.go`).FindAllString(strings.Join(live, "\n"), -1)
	require.NotEmpty(t, paths, "expected the advisory to name some files")

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			_, err := os.Stat(p)
			require.NoErrorf(t, err, "advisory names %q, which does not exist", p)
		})
	}
}
