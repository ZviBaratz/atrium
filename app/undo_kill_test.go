package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// undoSandbox gives a test its own data dir, so the journal it writes cannot be
// seen by any other test (or by the developer's real ~/.atrium).
func undoSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// gitSession builds a real, started git-backed session with a worktree, so the
// teardown path under test runs against actual git rather than a mock.
func gitSession(t *testing.T, title string) (*session.Instance, string) {
	t.Helper()
	// Start launches a real tmux session, so these are integration tests: skip
	// where tmux is absent (CI sets ATRIUM_CI_REQUIRE_TMUX=1 so they cannot go
	// dark there). The refusal logic is exercised tmux-free in undo_restore_test.go.
	testutil.RequireTmux(t)
	repo := filepath.Join(t.TempDir(), "repo")
	runGitIn(t, t.TempDir(), "init", repo)
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	runGitIn(t, repo, "add", ".")
	runGitIn(t, repo, "commit", "-m", "initial")

	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: repo, Program: "echo"})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })
	return inst, repo
}

func undoHome(t *testing.T) *home {
	t.Helper()
	h := newCreateFormHome(t)
	return h
}

// TestJournalKillRetainsTheBranchSoTeardownCannotDestroyIt is the end-to-end claim
// of the teardown half: after journalKill, the session's commits survive the
// `branch -D` that Cleanup is about to run.
func TestJournalKillRetainsTheBranchSoTeardownCannotDestroyIt(t *testing.T) {
	undoSandbox(t)
	inst, repo := gitSession(t, "fix-auth")
	h := undoHome(t)

	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)
	head := runGitIn(t, repo, "rev-parse", wt.GetBranchName())

	entry, undoable := h.journalKill(inst, "")
	require.True(t, undoable)
	assert.Equal(t, head, entry.SHA)
	assert.Equal(t, wt.GetBranchName(), entry.Branch)
	assert.Equal(t, repo, entry.RepoPath)

	require.NoError(t, inst.Kill())
	require.Error(t, git.CreateBranchAt(context.Background(), repo, wt.GetBranchName(), "HEAD~0~1"),
		"sanity: the helper below only proves something if the branch really went away")

	got, ok := git.RefExists(context.Background(), repo, entry.Ref)
	require.True(t, ok, "the retention ref must outlive the teardown")
	assert.Equal(t, head, got)
}

// TestJournalKillWritesTheEntryBeforeTheRef. If the ref went first, a crash in
// between would leave the user's objects pinned in their repository with nothing
// left in the world that names them — invisible to `git branch`, invisible to the
// sweep, and impossible to expire.
func TestJournalKillWritesTheEntryBeforeTheRef(t *testing.T) {
	undoSandbox(t)
	inst, repo := gitSession(t, "fix-auth")
	h := undoHome(t)

	// Make the ref impossible to create by taking the repository away, then assert
	// the record still exists: only entry-first ordering produces that.
	require.NoError(t, os.RemoveAll(repo))

	entry, undoable := h.journalKill(inst, "")
	assert.False(t, undoable, "a kill whose commits could not be pinned is not undoable")

	stored, err := undo.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1, "the record must survive a failed retention")
	assert.Equal(t, entry.ID, stored[0].ID)
	assert.Empty(t, stored[0].SHA, "no SHA is what marks the record unrestorable")
	assert.False(t, stored[0].Restorable(time.Now()))
}

// TestTeardownJournalsBeforeItDeletesFromStorage. The two orderings are
// indistinguishable on the happy path and differ entirely on a half-kill: the
// storage delete is the first step that can fail, and a record written after it
// would never exist for exactly the teardown the user most needs to undo.
func TestTeardownJournalsBeforeItDeletesFromStorage(t *testing.T) {
	undoSandbox(t)
	inst, _ := gitSession(t, "fix-auth")
	h := undoHome(t)
	// An empty storage makes DeleteInstance fail with "instance not found", which
	// is the earliest failure the teardown can hit.
	storage, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = storage

	msg := h.instanceTeardownCmd(inst)()
	err, isErr := msg.(error)
	require.True(t, isErr, "the teardown must surface the storage failure, got %T", msg)
	require.Error(t, err)

	stored, err := undo.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1, "the session was recorded before the step that failed")
	assert.Equal(t, "fix-auth", stored[0].Title)
	assert.True(t, stored[0].Restorable(time.Now()))
}

// TestJournalKillSkipsASessionThatCannotComeBack. An unstarted or still-Loading
// session has no branch, is filtered out of state.json, and may still be being
// written by its own Start goroutine. A record for one would be an undo offer that
// fails after the confirmation, with the session already gone.
func TestJournalKillSkipsASessionThatCannotComeBack(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)

	unstarted, err := session.NewInstance(session.InstanceOptions{
		Title: "never-ran", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	_, undoable := h.journalKill(unstarted, "")
	assert.False(t, undoable)

	loading, _ := gitSession(t, "still-starting")
	loading.SetStatus(session.Loading)
	_, undoable = h.journalKill(loading, "")
	assert.False(t, undoable)

	stored, err := undo.Load()
	require.NoError(t, err)
	assert.Empty(t, stored, "neither session may leave a record")
}

// TestJournalKillOnADirectSessionRunsNoGit — a direct session has no repository, so
// reaching for git would only log a misleading failure. Its snapshot is the whole
// record, and it is still restorable.
func TestJournalKillOnADirectSessionRunsNoGit(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)

	testutil.RequireTmux(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "scratch", Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	entry, undoable := h.journalKill(inst, "")
	require.True(t, undoable)
	assert.True(t, entry.Direct)
	assert.Empty(t, entry.Ref, "a direct session has no ref to name")
	assert.Empty(t, entry.RepoPath)
	assert.True(t, entry.Restorable(time.Now()))
}

// TestJournalKillRecordsTheSnapshotThatRebuildsTheSession. The record has to carry
// enough to rebuild the session, and in particular the worktree path byte for byte
// — that path is what the agent's conversation is keyed by.
func TestJournalKillRecordsTheSnapshotThatRebuildsTheSession(t *testing.T) {
	undoSandbox(t)
	inst, _ := gitSession(t, "fix-auth")
	h := undoHome(t)
	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)

	entry, undoable := h.journalKill(inst, "")
	require.True(t, undoable)

	var data session.InstanceData
	require.NoError(t, json.Unmarshal(entry.Snapshot, &data))
	assert.Equal(t, "fix-auth", data.Title)
	assert.Equal(t, wt.GetWorktreePath(), data.Worktree.WorktreePath)
	assert.Equal(t, inst.TmuxSessionName(), data.TmuxName)
}

// TestJournalKillGroupsABatchUnderOneID. A visual-mode kill of several sessions is
// one action the user took, so undo has to be able to reverse it as one.
func TestJournalKillGroupsABatchUnderOneID(t *testing.T) {
	undoSandbox(t)
	h := undoHome(t)
	batch := "0000000000000000042-cafebabe"

	for _, title := range []string{"a", "b"} {
		inst, _ := gitSession(t, title)
		_, undoable := h.journalKill(inst, batch)
		require.True(t, undoable)
	}

	group, ok := undo.LatestBatch(time.Now())
	require.True(t, ok)
	require.Len(t, group, 2)
	assert.Equal(t, []string{"a", "b"}, []string{group[0].Title, group[1].Title})
}

// TestKilledNoticeOnlyAdvertisesARecoveryThatExists. The advert is the discovery
// path for the whole feature — but promising undo after a kill that was not
// recorded would be worse than saying nothing.
func TestKilledNoticeOnlyAdvertisesARecoveryThatExists(t *testing.T) {
	assert.Equal(t, "killed 'fix-auth' · U to undo", killedNotice("fix-auth", true))
	assert.Equal(t, "killed 'fix-auth'", killedNotice("fix-auth", false))

	assert.Equal(t, "killed 3 sessions · U to undo", batchKilledNotice(3, 3))
	assert.Equal(t, "killed 3 sessions", batchKilledNotice(3, 2),
		"a partially recorded batch must not promise the whole batch back")
	assert.Equal(t, "killed 0 sessions", batchKilledNotice(0, 0))
}

// TestNoticeNamesTheRegisteredKey. The notice names a key in prose, which is the
// drift shape ui/key_prose_test.go exists for — deriving the label from the
// registry means a rebinding can never leave the sentence lying.
func TestNoticeNamesTheRegisteredKey(t *testing.T) {
	assert.Equal(t, "U", undoKeyLabel())
	assert.Contains(t, killedNotice("x", true), undoKeyLabel())
}
