package session

import (
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreviewSkipsCaptureWhenSessionDead asserts that previewing a started instance
// whose tmux session has died returns empty (not an error) without running
// capture-pane, so the preview refresh can't escalate the failure to the error box.
func TestPreviewSkipsCaptureWhenSessionDead(t *testing.T) {
	captured := false
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { captured = true; return nil, fmt.Errorf("capture fail") },
	}
	ts := tmux.NewTmuxSessionWithDeps("dead", "claude", tmux.MakePtyFactory(), mockExec)
	inst := &Instance{Title: "dead", Status: Running, started: true, tmuxSession: ts}

	content, err := inst.Preview()
	require.NoError(t, err)
	require.Equal(t, "", content)
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestRecoverLostSessionTransitionsToPaused asserts that a started instance whose
// tmux session has died is moved to Paused (so it stops being polled and can be
// brought back with Resume), reusing the Pause path to preserve the branch.
func TestRecoverLostSessionTransitionsToPaused(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	wt, _, err := git.NewGitWorktree(repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	deadExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("dead") },
	}
	ts := tmux.NewTmuxSessionWithDeps("sess", "claude", tmux.MakePtyFactory(), deadExec)
	inst := &Instance{Title: "sess", Status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

	require.False(t, inst.TmuxAlive())
	require.NoError(t, inst.RecoverLostSession())
	require.True(t, inst.Paused(), "a lost session must transition to Paused")
}

func TestSetPath_ResolvesToAbsolute(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	require.NoError(t, inst.SetPath("/tmp/some/repo"))
	assert.Equal(t, "/tmp/some/repo", inst.Path)

	// A relative path is resolved to absolute, mirroring NewInstance.
	require.NoError(t, inst.SetPath("relative/dir"))
	want, _ := filepath.Abs("relative/dir")
	assert.Equal(t, want, inst.Path)
	assert.True(t, filepath.IsAbs(inst.Path))
}

func TestToInstanceData_PersistsGitContext(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// NewGitWorktreeFromStorage is a pure constructor (no git I/O), so we can use it
	// to stand up a worktree carrying a base ref without starting the instance.
	inst.gitWorktree = git.NewGitWorktreeFromStorage(
		"/repo", "/repo/wt", "t", "session/t", "abc123", "main", false)
	inst.diffStats = &git.DiffStats{
		Added: 12, Removed: 3, FilesChanged: 4, Commits: 2, Behind: 5, Dirty: true,
	}

	data := inst.ToInstanceData()

	assert.Equal(t, "main", data.Worktree.BaseRef, "base ref must survive persistence")
	assert.Equal(t, 4, data.DiffStats.FilesChanged)
	assert.Equal(t, 2, data.DiffStats.Commits)
	assert.Equal(t, 5, data.DiffStats.Behind)
	assert.True(t, data.DiffStats.Dirty)
}

func TestSetPath_RejectedAfterStart(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// Simulate a started instance without spinning up tmux/git.
	inst.started = true
	err = inst.SetPath("/tmp/other")
	require.Error(t, err)
}

func TestDisplayName_FallsBackToTitle(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// With no label set, DisplayName mirrors Title.
	assert.Equal(t, "my-task", inst.DisplayName())

	inst.SetDisplayName("Nicer Label")
	assert.Equal(t, "Nicer Label", inst.DisplayName())
	// Title (the stable identifier) is untouched by the label.
	assert.Equal(t, "my-task", inst.Title)
}

func TestSetDisplayName_WorksAfterStart(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// Unlike SetTitle, the cosmetic label can change after the instance has started.
	inst.started = true
	require.Error(t, inst.SetTitle("renamed"), "SetTitle must reject a started instance")

	inst.SetDisplayName("After Start")
	assert.Equal(t, "After Start", inst.DisplayName())
}

func TestSetDisplayName_TrimsAndClears(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	inst.SetDisplayName("  spaced label  ")
	assert.Equal(t, "spaced label", inst.DisplayName())

	// Empty/whitespace input clears the label, reverting to Title.
	inst.SetDisplayName("   ")
	assert.Equal(t, "my-task", inst.DisplayName())
}

func TestDisplayName_SerializedInInstanceData(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetDisplayName("Nicer Label")

	data := inst.ToInstanceData()
	assert.Equal(t, "Nicer Label", data.DisplayName)
	assert.Equal(t, "my-task", data.Title)
}

func TestInstanceData_MissingDisplayNameIsEmpty(t *testing.T) {
	// State files written before this feature have no display_name key; they must load with
	// an empty label so the name falls back to Title.
	var data InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"my-task"}`), &data))
	assert.Equal(t, "", data.DisplayName)
}
