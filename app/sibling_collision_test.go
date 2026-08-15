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

// siblingHome puts two sessions in one repo group, one in each state a session that hosts
// siblings can be in.
//
// The HOLDER was deep-renamed from "held" to "holder" while hosting both — so its tmux name
// is `<g>_holder` while it still holds `<g>_held_term` and `<g>_held_run`. The freed "held"
// title is the hazard: nothing else stops it being minted straight onto the holder's live
// shell.
//
// PLAIN was never renamed and simply has its terminal tab open, so it holds the shell name
// its own current title mints. That is the ordinary state of any session with an open
// terminal, and it is the one a guard that treats every owned sibling as a conflict gets
// wrong: it makes a session's own name look taken by its own shell.
//
// Neither is started, and neither guard performs I/O — restored is also the shape an owned
// sibling name actually reaches a new process in.
func siblingHome(t *testing.T) (*home, *session.Instance, *session.Instance) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	repoRoot := t.TempDir()
	group := filepath.Base(repoRoot)
	heldName := tmux.QualifiedSessionName(group, "held")
	plainName := tmux.QualifiedSessionName(group, "plain")

	holder := restoredSession(t, repoRoot, "holder", tmux.QualifiedSessionName(group, "holder"),
		heldName+session.TermSessionSuffix, heldName+session.RunSessionSuffix)
	plain := restoredSession(t, repoRoot, "plain", plainName,
		plainName+session.TermSessionSuffix, "")

	require.Equal(t, group, holder.GroupKey(), "precondition: the holder is in the candidate group")
	require.Equal(t, group, plain.GroupKey(), "precondition: both sessions share one group")
	require.Equal(t, heldName+session.TermSessionSuffix, holder.TerminalSessionName(),
		"precondition: the holder still holds its pre-rename shell name")
	require.Equal(t, plainName+session.TermSessionSuffix, plain.TerminalSessionName(),
		"precondition: plain holds the shell name its own current title mints")

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
// may keep its own name — but it must NOT skip it entirely. A title that sanitizes onto one
// of the session's OWN sibling names would leave the agent and its shell answering to a
// single tmux session, which is a conflict with itself.
func TestDeepRenameRejectsATitleCollidingWithItsOwnSibling(t *testing.T) {
	h, holder, plain := siblingHome(t)

	require.Error(t, h.validateDeepRename(holder, "held.term"),
		"a session may not put its agent session on the shell name it is still hosting")
	require.Error(t, h.validateDeepRename(holder, "held.run"),
		"nor on its run command's")
	require.Error(t, h.validateDeepRename(plain, "plain.term"),
		"and the same for a session hosting a shell under its current title")
	require.NoError(t, h.validateDeepRename(holder, "holder-again"),
		"control: an ordinary rename of the same session is still allowed")
}

// The other side of that check, and the one the first version of this guard got wrong: a
// session's own siblings are named AFTER it, so a title whose siblings it already holds is
// the consistent state, not a collision. Asking the full OwnedSiblingCollides question about
// the session being renamed refuses exactly the titles that agree with what it is hosting.
//
// Both cases below are ordinary. The no-op is what the deep-rename dialog pre-fills, so it
// is what a user editing only the note submits — and any session that has opened its
// terminal tab holds `<its own name>_term`, which the full check reads as its own name being
// taken. The round trip is the same equation reached the other way.
func TestDeepRenameAllowsATitleWhoseSiblingsTheSessionAlreadyHolds(t *testing.T) {
	h, holder, plain := siblingHome(t)

	require.NoError(t, h.validateDeepRename(plain, "plain"),
		"a session with its terminal tab open must still be renameable to its own current title")
	require.NoError(t, h.validateDeepRename(holder, "held"),
		"and back to a title whose siblings it is still hosting — the shell and server are "+
			"then named after the title the session has again")
}
