package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// minTmuxRe pulls the assignment out of the script. It is anchored to the start of a
// line so the sentence about 3.2 in the comment above it cannot match.
var minTmuxRe = regexp.MustCompile(`(?m)^MIN_TMUX=(\S+)$`)

// TestDriveAgentTmuxFloorMatchesAtrium keeps scripts/drive-agent.sh's tmux floor equal
// to session/tmux.MinVersion.
//
// A shell script cannot read a Go const, so that floor is necessarily a second copy —
// the same shape as the drift sites .claude/skills/tui-drift-sites/SKILL.md enumerates,
// and with the same failure mode: when MinVersion moves, nothing makes the copy move
// with it, and the script goes on admitting a tmux Atrium itself refuses. It is worse
// than a stale doc line, because the script would then start a real server running a
// real agent CLI before anything noticed.
func TestDriveAgentTmuxFloorMatchesAtrium(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	m := minTmuxRe.FindSubmatch(src)
	require.NotNil(t, m, "no MIN_TMUX= assignment in %s — this guard is matching nothing", path)
	require.Equal(t, tmux.MinVersion, string(m[1]),
		"scripts/drive-agent.sh's MIN_TMUX disagrees with session/tmux.MinVersion; update the script")
}

// resumeRowRe pulls one RESUME_TABLE row out of the script:
// `"<agent>|<command>|<verdict>"`, one per line, tab-indented inside the array. It is
// anchored so the prose above the table — which spells the same commands in sentences —
// cannot match, and so a row that loses its quoting fails as a missing row rather than
// matching something adjacent.
var resumeRowRe = regexp.MustCompile(`(?m)^\t"([^"|]+)\|([^"|]+)\|([^"|]+)"$`)

// TestDriveAgentResumeTableMatchesTheRegistry holds scripts/drive-agent.sh's
// RESUME_TABLE to session/agent's adapters, in both directions.
//
// The table is what `drive-agent.sh resume <agent>` launches, so it is a second copy of
// each adapter's Resume rewrite — MIN_TMUX's problem again, with a worse failure: a
// stale command still starts a real CLI and still prints a verdict, so the run reports
// on a launch Atrium would never make and reads exactly like a clean one.
//
// Both directions are asserted because they fail differently. A wrong command drives
// the wrong thing; a MISSING row is an agent whose resume survivability nobody has ever
// driven, which is the gap #712 is about — and the one that appears by itself, the next
// time an adapter grows a Resume.
//
// The verdict column is deliberately unchecked here: no Go test can know whether a
// vendor's CLI survives its own resume flag. Driving it is the only thing that can, and
// that is the point of the verb.
func TestDriveAgentResumeTableMatchesTheRegistry(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	rows := resumeRowRe.FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, rows, "no RESUME_TABLE rows in %s — this guard is matching nothing", path)

	driven := map[string]bool{}
	for _, row := range rows {
		name, command, verdict := row[1], row[2], row[3]
		driven[name] = true
		adapter := agent.Resolve(name)
		require.Equal(t, agent.Key(name), adapter.Key,
			"RESUME_TABLE names %q, which resolves to the %q adapter", name, adapter.Key)
		require.NotNil(t, adapter.Resume,
			"RESUME_TABLE has a row for %q, whose adapter has no Resume to drive", name)
		require.Equal(t, adapter.Resume(name), command,
			"RESUME_TABLE's command for %q is not what Atrium relaunches with; re-drive and update the script", name)
		require.Contains(t, []string{"alive", "dead"}, verdict,
			"RESUME_TABLE's verdict for %q must be alive or dead", name)
	}

	for _, adapter := range agent.Adapters() {
		if adapter.Resume == nil {
			continue
		}
		require.True(t, driven[string(adapter.Key)],
			"the %q adapter can resume a conversation and RESUME_TABLE has no row for it, so nothing has ever driven whether its resume flag survives with nothing to resume (#712)",
			adapter.Key)
	}
}
