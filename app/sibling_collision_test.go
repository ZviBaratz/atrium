package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"

	"charm.land/bubbles/v2/spinner"
	"github.com/stretchr/testify/require"
)

// restoredSession rehydrates a git session from state with an explicit repo root, which is
// what pins its group: FromInstanceData builds a Worktree for every non-direct session and
// GroupKey resolves through it (GetRepoName), so the repo root's basename IS the group.
// Restored rather than started because that is the shape an owned sibling name actually
// reaches a new process in — persisted, not derived — and because neither guard under test
// performs any I/O.
//
// termSession/runSession are the names this session HOLDS; "" for one that hosts nothing.
func restoredSession(t *testing.T, repoRoot, title, tmuxName, termSession, runSession string) *session.Instance {
	t.Helper()
	inst, err := session.FromInstanceData(context.Background(), session.InstanceData{
		Title:       title,
		Path:        repoRoot,
		Program:     "echo",
		TmuxName:    tmuxName,
		TermSession: termSession,
		RunSession:  runSession,
		Worktree:    session.GitWorktreeData{RepoPath: repoRoot},
	}, "test-")
	require.NoError(t, err)
	return inst
}

// siblingHome puts two sessions in one repo group: a HOLDER that was deep-renamed from
// "held" to "holder" while hosting both siblings — so its tmux name is `<g>_holder` while
// it still holds `<g>_held_term` and `<g>_held_run` — and a plain session hosting nothing.
// The freed "held" title is the hazard: nothing else stops it being minted straight onto
// the holder's live shell.
func siblingHome(t *testing.T) (*home, *session.Instance, *session.Instance) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	repoRoot := t.TempDir()
	group := filepath.Base(repoRoot)
	heldName := tmux.QualifiedSessionName(group, "held")

	holder := restoredSession(t, repoRoot, "holder", tmux.QualifiedSessionName(group, "holder"),
		heldName+session.TermSessionSuffix, heldName+"_run")
	plain := restoredSession(t, repoRoot, "plain", tmux.QualifiedSessionName(group, "plain"), "", "")

	require.Equal(t, group, holder.GroupKey(), "precondition: the holder is in the candidate group")
	require.Equal(t, group, plain.GroupKey(), "precondition: both sessions share one group")
	require.Equal(t, heldName+session.TermSessionSuffix, holder.TerminalSessionName(),
		"precondition: the holder still holds its pre-rename shell name")

	list.AddInstance(plain)()
	list.AddInstance(holder)()

	h := &home{list: list, appConfig: config.DefaultConfig(), newSessionGroup: group}
	return h, holder, plain
}

// A deep rename onto a title whose shell name another session is still holding must be
// rejected. DerivedTmuxNameCollides cannot see it: it derives the holder's siblings from the
// holder's CURRENT tmux name, and the rename moved that away from the names actually held.
// Letting it through gives two sessions one tmux session — the second one's EnsureSession
// adopts the first one's live shell, in the first one's worktree, and either teardown kills
// it for both.
func TestDeepRenameRejectsATitleHoldingAnotherSessionsSibling(t *testing.T) {
	h, _, plain := siblingHome(t)

	require.Error(t, h.validateDeepRename(plain, "held"),
		"renaming onto a title whose shell another session still holds must be refused")
	require.Error(t, h.validateDeepRename(plain, "held.term"),
		"and onto the held shell name itself, which a dot sanitizes straight onto")
	require.NoError(t, h.validateDeepRename(plain, "unheld"),
		"control: an unrelated title must still be allowed")
}

// The same reservation on the create form, which is the commoner route: after a rename the
// old TITLE is free, so nothing stops a user typing it again — and the new session would
// mint exactly the names the renamed one is still hosting.
func TestTitleConflictRejectsATitleHoldingAnotherSessionsSibling(t *testing.T) {
	h, _, _ := siblingHome(t)

	require.Equal(t, titleErrNameTaken, h.titleConflict("held"),
		"a new session with the freed title would mint onto the held siblings")
	require.Equal(t, titleErrNameTaken, h.titleConflict("held.run"),
		"and onto the held run-session name itself")
	require.Empty(t, h.titleConflict("unheld"), "control: an unrelated title is free")
}

// validateDeepRename skips the selected session for the duplicate-title check — a session
// may keep its own name — but it must NOT skip it for the sibling checks. Those are about
// the session's own shell and dev server, which a rename does not move, so renaming onto one
// of them would leave the agent and its shell answering to a single tmux session.
func TestDeepRenameRejectsATitleCollidingWithItsOwnSibling(t *testing.T) {
	h, holder, _ := siblingHome(t)

	require.Error(t, h.validateDeepRename(holder, "held"),
		"a session may not rename onto the shell and run sessions it is still hosting")
	require.NoError(t, h.validateDeepRename(holder, "holder-again"),
		"control: an ordinary rename of the same session is still allowed")
}
