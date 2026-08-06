package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failedSetupHome builds a home holding one instance whose setup script really ran and
// really failed. Driving the production path rather than stuffing a field is what makes
// these tests worth having: the report's shape, and the fact that a non-zero exit
// records one at all, are both part of what is under test.
func failedSetupHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".atrium"), 0o755))
	cfg := config.DefaultConfig()
	cfg.RepoScripts = []config.RepoScript{{Name: "web", SetupScript: "echo 'npm ERR! offline' >&2; exit 1"}}
	require.NoError(t, config.SaveConfig(cfg))

	dir := t.TempDir()
	h := newTestHomeWithInstances(t, dir)
	h.state = stateDefault
	inst := h.list.GetInstances()[0]
	inst.RunSetupScript(dir)
	require.NotEmpty(t, inst.SetupFailureReport(), "fixture must actually have failed")
	return h, inst
}

// A setup script's failure reaches the user, and it carries the output. The script runs
// off the UI thread inside Start, and Start deliberately does not return its failure —
// so without this the only trace of a session whose dependencies never installed is a
// line in the log.
func TestFlushSetupFailures_ShowsTheReportAndItsOutput(t *testing.T) {
	h, _ := failedSetupHome(t)

	assert.Nil(t, h.flushSetupFailures(), "the report is opened directly, not returned as a command")

	require.Equal(t, stateInfo, h.state, "a failed setup script must reach the user")
	require.NotNil(t, h.textOverlay)
	assert.Contains(t, h.textOverlay.Render(), "npm ERR! offline")
}

// Reported once, not on every tick. The flush runs from the 100ms preview loop, so a
// report that did not clear itself would reopen the modal forever.
func TestFlushSetupFailures_ReportsEachFailureOnce(t *testing.T) {
	h, inst := failedSetupHome(t)

	h.flushSetupFailures()
	h.state = stateDefault
	h.textOverlay = nil

	h.flushSetupFailures()

	assert.Nil(t, h.textOverlay)
	assert.Empty(t, inst.SetupFailureReport())
}

// The screen belongs to whoever owns it. Same rule flushCustomCommandProblems follows:
// a modal opened from under a live overlay recomputes the height budget beneath it.
func TestFlushSetupFailures_WaitsForTheScreen(t *testing.T) {
	h, inst := failedSetupHome(t)
	h.state = stateHelp

	assert.Nil(t, h.flushSetupFailures())

	assert.Equal(t, stateHelp, h.state)
	assert.NotEmpty(t, inst.SetupFailureReport(), "the report must survive to be shown later")
}

// A fleet with nothing to report costs nothing and says nothing.
func TestFlushSetupFailures_SaysNothingOnACleanFleet(t *testing.T) {
	h := newTestHomeWithInstances(t, t.TempDir())
	h.state = stateDefault

	assert.Nil(t, h.flushSetupFailures())
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.textOverlay)
}
