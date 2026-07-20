package main

import (
	"encoding/json"
	"os"
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
// start from.
func seedInstances(t *testing.T, instances ...session.InstanceData) {
	t.Helper()
	data, err := json.Marshal(instances)
	require.NoError(t, err)
	require.NoError(t, config.LoadState().SaveInstances(data))
}
