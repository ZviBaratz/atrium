package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The refusals are the part of undo that has to be right, and none of them needs
// tmux: they are decisions about a journal entry, a git repository, and the names
// the running fleet already owns. Keeping them tmux-free means they run everywhere,
// including the hosts where the integration tests in undo_kill_test.go skip.

// retainedRepo builds a repo with a session-shaped branch retained under a ref, and
// returns the entry a kill would have written for it.
func retainedRepo(t *testing.T, title string) (undo.Entry, string) {
	t.Helper()
	undoSandbox(t)
	repo := filepath.Join(t.TempDir(), "repo")
	runGitIn(t, t.TempDir(), "init", repo)
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	runGitIn(t, repo, "add", ".")
	runGitIn(t, repo, "commit", "-m", "initial")

	branch := "zvi/" + title
	runGitIn(t, repo, "branch", branch)
	head := runGitIn(t, repo, "rev-parse", branch)

	snapshot, err := json.Marshal(session.InstanceData{Title: title, Path: repo})
	require.NoError(t, err)
	entry, err := undo.Write(undo.Entry{
		Title:    title,
		Display:  title,
		Path:     repo,
		RepoPath: repo,
		Branch:   branch,
		SHA:      head,
		TmuxName: "atrium_repo_" + title,
		Snapshot: snapshot,
	})
	require.NoError(t, err)
	runGitIn(t, repo, "update-ref", entry.Ref, head)
	runGitIn(t, repo, "branch", "-D", branch)
	return entry, repo
}

// TestRestoreIsAllowedWhenNothingHasChanged is the control the refusals are read
// against: with the branch gone, the ref present and the name free, nothing blocks.
func TestRestoreIsAllowedWhenNothingHasChanged(t *testing.T) {
	entry, _ := retainedRepo(t, "fix-auth")
	h := undoHome(t)

	assert.Empty(t, h.restoreBlocker(entry, map[string]struct{}{}))
}

// TestRestoreRefusesALiveTmuxName is the sharp one, and the reason undo needs
// refusals at all. Kill frees a name; recreating the session takes it back. Resume
// branches on whether that tmux session exists, so restoring into a live name binds
// the rebuilt instance to *another session's pane* — two instances then drive one
// tmux session and one hook directory, and killing either destroys the other's
// agent. Nothing about that is visible until it happens.
func TestRestoreRefusesALiveTmuxName(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)

	live := map[string]struct{}{entry.TmuxName: {}}
	reason := h.restoreBlocker(entry, live)

	require.NotEmpty(t, reason, "restoring onto a live tmux name must be refused")
	assert.Contains(t, reason, "fix-auth")
	assert.Contains(t, reason, entry.Ref, "a refusal must name the ref that still holds the commits")
	assert.Contains(t, reason, repo)
}

// TestRestoreRefusesAnOccupiedBranchName — the benign sibling of the tmux collision,
// and the one the user is most likely to hit: they killed the session, started
// another with the same title, and the branch name is taken again.
func TestRestoreRefusesAnOccupiedBranchName(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)

	// Someone takes the name back, pointing at something else.
	runGitIn(t, repo, "commit", "--allow-empty", "-m", "later work")
	runGitIn(t, repo, "branch", entry.Branch, "HEAD")
	require.NotEqual(t, entry.SHA, runGitIn(t, repo, "rev-parse", entry.Branch))

	reason := h.restoreBlocker(entry, map[string]struct{}{})
	require.NotEmpty(t, reason)
	assert.Contains(t, reason, "moved on")
	assert.Contains(t, reason, entry.Ref)
}

// TestRestoreAllowsABranchStillAtTheKilledTip. A session flagged as sitting on a
// branch the user already owned keeps that branch through the kill, so finding it
// present is normal — as long as it still points where it did. Only a moved branch
// is a problem.
func TestRestoreAllowsABranchStillAtTheKilledTip(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)

	runGitIn(t, repo, "branch", entry.Branch, entry.SHA)

	assert.Empty(t, h.restoreBlocker(entry, map[string]struct{}{}))
}

// TestRestoreRefusesWhenTheRepositoryIsGone — the path is absolute and persisted, so
// a project the user moved or deleted has to be named rather than reported as some
// opaque git failure.
func TestRestoreRefusesWhenTheRepositoryIsGone(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	require.NoError(t, os.RemoveAll(repo))

	reason := h.restoreBlocker(entry, map[string]struct{}{})
	require.NotEmpty(t, reason)
	assert.Contains(t, reason, repo, "the refusal must name the path we looked in")
}

// TestRestoreRefusesWhenTheRetainedCommitsAreGone. The repository is fine but the
// ref is not there — someone pruned it by hand, or another tool did. That is a
// different sentence from "your project moved", and conflating them would send the
// user looking in the wrong place.
func TestRestoreRefusesWhenTheRetainedCommitsAreGone(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	runGitIn(t, repo, "update-ref", "-d", entry.Ref)

	reason := h.restoreBlocker(entry, map[string]struct{}{})
	require.NotEmpty(t, reason)
	assert.NotContains(t, reason, "no longer at", "the repository is right where it was")
}

// TestADirectRestoreSkipsEveryGitCheck — a direct session has no repository, so the
// git refusals must not fire on it (its RepoPath is empty, which every one of them
// would otherwise fail on).
func TestADirectRestoreSkipsEveryGitCheck(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)

	snapshot, err := json.Marshal(session.InstanceData{Title: "scratch", Direct: true})
	require.NoError(t, err)
	entry, err := undo.Write(undo.Entry{
		Title: "scratch", Display: "scratch", Direct: true,
		TmuxName: "atrium_scratch", Snapshot: snapshot,
	})
	require.NoError(t, err)

	assert.Empty(t, h.restoreBlocker(entry, map[string]struct{}{}))
	assert.NotEmpty(t, h.restoreBlocker(entry, map[string]struct{}{entry.TmuxName: {}}),
		"a direct session's tmux name can still be taken")
}

// TestRecoverByHandNamesAWorkingCommand. Every refusal ends in an escape hatch, and
// it is only worth printing if it is actually runnable: the ref really does still
// hold the commits at this point.
func TestRecoverByHandNamesAWorkingCommand(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")

	hint := recoverByHand(entry)
	require.Contains(t, hint, "git -C "+repo+" branch <name> "+entry.Ref)

	// The command the hint describes must work.
	runGitIn(t, repo, "branch", "rescued", entry.Ref)
	assert.Equal(t, entry.SHA, runGitIn(t, repo, "rev-parse", "rescued"))
}

// TestRecoverByHandStaysSilentForADirectSession — there is no ref and no repository,
// so a "recover it by hand" hint would name a command that cannot be run.
func TestRecoverByHandStaysSilentForADirectSession(t *testing.T) {
	assert.Empty(t, recoverByHand(undo.Entry{Title: "scratch", Direct: true}))
}

// TestUndoConfirmDoesNotPromiseTheConversation. Resume asks the agent adapter
// whether a transcript is resumable and quietly starts a fresh agent when it is
// not, so a dialog that promised the conversation would be printing a coin flip as
// a fact. What it must name instead is what is definitely gone.
func TestUndoConfirmDoesNotPromiseTheConversation(t *testing.T) {
	msg := undoConfirmMessage([]undo.Entry{{Display: "fix-auth"}})

	assert.Contains(t, msg, "Restore session 'fix-auth'?")
	assert.NotContains(t, msg, "conversation")
	assert.Contains(t, msg, "scrollback")
	assert.Contains(t, msg, "gitignored")
}

// TestUndoConfirmSpeaksForTheWholeBatch — undo reverses the action the user took, so
// when that action removed five sessions the dialog has to say five.
func TestUndoConfirmSpeaksForTheWholeBatch(t *testing.T) {
	group := []undo.Entry{{Display: "a"}, {Display: "b"}, {Display: "c"}}

	assert.Contains(t, undoConfirmMessage(group), "Restore 3 killed sessions?")
	assert.Equal(t, "restore 3 sessions", undoConfirmLabel(len(group)))
	assert.Equal(t, "restoring 3 sessions…", undoBusyLabel(len(group)))
	assert.Equal(t, "restore", undoConfirmLabel(1))
	assert.Equal(t, "restoring…", undoBusyLabel(1))
}

// TestUndoWithAnEmptyJournalSaysSo — a key that does nothing is indistinguishable
// from a key that is broken, and the command palette now lists this action by name.
func TestUndoWithAnEmptyJournalSaysSo(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)

	cmd := h.undoLastKill()
	require.NotNil(t, cmd, "pressing undo with nothing to undo must still say something")
	assert.Equal(t, stateDefault, h.state, "and must not open a confirmation")
}

// TestUndoArmsAConfirmationForTheCapturedGroup. The sweep runs on the poll tick, so
// "the newest entry" is a different question a second later; the dialog has to act
// on what it named.
func TestUndoArmsAConfirmationForTheCapturedGroup(t *testing.T) {
	entry, _ := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	h.windowWidth = 120

	// confirmAction builds the overlay synchronously and returns a nil cmd, so the
	// arming is observable on the model, not in what comes back.
	h.undoLastKill()
	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, rendered, entry.Display, "the dialog names the session it captured")
	assert.Contains(t, rendered, "restore", "and the key hint names the verb, not a generic confirm")
}

// TestUndoFailureReportNamesEverySessionAndWhy. A refusal the user cannot read is a
// refusal they will hit again — and after a partial batch restore they need to know
// which sessions did not come back.
func TestUndoFailureReportNamesEverySessionAndWhy(t *testing.T) {
	all := undoDoneMsg{failures: []undoFailure{{"a", "its repository is no longer at /gone"}}}
	require.Contains(t, undoFailureReport(all), "Could not restore 1 session:")
	require.Contains(t, undoFailureReport(all), "a — its repository is no longer at /gone")

	partial := undoDoneMsg{
		instances: []*session.Instance{{Title: "b"}},
		failures:  []undoFailure{{"a", "branch moved on"}},
	}
	report := undoFailureReport(partial)
	require.Contains(t, report, "Restored 1 of 2 sessions. 1 could not come back:")
	require.Contains(t, report, "a — branch moved on")
}

// TestLiveSessionNamesSeesTheWholeFleet, not just the visible slice: a filtered or
// folded list still owns every tmux name it holds, and a restore that consulted only
// what is on screen would collide with a session the user cannot currently see.
func TestLiveSessionNamesSeesTheWholeFleet(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "visible", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)()

	names := h.liveSessionNames()
	if name := inst.TmuxSessionName(); name != "" {
		assert.Contains(t, names, name)
	}
}

// TestARefusedRestoreCreatesNothing. The refusals run before any git write, so a
// restore that is turned away leaves the repository exactly as it found it — and
// leaves its journal record intact, so the user can act on the refusal and try
// again.
func TestARefusedRestoreCreatesNothing(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	h.ctx = context.Background()

	// The fleet holds the name again, the way recreating the session would.
	live := map[string]struct{}{entry.TmuxName: {}}

	res := h.restoreKilled([]undo.Entry{entry}, live)

	require.Empty(t, res.instances)
	require.Len(t, res.failures, 1)
	_, exists := git.BranchTip(context.Background(), repo, entry.Branch)
	assert.False(t, exists, "a refused restore must not have recreated the branch")

	stored, err := undo.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1, "the record survives so the user can act on the refusal")
	assert.True(t, stored[0].Restorable(time.Now()))
}

// TestUndoIsSingleFlight. Two presses before the restore returns would run the same
// record twice, and the second would try to recreate a branch and a worktree the
// first already claimed. Nothing in the undo path guards that — the busy gate does,
// by NOT listing the key, which is why an accidental addition to
// keyAllowedWhileBusy has to fail here.
func TestUndoIsSingleFlight(t *testing.T) {
	assert.False(t, keyAllowedWhileBusy(keys.KeyUndoKill),
		"undo must be blocked while an action is in flight, or a double press restores twice")
}

// TestASuccessfulRestoreReleasesTheRetainedBranch. The restored branch holds those
// commits itself now, so the ref buys nothing — and the record that named it is
// spent, which means the sweep can never reach it again. A ref left behind here
// would sit in the user's repository forever: gc-immune, invisible to `git branch`,
// and named by nothing.
func TestASuccessfulRestoreReleasesTheRetainedBranch(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	h.ctx = context.Background()
	storage, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = storage

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "fix-auth", Path: repo, Program: "sleep 300",
	})
	require.NoError(t, err)

	h.handleUndoDone(undoDoneMsg{
		instances: []*session.Instance{inst},
		entries:   []undo.Entry{entry},
	})

	stored, err := undo.Load()
	require.NoError(t, err)
	require.Empty(t, stored, "the record is spent")

	_, refStillThere := git.RefExists(context.Background(), repo, entry.Ref)
	assert.False(t, refStillThere,
		"the retained branch must be released with the record, or nothing can ever release it")
}

// TestAFailedPersistKeepsTheRestoreOnOffer. Rows and branches exist but nothing is
// on disk, so the record has to survive: retiring it there would leave the user
// with an error message and no second attempt.
func TestAFailedPersistKeepsTheRestoreOnOffer(t *testing.T) {
	entry, repo := retainedRepo(t, "fix-auth")
	h := undoHome(t)
	h.ctx = context.Background()
	storage, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = storage

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "fix-auth", Path: repo, Program: "sleep 300",
	})
	require.NoError(t, err)

	// Make the data dir unwritable so the state save fails the way a full disk or a
	// permissions problem would.
	dataDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dataDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })

	h.handleUndoDone(undoDoneMsg{
		instances: []*session.Instance{inst},
		entries:   []undo.Entry{entry},
	})
	require.NoError(t, os.Chmod(dataDir, 0o700))

	stored, err := undo.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1, "an undurable restore must stay undoable")
	_, refStillThere := git.RefExists(context.Background(), repo, entry.Ref)
	assert.True(t, refStillThere, "and its commits must stay pinned")
}
