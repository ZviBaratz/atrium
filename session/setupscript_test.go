package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSetupExec swaps the exec seam and returns the restore func. It exists so a
// test can assert that a refused entry spawns NO PROCESS: a gate that only
// suppressed the recorded failure would satisfy every assertion about what the row
// says while still running the script.
func stubSetupExec(fn func(context.Context, setupRun) (string, error)) func() {
	prev := execSetup
	execSetup = fn
	return func() { execSetup = prev }
}

// realPath resolves every symlink in p, so a path Go reported and a path a shell
// reported can be compared on a platform where the two spell the same directory
// differently.
func realPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return resolved
}

// writeRepoScriptConfig installs a config.json holding one catch-all repo_scripts
// entry and returns the worktree directory the script should run in.
func writeRepoScriptConfig(t *testing.T, entry config.RepoScript) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))
	cfg := config.DefaultConfig()
	cfg.RepoScripts = []config.RepoScript{entry}
	require.NoError(t, config.SaveConfig(cfg))
	return t.TempDir()
}

func TestRunSetupScript_RunsTheScriptInTheWorktree(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{Name: "any", SetupScript: "pwd > ran.txt"})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	got, err := os.ReadFile(filepath.Join(dir, "ran.txt"))
	require.NoError(t, err, "the script must run with the worktree as its working directory")
	// Compared through EvalSymlinks on both sides. On macOS t.TempDir() hands back a
	// path under /var, which is a symlink to /private/var, and `pwd` reports the
	// resolved one — so a literal comparison fails there and only there, for a reason
	// that has nothing to do with which directory the script ran in.
	assert.Equal(t, realPath(t, dir), realPath(t, strings.TrimSpace(string(got))))
	assert.NoError(t, inst.SetupError())
}

// The $ATRIUM_* set is the same one custom commands get, so a user need never
// interpolate a path into a shell string, and session_env rides alongside it.
func TestRunSetupScript_ExportsTheSessionEnvironment(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: `printf '%s\n%s\n' "$ATRIUM_WORKTREE" "$CACHE_DIR" > env.txt`,
		SessionEnv:  map[string]string{"CACHE_DIR": "/tmp/cache-{{.Session.Title}}"},
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	got, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	require.NoError(t, err)
	assert.Equal(t, []string{dir, "/tmp/cache-web"}, strings.Fields(string(got)))
}

// A failing script is recorded, never fatal: Start's error path tears the whole
// session down, so a script that legitimately exits non-zero must not reach it.
func TestRunSetupScript_FailureIsRecordedWithItsStderr(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{
		Name:        "web",
		SetupScript: "echo boom >&2; exit 3",
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	err := inst.SetupError()
	require.Error(t, err)
	assert.Contains(t, inst.SetupOutput(), "boom", "the stderr the user needs must be kept")
	assert.Empty(t, inst.SetupPhase(), "the phase clears whether the script passed or failed")
}

// A repo with nothing configured behaves exactly as it did before the feature
// existed — no process, no recorded failure.
func TestRunSetupScript_UnconfiguredRepoRunsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))
	require.NoError(t, config.SaveConfig(config.DefaultConfig()))
	dir := t.TempDir()

	var ran bool
	restore := stubSetupExec(func(context.Context, setupRun) (string, error) { ran = true; return "", nil })
	defer restore()

	(&Instance{Title: "web", Path: dir}).RunSetupScript(dir)

	assert.False(t, ran, "an unconfigured repo must not spawn a process")
}

// An entry that routes to a different repo does not claim this one.
func TestRunSetupScript_NonMatchingEntryRunsNothing(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{
		Name:          "other",
		RemoteMatches: []string{"someone-else/thing"},
		SetupScript:   "touch ran.txt",
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	_, err := os.Stat(filepath.Join(dir, "ran.txt"))
	assert.True(t, os.IsNotExist(err), "an entry whose route rules miss must not run")
}

// An entry the validator refuses is dropped, not run: a template with a typo would
// otherwise hand a shell a command with a hole in it.
func TestRunSetupScript_InvalidEntryRunsNothing(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "npm ci {{.Session.Wortree}}"})
	inst := &Instance{Title: "web", Path: dir}

	var ran bool
	restore := stubSetupExec(func(context.Context, setupRun) (string, error) { ran = true; return "", nil })
	defer restore()

	inst.RunSetupScript(dir)

	assert.False(t, ran)
}

// The phase is what the row shows while the script runs, so it has to be set BEFORE
// the process starts and cleared after it — asserted from inside the exec seam,
// because "during" is otherwise unobservable without a race.
func TestRunSetupScript_PhaseIsSetWhileRunningAndClearedAfter(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "true"})
	inst := &Instance{Title: "web", Path: dir}

	var during string
	restore := stubSetupExec(func(context.Context, setupRun) (string, error) {
		during = inst.SetupPhase()
		return "", nil
	})
	defer restore()

	assert.Empty(t, inst.SetupPhase(), "nothing is running before the call")
	inst.RunSetupScript(dir)

	assert.NotEmpty(t, during, "the row has nothing to say while the script runs")
	assert.Empty(t, inst.SetupPhase())
}

// Every run starts from a clean slate: a failure recorded by the run before a
// pause must not still be showing after a resume whose script succeeded.
func TestRunSetupScript_ClearsAPreviousFailure(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "true"})
	inst := &Instance{Title: "web", Path: dir}
	inst.setSetupResult("stale output", errors.New("stale failure"))

	inst.RunSetupScript(dir)

	assert.NoError(t, inst.SetupError())
	assert.Empty(t, inst.SetupOutput())
}

// The recorded output is bounded: a setup script can stream for minutes, and the
// tail is the part that says why it failed.
func TestRunSetupScript_BoundsTheRecordedOutput(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: "for i in $(seq 1 20000); do echo pad-line-$i; done; exit 1",
	})
	inst := &Instance{Title: "web", Path: dir}

	inst.RunSetupScript(dir)

	require.Error(t, inst.SetupError())
	assert.LessOrEqual(t, len(inst.SetupOutput()), setupOutputCap)
	assert.Contains(t, inst.SetupOutput(), "pad-line-20000", "the tail is what is kept")
}

// A setup script has no timeout — `npm ci` on a cold cache legitimately runs for
// minutes — which puts an unbounded wait inside Start's goroutine, the one shutdown
// reconciliation drains before it can adopt or tear down a session that was still
// Loading. AbortSetupScript is how that drain stays bounded: on the force-quit path
// the lifecycle context is still live, so cancelling it is the only thing that ends
// the script.
func TestAbortSetupScript_EndsARunningScript(t *testing.T) {
	// `exec` so the shell BECOMES sleep: a shell that forks leaves the grandchild
	// holding the output pipe, and Run then blocks for the full sleep anyway.
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "exec sleep 60 >/dev/null 2>&1"})
	inst := &Instance{Title: "web", Path: dir}

	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.RunSetupScript(dir)
	}()
	require.Eventually(t, func() bool { return inst.SetupPhase() != "" }, 5*time.Second, 5*time.Millisecond)

	inst.AbortSetupScript()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("AbortSetupScript did not end the script")
	}
	assert.Empty(t, inst.SetupPhase())
}

// Aborting when nothing is running is a no-op, because the shutdown path calls it for
// every Loading session without knowing which of them has a script.
func TestAbortSetupScript_IsSafeWhenNothingIsRunning(t *testing.T) {
	assert.NotPanics(t, func() { (&Instance{Title: "web"}).AbortSetupScript() })
}
