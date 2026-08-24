package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// TestSessionBriefFacts covers the fact-gathering half of the SessionStart brief (#485): the
// rendering and the copy live in session/tmux, this is where the four values come from.
func TestSessionBriefFacts(t *testing.T) {
	wt := newTestWorktree(t) // real repo + real worktree under a temp HOME
	inst := &Instance{ident: identity{title: "issue-485", branch: wt.GetBranchName()}, Path: wt.GetRepoPath(), gitWorktree: wt}

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
	sub := &Instance{ident: identity{title: "issue-485", branch: wt.GetBranchName()}, Path: filepath.Join(wt.GetRepoPath(), "docs"), gitWorktree: wt}
	require.Equal(t, wt.GetRepoPath(), sub.sessionBrief().Origin)

	// The worktrees root has to be the tree git actually materializes into — never a hardcoded
	// ~/.atrium — because the copy tells the agent everything under it belongs to another
	// session. Asserted as an ANCESTOR rather than the parent: a branch prefix containing a
	// slash ("zvi/") nests the worktree a directory deeper (<root>/zvi/<slug>_<hex>), so the
	// root is above the worktree but not immediately above it.
	require.True(t, strings.HasPrefix(wt.GetWorktreePath(), root+string(filepath.Separator)),
		"the named root (%s) must contain this session's own worktree (%s)", root, wt.GetWorktreePath())
}

// TestSessionBriefFollowsRename is the Instance half of what keeps the brief honest across a
// deep rename. The tmux half — start() reading the provider at each launch instead of trusting a
// value stamped at the last one — is TestStartRederivesSessionBriefAtLaunch; this pins the other
// end, that the provider bound onto the Session (this method) really yields the NEW title and
// branch once Rename has moved them, rather than anything memoized.
//
// Together they close the path that made the provider necessary: rename, then pause→resume or
// recover-in-place, both of which relaunch through start() WITHOUT going through Instance.Start
// and so rewrite settings.json from whatever the brief says at that moment. A stale one would
// tell the agent it is on a branch it is not on — and that clause is instruction-bearing
// ("already the session branch, so do not create another").
func TestSessionBriefFollowsRename(t *testing.T) {
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
	before := inst.sessionBrief()
	require.Equal(t, "formalize-packaing", before.Name)

	require.NoError(t, renameAndAdopt(inst, "formalize-packaging"))

	after := inst.sessionBrief()
	require.Equal(t, "formalize-packaging", after.Name, "the provider yields the renamed title")
	require.Equal(t, wt.GetBranchName(), after.Branch, "and the renamed branch")
	require.NotEqual(t, before.Branch, after.Branch, "the branch really did move, so this is not a no-op rename")
	// "packaing" is not a substring of "packaging", so this catches any pre-rename spelling
	// surviving into the copy the agent would actually be handed.
	require.NotContains(t, tmux.RenderSessionBrief(after), "packaing",
		"no pre-rename spelling may survive into the rendered brief")
}

// TestSessionBriefEmptyWithoutWorktree: a session with no worktree says nothing. Every
// load-bearing sentence of the brief is about an Atrium-managed worktree — it is disposable,
// Atrium owns it, its siblings belong to other sessions — and none of that is true of a direct
// session, whose cwd is the user's own checkout. A brief there would be confidently wrong, and
// wrong is worse than silent.
func TestSessionBriefEmptyWithoutWorktree(t *testing.T) {
	direct := &Instance{ident: identity{title: "d"}, Path: t.TempDir(), direct: true}
	require.Equal(t, tmux.SessionBrief{}, direct.sessionBrief())
	require.Empty(t, tmux.RenderSessionBrief(direct.sessionBrief()))

	// An unstarted git session has no worktree either, and no branch to name yet.
	unstarted := &Instance{ident: identity{title: "u"}, Path: t.TempDir()}
	require.Equal(t, tmux.SessionBrief{}, unstarted.sessionBrief())
}
