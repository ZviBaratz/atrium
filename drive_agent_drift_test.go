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

// resumeTableRe pulls the RESUME_TABLE array out of the script, and rows are then read
// only from inside it. Matching rows against the whole file would leave the guard
// anchored to a SHAPE rather than to the array: deleting or renaming RESUME_TABLE while
// leaving four tab-indented `"a|b|c"` lines anywhere in the script would still yield
// rows, still satisfy the both-directions checks, and still pass — which is the mutation
// this guard most needs to fail on, since the table is what the verb reads.
var resumeTableRe = regexp.MustCompile(`(?s)\nRESUME_TABLE=\(\n(.*?)\n\)\n`)

// resumeRowRe pulls one RESUME_TABLE row out of that block:
// `"<agent>|<command>|<verdict>"`, one per line, tab-indented inside the array. It is
// anchored so a row that loses its quoting fails as a missing row rather than matching
// something adjacent.
var resumeRowRe = regexp.MustCompile(`(?m)^\t"([^"|]+)\|([^"|]+)\|([^"|]+)"$`)

// resumeLauncherRe is what holds cmd_resume to the COLUMN it launches. The verb re-parses
// each row itself, so the drift the table guard cannot see is in that parse: swap the read
// order, or write `program="$want"`, and the script drives the literal `alive` as a command
// while every assertion below stays green — under a docstring promising that the table is
// what `resume` launches.
//
// Written as one expression over the three lines rather than three lookups, so a reordered
// read cannot be satisfied by fragments matching in the wrong places.
var resumeLauncherRe = regexp.MustCompile(`(?s)IFS='\|' read -r name cmd want <<<"\$row".*?program="\$cmd".*?expect="\$want"`)

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
//
// Two mutations this would otherwise miss are pinned first, because both keep every
// assertion below green: rows read from anywhere in the file rather than from the array
// (resumeTableRe), and a launcher that reads the columns in a different order or launches
// a different one (resumeLauncherRe).
func TestDriveAgentResumeTableMatchesTheRegistry(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	require.Regexp(t, resumeLauncherRe, string(src),
		"cmd_resume no longer reads RESUME_TABLE's columns as <agent>|<command>|<verdict> and launches the command; "+
			"the table below would then be held to a script that drives something else")

	table := resumeTableRe.FindStringSubmatch(string(src))
	require.NotNil(t, table, "no RESUME_TABLE=( … ) array in %s — this guard is matching nothing", path)

	rows := resumeRowRe.FindAllStringSubmatch(table[1], -1)
	require.NotEmpty(t, rows, "RESUME_TABLE has no rows this guard recognizes in %s", path)

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
