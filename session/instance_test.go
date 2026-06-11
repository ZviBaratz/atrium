package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusAccessorsAreRaceFree exercises the lifecycle-field accessors from two
// goroutines at once: a writer mutating the mu-guarded fields (status, tmuxSession)
// while the metadata-poll / UI readers query them. Before the RWMutex was added this
// raced (writer = Start's SetStatus(Running) + tmuxSession assignment; readers = the
// poll loop and the UI methods below), which under `go test -race` is a hard failure
// and at runtime could leave a session pinned at Loading. Every method exercised by the
// reader goroutine must read the guarded fields through the locked accessors, not the
// bare struct fields, so this also guards against a regression that reintroduces a
// direct read. It must pass under -race.
func TestStatusAccessorsAreRaceFree(t *testing.T) {
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	newSession := func() *tmux.Session {
		return tmux.NewSessionWithDeps(context.Background(), "race", "claude", tmux.MakePtyFactory(), mockExec)
	}
	inst := &Instance{Title: "race", status: Loading, started: true, tmuxSession: newSession()}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			inst.SetStatus(Running)
			inst.SetTmuxSession(newSession())
			inst.SetStatus(Ready)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = inst.GetStatus()
			_ = inst.Started()
			_ = inst.Paused()
			_ = inst.TmuxAlive()
			_ = inst.IsReadyForPrompt()
			_ = inst.SetPreviewSize(80, 24)
			_, _ = inst.PreviewFullHistory()
			_ = inst.SendKeys("x")
		}
	}()
	wg.Wait()
}

// TestPreviewSkipsCaptureWhenSessionDead asserts that previewing a started instance
// whose tmux session has died returns empty (not an error) without running
// capture-pane, so the preview refresh can't escalate the failure to the error box.
func TestPreviewSkipsCaptureWhenSessionDead(t *testing.T) {
	captured := false
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { captured = true; return nil, fmt.Errorf("capture fail") },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "dead", "claude", tmux.MakePtyFactory(), mockExec)
	inst := &Instance{Title: "dead", status: Running, started: true, tmuxSession: ts}

	content, err := inst.Preview()
	require.NoError(t, err)
	require.Equal(t, "", content)
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
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

	wt, _, err := git.NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	deadExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("dead") },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", tmux.MakePtyFactory(), deadExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

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

	// NewWorktreeFromStorage is a pure constructor (no git I/O), so we can use it
	// to stand up a worktree carrying a base ref without starting the instance.
	inst.gitWorktree = git.NewWorktreeFromStorage(
		context.Background(),
		"/repo", "/repo/wt", "t", "session/t", "abc123", "main", false, "session/")
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

// approveRecorder builds a MockCmdExec that records every send-keys argv and
// resolves the agent pane as %7, so tests can assert exactly what reached tmux.
func approveRecorder(sendKeysArgs *[][]string) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			for i, arg := range cmd.Args {
				if arg == "send-keys" {
					*sendKeysArgs = append(*sendKeysArgs, cmd.Args[i+1:])
					break
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte("%7\n"), nil },
	}
}

// ApprovePrompt is the user-initiated twin of the autoyes TapEnter: it must work
// with AutoYes off — that gate is what it deliberately bypasses.
func TestApprovePrompt_TapsEnterWithoutAutoYes(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve",
		status:      NeedsInput,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}
	require.False(t, inst.AutoYes, "the test must exercise the AutoYes-off path")

	require.NoError(t, inst.ApprovePrompt())

	require.Len(t, sent, 1, "exactly one keystroke batch must reach tmux")
	assert.Contains(t, sent[0], "Enter")
}

func TestApprovePrompt_NotStartedErrors(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve-unstarted",
		status:      Ready,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve-unstarted", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}

	require.Error(t, inst.ApprovePrompt())
	assert.Empty(t, sent, "an unstarted instance must never reach tmux")
}

// A started instance with no tmux session must error, not panic: ApprovePrompt
// follows the same nil guard as the other pane-touching methods (Poll,
// IsReadyForPrompt, CheckAndHandleTrustPrompt).
func TestApprovePrompt_NilTmuxSessionErrors(t *testing.T) {
	inst := &Instance{
		Title:   "approve-no-pane",
		status:  NeedsInput,
		started: true,
	}

	require.Error(t, inst.ApprovePrompt())
}

func TestApprovePrompt_PausedErrors(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve-paused",
		status:      Paused,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve-paused", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}

	require.Error(t, inst.ApprovePrompt())
	assert.Empty(t, sent, "a paused instance has no live pane to tap")
}
