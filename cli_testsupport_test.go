package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// sandboxDataDir sandboxes HOME per the repo's hermetic-test convention and
// returns the resolved (created) data dir. It lives in this untagged file rather
// than in reset_test.go (which is !windows) so every CLI test can use it.
func sandboxDataDir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// storedInstances re-reads state.json from disk and returns its instance list.
func storedInstances(t *testing.T) []session.InstanceData {
	t.Helper()
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(config.LoadState().GetInstances(), &data))
	return data
}

// seedInstances persists an arbitrary instance list, the fixture most CLI tests
// start from. Test-goroutine only: require.NoError ends in t.FailNow, which testing
// documents as callable only from the goroutine running the test — from a spawned one
// it kills that goroutine silently. A --wait test whose fake drain runs in a goroutine
// must use writeInstances and assert the error back on the test goroutine, or a broken
// fixture surfaces as a wait timeout blamed on the wait protocol.
func seedInstances(t *testing.T, instances ...session.InstanceData) {
	t.Helper()
	require.NoError(t, writeInstances(instances...))
}

// writeInstances is seedInstances' error-returning half, safe to call from any
// goroutine.
func writeInstances(instances ...session.InstanceData) error {
	data, err := json.Marshal(instances)
	if err != nil {
		return err
	}
	return config.LoadState().SaveInstances(data)
}

// gitRepoWithBranches returns a real repository carrying branches, which is what the
// --variants path needs: it asks git which session branches are taken, so a plain
// directory is refused rather than read as a repo with none.
//
// The identity is set in the environment rather than left to the developer's global
// config, which may set neither and would fail the commit. The global and system configs
// are taken out of the picture entirely for the same reason one step further on: this
// package has no TestMain, so sandboxDataDir's HOME is the only isolation in force and
// XDG_CONFIG_HOME still points at the developer's real git config — where an
// init.templatedir with hooks, or commit.gpgsign, decides whether these tests pass on
// one machine and not another.
func gitRepoWithBranches(t *testing.T, branches ...string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	for _, branch := range branches {
		run("branch", branch)
	}
	return dir
}
