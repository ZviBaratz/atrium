package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

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
