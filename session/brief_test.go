package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// TestSessionBriefFacts covers the fact-gathering half of the SessionStart brief (#485): the
// rendering and the copy live in session/tmux, this is where the four values come from.
func TestSessionBriefFacts(t *testing.T) {
	wt := newTestWorktree(t) // real repo + real worktree under a temp HOME
	inst := &Instance{Title: "issue-485", Path: wt.GetRepoPath(), Branch: wt.GetBranchName(), gitWorktree: wt}

	root, err := config.WorktreesDir()
	require.NoError(t, err)

	require.Equal(t, tmux.SessionBrief{
		Name:          "issue-485",
		Origin:        wt.GetRepoPath(),
		Branch:        wt.GetBranchName(),
		WorktreesRoot: root,
	}, inst.sessionBrief())

	// The origin must be the worktree's RESOLVED repo root, not i.Path — a user can create a
	// session from a subdirectory of the repo, and telling the agent the origin is
	// <repo>/some/subdir would be a confidently wrong statement about where its work came from.
	sub := &Instance{Title: "issue-485", Path: filepath.Join(wt.GetRepoPath(), "docs"),
		Branch: wt.GetBranchName(), gitWorktree: wt}
	require.Equal(t, wt.GetRepoPath(), sub.sessionBrief().Origin)

	// The worktrees root has to be the tree git actually materializes into — never a hardcoded
	// ~/.atrium — because the copy tells the agent everything under it belongs to another
	// session. Asserted as an ANCESTOR rather than the parent: a branch prefix containing a
	// slash ("zvi/") nests the worktree a directory deeper (<root>/zvi/<slug>_<hex>), so the
	// root is above the worktree but not immediately above it.
	require.True(t, strings.HasPrefix(wt.GetWorktreePath(), root+string(filepath.Separator)),
		"the named root (%s) must contain this session's own worktree (%s)", root, wt.GetWorktreePath())
}

// TestSessionBriefEmptyWithoutWorktree: a session with no worktree says nothing. Every
// load-bearing sentence of the brief is about an Atrium-managed worktree — it is disposable,
// Atrium owns it, its siblings belong to other sessions — and none of that is true of a direct
// session, whose cwd is the user's own checkout. A brief there would be confidently wrong, and
// wrong is worse than silent.
func TestSessionBriefEmptyWithoutWorktree(t *testing.T) {
	direct := &Instance{Title: "d", Path: t.TempDir(), direct: true}
	require.Equal(t, tmux.SessionBrief{}, direct.sessionBrief())
	require.Empty(t, tmux.RenderSessionBrief(direct.sessionBrief()))

	// An unstarted git session has no worktree either, and no branch to name yet.
	unstarted := &Instance{Title: "u", Path: t.TempDir()}
	require.Equal(t, tmux.SessionBrief{}, unstarted.sessionBrief())
}
