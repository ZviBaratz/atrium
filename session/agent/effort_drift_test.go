package agent

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestClaudeEffortLevels_MatchInstalledCLI sources the valid --effort levels from
// the installed claude binary and asserts ClaudeEffortLevels matches, so a CLI
// that adds or removes a level trips this test rather than silently drifting. It
// self-skips when claude is not on PATH (like the real-tmux tests) and runs under
// a temp HOME so it never reads the user's real config. `--help` short-circuits
// before any session/API call (exit 0, no network).
func TestClaudeEffortLevels_MatchInstalledCLI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH; skipping effort-level drift check")
	}
	cmd := exec.CommandContext(context.Background(), "claude", "--effort", "__atrium_drift_probe__", "--help")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, _ := cmd.CombinedOutput() // exit 0 expected; parse regardless

	// Prefer the warning line: "... Valid values: low, medium, high, xhigh, max."
	m := regexp.MustCompile(`Valid values:\s*([a-z, ]+?)\.`).FindSubmatch(out)
	if m == nil {
		// Fallback: the --help line "Effort level for the current session (a, b, c)".
		m = regexp.MustCompile(`Effort level for the current session \(([a-z, ]+)\)`).FindSubmatch(out)
	}
	if m == nil {
		t.Skipf("no parseable effort-level list in claude output; format may have changed:\n%s", out)
	}

	var got []string
	for _, part := range strings.Split(string(m[1]), ",") {
		if s := strings.TrimSpace(part); s != "" {
			got = append(got, s)
		}
	}
	want := append([]string(nil), ClaudeEffortLevels...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("installed claude --effort levels = %v; ClaudeEffortLevels = %v — update session/agent/effort.go", got, want)
	}
}
