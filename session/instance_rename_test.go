package session

import (
	"context"
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

// renameAndAdopt runs both halves of a deep rename: the I/O (Rename, which the app
// runs on a goroutine) and the identity adoption the app's renameDoneMsg handler
// applies on the update thread. Tests assert on what the user ends up seeing, which
// is the pair — Rename alone deliberately leaves Title and Branch untouched.
//
// On failure it adopts nothing, so a test asserting "identity untouched" after a
// failed rename is asserting against the same sequence the app performs.
func renameAndAdopt(inst *Instance, newTitle string) error {
	renamed, err := inst.Rename(newTitle)
	if err != nil {
		return err
	}
	inst.AdoptRename(renamed)
	return nil
}

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
	return tmux.NewSessionWithDeps(context.Background(), name, "claude", tmux.MakePtyFactory(), exec)
}

// A deep rename fixes the typo everywhere at once: the title, the rendered branch field, the
// git branch, and the worktree directory all move to the corrected name.
func TestInstanceRename_RenamesBranchWorktreeAndTitle(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(context.Background(), repoPath, "formalize-packaing")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	inst := &Instance{
		ident:       identity{title: "formalize-packaing", branch: wt.GetBranchName()},
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, "formalize-packaing"),
	}
	oldBranch := wt.GetBranchName()
	oldPath := wt.GetWorktreePath()

	require.NoError(t, renameAndAdopt(inst, "formalize-packaging"))

	require.Equal(t, "formalize-packaging", inst.Title())
	require.NotEqual(t, oldBranch, inst.Branch())
	require.Equal(t, wt.GetBranchName(), inst.Branch(), "Instance.Branch must track the renamed git branch")

	// git side: new branch exists, old branch gone, worktree dir moved.
	require.Empty(t, strings.TrimSpace(mustGit(t, repoPath, "branch", "--list", oldBranch)), "old branch should be gone")
	require.NotEmpty(t, strings.TrimSpace(mustGit(t, repoPath, "branch", "--list", inst.Branch())), "new branch should exist")
	require.NotEqual(t, oldPath, wt.GetWorktreePath())
	_, statErr := os.Stat(oldPath)
	require.True(t, os.IsNotExist(statErr), "old worktree dir should be gone")
}

// If the git rename fails (here a branch-name collision), the already-renamed tmux session is
// rolled back and the instance identity is left completely untouched.
func TestInstanceRename_RollsBackTmuxOnGitFailure(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(context.Background(), repoPath, "alpha")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	// Occupy the target branch name so the git rename collides and fails.
	collide, _, err := git.NewWorktree(context.Background(), repoPath, "alpha-fixed")
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
	ts := tmux.NewSessionWithDeps(context.Background(), "alpha", "claude", tmux.MakePtyFactory(), tmuxExec)
	inst := &Instance{
		ident:       identity{title: "alpha", branch: wt.GetBranchName()},
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: ts,
	}
	oldBranch := wt.GetBranchName()
	oldPath := wt.GetWorktreePath()

	require.Error(t, renameAndAdopt(inst, "alpha-fixed"))

	// Identity untouched.
	require.Equal(t, "alpha", inst.Title())
	require.Equal(t, oldBranch, inst.Branch())
	require.Equal(t, oldBranch, wt.GetBranchName())
	require.Equal(t, oldPath, wt.GetWorktreePath())
	_, statErr := os.Stat(oldPath)
	require.NoError(t, statErr, "worktree dir must be intact after rollback")

	// The tmux session was renamed forward (to the freshly-minted qualified
	// name) then rolled back to its exact original — here the legacy derived
	// name, since this session predates persisted tmux names.
	oldName := tmux.Prefix() + "alpha"
	newName := tmux.QualifiedSessionName(inst.GroupKey(), "alpha-fixed")
	requireSubstr(t, ran, "rename-session", oldName, newName)
	requireSubstr(t, ran, "rename-session", newName, oldName)
}

func TestInstanceRename_RejectsUnstarted(t *testing.T) {
	inst := &Instance{ident: identity{title: "x"}}
	require.Error(t, renameAndAdopt(inst, "y"))
}

func TestInstanceRename_RejectsEmpty(t *testing.T) {
	inst := &Instance{ident: identity{title: "x"}, started: true}
	require.Error(t, renameAndAdopt(inst, "   "))
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
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

// TestInstanceRename_IOHalfLeavesTheIdentityAlone pins the split the deep rename
// depends on. Rename runs on a background goroutine (renameIOCmd) and must return the
// new identity rather than adopt it, so the name a row shows changes at one instant on
// the update thread — after both the tmux session and the git branch have moved, and
// never for a rename that failed halfway.
//
// What is observable is that the I/O alone changes nothing the renderer reads.
//
// This comment used to argue the split in terms of a data race, and two successive
// drafts of that argument were wrong in the same way — each promoted one closed window
// into a claim about all of them, when the fields were plain and every reader needed its
// own argument. #795 removed the premise: the identity family is unexported and guarded
// (session/identity.go), so no reader needs an argument and this test is about ordering
// rather than safety. The race half now has a guard of its own,
// TestIdentityReadsDoNotRaceItsWrites, which — unlike this test — fails only under -race.
func TestInstanceRename_IOHalfLeavesTheIdentityAlone(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(context.Background(), repoPath, "before")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	inst := &Instance{
		ident:       identity{title: "before", branch: wt.GetBranchName()},
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, "before"),
	}
	oldBranch := wt.GetBranchName()

	renamed, err := inst.Rename("after")
	require.NoError(t, err)

	// The I/O really happened...
	require.Equal(t, "after", renamed.Title)
	require.NotEqual(t, oldBranch, renamed.Branch, "the git branch moved")
	require.NotEmpty(t, renamed.TmuxName)

	// ...but nothing the main thread reads has moved yet.
	require.Equal(t, "before", inst.Title(), "the I/O half must not write Title")
	require.Equal(t, oldBranch, inst.Branch(), "nor Branch")

	inst.AdoptRename(renamed)
	require.Equal(t, "after", inst.Title(), "adoption is what the user sees")
	require.Equal(t, renamed.Branch, inst.Branch())
	require.Equal(t, renamed.TmuxName, inst.TmuxSessionName())
}
