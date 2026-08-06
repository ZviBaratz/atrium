package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupportsCheckpoints pins the claude-only gate that keeps the surface
// invisible for every other agent.
func TestSupportsCheckpoints(t *testing.T) {
	for _, tc := range []struct {
		name, program string
		want          bool
	}{
		{"bare claude", "claude", true},
		{"claude with flags", "claude --model opus --effort high", true},
		{"claude by absolute path", "/home/zvi/.local/bin/claude", true},
		{"codex", "codex", false},
		{"gemini", "gemini", false},
		{"aider", "aider --model sonnet", false},
		{"empty program", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewInstance(InstanceOptions{Title: "s", Path: ".", Program: tc.program})
			require.NoError(t, err)
			assert.Equal(t, tc.want, inst.SupportsCheckpoints(), "program=%q", tc.program)
		})
	}
}

// TestLoadCheckpoints_DirectSession runs the full derivation without tmux, the
// same way TestComputeAsked_DirectSession does: a started direct session's
// WorkingDir is its Path and claudeConfigDir routes the transcript root.
func TestLoadCheckpoints_DirectSession(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	const sid = "abcdabcd-1234-4123-8123-abcdabcdabcd"
	lines := `{"type":"user","uuid":"11111111-1111-4111-8111-111111111111","timestamp":"2026-08-05T10:00:00Z","isSidechain":false,"message":{"role":"user","content":"tidy the parser"}}` + "\n" +
		`{"type":"file-history-snapshot","messageId":"11111111-1111-4111-8111-111111111111","snapshot":{"messageId":"11111111-1111-4111-8111-111111111111","trackedFileBackups":{"parse.go":{"backupFileName":"aaaa@v1","version":1}},"timestamp":"2026-08-05T10:00:00Z"}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWDForTest(workDir), sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.WriteFile(dest, []byte(lines), 0o644))

	inst, err := NewInstance(InstanceOptions{Title: "d", Path: workDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("work", root, false)

	got, err := inst.LoadCheckpoints(context.Background())
	require.NoError(t, err)
	assert.Equal(t, sid, got.SessionID, "the session id is the transcript's filename")
	require.Len(t, got.List, 1)
	assert.Equal(t, "tidy the parser", got.List[0].Label)
	assert.Equal(t, 1, got.List[0].Files)

	// Paused sessions deliberately still list, unlike ComputeModel/ComputeUsage/
	// ComputeAsked, which all bail on Paused. What this pins is only that gate —
	// LoadCheckpoints does not refuse a paused status.
	//
	// It does NOT prove the property that makes the reading correct for a real
	// paused session: that WorkingDir() keeps returning the worktree path after
	// pause has removed the tree, so the project dir it resolves is still the one
	// that produced the transcript. This fixture is Direct, so it has no worktree to
	// lose. That property lives in Instance.pause (which keeps i.gitWorktree) and is
	// stated in ContextSourceKey's comment; nothing here would fail if a future
	// change nilled it, which would silently re-point this read at the parent
	// checkout's project dir.
	inst.status = Paused
	paused, err := inst.LoadCheckpoints(context.Background())
	require.NoError(t, err)
	assert.Len(t, paused.List, 1, "a paused status must not gate the read")
}

// TestLoadCheckpoints_Unstarted keeps an unstarted session from doing any I/O:
// there is no transcript yet, and an empty list is not an error condition.
func TestLoadCheckpoints_Unstarted(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "u", Path: t.TempDir(), Program: "claude", Direct: true})
	require.NoError(t, err)

	got, err := inst.LoadCheckpoints(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got.List)
	assert.Empty(t, got.SessionID)
}
