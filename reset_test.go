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
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
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

// gitRepoWithRetainedBranch builds a repo holding a killed session's commits under
// a retention ref, and returns the journal entry naming it.
func gitRepoWithRetainedBranch(t *testing.T, title string) (undo.Entry, string) {
	t.Helper()
	run := func(dir string, args ...string) string {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(t.TempDir(), "repo")
	run(t.TempDir(), "init", repo)
	run(repo, "config", "user.email", "test@example.com")
	run(repo, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	run(repo, "add", ".")
	run(repo, "commit", "-m", "initial")
	head := run(repo, "rev-parse", "HEAD")

	entry, err := undo.Write(undo.Entry{
		Title: title, Display: title, Path: repo, RepoPath: repo,
		Branch: "zvi/" + title, SHA: head,
		Snapshot: []byte(`{"title":"` + title + `"}`),
	})
	require.NoError(t, err)
	run(repo, "update-ref", entry.Ref, head)
	return entry, repo
}

// TestRunReset_ReleasesRetainedBranches. Nothing else in reset can reach these:
// CleanupWorktrees enumerates branches from `git worktree list`, which cannot see a
// ref outside refs/heads. Left behind, they would keep every killed session's
// objects alive in the user's repositories forever, gc-immune and with no record
// left to expire them — a permanent leak caused by the command whose entire job is
// cleanup.
func TestRunReset_ReleasesRetainedBranches(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstance(t, dir)
	entry, repo := gitRepoWithRetainedBranch(t, "fix-auth")

	require.NoError(t, runReset(context.Background(), noopExec()))

	_, ok := git.RefExists(context.Background(), repo, entry.Ref)
	assert.False(t, ok, "reset must release the retained branch")
	stored, err := undo.Load()
	require.NoError(t, err)
	assert.Empty(t, stored, "and clear the journal that named it")
}

// TestRunReset_SurvivesARepositoryThatMoved — the retained refs live in the user's
// projects, which reset does not own. A project that has since been renamed or
// deleted must not fail a reset whose real work is already done.
func TestRunReset_SurvivesARepositoryThatMoved(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstance(t, dir)
	_, repo := gitRepoWithRetainedBranch(t, "fix-auth")
	require.NoError(t, os.RemoveAll(repo))

	require.NoError(t, runReset(context.Background(), noopExec()))

	stored, err := undo.Load()
	require.NoError(t, err)
	assert.Empty(t, stored, "the record goes even when its repository cannot be reached")
}

// TestRunReset_ClearsQueuedCreateRequests: a queued `atrium new` request is the one
// piece of state that can *create* after the wipe. Everything else reset does is
// ordered so nothing survives to re-persist a deleted session; a create request
// defeats that from outside the lock by design, and with no session left to collide
// with, every gate it meets on the next launch passes. So the next TUI would silently
// build a worktree, a branch and an agent from a request made before the reset.
func TestRunReset_ClearsQueuedCreateRequests(t *testing.T) {
	sandboxDataDir(t)
	create, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NoError(t, err)
	msg, err := outbox.Write(outbox.Message{Title: "s", Path: t.TempDir(), Text: "hi"})
	require.NoError(t, err)

	require.NoError(t, runReset(context.Background(), noopExec()))

	assert.NoFileExists(t, create, "a queued create must not outlive the state it would rebuild")
	assert.NoFileExists(t, msg)
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestRunReset_RefusedResetLeavesTheSpoolAlone: the refusal is "nothing was deleted",
// and the spool is state like any other. A reset that bailed on a live TUI but had
// already eaten a queued request would lose work the user never asked it to touch.
func TestRunReset_RefusedResetLeavesTheSpoolAlone(t *testing.T) {
	sandboxDataDir(t)
	create, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NoError(t, err)

	lockPath, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(lockPath)
	require.NoError(t, err)
	defer release()

	require.Error(t, runReset(context.Background(), noopExec()))
	assert.FileExists(t, create, "a refused reset must not touch the spool either")
}
