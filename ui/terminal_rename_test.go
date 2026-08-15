package ui

import (
	"context"
	"os/exec"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellPane starts an instance with a real shell cached for it and returns both, plus the
// key that shell is filed under. The instance's own agent session is mocked
// (makeStartedInstance) and its worktree is real, which is the split this file needs: the
// rename under test moves a real git worktree and a real branch, while the only thing on
// the tmux socket is the shell whose fate is the question.
func shellPane(t *testing.T, title string) (*TerminalPane, *session.Instance, string) {
	t.Helper()
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	inst := makeStartedInstance(t, title)
	t.Cleanup(func() { _ = inst.Kill() })

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	key, err := tp.EnsureSession(inst)
	require.NoError(t, err)
	require.NotEmpty(t, key)
	require.True(t, shellNamed(t, key), "precondition: the shell is alive on the socket")
	return tp, inst, key
}

// renameAndAdopt runs the deep rename the way app's renameDoneMsg handler does — the I/O
// off-thread, the identity adopted on it — rather than fabricating a RenamedIdentity, so
// the fields the handler writes are the fields this test observes.
func renameAndAdopt(t *testing.T, inst *session.Instance, newTitle string) {
	t.Helper()
	renamed, err := inst.Rename(newTitle)
	require.NoError(t, err)
	inst.AdoptRename(renamed)
}

// A deep rename must leave the shell reachable, which is the whole of #708: terminalKey was
// the instance's tmux name, AdoptRename rewrites exactly that field, and nothing re-keyed
// the pane — so CloseForInstance computed a key no entry used and the shell it should have
// killed stayed on the socket. Every teardown inherits that reap, so a pause then removed
// the worktree out from under a live shell with nothing left to name it but `atrium reset`.
//
// Observed on the unfixed tree at 0282ce0: after the rename the old shell was alive and
// cached, CloseForInstance reaped only the second shell created after it, and the first
// survived with its cwd following the moved worktree.
func TestDeepRenameLeavesTheShellReapable(t *testing.T) {
	tp, inst, key := shellPane(t, "rename-reap")

	renameAndAdopt(t, inst, inst.Title+"-renamed")

	// assert, not require: the socket check at the end is the one about the bug, and a
	// fatal above it would stop it running under the mutations that prove it.
	assert.Equal(t, key, terminalKey(inst), "a rename must not move the shell's cache key")
	sess, capKey, ok := tp.CaptureTarget(inst)
	assert.True(t, ok, "the renamed instance must still resolve its cached shell")
	assert.Equal(t, key, capKey)
	assert.NotNil(t, sess)

	tp.CloseForInstance(inst)
	assert.False(t, shellNamed(t, key),
		"the shell survived the reap under its pre-rename name, with nothing naming it")
}

// The other half of the same defect, and the one the user sees. A missed key is not merely
// an unreaped shell: UpdateContent files currentKey under the new key, misses the map and
// renders "Opening terminal…", and the next capture calls EnsureSession, which creates a
// SECOND shell. The terminal tab is silently replaced by an empty one while the first keeps
// running whatever was left in it.
func TestDeepRenameDoesNotMintASecondShell(t *testing.T) {
	tp, inst, key := shellPane(t, "rename-second-shell")

	renameAndAdopt(t, inst, inst.Title+"-renamed")

	// The name a derivation would reach for now — what the pane would create a shell
	// under if the key still followed the agent session.
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	require.NoError(t, tp.UpdateContent(inst))
	again, err := tp.EnsureSession(inst)
	require.NoError(t, err)

	assert.Equal(t, key, again, "EnsureSession must return the owned key, not a re-derived one")
	tp.mu.Lock()
	n := len(tp.sessions)
	tp.mu.Unlock()
	assert.Equal(t, 1, n, "a rename must not leave the pane holding two shells for one instance")
	assert.False(t, shellNamed(t, derivedNow),
		"a second shell was created under the post-rename name, orphaning the first")
	assert.True(t, shellNamed(t, key), "the original shell must still be the one in use")
}

// Negative control. A rename whose git half fails never reaches AdoptRename, so nothing
// about the session's identity changed — and the shell must be exactly as untouched as it
// would have been had the user never pressed the key.
//
// It is the test a fix that re-keys on the rename ATTEMPT rather than on its success fails,
// and it is why the fix touches no rename path at all: an owned name has nothing to roll
// back. What this fixture proves is that half — the identity, the cache and the shell.
// Rename's own tmux rollback runs against makeStartedInstance's MOCK agent session, so this
// asserts nothing about the socket; session/instance_rename_test.go owns that.
func TestFailedDeepRenameLeavesTheShellCachedAndAlive(t *testing.T) {
	tp, inst, key := shellPane(t, "rename-rollback")

	// Make the git half fail the way worktree.Rename's pre-flight does: the target branch
	// already exists, so it refuses before renaming anything.
	newTitle := inst.Title + "-taken"
	target := git.BranchNameForSession(config.LoadConfig().BranchPrefix, newTitle)
	require.NotEmpty(t, target)
	branch := exec.CommandContext(context.Background(), "git", "branch", target)
	branch.Dir = inst.Path
	require.NoError(t, branch.Run(), "precondition: the blocking branch must be created")

	_, err := inst.Rename(newTitle)
	require.Error(t, err, "precondition: this rename must fail at the git half")

	assert.Equal(t, key, terminalKey(inst), "a failed rename must leave the key alone")
	tp.mu.Lock()
	_, cached := tp.sessions[key]
	tp.mu.Unlock()
	assert.True(t, cached, "a failed rename must leave the shell cached")
	assert.True(t, shellNamed(t, key), "a failed rename must not cost the user their shell")

	// And the shell stays reapable, which is what "unchanged" has to mean here.
	tp.CloseForInstance(inst)
	assert.False(t, shellNamed(t, key), "the still-cached shell must reap normally")
}

// What earns ownership over re-keying the pane's maps in place. Nothing sweeps shells at
// exit — TabbedWindow.CloseTerminal has no production caller — so a shell is MEANT to
// outlive Atrium and be adopted by the next run. A fix that only re-keyed the live maps
// would leave that next run deriving <newName>_term, finding nothing, and minting a second
// shell beside the one still running, permanently.
//
// The fixture is a FRESH pane over a renamed instance, which is exactly what a restart
// changes for this code path: an empty cache asked to resolve a shell for a session whose
// agent name has moved since that shell was created. It deliberately does not rehydrate
// through FromInstanceData — bringing a restored session back online needs reattach, which
// is unexported and would drive tmux recovery. The other half of the property, that the
// owned name reaches the next process at all, is TestTermSessionSurvivesTheStateRoundTrip
// in session.
func TestAFreshPaneAdoptsTheOwnedShellAfterARename(t *testing.T) {
	_, inst, key := shellPane(t, "rename-restart")

	renameAndAdopt(t, inst, inst.Title+"-renamed")
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	fresh := NewTerminalPane(context.Background())
	t.Cleanup(fresh.Close)
	fresh.SetSize(80, 30)

	adopted, err := fresh.EnsureSession(inst)
	require.NoError(t, err)

	assert.Equal(t, key, adopted, "the next run must adopt the shell it left running")
	assert.False(t, shellNamed(t, derivedNow),
		"the next run minted a second shell instead of adopting the one already there")
	assert.True(t, shellNamed(t, key), "the adopted shell must still be alive")
}

// The reap releases the name as well as the shell, which is what stops ownership outliving
// what it owns. A session renamed while its shell was up, then paused and resumed, must get
// a shell named after the title it has now — not a second one under the name its first
// shell happened to be minted with. It also stops a renamed session squatting indefinitely
// on a name a new session with its old title would mint (session.OwnedSiblingCollides).
func TestReapReleasesTheNameSoTheNextShellFollowsTheRename(t *testing.T) {
	tp, inst, key := shellPane(t, "rename-reap-release")

	renameAndAdopt(t, inst, inst.Title+"-renamed")
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	tp.CloseForInstance(inst)
	require.Empty(t, inst.TerminalSessionName(), "a reap must give the shell's name up")
	assert.Equal(t, derivedNow, terminalKey(inst), "the key must follow the current title again")

	next, err := tp.EnsureSession(inst)
	require.NoError(t, err)
	assert.Equal(t, derivedNow, next, "the shell created after a reap must carry the new name")
	assert.True(t, shellNamed(t, derivedNow), "and must actually be on the socket under it")
}
