package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// WorkingDirGone is the discriminator app reaps a session's cached terminal shell on
// (#707), so what it has to be right about is pause()'s branches — not a mock of
// them. Every case here runs the real pause() over a real git worktree and then asks
// the predicate, which is the only way to catch the two shapes that make the obvious
// fixes wrong:
//
//   - "the pause errored" is not the question. The WIP case below errors and KEEPS
//     the worktree; the orphan case below errors and REMOVES it.
//   - pause()'s own removeWorktree local is not the answer either. It is declared
//     after the orphan branch returns, so a fix that plumbed it would report "kept"
//     for a directory that branch deleted. TestWorkingDirGone_OrphanedWorktree is
//     that fix's counterexample.
//
// The two false cases are the negative controls: a guard set that only proves "a
// failed pause now reaps" is passed by a fix that reaps on err != nil, and that fix
// destroys a shell the user needs.

// A clean pause removes the worktree, so the shell that was running in it is
// stranded and must be reaped. The positive control: without this, every False
// below is satisfiable by a predicate that always says false.
func TestWorkingDirGone_CleanPauseFreesTheWorktree(t *testing.T) {
	wt := newTestWorktree(t)
	inst := startedInstance(t, wt)

	require.False(t, inst.WorkingDirGone(), "precondition: the worktree is on disk before the pause")
	require.NoError(t, inst.Pause())

	require.NoDirExists(t, wt.GetWorktreePath(), "precondition: a clean pause removes the worktree")
	require.True(t, inst.WorkingDirGone(),
		"a pause that freed the worktree must report it, or the shell running in it is never reaped")
}

// The negative control, and the reason the discriminator is not "did the pause
// error". When the auto-pause commit fails, pause() deliberately keeps the worktree
// so the uncommitted work survives (#270) — and still returns an error. The shell
// sitting in that worktree is exactly the one the user would rescue the WIP with, so
// it must not be reaped.
func TestWorkingDirGone_FailedWIPCommitKeepsTheWorktree(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	// Break the identity so the auto-pause commit fails deterministically, the same
	// way TestPause_CommitFailureParksPausedAndKeepsWIP does: unsetting alone is not
	// portable because git auto-detects user@host on some platforms.
	runGit(t, repoPath, "config", "user.useConfigOnly", "true")
	runGit(t, repoPath, "config", "--unset", "user.email")
	runGit(t, repoPath, "config", "--unset", "user.name")
	wipPath := filepath.Join(wt.GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wipPath, []byte("uncommitted work\n"), 0o644))

	inst := startedInstance(t, wt)
	err := inst.Pause()

	require.Error(t, err, "precondition: a failed WIP commit surfaces an error")
	require.True(t, inst.Paused(), "precondition: it still parks the session")
	require.FileExists(t, wipPath, "precondition: the WIP is left on disk to be rescued")
	require.False(t, inst.WorkingDirGone(),
		"a pause that kept the worktree must not report it gone: reaping here kills the shell the user would rescue the WIP with")
}

// The branch a fix built on pause()'s removeWorktree local would miss. An orphaned
// worktree (path present, .git missing) skips the dirty check and the git remove
// entirely, deletes the directory with os.RemoveAll, and returns before
// removeWorktree is even declared — yet the shell in that directory is just as
// stranded as after a clean pause.
func TestWorkingDirGone_OrphanedWorktreeIsStillFreed(t *testing.T) {
	wt := newTestWorktree(t)
	// Make IsValidWorktree report false without removing the directory, so the
	// orphan branch is what deletes it and the assertion is about that branch.
	require.NoError(t, os.RemoveAll(filepath.Join(wt.GetWorktreePath(), ".git")))
	require.DirExists(t, wt.GetWorktreePath(), "precondition: the directory is still there, only .git is gone")

	inst := startedInstance(t, wt)
	// Deliberately not asserted on: this branch returns tc.Err(), which is nil when
	// its detach/RemoveAll/Prune all succeed, and non-nil when any does not. Either
	// way the directory is gone — which is the whole point that the error cannot be
	// read for the outcome.
	_ = inst.Pause()

	require.True(t, inst.Paused(), "precondition: the orphan branch still parks the session")
	require.NoDirExists(t, wt.GetWorktreePath(), "precondition: the orphan branch removes the directory")
	require.True(t, inst.WorkingDirGone(),
		"the orphan branch frees the directory too, and it returns before removeWorktree exists — a fix plumbing that local reports the opposite here")
}

// The second negative control, and the one only a lost-session recovery reaches with
// a shell at stake: Pause refuses a direct session outright and both batch entry
// points filter them out. (Two load-time paths park a direct session too —
// recoverInPlace's direct branch and parkOverBudget, both under bringOnline — but a
// load has no cached terminal shell to strand yet.) A direct session has no worktree
// at all; its working directory is the user's own checkout, so reaping would kill a
// shell in a directory that is very much there.
func TestWorkingDirGone_DirectSessionRecoveryTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: dir, tmuxSession: directTmux("d")}

	require.NoError(t, inst.RecoverLostSession())

	require.True(t, inst.Paused(), "precondition: a lost direct session is still parked")
	require.DirExists(t, dir, "precondition: recovery must never touch the user's real directory")
	require.False(t, inst.WorkingDirGone(),
		"a parked direct session runs in the user's own checkout: reporting it gone would reap a shell sitting in a live directory")
}

// A permission error is not "gone". The reap is destructive, so an unreadable
// directory must fail closed — the cost of a false positive is the user's shell and
// whatever is running in it, while the cost of a false negative is one uncollected
// shell in a directory that still exists.
func TestWorkingDirGone_UnstattableDirectoryIsNotReportedGone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions, so the unreadable parent cannot be built")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "work")
	require.NoError(t, os.Mkdir(dir, 0o755))
	// Clear +x on the parent so stat of the child fails with EACCES rather than
	// ENOENT. Restored in cleanup or t.TempDir's own removal fails.
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	inst := &Instance{Title: "d", status: Running, started: true, direct: true, Path: dir}

	_, err := os.Stat(dir)
	require.Error(t, err, "precondition: the directory is unstattable")
	require.False(t, os.IsNotExist(err), "precondition: the error is a permission error, not ENOENT")
	require.False(t, inst.WorkingDirGone(),
		"only fs.ErrNotExist means gone; a permission error must leave the shell alone")
}

// An instance with no working directory at all (never started, no path) has no shell
// to strand. Reporting it gone would be harmless today — CloseForInstance no-ops on
// an uncached key — but it would make the predicate say "this session's directory
// was removed" about a session that never had one.
func TestWorkingDirGone_EmptyWorkingDirIsNotGone(t *testing.T) {
	inst := &Instance{Title: "d", status: Running, direct: true}

	require.Empty(t, inst.WorkingDir(), "precondition: the fixture has no working directory")
	require.False(t, inst.WorkingDirGone(), "no working directory is not a removed working directory")
}
