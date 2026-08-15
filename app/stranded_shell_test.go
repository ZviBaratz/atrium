package app

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three teardowns that removed a session's worktree and left its cached terminal
// shell running in the deleted directory (#707). The kill handlers reap first and
// report afterwards; these three reported first and reaped only successes.
//
// Every case below comes in a pair, and the pair is the point. A guard set that only
// proves "a failed pause now reaps" is passed by a fix that reaps whenever the pause
// errored — and that fix destroys the shell sitting in a worktree pause deliberately
// KEPT, holding WIP it could not commit. The false arms are what reject it.
//
// These pin the wiring: which instances each path hands to the reap, captured through
// the cleanupTerminalForInstance seam. Whether worktreeGone itself is right about
// pause()'s branches is session/workingdir_gone_test.go's job, against the real
// pause(); proving it twice here would only prove the fixture.

// Site 1, the failed single pause. handlePauseDone returned on msg.err before ever
// reaching its reap, while pause() had already removed the worktree and set Paused.
func TestPauseDone_FailedPauseReapsTheShellStrandedInTheRemovedWorktree(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(pauseDoneMsg{instance: inst, err: os.ErrClosed, worktreeGone: true})

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a pause that errored after removing the worktree must still reap the shell running in it")
}

// Site 1 one layer down: WHEN that failed pause writes. Since #708 the reap releases the
// shell's owned tmux name, so a handler that reaps and then returns without persisting leaves
// state.json naming a shell it has just killed — the next run claims that dead name for a
// live shell instead of minting from the title the session has by then, and reserves that
// title against new sessions for as long as it holds it. The batch path is ordered for the
// same reason (TestBatchOutcome_PausePersistsAfterTearingDownTerminals); this is its
// single-session twin, and the ordering is the assertion rather than the write.
func TestPauseDone_FailedPausePersistsAfterReapingTheStrandedShell(t *testing.T) {
	h := newCreateFormHome(t)
	var order []string
	st, err := session.NewStorage(recordingState{InstanceStorage: config.DefaultState(), log: &order})
	require.NoError(t, err)
	h.storage = st

	inst := addActive(t, h, "alpha")
	orig := cleanupTerminalForInstance
	t.Cleanup(func() { cleanupTerminalForInstance = orig })
	cleanupTerminalForInstance = func(_ *ui.TabbedWindow, reaped *session.Instance) {
		order = append(order, "reap "+reaped.Title)
	}

	_, _ = h.Update(pauseDoneMsg{instance: inst, err: os.ErrClosed, worktreeGone: true})

	assert.Equal(t, []string{"reap alpha", "persist"}, order,
		"a failed pause must persist after reaping the shell whose name that reap released")
}

// The negative control for site 1, and the reason the discriminator is not the error.
func TestPauseDone_FailedPauseKeepingTheWorktreeLeavesTheShellAlone(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(pauseDoneMsg{instance: inst, err: os.ErrClosed, worktreeGone: false})

	require.Empty(t, *captured,
		"a pause whose WIP commit failed keeps the worktree on purpose: reaping there kills the shell the user would rescue the work with")
}

// The success path still reaps, and now does so through the same seam — so this is
// the first guard that can see it at all. Without it, routing that call through
// cleanupTerminalForInstance could drop the reap and nothing would notice.
func TestPauseDone_SuccessfulPauseStillReaps(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(pauseDoneMsg{instance: inst})

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a clean pause removes the worktree, so its shell must be torn down as it always was")
}

// Site 2, the failed batch pause. A per-instance failure never entered
// pausedInstances, which is the only slice finishBatch reaps — so a "pause all" lost
// one shell per failing session.
func TestBatchPause_FailedEntryReapsTheShellStrandedInTheRemovedWorktree(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	parked := addActive(t, h, "alpha")
	failed := addActive(t, h, "bravo")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchPauseDoneMsg{
		paused:          1,
		pausedInstances: []*session.Instance{parked},
		failures:        []pauseFailure{{inst: failed, title: "bravo", err: os.ErrClosed, worktreeGone: true}},
	})

	require.ElementsMatch(t, []*session.Instance{parked, failed}, *captured,
		"a batch pause must reap the failed entries whose worktree went, not only the parked ones")
}

// The negative control for site 2. The failing session here is the WIP case, so the
// batch must reap the parked session and leave the other alone — which also proves
// the reap is per-instance rather than "the batch had a failure".
func TestBatchPause_FailedEntryKeepingItsWorktreeLeavesThatShellAlone(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	parked := addActive(t, h, "alpha")
	failed := addActive(t, h, "bravo")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchPauseDoneMsg{
		paused:          1,
		pausedInstances: []*session.Instance{parked},
		failures:        []pauseFailure{{inst: failed, title: "bravo", err: os.ErrClosed, worktreeGone: false}},
	})

	require.Equal(t, []*session.Instance{parked}, *captured,
		"the session that kept its worktree must keep its shell, while the parked one is still reaped")
}

// lostRecoveryHome drives site 3 end to end through its real caller: a started
// session whose pane the metadata tick reports as gone, observed for the full
// debounce, so recoverLostInstances actually runs RecoverLostSession and the
// metadataUpdateDoneMsg handler reaches the reap. Returns what the seam captured.
//
// Driven through Update rather than by calling recoverLostInstances directly,
// because the free function is not where the bug was — it has no m and never could
// reap. The missing wiring was in the caller.
func lostRecoveryHome(t *testing.T, removeWorkingDir bool) (*session.Instance, *[]*session.Instance) {
	t.Helper()
	h, inst := newCaptureHome(t, newFrameSpy("shell prompt $"))
	withStorage(t, h)
	if removeWorkingDir {
		require.NoError(t, os.RemoveAll(inst.WorkingDir()))
	}
	captured := withCapturingCleanup(t)

	lost := []instanceMetaResult{{instance: inst, sessionLost: true}}
	for range lostSessionRecoverThreshold {
		_, _ = h.Update(metadataUpdateDoneMsg{results: lost, attachGen: h.attachGen})
	}
	require.True(t, inst.Paused(), "precondition: the debounced recovery parked the session")
	return inst, captured
}

// Site 3, the lost-session recovery — the one that was not a reap site at all, on
// success or failure.
//
// What this pins is the wiring: that the caller samples WorkingDirGone after the
// recovery and reaps on it. The fixture does not itself free a worktree —
// newCaptureHome builds a direct session, whose pause branch never touches the
// directory — so the test removes the directory to put the recovery in the state a
// git session's own removal leaves behind. That a recovery really does free a
// worktree is pause()'s behaviour, and RecoverLostSession is pause() verbatim: held
// by session.TestWorkingDirGone_CleanPauseFreesTheWorktree over that same body.
func TestLostRecovery_ReapsTheShellStrandedInTheRemovedWorkingDir(t *testing.T) {
	inst, captured := lostRecoveryHome(t, true)

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a recovery that leaves no working directory behind must reap the shell that was in it")
}

// The negative control for site 3, and the one only this site reaches with a shell at
// stake: Pause refuses a direct session and both batch entry points filter them out.
// (A load parks one too — session.recoverInPlace's direct branch and parkOverBudget —
// but nothing is cached that early.) Its working directory is the user's own
// checkout, still very much on disk, so reaping would kill a live shell in the user's
// real repo.
func TestLostRecovery_DirectSessionInALiveDirectoryKeepsItsShell(t *testing.T) {
	inst, captured := lostRecoveryHome(t, false)

	require.DirExists(t, inst.WorkingDir(), "precondition: recovery never touches a direct session's directory")
	require.Empty(t, *captured,
		"a parked session whose directory is still there must keep its shell")
}

// reapStrandedShell is called from three handlers, so its own refusals are asserted
// once here rather than three times above. A nil instance reaches it from
// pauseDoneMsg, whose instance field a hand-built message can leave unset.
func TestReapStrandedShell_RefusesNilAndAnIntactWorkingDir(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	h.reapStrandedShell(nil, true)
	h.reapStrandedShell(inst, false)
	require.Empty(t, *captured, "neither a nil instance nor a live working directory may be reaped")

	// The control: the same helper does reap when both conditions hold, so the
	// emptiness above is a refusal rather than a helper that never fires.
	h.reapStrandedShell(inst, true)
	require.Equal(t, []*session.Instance{inst}, *captured, "control: a stranded shell is reaped")
}
