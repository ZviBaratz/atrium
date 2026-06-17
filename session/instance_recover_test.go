package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// recordingPtyFactory is a tmux.PtyFactory that records every command it is
// asked to start, so tests can assert *which* program a recovery path launched
// (e.g. `claude --continue` vs a blank `claude`). When startErr is set every
// Start fails, letting tests drive the failure branches without a real tmux.
type recordingPtyFactory struct {
	cmds     []*exec.Cmd
	startErr error
}

func (f *recordingPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	f.cmds = append(f.cmds, cmd)
	if f.startErr != nil {
		return nil, f.startErr
	}
	// A real *os.File the caller can Close(); contents are irrelevant.
	return os.CreateTemp("", "pty-stub")
}

func (f *recordingPtyFactory) Close() {}

func (f *recordingPtyFactory) commands() []string {
	out := make([]string, 0, len(f.cmds))
	for _, c := range f.cmds {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// newTestWorktree stands up a real, valid git worktree in a temp HOME so
// IsValidWorktree() returns true and Cleanup() has something to remove.
func newTestWorktree(t *testing.T) *git.Worktree {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
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
	return wt
}

// newTestWorktreeFromBase is newTestWorktree's counterpart for a session created
// from a chosen base branch (baseRef != ""), exercising the Setup path that
// branches off baseRef instead of HEAD. baseRef is the repo's initial branch so
// it resolves locally.
func newTestWorktreeFromBase(t *testing.T) *git.Worktree {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	baseBranch := gitOutput(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	wt, _, err := git.NewWorktreeFromBase(context.Background(), repoPath, "sess", baseBranch)
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	return wt
}

// deadExec fails every tmux command, so DoesSessionExist() reports false and the
// duplicate-name guard in start() does not block the PTY launch.
func deadExec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("dead") },
	}
}

// TestRecoverInPlace_OrphanedWorktreeDegradesToPaused asserts that when the
// worktree is gone there is nothing to restart, so recovery leaves the instance
// Paused (branch preserved, recoverable via Resume) without touching tmux.
func TestRecoverInPlace_OrphanedWorktreeDegradesToPaused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A storage-only worktree pointing at a path that does not exist.
	wt := git.NewWorktreeFromStorage(
		context.Background(),
		filepath.Join(t.TempDir(), "repo"),
		filepath.Join(t.TempDir(), "gone"),
		"sess", "session/sess", "", "main", false, "session/")
	pty := &recordingPtyFactory{}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{Title: "sess", status: Running, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.True(t, inst.started, "a recovered instance must be marked started")
	require.True(t, inst.Paused(), "an orphaned worktree must degrade to Paused")
	require.Empty(t, pty.cmds, "no session should be launched when the worktree is gone")
}

// TestRecoverInPlace_ResumesConversationWhenWorktreeValid asserts that a valid
// worktree is brought back to Running by resuming the agent's prior conversation
// (StartContinue → `--continue`), not by starting a blank agent.
func TestRecoverInPlace_ResumesConversationWhenWorktreeValid(t *testing.T) {
	wt := newTestWorktree(t)
	pty := &recordingPtyFactory{}
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		// First has-session (the duplicate-name guard) must report "gone" so the
		// launch proceeds; the poll that follows must then see it as alive.
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls == 1 {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, liveExec)
	inst := &Instance{Title: "sess", status: Running, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.True(t, inst.started)
	require.Equal(t, Running, inst.GetStatus(), "a valid worktree must recover to Running")
	require.NotEmpty(t, pty.cmds, "the session must be (re)launched")
	require.Contains(t, pty.commands()[0], "--continue",
		"recovery must resume the prior conversation, not start blank")
}

// TestRecoverInPlace_FailedRestartDegradesToPaused asserts that if the restart
// itself fails, recovery still degrades to Paused rather than aborting — one bad
// session must never block loading the rest — while still having attempted to
// resume the conversation.
func TestRecoverInPlace_FailedRestartDegradesToPaused(t *testing.T) {
	wt := newTestWorktree(t)
	pty := &recordingPtyFactory{startErr: fmt.Errorf("pty boom")}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{Title: "sess", status: Running, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.True(t, inst.started)
	require.True(t, inst.Paused(), "a failed restart must degrade to Paused, not abort")
	require.Contains(t, pty.commands()[0], "--continue",
		"recovery must attempt to resume the prior conversation")
}

// TestRecreateSession_ResumesConversationAndCleansUpOnFailure asserts the Resume
// fallback helper resumes the conversation (StartContinue) and, when the launch
// fails, tears down the worktree and returns an error rather than leaking it.
func TestRecreateSession_ResumesConversationAndCleansUpOnFailure(t *testing.T) {
	wt := newTestWorktree(t)
	pty := &recordingPtyFactory{startErr: fmt.Errorf("pty boom")}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{Title: "sess", started: true, gitWorktree: wt, tmuxSession: ts}

	err := inst.recreateSession()

	require.Error(t, err, "a failed launch must surface an error to Resume's caller")
	require.Contains(t, pty.commands()[0], "--continue",
		"the fallback must resume the prior conversation, not start blank")
	valid, vErr := wt.IsValidWorktree()
	require.NoError(t, vErr)
	require.False(t, valid, "the worktree must be cleaned up after a failed launch")
}

// Resume must surface a typed *git.BranchCheckedOutError when the session branch
// is already checked out elsewhere — the app layer keys its detach-and-recover
// offer off errors.As against that type, so the type is the cross-package
// contract (a reworded message must not silently break recovery).
func TestResume_BranchCheckedOutReturnsTypedError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")
	// The base repo itself holds the session branch — the common Checkout case.
	runGit(t, repoPath, "switch", "-c", "session/sess")

	wt := git.NewWorktreeFromStorage(
		context.Background(),
		repoPath, filepath.Join(t.TempDir(), "wt"),
		"sess", "session/sess", "", "main", true, "session/")
	inst := &Instance{Title: "sess", status: Paused, started: true, gitWorktree: wt}

	err := inst.Resume()
	require.Error(t, err)
	var busy *git.BranchCheckedOutError
	require.ErrorAs(t, err, &busy, "Resume must return a *git.BranchCheckedOutError")
	require.NotEmpty(t, busy.Path, "the error should name the holding worktree")
}
