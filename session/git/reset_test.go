package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCommitSubjects_ReturnsNewestFirstAndBoundsToHistory covers what the resume
// unwind leans on CommitSubjects for: reading leading subjects newest-first, and
// bounding the result to the available history (a limit past the root returns
// only the real commits, never empties, so the caller can detect the end).
func TestCommitSubjects_ReturnsNewestFirstAndBoundsToHistory(t *testing.T) {
	repoPath := newTestRepo(t)
	wt, _, err := NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("v\n"), 0644))
	require.NoError(t, wt.CommitChanges("second commit"))

	subjects, err := wt.CommitSubjects(1)
	require.NoError(t, err)
	require.Equal(t, []string{"second commit"}, subjects, "limit bounds the slice; newest first")

	// History is two commits deep (initial + second); asking for more returns just
	// those two rather than erroring or padding past the root.
	subjects, err = wt.CommitSubjects(5)
	require.NoError(t, err)
	require.Len(t, subjects, 2, "result is bounded by available history")
	require.Equal(t, "second commit", subjects[0])
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
