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

// A probe that finds the session gone clears the wanted flag as well as the live one.
// Without that a crashed server leaves the session probing for it forever, and a resume
// restarting a server nobody asked to have back.
func TestApplyRunState_ADeadProbeForgetsTheServer(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())

	inst.ApplyRunState(RunState{LiveKnown: true, Live: false})

	assert.False(t, inst.RunLive())
	assert.False(t, inst.RunWanted(), "a server that died is not one to bring back")
}

// A tick that answered neither question must not erase the answers an earlier one gave.
// Both questions are skipped on most ticks — the config answer is memoized, the probe is
// only run for a session that started a server — so a zero RunState is the common case.
func TestApplyRunState_AnEmptyObservationChangesNothing(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})
	require.NoError(t, inst.StartRunCommand())
	inst.ApplyRunState(RunState{Configured: true, ConfiguredKnown: true})

	inst.ApplyRunState(RunState{})

	assert.True(t, inst.RunLive())
	assert.True(t, inst.RunConfigured())
	assert.False(t, inst.RunCommandUnavailable())
}

// The configured answer is asked once and then memoized, which is what keeps a repo with
// no repo_scripts from paying a git fork per session per tick.
func TestComputeRunState_AsksTheConfigOnceAndThenSaysNothing(t *testing.T) {
	inst, _ := runInstance(t, config.RepoScript{Name: "any", RunCommand: "serve"})

	first := inst.ComputeRunState()
	require.True(t, first.ConfiguredKnown)
	assert.True(t, first.Configured)
	inst.ApplyRunState(first)

	assert.False(t, inst.ComputeRunState().ConfiguredKnown,
		"a second tick must not re-resolve the config")
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

// The persisted half round-trips, so a restarted Atrium knows to look for the server
// that is still running on the socket.
func TestInstanceData_RoundTripsTheRunStartedFlag(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	inst, err := NewInstance(InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	inst.setRunWanted(true)

	data := inst.ToInstanceData()
	require.True(t, data.RunStarted)

	restored, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	assert.True(t, restored.RunWanted())
}
