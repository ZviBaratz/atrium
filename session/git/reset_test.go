package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCommitSubject_ReadsHeadAndErrorsPastRoot covers the two ways the resume
// unwind loop leans on CommitSubject: reading the current subject, and detecting
// the end of history (a ref past the root commit must error, not return "").
func TestCommitSubject_ReadsHeadAndErrorsPastRoot(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, _, err := NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("v\n"), 0644))
	require.NoError(t, wt.CommitChanges("second commit"))

	subj, err := wt.CommitSubject("HEAD")
	require.NoError(t, err)
	require.Equal(t, "second commit", subj)

	_, err = wt.CommitSubject("HEAD~5")
	require.Error(t, err, "a ref past the root commit must error so the loop can stop")
}

// TestResetSoft_RewindsHeadAndRestagesChanges asserts ResetSoft moves HEAD back
// while leaving the unwound commit's content as staged changes — the property
// that makes pause/resume round-trip without losing work.
func TestResetSoft_RewindsHeadAndRestagesChanges(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, _, err := NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	wtPath := wt.GetWorktreePath()

	base := mustRunGit(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("v\n"), 0644))
	require.NoError(t, wt.CommitChanges("a commit"))

	require.NoError(t, wt.ResetSoft("HEAD~1"))

	require.Equal(t, base, mustRunGit(t, wtPath, "rev-parse", "HEAD"), "HEAD must rewind to the parent")
	require.NotEmpty(t, mustRunGit(t, wtPath, "status", "--porcelain"), "the change must survive as a pending change")
	require.Contains(t, mustRunGit(t, wtPath, "diff", "--cached", "--name-only"), "f.txt",
		"soft reset must leave the change staged")
}
