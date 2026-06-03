package session

import (
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// renameTestRepo sets up a sandboxed HOME + a one-commit repo and returns the repo path.
func renameTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")
	return repoPath
}

// liveTmux returns a fake tmux session whose every command (incl. has-session) succeeds.
func liveTmux(t *testing.T, name string) *tmux.Session {
	t.Helper()
	exec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	return tmux.NewSessionWithDeps(name, "claude", tmux.MakePtyFactory(), exec)
}

// A deep rename fixes the typo everywhere at once: the title, the rendered branch field, the
// git branch, and the worktree directory all move to the corrected name.
func TestInstanceRename_RenamesBranchWorktreeAndTitle(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(repoPath, "formalize-packaing")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	inst := &Instance{
		Title:       "formalize-packaing",
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, "formalize-packaing"),
		Branch:      wt.GetBranchName(),
	}
	oldBranch := wt.GetBranchName()
	oldPath := wt.GetWorktreePath()

	require.NoError(t, inst.Rename("formalize-packaging"))

	require.Equal(t, "formalize-packaging", inst.Title)
	require.NotEqual(t, oldBranch, inst.Branch)
	require.Equal(t, wt.GetBranchName(), inst.Branch, "Instance.Branch must track the renamed git branch")

	// git side: new branch exists, old branch gone, worktree dir moved.
	require.Empty(t, strings.TrimSpace(mustGit(t, repoPath, "branch", "--list", oldBranch)), "old branch should be gone")
	require.NotEmpty(t, strings.TrimSpace(mustGit(t, repoPath, "branch", "--list", inst.Branch)), "new branch should exist")
	require.NotEqual(t, oldPath, wt.GetWorktreePath())
	_, statErr := os.Stat(oldPath)
	require.True(t, os.IsNotExist(statErr), "old worktree dir should be gone")
}

// If the git rename fails (here a branch-name collision), the already-renamed tmux session is
// rolled back and the instance identity is left completely untouched.
func TestInstanceRename_RollsBackTmuxOnGitFailure(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(repoPath, "alpha")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	// Occupy the target branch name so the git rename collides and fails.
	collide, _, err := git.NewWorktree(repoPath, "alpha-fixed")
	require.NoError(t, err)
	require.NoError(t, collide.Setup())

	var ran []string
	tmuxExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps("alpha", "claude", tmux.MakePtyFactory(), tmuxExec)
	inst := &Instance{
		Title:       "alpha",
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: ts,
		Branch:      wt.GetBranchName(),
	}
	oldBranch := wt.GetBranchName()
	oldPath := wt.GetWorktreePath()

	require.Error(t, inst.Rename("alpha-fixed"))

	// Identity untouched.
	require.Equal(t, "alpha", inst.Title)
	require.Equal(t, oldBranch, inst.Branch)
	require.Equal(t, oldBranch, wt.GetBranchName())
	require.Equal(t, oldPath, wt.GetWorktreePath())
	_, statErr := os.Stat(oldPath)
	require.NoError(t, statErr, "worktree dir must be intact after rollback")

	// The tmux session was renamed forward then rolled back to its original name.
	// The prefix follows the active brand (see tmux.Prefix), so resolve it
	// dynamically rather than hardcoding the legacy claudesquad_ value.
	prefix := tmux.Prefix()
	requireSubstr(t, ran, "rename-session", prefix+"alpha", prefix+"alpha-fixed")
	requireSubstr(t, ran, "rename-session", prefix+"alpha-fixed", prefix+"alpha")
}

func TestInstanceRename_RejectsUnstarted(t *testing.T) {
	inst := &Instance{Title: "x"}
	require.Error(t, inst.Rename("y"))
}

func TestInstanceRename_RejectsEmpty(t *testing.T) {
	inst := &Instance{Title: "x", started: true}
	require.Error(t, inst.Rename("   "))
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func requireSubstr(t *testing.T, ran []string, substrs ...string) {
	t.Helper()
	for _, s := range ran {
		ok := true
		for _, sub := range substrs {
			if !strings.Contains(s, sub) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("no command matched %v; ran: %v", substrs, ran)
}
