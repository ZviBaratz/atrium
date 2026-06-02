package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// directTmux returns a tmux session backed by a mock executor that reports success for
// every command (so DoesSessionExist is true and Close/Detach/Rename succeed). It never
// drives a real tmux server, keeping these tests hermetic.
func directTmux(name string) *tmux.TmuxSession {
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	return tmux.NewTmuxSessionWithDeps(name, "claude", tmux.MakePtyFactory(), mockExec)
}

// TestNewInstance_DirectFlag verifies a direct session is born with no worktree, no
// branch, and IsDirect() true, and that WorkingDir() resolves to Path (the cwd the tmux
// session runs in — the actual -c wiring is covered by tmux.TestStartTmuxSession).
func TestNewInstance_DirectFlag(t *testing.T) {
	dir := t.TempDir()
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: dir, Program: "echo", Direct: true})
	require.NoError(t, err)

	assert.True(t, inst.IsDirect(), "Direct option must set IsDirect")
	assert.Nil(t, inst.worktree(), "a direct session has no worktree")
	assert.Empty(t, inst.Branch, "a direct session has no branch")
	assert.Equal(t, dir, inst.WorkingDir(), "tmux cwd must be Path for a direct session")
}

// TestDirectSession_RepoNameIsBasename verifies grouping identity falls back to the
// directory name (e.g. ~/quantivly/qspace → "qspace") with no git repo.
func TestDirectSession_RepoNameIsBasename(t *testing.T) {
	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: "/home/user/quantivly/qspace"}
	name, err := inst.RepoName()
	require.NoError(t, err)
	assert.Equal(t, "qspace", name)
}

// TestDirectSession_GetGitWorktreeErrors verifies git-dependent app actions get a clean
// error (not a nil worktree to dereference) for a direct session.
func TestDirectSession_GetGitWorktreeErrors(t *testing.T) {
	inst := &Instance{Title: "d", status: Running, started: true, direct: true}
	wt, err := inst.GetGitWorktree()
	assert.Nil(t, wt)
	assert.ErrorIs(t, err, ErrNoWorktree)
}

// TestDirectSession_UpdateDiffStatsNoPanic verifies the poll loop's diff update is a
// safe no-op for a running direct session (it would otherwise nil-deref the worktree).
func TestDirectSession_UpdateDiffStatsNoPanic(t *testing.T) {
	inst := &Instance{Title: "d", status: Running, started: true, direct: true}
	require.NoError(t, inst.UpdateDiffStats())
	assert.Nil(t, inst.GetDiffStats())
}

// TestDirectSession_KillKeepsDirectory is the core safety guarantee: killing a direct
// session tears down tmux but must NEVER delete the user's real directory.
func TestDirectSession_KillKeepsDirectory(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	require.NoError(t, os.WriteFile(marker, []byte("x"), 0644))

	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: dir, tmuxSession: directTmux("d")}
	require.NoError(t, inst.Kill())

	_, err := os.Stat(marker)
	require.NoError(t, err, "Kill must not delete the direct session's real directory")
}

// TestDirectSession_PauseRefused verifies user-initiated Pause is disabled for a direct
// session: it returns an error, leaves the session Running, and never touches the
// directory. (Pausing would only detach a still-running agent while claiming it parked.)
func TestDirectSession_PauseRefused(t *testing.T) {
	dir := t.TempDir()
	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: dir, tmuxSession: directTmux("d")}

	err := inst.Pause()
	require.Error(t, err, "Pause must be refused for a direct session")
	assert.False(t, inst.Paused(), "a refused Pause must leave the session Running")
	assert.Equal(t, Running, inst.GetStatus())

	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("Pause must not remove the direct session's directory: %v", statErr)
	}
}

// TestDirectSession_RecoverLostSessionParks verifies that when a direct session's pane
// dies, the system-initiated RecoverLostSession still parks it (Paused) so the poll loop
// stops — without removing the user's real directory.
func TestDirectSession_RecoverLostSessionParks(t *testing.T) {
	dir := t.TempDir()
	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: dir, tmuxSession: directTmux("d")}

	require.NoError(t, inst.RecoverLostSession())
	assert.True(t, inst.Paused(), "a lost direct session must be parked as Paused")

	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("RecoverLostSession must not remove the direct session's directory: %v", statErr)
	}
}

// TestDirectSession_RenameRenamesTmuxOnly verifies a deep rename of a direct session
// renames the tmux session (no worktree to move) and does not panic.
func TestDirectSession_RenameRenamesTmuxOnly(t *testing.T) {
	inst := &Instance{Title: "old", status: Running, started: true, direct: true, Path: t.TempDir(), tmuxSession: directTmux("old")}

	require.NoError(t, inst.Rename("new"))
	assert.Equal(t, "new", inst.Title)
	assert.Empty(t, inst.Branch, "a direct session never gains a branch on rename")
}

// TestDirectSession_RoundTrip verifies the Direct flag survives serialization and that
// no worktree is written or rehydrated.
func TestDirectSession_RoundTrip(t *testing.T) {
	inst := &Instance{Title: "d", status: Paused, started: true, direct: true, Path: t.TempDir(), Program: "claude"}

	data := inst.ToInstanceData()
	assert.True(t, data.Direct, "Direct must be persisted")
	assert.Empty(t, data.Worktree.RepoPath, "no worktree data for a direct session")

	blob, err := json.Marshal(data)
	require.NoError(t, err)
	var decoded InstanceData
	require.NoError(t, json.Unmarshal(blob, &decoded))

	restored, err := FromInstanceData(decoded)
	require.NoError(t, err)
	assert.True(t, restored.IsDirect(), "Direct must survive restore")
	assert.Nil(t, restored.worktree(), "restore must not fabricate a worktree for a direct session")
}
