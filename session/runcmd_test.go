package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubRunSessions points newRunSession at a fake tmux server for the duration of a test
// and returns it, so a test can assert what the run command DID to a tmux session — a
// new-session here, a kill-session there — with no server to do it to.
//
// It reuses the fakeTmux from the setup-script lifecycle tests, which is the same shape
// of stub: new-session arrives through the pty factory, and the existence poll start()
// then blocks on arrives through the command executor, so the two halves have to agree.
func stubRunSessions(t *testing.T) *fakeTmux {
	t.Helper()
	// The settle wait is real time on a path every test here takes, and the fake server
	// answers instantly, so there is nothing for it to wait for.
	// TestStartRunCommand_RefusesACommandThatExitsImmediately drives the check itself.
	prevDelay := runSettleDelay
	runSettleDelay = 0
	t.Cleanup(func() { runSettleDelay = prevDelay })

	fake := newFakeTmux(t, "")
	prev := newRunSession
	newRunSession = func(ctx context.Context, sessionName, windowName, program string) *tmux.Session {
		return tmux.NewSessionWithNameAndDeps(ctx, sessionName, windowName, program, fake, fake.exec())
	}
	t.Cleanup(func() { newRunSession = prev })
	return fake
}

// runInstance is a started git session whose repo declares a run command, ready to have
// one started against the stubbed tmux above.
func runInstance(t *testing.T, entry config.RepoScript) (*Instance, *fakeTmux) {
	t.Helper()
	wt := newTestWorktree(t) // sandboxes HOME and builds a repo with one commit
	installRepoScript(t, entry)
	fake := stubRunSessions(t)
	agent := newFakeTmux(t, "")
	ts := tmux.NewSessionWithNameAndDeps(context.Background(), "runner", "runner", "claude", agent, agent.exec())
	inst := &Instance{
		Title: "runner", status: Running, started: true,
		gitWorktree: wt, tmuxSession: ts, tmuxName: "runner",
	}
	// The port is allocated where the worktree is materialized, which in production is
	// Start/Resume and here is this call. StartRunCommand deliberately does NOT reserve
	// one — it reads the config, it does not reconcile port state — so a fixture that
	// skipped this would be testing a session that production never produces.
	inst.RunSetupScript(inst.WorkingDir())
	t.Cleanup(func() { inst.releasePort() })
	return inst, fake
}

func TestStartRunCommand_LaunchesTheSiblingSession(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})

	require.NoError(t, inst.StartRunCommand())

	assert.True(t, inst.RunLive())
	assert.True(t, inst.RunWanted())
	argv := fake.newSessionArgv(t)
	assert.Contains(t, argv, "runner_run",
		"the run command must live in the reserved sibling name, never in the agent's own session")
	assert.Contains(t, argv, "serve", "the rendered run_command is what tmux launches")
}

// The port and session_env reach the server's own environment, not only the template
// that renders its command line — a dev server that reads $PORT is at least as common as
// one that takes a flag.
func TestStartRunCommand_InjectsTheSessionEnvironment(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{
		Name:       "any",
		RunCommand: "serve --port {{.Session.Port}}",
		PortRange:  "34000-34099",
		SessionEnv: map[string]string{"STACK": "web"},
	})

	require.NoError(t, inst.StartRunCommand())
	require.NotZero(t, inst.Port(), "the fixture must have been handed a port")

	argv := fake.newSessionArgv(t)
	assert.Contains(t, argv, "ATRIUM_PORT="+inst.PortText())
	assert.Contains(t, argv, "STACK=web")
	assert.Contains(t, argv, "serve --port "+inst.PortText(),
		"the same number reaches the command line and the environment")
}

// A command that cannot run at all still gets a tmux session created for it, and the
// existence poll inside Start catches that session alive a moment before the shell exits
// 127. Reporting it as running is a lie the row then quietly corrects a tick later — the
// worst of both — so the start looks again after a beat and reports what it finds.
//
// Found by driving the real app, not by reading the code: `tmux new-session` succeeding
// for a command that does not exist is not what the call reads like.
func TestStartRunCommand_RefusesACommandThatExitsImmediately(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", RunCommand: "not-a-real-binary"})
	// The shell exits between Start's own existence poll and the settle check.
	fake.dieOnAttach()

	err := inst.StartRunCommand()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited immediately")
	assert.Contains(t, err.Error(), "not-a-real-binary", "the report quotes what was run")
	assert.False(t, inst.RunLive(), "and must not claim a server that is not there")
	assert.False(t, inst.RunWanted(), "nor arm a resume to bring one back")
}

// The `_run` name is reserved against titles created or renamed from now on, but a fleet
// that predates the feature can already hold a session whose own qualified tmux name IS
// `<us>_run` — a session titled "foo.run" sanitizes to exactly that. Nothing this session
// has started may be assumed to live there, so a teardown must not touch it and a start
// must refuse rather than adopt it.
func TestRunCommand_NeverTouchesASessionItDoesNotOwn(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.Empty(t, inst.RunSessionName(), "this session has never hosted one")
	// A stranger is already sitting on the name a first start would mint.
	fake.adopt()

	// Teardown leaves it alone: there is nothing owned to tear down.
	require.NoError(t, inst.StopRunCommand())
	assert.True(t, fake.sessionExists(), "another session's agent must survive our teardown")
	require.NoError(t, inst.Kill())
	assert.True(t, fake.sessionExists(), "a kill must not take a session it never created")

	// And a start refuses rather than attaching to it.
	err := inst.StartRunCommand()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.False(t, inst.RunLive())
}

// The run session's name is OWNED, not derived, so a deep rename cannot strand it. A
// derived name stopped resolving the moment the agent session was renamed, leaving the
// dev server running, unkillable, and still bound to a port the next session was handed.
func TestRunCommand_SurvivesARenameOfTheAgentSession(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())
	owned := inst.RunSessionName()
	require.Equal(t, "runner_run", owned)

	// What AdoptRename does to the identity: a new title and a new tmux name.
	inst.AdoptRename(RenamedIdentity{Title: "webui", Branch: inst.Branch, TmuxName: "webui"})

	assert.Equal(t, owned, inst.RunSessionName(),
		"the server keeps the name it was started under, not one derived from the new title")
	require.NoError(t, inst.StopRunCommand())
	assert.False(t, fake.sessionExists(), "and is therefore still reachable by the teardown")
}

// A repo that declares no run command refuses by name, rather than launching a session
// running the empty string.
func TestStartRunCommand_RefusesAnUnconfiguredRepo(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", SetupScript: "true"})

	err := inst.StartRunCommand()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no run_command")
	assert.False(t, fake.launched, "a refused start must spawn nothing")
	assert.False(t, inst.RunWanted())
}

// Pause removes the worktree, which IS the run command's working directory, so the
// server has to stop with it — but the fact that the user wanted one has to survive, or
// the resume below has nothing to act on.
func TestPause_StopsTheRunCommandButRemembersIt(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())
	wtPath := inst.WorkingDir()

	require.NoError(t, inst.Pause())

	require.NoDirExists(t, wtPath, "pause must remove the worktree for this test to mean anything")
	assert.False(t, inst.RunLive(), "the server is gone with its directory")
	assert.True(t, inst.RunWanted(), "and resume has to know it was there")
}

// The other half: a resume brings the worktree back, so it brings the server back too —
// on the port the pause kept, which is the whole reason the port is not released there.
func TestResume_RestartsTheRunCommandThePauseStopped(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{
		Name: "any", RunCommand: "serve", PortRange: "34100-34199",
	})
	require.NoError(t, inst.StartRunCommand())
	port := inst.Port()
	require.NotZero(t, port)
	require.NoError(t, inst.Pause())
	fake.reset()

	require.NoError(t, inst.Resume())

	assert.True(t, inst.RunLive())
	assert.Contains(t, fake.newSessionArgv(t), "runner_run", "the resume relaunches it")
	assert.Equal(t, port, inst.Port(), "and on the number it was born with, not a new one")
}

// A user who stops the server has stopped it: the wanted flag goes, so the next
// pause/resume round trip does not bring back something they turned off.
func TestStopRunCommand_ClearsTheWantedFlag(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())

	require.NoError(t, inst.StopRunCommand())

	assert.False(t, inst.RunLive())
	assert.False(t, inst.RunWanted())
}

// Kill takes the sibling session with it. Here rather than in the app layer so every
// retire path is covered by construction — this is the assertion that says so.
func TestKill_ClosesTheRunSession(t *testing.T) {
	inst, fake := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())
	require.True(t, fake.sessionExists())

	require.NoError(t, inst.Kill())

	assert.False(t, fake.sessionExists(), "the dev server must not outlive the session it belongs to")
}

// A probe that finds the session gone clears the LIVE flag and nothing else. The wanted
// flag is the user's intent — they pressed the key — and a probe is in no position to
// revoke it: pause stops the server seconds before it flips the session to Paused, so a
// probe landing in that window used to convert "restart this on resume" into "never
// wanted", and the resume brought nothing back.
func TestApplyRunState_ADeadProbeClearsLivenessButKeepsIntent(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())

	dead := inst.ComputeRunState()
	dead.Live, dead.LiveKnown = false, true
	inst.ApplyRunState(dead)

	assert.False(t, inst.RunLive())
	assert.True(t, inst.RunWanted(), "the user asked for a server; only they unask")
}

// A tick that answered neither question must not erase the answers an earlier one gave.
// A light tick polls only the selected session, so a zero RunState is the common case.
func TestApplyRunState_AnEmptyObservationChangesNothing(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())
	inst.ApplyRunState(inst.ComputeRunState())

	inst.ApplyRunState(RunState{})

	assert.True(t, inst.RunLive())
	assert.True(t, inst.RunConfigured())
	assert.False(t, inst.RunCommandUnavailable())
}

// An observation taken before a local write is discarded whole. The metadata sweep waits
// on its slowest member, so a batch can land long after the keypress it predates —
// re-asserting a server the user has just stopped, permanently, because the probe that
// would correct it only runs while the session wants one.
func TestApplyRunState_DiscardsAnObservationOlderThanTheLastWrite(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())

	// Taken while the server was up, and still in flight when the user pressed stop.
	inFlight := inst.ComputeRunState()
	require.True(t, inFlight.Live)
	require.NoError(t, inst.StopRunCommand())

	inst.ApplyRunState(inFlight)

	assert.False(t, inst.RunLive(), "a stale batch must not resurrect a stopped server")
	assert.False(t, inst.RunWanted())
}

// The configured answer is re-resolved on every observation, so a config edit that adds a
// run_command reaches a running session. Memoizing it for the process left the `d` key
// permanently refusing for any session whose first tick answered "no" — including one
// whose first tick simply raced a config save.
func TestComputeRunState_ReResolvesTheConfigSoAnEditLands(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", SetupScript: "true"})

	first := inst.ComputeRunState()
	require.True(t, first.ConfiguredKnown)
	require.False(t, first.Configured)
	inst.ApplyRunState(first)
	require.True(t, inst.RunCommandUnavailable())

	installRepoScript(t, config.RepoScript{Name: "any", RunCommand: "serve"})

	second := inst.ComputeRunState()
	require.True(t, second.ConfiguredKnown, "a later tick must ask again")
	assert.True(t, second.Configured)
	inst.ApplyRunState(second)
	assert.False(t, inst.RunCommandUnavailable(), "the key must come back to life without a restart")
}

// RunCommandUnavailable is "known to have none", not "not known to have one". The
// palette dims the action on it and the key refuses on it, so a session nobody has
// polled yet must read as available — it resolves its own config on the way through.
func TestRunCommandUnavailable_IsSilentUntilTheConfigHasBeenRead(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", SetupScript: "true"})

	assert.False(t, inst.RunCommandUnavailable(), "nothing has looked yet")

	inst.ApplyRunState(inst.ComputeRunState())

	assert.True(t, inst.RunCommandUnavailable())
	assert.False(t, inst.RunConfigured())
}

// Both persisted halves round-trip: the flag, so a restarted Atrium knows to look for a
// server still on the socket, and the NAME, so it looks under the one the server was
// actually started with rather than one re-derived from a title that may have changed.
func TestInstanceData_RoundTripsTheRunCommandState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	inst, err := NewInstance(InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	inst.mu.Lock()
	inst.runWanted, inst.runName = true, "atrium_grp_web_run"
	inst.mu.Unlock()

	data := inst.ToInstanceData()
	require.True(t, data.RunStarted)
	require.Equal(t, "atrium_grp_web_run", data.RunSession)

	restored, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	assert.True(t, restored.RunWanted())
	assert.Equal(t, "atrium_grp_web_run", restored.RunSessionName())
}
