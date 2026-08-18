package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// The tests here are all about one question: which sessions did a load actually
// START AN AGENT for. That is what the host budget rations, and the recorded pty
// commands are the only witness to it — a status of Running is what the code claims,
// while a recorded command is what it did. The fixtures exist to make a launch succeed
// or fail on demand without a real tmux server or a real agent.
//
// The witness is `new-session`, not "the pty was used at all". Reattaching runs
// `attach-session` through the same pty factory, so a non-empty pty.cmds proves only
// that tmux was spoken to — it cannot tell a relaunch (which the budget rations) from a
// reattach (which adds no load, and which the budget must never refuse). Asserting on
// the count alone would pass while the two were confused, which is exactly the
// distinction this change turns on.

// launchedAgent reports whether pty was used to start a fresh agent — a `new-session`
// carrying the program — rather than to attach to one already running.
func launchedAgent(pty *recordingPtyFactory) bool {
	return slices.ContainsFunc(pty.commands(), func(c string) bool {
		return strings.Contains(c, "new-session")
	})
}

// softCap is the host-derived cap shape: exceeding it warns rather than blocks, and
// it is the only shape that rations recovery.
func softCap(limit int) config.SessionCap { return config.SessionCap{Limit: limit, Soft: true} }

// parkedTitles is the titles a report names, for the assertions that are about WHICH
// sessions were parked rather than about how they are identified. The pair itself is
// asserted where it matters — see TestBringOnline_ParksRecoveryPastTheHostBudget.
func parkedTitles(d DeferredRecovery) []string {
	titles := make([]string, 0, len(d.Sessions))
	for _, parked := range d.Sessions {
		titles = append(titles, parked.Title)
	}
	return titles
}

// recoverableInstance builds an instance whose tmux session is gone but whose
// worktree is real and valid, so recovery relaunches its agent and reaches Running.
// It returns the pty factory so the caller can tell a launch from a park.
//
// The exec mock keys off the pty rather than counting calls, the way the recoverInPlace
// fixtures do: `has-session` must report gone until the agent has actually been
// launched — the liveness probe asks, and so does the duplicate-name guard inside
// start(), and a later Resume asks again through that same guard — so a fixed call count
// would encode the path under test rather than the fact. Counting is what breaks when the
// path changes: Resume used to add a probe of its own here and now closes instead, which
// moves every later answer along by one. Once a pty exists the session is real and every
// command succeeds.
func recoverableInstance(t *testing.T, title string) (*Instance, *recordingPtyFactory) {
	t.Helper()
	return recoverableInstanceLaunching(t, title, nil)
}

// recoverableInstanceLaunching is recoverableInstance with control over whether the
// launch succeeds: a non-nil startErr fails every pty Start, driving the paths that
// have to survive an agent that will not come back.
func recoverableInstanceLaunching(t *testing.T, title string, startErr error) (*Instance, *recordingPtyFactory) {
	t.Helper()
	wt := newTestWorktree(t)
	cfgDir := t.TempDir()
	writeClaudeTranscript(t, cfgDir, wt.GetWorktreePath())
	pty := newRecordingPtyFactory(t, startErr)
	relaunchExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if slices.Contains(c.Args, "has-session") && len(pty.commands()) == 0 {
				return fmt.Errorf("no such session")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), title, "claude", pty, relaunchExec)
	inst := &Instance{
		Title: title, status: Running, Program: "claude",
		claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts,
	}
	return inst, pty
}

// survivingInstance builds an instance whose tmux session is alive and whose attach
// succeeds, so bringing it online reattaches and starts no agent.
func survivingInstance(t *testing.T, title string) (*Instance, *recordingPtyFactory) {
	t.Helper()
	pty := newRecordingPtyFactory(t, nil)
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil }, // has-session succeeds
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), title, "claude", pty, aliveExec)
	return &Instance{Title: title, status: Running, Program: "claude", tmuxSession: ts}, pty
}

// TestBringOnline_ParksRecoveryPastTheHostBudget is #474 itself: a reboot with more
// persisted-live sessions than the host can carry must not bring every agent back at
// once. The sessions past the budget are parked as Paused and reported, and — the part
// that makes the park recoverable rather than destructive — their worktrees are left
// materialized on disk.
func TestBringOnline_ParksRecoveryPastTheHostBudget(t *testing.T) {
	a, ptyA := recoverableInstance(t, "alpha")
	b, ptyB := recoverableInstance(t, "bravo")
	c, ptyC := recoverableInstance(t, "charlie")
	c.Path = "/repo/web"

	deferred := bringOnline([]*Instance{a, b, c}, softCap(2))

	require.Equal(t, Running, a.GetStatus(), "the first session within budget comes back")
	require.True(t, launchedAgent(ptyA), "and its agent is launched")
	require.Equal(t, Running, b.GetStatus(), "so does the second")
	require.True(t, launchedAgent(ptyB))

	require.True(t, c.Paused(), "the session past the budget is parked, not relaunched")
	require.False(t, launchedAgent(ptyC), "and NO agent is started for it — the whole point")
	require.Empty(t, ptyC.cmds, "nor is its pane touched at all")
	require.True(t, c.started, "a parked session is still started, so its row renders")

	// Reported as the (Title, Path) pair, not a bare title: the report can outlive the
	// process that made it (internal/parkreport), and a reader reconciling it against a
	// later fleet has to know which row it names — a title is unique only within a repo
	// group.
	require.Equal(t, []ParkedSession{{Title: "charlie", Path: "/repo/web"}}, deferred.Sessions,
		"the park is reported, not silent, and identified the way storage matches instances")
	require.Equal(t, 2, deferred.Limit, "in the number the loader actually applied")

	// The park must be a bare status flip: routing it through pause() would commit the
	// worktree's uncommitted work and remove it, and Resume only reuses a worktree that
	// is still valid on disk.
	valid, err := c.worktree().IsValidWorktree()
	require.NoError(t, err)
	require.True(t, valid, "a parked session keeps its worktree, so Resume reuses it rather than re-adding it")
}

// TestBringOnline_SurvivorsReserveTheBudgetBeforeRelaunches is the ordering guard, and
// the reason the budget is two-phase rather than one counter walked in stored order.
//
// A surviving session cannot be refused — its agent is already running — so it has to
// be counted before the first relaunch is granted. Spend in stored order instead and
// the survivor at the end of the list gets reattached anyway, on top of a budget two
// dead sessions have already emptied: three live agents on a host budget of two,
// which is the bug this whole change exists to prevent.
func TestBringOnline_SurvivorsReserveTheBudgetBeforeRelaunches(t *testing.T) {
	a, ptyA := recoverableInstance(t, "alpha")
	b, ptyB := recoverableInstance(t, "bravo")
	c, ptyC := survivingInstance(t, "charlie")

	deferred := bringOnline([]*Instance{a, b, c}, softCap(2))

	require.Equal(t, Running, c.GetStatus(), "the survivor is reattached — it is already running")
	require.False(t, launchedAgent(ptyC), "reattaching launches no agent")

	require.Equal(t, Running, a.GetStatus(), "one relaunch fits beside the survivor")
	require.True(t, launchedAgent(ptyA))

	require.True(t, b.Paused(), "the second relaunch does not: the survivor already took a slot")
	require.False(t, launchedAgent(ptyB), "a single-pass budget would have launched this agent")
	require.Equal(t, []string{"bravo"}, parkedTitles(deferred))
}

// TestBringOnline_SurvivingFleetIsNeverParked pins the case that must stay silent: a
// TUI restart where every tmux session survived adds no load at all, however far past
// the budget the fleet is, because nothing is being started. Refusing here would park
// sessions whose agents are running — and killing a working agent to satisfy a cap is
// not a trade Atrium makes.
func TestBringOnline_SurvivingFleetIsNeverParked(t *testing.T) {
	a, ptyA := survivingInstance(t, "alpha")
	b, ptyB := survivingInstance(t, "bravo")
	c, ptyC := survivingInstance(t, "charlie")

	deferred := bringOnline([]*Instance{a, b, c}, softCap(1))

	for _, inst := range []*Instance{a, b, c} {
		require.Equal(t, Running, inst.GetStatus(), "every survivor stays live")
	}
	for _, pty := range []*recordingPtyFactory{ptyA, ptyB, ptyC} {
		require.False(t, launchedAgent(pty), "and none of them launches an agent")
	}
	require.Empty(t, deferred.Sessions, "so there is nothing to report and no toast to show")
}

// TestBringOnline_PausedSessionsCostNothing asserts a stored-Paused session neither
// consumes a slot nor is counted as a park. It has no agent and no live pane, so it
// imposes no load — and reporting it would tell the user Atrium had just parked
// something it found already parked.
func TestBringOnline_PausedSessionsCostNothing(t *testing.T) {
	parked := &Instance{
		Title: "already-paused", status: Paused, Program: "claude",
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "already-paused", "claude",
			newRecordingPtyFactory(t, nil), deadExec()),
	}
	live, ptyLive := recoverableInstance(t, "alpha")

	deferred := bringOnline([]*Instance{parked, live}, softCap(1))

	require.True(t, parked.Paused(), "a stored-Paused session stays paused")
	require.True(t, parked.started, "and is marked started")
	require.Equal(t, Running, live.GetStatus(), "the paused row did not spend the only slot")
	require.True(t, launchedAgent(ptyLive))
	require.Empty(t, deferred.Sessions, "and nothing is reported as newly parked")
}

// TestBringOnline_FailedRecoveryReturnsItsSlot asserts a relaunch that fails hands its
// slot back. A session that degraded to Paused never became host load, so parking the
// next candidate behind it would refuse a relaunch the host can actually afford.
func TestBringOnline_FailedRecoveryReturnsItsSlot(t *testing.T) {
	// An orphaned worktree cannot be recovered, so this one ends Paused having
	// launched nothing.
	doomed, ptyDoomed := orphanedWorktreeInstance(t)
	healthy, ptyHealthy := recoverableInstance(t, "healthy")

	deferred := bringOnline([]*Instance{doomed, healthy}, softCap(1))

	require.True(t, doomed.Paused(), "an orphaned worktree still degrades to Paused")
	require.False(t, launchedAgent(ptyDoomed), "having started nothing")
	require.Equal(t, Running, healthy.GetStatus(), "so the slot it took is available again")
	require.True(t, launchedAgent(ptyHealthy))
	require.Empty(t, deferred.Sessions, "and the healthy session is not reported as parked")
}

// TestBringOnline_ExplicitMaxSessionsDoesNotRation is the answer to whether a hard cap
// participates: it does not.
//
// An explicit positive max_sessions is a hard cap over EVERY session, paused ones
// included (capCount measures it with NumInstances), and recovery only flips Paused to
// Running — so it changes no total and cannot cross that cap. The one state where a
// hard-cap gate could bite is a max_sessions lowered under an existing fleet, and
// parking there would strand work the user's own setting says is allowed. An explicit
// non-positive value is the documented escape hatch.
func TestBringOnline_ExplicitMaxSessionsDoesNotRation(t *testing.T) {
	for _, tc := range []struct {
		name string
		sc   config.SessionCap
	}{
		{"a hard cap below the fleet", config.SessionCap{Limit: 1, Soft: false}},
		{"the explicit-unlimited escape hatch", config.SessionCap{Limit: 0, Soft: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ptyA := recoverableInstance(t, "alpha")
			b, ptyB := recoverableInstance(t, "bravo")

			deferred := bringOnline([]*Instance{a, b}, tc.sc)

			require.Equal(t, Running, a.GetStatus())
			require.True(t, launchedAgent(ptyA))
			require.Equal(t, Running, b.GetStatus(), "an explicit cap does not park recovery")
			require.True(t, launchedAgent(ptyB))
			require.Empty(t, deferred.Sessions)
		})
	}
}

// TestParkedOverflowResumesWithoutLosingUncommittedWork is the data-safety guard, and
// the one claim in this change that would be a bug if it were wrong.
//
// Parking as Paused is only safe because the park leaves the worktree materialized and
// Resume reuses a materialized worktree as-is. Route the park through pause() instead —
// which commits dirty work and runs `git worktree remove` — or let Resume take its
// Setup branch, and the uncommitted file below is gone: Setup clears the stale worktree
// and re-adds it from the branch.
func TestParkedOverflowResumesWithoutLosingUncommittedWork(t *testing.T) {
	a, _ := recoverableInstance(t, "alpha")
	parked, ptyParked := recoverableInstance(t, "bravo")

	// Uncommitted work in the session that is about to be parked.
	wip := filepath.Join(parked.worktree().GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wip, []byte("half-finished\n"), 0o644))

	require.Equal(t, []string{"bravo"}, parkedTitles(bringOnline([]*Instance{a, parked}, softCap(1))))
	require.True(t, parked.Paused())

	// The park itself must not have touched the file — a pause()-routed park would
	// have committed and removed it here, before Resume ever ran.
	onDisk, err := os.ReadFile(wip)
	require.NoError(t, err, "the park must leave the worktree materialized")
	require.Equal(t, "half-finished\n", string(onDisk))

	require.NoError(t, parked.Resume(), "the user's way back")

	require.Equal(t, Running, parked.GetStatus(), "resume brings the agent back")
	require.True(t, launchedAgent(ptyParked), "relaunching it, since its pane died with the server")
	onDisk, err = os.ReadFile(wip)
	require.NoError(t, err, "resume must not have re-added the worktree from the branch")
	require.Equal(t, "half-finished\n", string(onDisk), "the uncommitted work survives the round trip")

	// And it is still uncommitted, not folded into a commit behind the user's back.
	dirty, err := parked.worktree().IsDirty()
	require.NoError(t, err)
	require.True(t, dirty, "the park committed nothing, so the work is still the user's to commit")
}

// TestNewRecoveryBudget_OnlyTheSoftCapRations pins the resolution directly, since it is
// what decides whether any of the above runs at all: nil means an unrationed load.
func TestNewRecoveryBudget_OnlyTheSoftCapRations(t *testing.T) {
	require.NotNil(t, newRecoveryBudget(config.SessionCap{Limit: 2, Soft: true}),
		"the host-derived soft cap rations recovery")
	require.Nil(t, newRecoveryBudget(config.SessionCap{Limit: 2, Soft: false}),
		"an explicit hard cap does not")
	require.Nil(t, newRecoveryBudget(config.SessionCap{Limit: 0, Soft: false}),
		"nor does the explicit-unlimited escape hatch")
	require.Nil(t, newRecoveryBudget(config.SessionCap{Limit: 0, Soft: true}),
		"nor a soft cap of zero, which is no cap at all")
}

// A nil budget is how an unrationed load is spelled, so every operation on it has to be
// a no-op that grants everything — otherwise the explicit-max_sessions path panics on
// the first session it meets.
func TestNilRecoveryBudgetGrantsEverything(t *testing.T) {
	var b *recoveryBudget
	require.True(t, b.spend(&Instance{Title: "anything"}))
	b.reserve()
	b.refund()
	require.Equal(t, DeferredRecovery{}, b.result())
}

// TestParkedOverflowSurvivesAFailedResume is the counterpart to the test above, and it
// covers the half that matters more: what happens when the way back FAILS.
//
// Leaving the worktree materialized is what makes the park safe, so nothing downstream
// may treat it as scratch. Resume reaches recreateSession for a budget-parked session
// every time — its tmux session is gone by construction, which is why it was being
// recovered at all — and recreateSession's failure path tears the worktree down through
// Worktree.Cleanup: `git worktree remove -f` AND `git branch -D`. That teardown is a
// rollback of the Setup a normal resume just ran, where the worktree came from the
// branch and nothing is lost. Here Resume materialized nothing, so the same teardown
// would destroy uncommitted work it did not create, and delete the branch holding the
// session's history — turning a load-shedding measure into the most destructive path in
// the program.
func TestParkedOverflowSurvivesAFailedResume(t *testing.T) {
	// A survivor reserves the only slot without launching anything, so the second
	// session is parked for want of budget.
	holder, _ := survivingInstance(t, "holder")
	parked, _ := recoverableInstanceLaunching(t, "bravo", fmt.Errorf("pty boom"))

	wt := parked.worktree()
	wip := filepath.Join(wt.GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wip, []byte("half-finished\n"), 0o644))
	branch := wt.GetBranchName()
	repo := wt.GetRepoPath()

	require.Equal(t, []string{"bravo"}, parkedTitles(bringOnline([]*Instance{holder, parked}, softCap(1))))
	require.True(t, parked.Paused())

	require.Error(t, parked.Resume(), "the agent cannot be relaunched in this fixture")

	valid, err := wt.IsValidWorktree()
	require.NoError(t, err)
	require.True(t, valid, "a failed resume must not remove a worktree it did not materialize")
	onDisk, err := os.ReadFile(wip)
	require.NoError(t, err, "the uncommitted work must still be there to retry or rescue")
	require.Equal(t, "half-finished\n", string(onDisk))
	require.Equal(t, branch, gitOutput(t, repo, "rev-parse", "--abbrev-ref", branch),
		"and the branch must survive: Cleanup would have branch -D'd the session's history")
}
