package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// gitOutput runs git in dir and returns its trimmed combined output, failing the
// test on error. (runGit, the sibling helper, discards output.)
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// autoMsg builds a message in pause's auto-commit format, so tests exercise the
// real detection path rather than a hand-typed string that could drift.
func autoMsg(when string) string {
	return autoPauseCommitPrefix + "'sess' on " + when + " " + autoPauseCommitSuffix
}

// TestPauseResume_RoundTripsWithoutHistoryArtifact is the core acceptance test
// for #141: pausing a dirty session then resuming it must leave branch HEAD and
// the working tree exactly as they were, with no leftover `(paused)` commit.
func TestPauseResume_RoundTripsWithoutHistoryArtifact(t *testing.T) {
	wt := newTestWorktree(t) // HEAD-based (baseRef == ""), so resume reuses the branch
	wtPath := wt.GetWorktreePath()
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()

	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	// An executor that reports the tmux session as present, so Resume takes the
	// Restore() path (a plain pty re-attach) instead of polling a real server.
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	pty := newRecordingPtyFactory(t, nil)
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, aliveExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

	// Pause commits the WIP so it survives the worktree removal.
	require.NoError(t, inst.Pause())
	require.True(t, inst.Paused())
	require.NotEqual(t, baseSHA, gitOutput(t, repoPath, "rev-parse", branch), "pause must commit the WIP")
	require.True(t, isAutoPauseCommit(gitOutput(t, repoPath, "log", "-1", "--format=%s", branch)),
		"the pause commit must be a recognizable auto-commit")

	// Resume must unwind that auto-commit back into pending changes.
	require.NoError(t, inst.Resume())
	require.Equal(t, Running, inst.GetStatus())

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"), "resume must unwind the auto-commit")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"), "the WIP must return as a pending change")
	content, err := os.ReadFile(filepath.Join(wtPath, "work.txt"))
	require.NoError(t, err)
	require.Equal(t, "in progress\n", string(content), "the edited content must be restored verbatim")
}

// TestPauseResume_BaseRefSession_RoundTripsWithoutDataLoss is the same acceptance
// test for a session created from a chosen base branch (baseRef != ""). Setup must
// reattach to the existing session branch on resume, not force-recreate it from
// baseRef — otherwise the paused WIP commit is force-deleted and lost. This fails
// before the existence-first Setup fix and passes after it.
func TestPauseResume_BaseRefSession_RoundTripsWithoutDataLoss(t *testing.T) {
	wt := newTestWorktreeFromBase(t) // baseRef != "", the path that previously lost WIP
	wtPath := wt.GetWorktreePath()
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()

	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	pty := newRecordingPtyFactory(t, nil)
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, aliveExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

	require.NoError(t, inst.Pause())
	require.True(t, inst.Paused())
	require.True(t, isAutoPauseCommit(gitOutput(t, repoPath, "log", "-1", "--format=%s", branch)),
		"the pause commit must be a recognizable auto-commit")

	require.NoError(t, inst.Resume())
	require.Equal(t, Running, inst.GetStatus())

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"),
		"resume must reuse the branch and unwind the auto-commit, not recreate from baseRef")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"), "the WIP must return as a pending change")
	content, err := os.ReadFile(filepath.Join(wtPath, "work.txt"))
	require.NoError(t, err)
	require.Equal(t, "in progress\n", string(content), "the edited content must be restored verbatim")
}

// TestUnwindAutoPauseCommits_CollapsesStackedAutoCommits covers the legacy case
// the issue calls out: several reboots stacked multiple auto-commits. A single
// unwind must collapse the whole consecutive run back to the real ancestor.
func TestUnwindAutoPauseCommits_CollapsesStackedAutoCommits(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()
	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Mon")))
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "b.txt"), []byte("b\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Tue")))

	inst := &Instance{Title: "sess", gitWorktree: wt}
	require.NoError(t, inst.unwindAutoPauseCommits(wt))

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"), "all consecutive auto-commits must collapse")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"))
	require.FileExists(t, filepath.Join(wtPath, "a.txt"))
	require.FileExists(t, filepath.Join(wtPath, "b.txt"))
}

// TestUnwindAutoPauseCommits_PreservesRealCommit guards the safety property: a
// genuine, user-authored commit on top of the branch is never soft-reset.
func TestUnwindAutoPauseCommits_PreservesRealCommit(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "auto.txt"), []byte("x\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Mon")))
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "real.txt"), []byte("y\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: a real change"))
	headBefore := gitOutput(t, wtPath, "rev-parse", "HEAD")

	inst := &Instance{Title: "sess", gitWorktree: wt}
	require.NoError(t, inst.unwindAutoPauseCommits(wt))

	require.Equal(t, headBefore, gitOutput(t, wtPath, "rev-parse", "HEAD"),
		"a real commit at HEAD must never be unwound")
}
