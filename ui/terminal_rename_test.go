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

	key, err := tp.EnsureSession(inst, inst.Title())
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

	renameAndAdopt(t, inst, inst.Title()+"-renamed")

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

	renameAndAdopt(t, inst, inst.Title()+"-renamed")

	// The name a derivation would reach for now — what the pane would create a shell
	// under if the key still followed the agent session.
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	require.NoError(t, tp.UpdateContent(inst))
	again, err := tp.EnsureSession(inst, inst.Title())
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
	newTitle := inst.Title() + "-taken"
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

	renameAndAdopt(t, inst, inst.Title()+"-renamed")
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	fresh := NewTerminalPane(context.Background())
	t.Cleanup(fresh.Close)
	fresh.SetSize(80, 30)

	adopted, err := fresh.EnsureSession(inst, inst.Title())
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

	renameAndAdopt(t, inst, inst.Title()+"-renamed")
	derivedNow := inst.MintTerminalSessionName()
	require.NotEqual(t, key, derivedNow, "precondition: the rename moved the derived name")

	tp.CloseForInstance(inst)
	require.Empty(t, inst.TerminalSessionName(), "a reap must give the shell's name up")
	assert.Equal(t, derivedNow, terminalKey(inst), "the key must follow the current title again")

	next, err := tp.EnsureSession(inst, inst.Title())
	require.NoError(t, err)
	assert.Equal(t, derivedNow, next, "the shell created after a reap must carry the new name")
	assert.True(t, shellNamed(t, derivedNow), "and must actually be on the socket under it")
}

// Ownership makes a shell reachable across a restart, and this is the bill for that: the
// name can be the ONLY thing that names a live shell. A pane that never opened the terminal
// tab this run has no entry for a shell the last run left, so a reap that dropped the name
// on a cache miss would destroy the last pointer to it — the permanent orphan of #708,
// reached by fixing #708.
//
// The fresh pane is the restart. session.releaseRunTmux is the shape borrowed: no cached
// session, so kill the owned name and forget it only once that returned clean.
func TestReapKillsAnOwnedShellThePaneNeverCached(t *testing.T) {
	_, inst, key := shellPane(t, "reap-uncached")

	fresh := NewTerminalPane(context.Background())
	t.Cleanup(fresh.Close)
	fresh.SetSize(80, 30)
	fresh.mu.Lock()
	_, cached := fresh.sessions[key]
	fresh.mu.Unlock()
	require.False(t, cached, "precondition: the fresh pane has no entry for the shell")

	fresh.CloseForInstance(inst)

	assert.False(t, shellNamed(t, key),
		"the shell was left running with its owned name released — nothing names it now")
	assert.Empty(t, inst.TerminalSessionName(), "a reap that got its shell must give the name up")
}

// The other half, and the one that decides between a probe and a kill. A reap that never got
// an answer out of tmux must keep the name: it is the only pointer to a shell that may still
// be running, and giving it up is #708's permanent orphan reached through the code written to
// prevent it.
//
// The fixture is a pane whose lifecycle context is already cancelled, which is what a reap
// racing app shutdown looks like — and it is the one inconclusive outcome that is
// deterministic rather than load-dependent. It matters because DoesSessionExist folds
// "inconclusive" into "no" (see liveness in session/tmux): a probe-then-kill reap reads the
// cancelled probe as an empty name, skips the kill, and releases. Close classifies instead,
// so the failure is visible and the name stays put.
func TestAReapThatNeverReachedTmuxKeepsTheName(t *testing.T) {
	_, inst, key := shellPane(t, "reap-inconclusive")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wedged := NewTerminalPane(ctx)
	t.Cleanup(wedged.Close)
	wedged.SetSize(80, 30)
	wedged.mu.Lock()
	_, cached := wedged.sessions[key]
	wedged.mu.Unlock()
	require.False(t, cached, "precondition: this pane must take the uncached-owned path")

	wedged.CloseForInstance(inst)

	assert.Equal(t, key, inst.TerminalSessionName(),
		"a reap that could not confirm the shell was gone released the only name it had")
	assert.True(t, shellNamed(t, key),
		"precondition for the assertion above: the shell it named is still running")
}

// An owned name whose shell is already gone must still be given up. The kill is where that
// is decided now that no has-session probe runs in front of it: tmux reporting no session
// left is the teardown goal already met (sessionAlreadyGone in session/tmux), so Close
// returns nil and the release follows. Without this the name would be held forever, naming
// nothing and reserving its title against every new session that wanted it.
//
// This asserts the integration claim only. WHICH of sessionAlreadyGone's messages tmux
// answers with here is not fixed: this pane starts no server, so an empty sandbox socket
// yields "error connecting to … (No such file or directory)" while a server another test
// left running yields "can't find session". The precondition above cannot tell them apart —
// DoesSessionExist is false either way — so the deterministic, per-message guard is
// close_test.go's table in session/tmux, not this test.
func TestReapReleasesAnOwnedNameWithNothingOnTheSocket(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	inst := makeStartedInstance(t, "reap-already-gone")
	t.Cleanup(func() { _ = inst.Kill() })
	owned, minted := inst.ClaimTerminalSessionName()
	require.True(t, minted, "precondition: this claim is the one that minted the name")
	require.False(t, shellNamed(t, owned), "precondition: no shell is on the socket under it")

	tp := NewTerminalPane(context.Background())
	t.Cleanup(tp.Close)
	tp.SetSize(80, 30)

	tp.CloseForInstance(inst)

	assert.Empty(t, inst.TerminalSessionName(),
		"a reap that found nothing left must give the name up, not hold it on behalf of nothing")
}

// A reap for an instance with no tmux name has no key either, and the missing key is not
// inert. currentKey is "" in exactly the states the idle splash and every fallback live in,
// so falling through to the currentKey comparison matches one of them and blanks the pane for
// a selection this reap is not about, until the next 100ms tick refills it.
func TestReapForAnInstanceWithNoTmuxNameLeavesThePaneAlone(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))
	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	require.NoError(t, tp.UpdateContent(nil))

	tp.mu.Lock()
	splashBefore := tp.splash
	tp.mu.Unlock()
	require.True(t, splashBefore, "precondition: a nil selection leaves the pane on the idle splash")

	tp.CloseForInstance(mkInst(t, "never-started", t.TempDir()))

	tp.mu.Lock()
	splashAfter, message, gens := tp.splash, tp.fallbackMessage, len(tp.reapGen)
	tp.mu.Unlock()
	assert.True(t, splashAfter, "a reap with no key must not clear the pane it is not about")
	assert.NotEmpty(t, message, "nor the message that pane is rendering")
	assert.Zero(t, gens, "and must not file a generation under the empty key")
}

// A create that fails must put back the name it minted. Otherwise the instance owns — and
// persists — a name no shell was ever started under, and OwnedSiblingCollides goes on
// reserving that title against new sessions on behalf of nothing, for good.
//
// The failure is a cancelled lifecycle context, which is the pane's own ctx — a create
// racing app shutdown. Deliberately not a missing working directory: tmux 3.6 creates the
// session anyway and only the pane's process dies, so that fixture would assert nothing
// here and would not even agree with itself across CI's tmux floor.
func TestAFailedCreateReleasesTheNameItMinted(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	inst := makeStartedInstance(t, "create-fails")
	t.Cleanup(func() { _ = inst.Kill() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tp := NewTerminalPane(ctx)
	t.Cleanup(tp.Close)
	tp.SetSize(80, 30)
	minted := inst.MintTerminalSessionName()

	_, err := tp.EnsureSession(inst, inst.Title())
	require.Error(t, err, "precondition: the create must fail")

	assert.Empty(t, inst.TerminalSessionName(),
		"a create that started no shell must not leave its name owned")
	assert.False(t, shellNamed(t, minted), "and must leave nothing on the socket")
}

// The other half of the same rollback: an install REFUSED after a successful create. The
// shell exists for the length of the round trip and is closed by the abort, so the name has
// to come back too.
//
// closeGen rather than a reap, deliberately. CloseForInstance releases the name itself, so
// driving this with one would pass whether or not the abort path releases anything; a
// whole-pane Close bumps only the epoch, leaving the abort as the only thing that could
// have given the name up.
func TestAnAbortedInstallReleasesTheNameItMinted(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	inst := makeStartedInstance(t, "install-aborts")
	t.Cleanup(func() { _ = inst.Kill() })
	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	minted := inst.MintTerminalSessionName()
	tp.beforeInstall = tp.Close

	key, err := tp.EnsureSession(inst, inst.Title())
	require.NoError(t, err)
	require.Empty(t, key, "precondition: the install must have been refused")

	assert.Empty(t, inst.TerminalSessionName(),
		"an install that was refused must not leave the shell's name owned")
	assert.False(t, shellNamed(t, minted), "and the shell it created must be closed")
}

// The symptom as the user meets it, at the render layer rather than the socket. With the
// key following the agent session, UpdateContent filed currentKey under the post-rename key,
// missed the map, and painted "Opening terminal…" over a shell that was running fine — and
// the frames still landing under the old key went into a slot nothing read. The visible
// result was a terminal that emptied itself on rename.
func TestDeepRenameKeepsThePaneRenderingTheShell(t *testing.T) {
	tp, inst, key := shellPane(t, "rename-render")

	// A frame has landed, so the pane is showing shell content rather than the
	// still-opening fallback. Without this the assertion below would be vacuous.
	showTerminal(t, tp, inst)
	require.NotContains(t, tp.String(), "Opening terminal",
		"precondition: the pane must be past the opening fallback before the rename")

	renameAndAdopt(t, inst, inst.Title()+"-renamed")
	require.NoError(t, tp.UpdateContent(inst))

	assert.NotContains(t, tp.String(), "Opening terminal",
		"the rename blanked the terminal tab: the pane lost the shell it was already showing")
	tp.mu.Lock()
	current := tp.currentKey
	tp.mu.Unlock()
	assert.Equal(t, key, current, "the pane must still be pointed at the shell it is rendering")
}
