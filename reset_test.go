//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopExec is a cmd.Executor whose every invocation succeeds with empty output,
// so tmux.CleanupSessions sees "no sessions" and no real tmux is ever touched.
func noopExec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, nil },
	}
}

// sandboxDataDir sandboxes HOME per the repo's hermetic-test convention and
// returns the resolved (created) data dir.
func sandboxDataDir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// seedInstance persists one stored (paused) instance and returns the state.json
// path.
func seedInstance(t *testing.T, dir string) string {
	t.Helper()
	data, err := json.Marshal([]session.InstanceData{{
		Title:    "seeded",
		Path:     t.TempDir(),
		Status:   session.Paused,
		Program:  "claude",
		Worktree: session.GitWorktreeData{RepoPath: t.TempDir()},
	}})
	require.NoError(t, err)
	require.NoError(t, config.LoadState().SaveInstances(data))
	return filepath.Join(dir, config.StateFileName)
}

// storedInstances re-reads state.json from disk and returns its instance list.
func storedInstances(t *testing.T) []session.InstanceData {
	t.Helper()
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(config.LoadState().GetInstances(), &data))
	return data
}

// startFakeDaemon launches a stand-in for the autoyes daemon: on SIGTERM it
// "persists its startup snapshot" by copying resurrectPath over statePath —
// mimicking RunDaemon's shutdown save — creates markerPath to prove that dying
// write ran, and exits. It records daemon.pid and deliberately no daemon.lock,
// so StopDaemon takes the legacy direct-signal path and polls liveness via
// signal 0 (hence the background reap: an unreaped zombie still answers it).
// The ready-file handshake guarantees the trap is installed before StopDaemon
// can deliver the SIGTERM.
func startFakeDaemon(t *testing.T, dir, statePath, resurrectPath, markerPath string) {
	t.Helper()
	readyPath := filepath.Join(dir, "fake-daemon-ready")
	// The `sleep 1 & wait $!` loop over a plain `sleep` matters: POSIX sh runs a
	// trap only once the current foreground command finishes, but a trapped
	// signal interrupts the wait builtin immediately.
	script := fmt.Sprintf(
		"trap 'cp %q %q; : > %q; exit 0' TERM; : > %q; while :; do sleep 1 & wait $!; done",
		resurrectPath, statePath, markerPath, readyPath,
	)
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(readyPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "fake daemon must install its trap before the test proceeds")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "daemon.pid"),
		[]byte(strconv.Itoa(cmd.Process.Pid)), 0o644))
}

// The core #265 regression: the autoyes daemon's dying state save must land
// BEFORE reset's deletion, never after it. runReset stops the daemon first, so
// the save (observed via the marker) happens and is then wiped. Under the old
// ordering (delete first, StopDaemon last) the blocking stop let that save
// deterministically resurrect every deleted instance.
func TestRunReset_DaemonDyingSaveCannotResurrectInstances(t *testing.T) {
	dir := sandboxDataDir(t)
	statePath := seedInstance(t, dir)

	// The fake daemon's dying write restores this pre-reset snapshot.
	resurrectPath := filepath.Join(dir, "resurrect.json")
	seeded, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(resurrectPath, seeded, 0o644))

	markerPath := filepath.Join(dir, "dying-save-ran")
	startFakeDaemon(t, dir, statePath, resurrectPath, markerPath)

	require.NoError(t, runReset(context.Background(), noopExec()))

	// The dying save must have actually run — otherwise this passes vacuously...
	_, statErr := os.Stat(markerPath)
	require.NoError(t, statErr, "the fake daemon's dying save never ran")
	// ...and still not survive: the daemon was stopped before the deletion.
	assert.Empty(t, storedInstances(t),
		"deleted instances must not be resurrected by the daemon's dying save")
}

// Reset under a live TUI must refuse outright and touch nothing: deleting
// sessions and worktrees under a running TUI would have its in-memory state
// re-persist every deleted instance on its next save.
func TestRunReset_RefusesWhileTUIHoldsLock(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstance(t, dir)

	lockPath, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(lockPath)
	require.NoError(t, err)

	err = runReset(context.Background(), noopExec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "close it before resetting")
	assert.Len(t, storedInstances(t), 1, "a refused reset must not touch stored instances")

	// Once the TUI is gone the same reset goes through.
	release()
	require.NoError(t, runReset(context.Background(), noopExec()))
	assert.Empty(t, storedInstances(t))
}

// A daemon that cannot be confirmed stopped must abort the reset BEFORE any
// deletion: proceeding would let a still-alive daemon's dying save rewrite
// state.json after the wipe.
func TestRunReset_AbortsWhenStopDaemonFails(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstance(t, dir)
	// An unparseable PID file makes StopDaemon error out.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "daemon.pid"), []byte("not-a-number"), 0o644))

	err := runReset(context.Background(), noopExec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing was deleted")
	assert.Len(t, storedInstances(t), 1, "an aborted reset must not delete stored instances")
}
