package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolatePorts gives the test its own registry.
//
// livePorts is process-wide by design, and a test that allocates through an Instance
// leaves an owner in it — a session only gives its port back when it is killed. The
// numbers come from freePort, which draws from the ephemeral range the OS re-hands out
// freely, so one test's leftover owner can refuse the very port a later test was just
// told is free. That failed roughly one run in ten, in whichever test drew the collision.
func isolatePorts(t *testing.T) {
	t.Helper()
	restore := livePorts
	livePorts = newPortRegistry()
	t.Cleanup(func() { livePorts = restore })
}

// freePort returns a port nothing was listening on a moment ago, which is the best any
// caller can have: a port is only truly held while a socket holds it.
func freePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// synthetic points the bind probe at nothing, so a test about the RESERVATION rules
// runs against a range of numbers rather than borrowing real ports from the machine.
// Without it these tests assert on whatever else happens to be listening: the ephemeral
// range freePort draws from is exactly where a busy host keeps its short-lived sockets.
func synthetic(t *testing.T) repocfg.PortRange {
	t.Helper()
	restore := probePort
	probePort = func(int) bool { return true }
	t.Cleanup(func() { probePort = restore })
	return repocfg.PortRange{Lo: 41000, Hi: 41002}
}

func TestPortRegistry_AllocatesTheLowestFreePortInTheRange(t *testing.T) {
	rng := synthetic(t)
	r := newPortRegistry()

	got, ok := r.allocate(rng, "web")

	require.True(t, ok)
	assert.Equal(t, rng.Lo, got)
}

// The whole point of the registry: two sessions of one repo must never be handed the
// same port. A bind probe alone cannot deliver that — the first session's port is free
// until its dev server actually starts, and it may never start at all.
func TestPortRegistry_NeverHandsOutAPortAnotherSessionHolds(t *testing.T) {
	rng := synthetic(t)
	r := newPortRegistry()

	first, ok := r.allocate(rng, "web-1")
	require.True(t, ok)
	second, ok := r.allocate(rng, "web-2")
	require.True(t, ok)

	assert.NotEqual(t, first, second)
}

// And the half the registry cannot know: a port some unrelated process is listening on
// — the user's own server, or a session of a repo whose range overlaps this one. This
// is the probe's own test, so it uses a real listener and the real probe.
//
// Both addresses a dev server is reachable at, because neither probe alone answers on
// every platform: Go sets SO_REUSEADDR, which BSD reads as permission to bind the same
// port on a different local address, so on macOS a wildcard-only probe called a
// loopback server's port free — and that is a collision the user meets as "port 3000 is
// already in use" from a server Atrium told the session to run.
func TestPortRegistry_WillNotHandOutAPortSomethingIsListeningOn(t *testing.T) {
	for _, host := range []string{"127.0.0.1", ""} {
		name := host
		if name == "" {
			name = "wildcard"
		}
		t.Run(name, func(t *testing.T) {
			var lc net.ListenConfig
			l, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort(host, "0"))
			require.NoError(t, err)
			defer func() { _ = l.Close() }()
			taken := l.Addr().(*net.TCPAddr).Port
			r := newPortRegistry()

			_, ok := r.allocate(repocfg.PortRange{Lo: taken, Hi: taken}, "web")

			assert.False(t, ok, "the only port in the range is in use")
		})
	}
}

// A refused port is not burned for the life of the process: whatever was listening may
// exit, and the next session asking should get it.
func TestPortRegistry_DoesNotHoldOntoAPortItRefused(t *testing.T) {
	rng := synthetic(t)
	r := newPortRegistry()
	probePort = func(int) bool { return false }
	_, ok := r.allocate(rng, "web-1")
	require.False(t, ok)
	probePort = func(int) bool { return true }

	got, ok := r.allocate(rng, "web-2")

	require.True(t, ok)
	assert.Equal(t, rng.Lo, got)
}

// A range with nothing free is reported rather than papered over with a port outside
// it: the user asked for these ports, and a session running on 3100 when the config
// says 3000-3001 would collide with whatever really owns 3100.
func TestPortRegistry_ReportsAnExhaustedRange(t *testing.T) {
	rng := synthetic(t)
	one := repocfg.PortRange{Lo: rng.Lo, Hi: rng.Lo}
	r := newPortRegistry()
	_, ok := r.allocate(one, "web-1")
	require.True(t, ok)

	_, ok = r.allocate(one, "web-2")

	assert.False(t, ok)
}

func TestPortRegistry_ReleaseReturnsThePortToTheRange(t *testing.T) {
	rng := synthetic(t)
	one := repocfg.PortRange{Lo: rng.Lo, Hi: rng.Lo}
	r := newPortRegistry()
	first, ok := r.allocate(one, "web-1")
	require.True(t, ok)

	r.release(first, "web-1")

	second, ok := r.allocate(one, "web-2")
	require.True(t, ok, "the released port must be available again")
	assert.Equal(t, first, second)
}

// Release is by owner, so a stale release cannot free a port a different session has
// since been handed — the case a paused session's teardown racing a new session's
// create would otherwise produce.
func TestPortRegistry_ReleaseIgnoresANonOwner(t *testing.T) {
	rng := synthetic(t)
	r := newPortRegistry()
	port, ok := r.allocate(repocfg.PortRange{Lo: rng.Lo, Hi: rng.Lo}, "web-1")
	require.True(t, ok)

	r.release(port, "web-2")

	assert.False(t, r.reserve(port, "web-3"), "the port is still web-1's")
}

// Reserve is how a restored session re-claims the port it was running on before the
// TUI restarted, before any new session can be allocated one.
func TestPortRegistry_ReserveClaimsAPortForARestoredSession(t *testing.T) {
	rng := synthetic(t)
	r := newPortRegistry()

	require.True(t, r.reserve(rng.Lo, "restored"))

	got, ok := r.allocate(rng, "fresh")
	require.True(t, ok)
	assert.Equal(t, rng.Lo+1, got, "the restored session's port must not be handed out again")
	assert.False(t, r.reserve(rng.Lo, "another"), "a held port cannot be claimed twice")
}

// The port reaches the setup script as $ATRIUM_PORT, which is the form a script can use
// without interpolating anything into a shell string.
func TestRunSetupScript_ExportsTheAllocatedPortToTheScript(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{
		Name:        "web",
		SetupScript: `echo "$ATRIUM_PORT" > port.txt`,
		PortRange:   fmt.Sprintf("%d-%d", base, base),
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	require.NoError(t, inst.SetupError())
	assert.Equal(t, base, inst.Port())
	raw, err := os.ReadFile(filepath.Join(dir, "port.txt"))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(base), strings.TrimSpace(string(raw)))
}

// And as {{.Session.Port}}, the template form, which is what a command spelling
// `--port` needs.
func TestRunSetupScript_RendersThePortIntoTheTemplate(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: `echo {{.Session.Port}} > port.txt`,
		PortRange:   fmt.Sprintf("%d-%d", base, base),
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	raw, err := os.ReadFile(filepath.Join(dir, "port.txt"))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(base), strings.TrimSpace(string(raw)))
}

// The agent's own pane gets it through the same `new-session -e` channel session_env
// rides, because that is the only per-session environment tmux has.
func TestResolveSetupRun_CarriesThePortInTheSessionEnvironment(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: "true",
		PortRange:   fmt.Sprintf("%d-%d", base, base),
		SessionEnv:  map[string]string{"CACHE": "/tmp/c"},
	})
	inst := &Instance{Title: "web", Path: dir}

	run, ok := inst.resolveSetupRun(dir)

	require.True(t, ok)
	assert.Contains(t, run.sessionEnv, fmt.Sprintf("ATRIUM_PORT=%d", base))
	assert.Contains(t, run.sessionEnv, "CACHE=/tmp/c")
}

// A repo whose entry declares no range gets no port, and its session_env carries no
// ATRIUM_PORT at all — an empty one would read as "the port is unset" to a script that
// tests for it.
func TestResolveSetupRun_LeavesAPortlessRepoWithout(t *testing.T) {
	isolatePorts(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "true", SessionEnv: map[string]string{"CACHE": "/tmp/c"}})
	inst := &Instance{Title: "web", Path: dir}

	run, ok := inst.resolveSetupRun(dir)

	require.True(t, ok)
	assert.Zero(t, inst.Port())
	for _, pair := range run.sessionEnv {
		assert.NotContains(t, pair, "ATRIUM_PORT")
	}
}

// A session keeps the port it holds across a re-resolve: the dev server is bound to
// that number, and a resume that renumbered it would leave the server unreachable at
// the port the row advertises.
func TestReservePort_KeepsThePortASessionAlreadyHolds(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: "true",
		PortRange:   fmt.Sprintf("%d-%d", base, base+4),
	})
	inst := &Instance{Title: "web", Path: dir}
	_, ok := inst.resolveSetupRun(dir)
	require.True(t, ok)
	first := inst.Port()
	require.NotZero(t, first)

	_, ok = inst.resolveSetupRun(dir)

	require.True(t, ok)
	assert.Equal(t, first, inst.Port())
}

// A range with nothing free does not stop the session — it comes up without a port, and
// says why.
func TestReservePort_ReportsAnExhaustedRangeWithoutStoppingTheSession(t *testing.T) {
	isolatePorts(t)
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	taken := l.Addr().(*net.TCPAddr).Port
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: "true",
		PortRange:   fmt.Sprintf("%d-%d", taken, taken),
	})
	inst := &Instance{Title: "web", Path: dir}

	run, ok := inst.resolveSetupRun(dir)

	require.True(t, ok, "an exhausted range must not cost the session its setup script")
	assert.Zero(t, inst.Port())
	assert.Contains(t, inst.PortProblem(), "port")
	assert.NotContains(t, run.env, "ATRIUM_PORT=0", "no port is empty, never zero")
}

// Releasing returns the port to the range for the next session, which is what pause and
// kill do with it.
func TestReleasePort_ReturnsThePortToTheRange(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: "true",
		PortRange:   fmt.Sprintf("%d-%d", base, base),
	})
	first := &Instance{Title: "web-1", Path: dir}
	_, ok := first.resolveSetupRun(dir)
	require.True(t, ok)
	require.Equal(t, base, first.Port())

	first.releasePort()

	assert.Zero(t, first.Port())
	second := &Instance{Title: "web-2", Path: dir}
	_, ok = second.resolveSetupRun(dir)
	require.True(t, ok)
	assert.Equal(t, base, second.Port(), "the freed port is the next session's")
}

// A pause must NOT renumber the session, because the agent's pane cannot be
// renumbered with it: tmux freezes $ATRIUM_PORT at `new-session -e` and a resume
// re-attaches (tmux.Session.Restore is attach-session, nothing more), so the shell the
// user types in keeps the number it was born with for the life of that pane.
//
// Releasing on pause and re-allocating on resume produced exactly the collision the
// port exists to prevent: park A (holding 3000), create C — which is handed 3000,
// lowest-free — then resume A onto 3002. A's row, templates and setup script all say
// 3002 while its pane still exports 3000, so `npm run dev -- --port $ATRIUM_PORT` in A
// binds the port C's server owns.
func TestPauseResume_KeepsThePortWhileThePaneLives(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	wt := newTestWorktree(t)
	writeRepoScriptConfig(t, config.RepoScript{Name: "any", PortRange: fmt.Sprintf("%d-%d", base, base)})
	aliveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", newRecordingPtyFactory(t, nil), aliveExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}
	_, ok := inst.resolveSetupRun(wt.GetWorktreePath())
	require.True(t, ok)
	require.Equal(t, base, inst.Port())

	require.NoError(t, inst.Pause())

	assert.Equal(t, base, inst.Port(), "a parked session still owns the port its pane exports")
	other := &Instance{Title: "other", Path: wt.GetRepoPath()}
	_, ok = other.resolveSetupRun(wt.GetWorktreePath())
	require.True(t, ok)
	assert.Zero(t, other.Port(), "and no other session may be handed it")

	require.NoError(t, inst.Resume())

	assert.Equal(t, base, inst.Port(), "so a resume finds the number its pane already has")
}

// When the pane is gone the port goes with it: there is no frozen environment left to
// contradict, and the next launch is a `new-session -e` that will carry whatever it is
// given. This is the path RecoverLostSession takes when tmux died under a session.
func TestPause_ReleasesThePortWhenThePaneIsGone(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{Name: "any", PortRange: fmt.Sprintf("%d-%d", base, base)})
	goneExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return errors.New("no server running on /tmp/none") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, errors.New("no server running on /tmp/none") },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", newRecordingPtyFactory(t, nil), goneExec)
	inst := &Instance{Title: "sess", Path: dir, status: Running, started: true, direct: true, tmuxSession: ts}
	_, ok := inst.resolveSetupRun(dir)
	require.True(t, ok)
	require.Equal(t, base, inst.Port())

	_ = inst.RecoverLostSession()

	assert.Zero(t, inst.Port(), "a session with no pane holds no port")
	next := &Instance{Title: "next", Path: dir}
	_, ok = next.resolveSetupRun(dir)
	require.True(t, ok)
	assert.Equal(t, base, next.Port())
}

// Kill releases it too, so a killed session's number is immediately reusable rather
// than held by a registry entry nothing will ever clear.
func TestKill_ReleasesThePort(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{Name: "any", PortRange: fmt.Sprintf("%d-%d", base, base)})
	inst := &Instance{Title: "web", Path: dir}
	_, ok := inst.resolveSetupRun(dir)
	require.True(t, ok)
	require.Equal(t, base, inst.Port())

	require.NoError(t, inst.Kill())

	assert.Zero(t, inst.Port())
	next := &Instance{Title: "web-2", Path: dir}
	_, ok = next.resolveSetupRun(dir)
	require.True(t, ok)
	assert.Equal(t, base, next.Port())
}

// The port is persisted, because a running session's dev server is bound to it: a TUI
// restart that renumbered the session would leave the row advertising a port nothing is
// serving.
func TestInstanceData_CarriesThePortAcrossARestart(t *testing.T) {
	inst := &Instance{Title: "web", Path: t.TempDir(), direct: true}
	inst.setPort(3007)

	restored, err := FromInstanceData(context.Background(), inst.ToInstanceData(), "")

	require.NoError(t, err)
	assert.Equal(t, 3007, restored.Port())
}

// And a restored session re-claims it, so a session created later in the same run
// cannot be handed the port an already-running server is on.
func TestFromInstanceData_ReclaimsThePersistedPort(t *testing.T) {
	isolatePorts(t)
	base := freePort(t)
	dir := writeRepoScriptConfig(t, config.RepoScript{Name: "any", PortRange: fmt.Sprintf("%d-%d", base, base)})
	data := (&Instance{Title: "restored", Path: dir, direct: true, port: base}).ToInstanceData()

	_, err := FromInstanceData(context.Background(), data, "")
	require.NoError(t, err)

	fresh := &Instance{Title: "fresh", Path: dir}
	_, ok := fresh.resolveSetupRun(dir)
	require.True(t, ok)
	assert.Zero(t, fresh.Port(), "the restored session's port is not free")
	assert.NotEmpty(t, fresh.PortProblem())
}

// Two repos can hold sessions of the same name — the list scopes titles per repo group,
// not globally — and a catch-all entry gives both the same range. So the registry's
// owner key cannot be the title alone: with one, the second session's reservation reads
// as the first re-reserving its own port and both come up on it.
func TestPortRegistry_TwoReposWithTheSameSessionNameGetDifferentPorts(t *testing.T) {
	isolatePorts(t)
	rng := synthetic(t)
	one := &Instance{Title: "web", Path: "/projects/alpha"}
	two := &Instance{Title: "web", Path: "/projects/beta"}

	first, ok := livePorts.allocate(rng, one.portOwner())
	require.True(t, ok)
	t.Cleanup(func() { livePorts.release(first, one.portOwner()) })
	second, ok := livePorts.allocate(rng, two.portOwner())
	require.True(t, ok)
	t.Cleanup(func() { livePorts.release(second, two.portOwner()) })

	assert.NotEqual(t, first, second)
}
