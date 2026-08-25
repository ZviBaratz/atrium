package app

// The flush-once modal for withheld repo config (#814): the third loop in
// flushSetupFailures, holding the same contract as its two siblings — shown
// once the screen is free, cleared as it is shown, never reopened by the tick.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlushSetupFailures_ReportsWithheldRepoConfigOnce(t *testing.T) {
	h := newCreateFormHome(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium.json"),
		[]byte(`{"repo_scripts":[{"name":"web","setup_script":"make deps"}]}`), 0o644))
	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)

	// Arm through the applying path, exactly as a create or resume would.
	inst.RunSetupScript(dir)
	require.NotEmpty(t, inst.RepoConfigProblem(), "the applying path must arm the one-shot report")

	// showInfo returns nil by contract (its effects are the state flip and the
	// overlay), so the modal is asserted on those, not on the Cmd.
	_ = h.flushSetupFailures()
	require.Equal(t, stateInfo, h.state, "an armed report must open the modal")
	require.NotNil(t, h.textOverlay)
	view := xansi.Strip(h.textOverlay.Render())
	assert.Contains(t, view, "atrium trust allow", "the modal must name the remedy")
	assert.Empty(t, inst.RepoConfigProblem(), "shown means cleared — the tick must not reopen it")

	// A later tick with nothing newly armed shows nothing.
	h.state = stateDefault
	assert.Nil(t, h.flushSetupFailures())
}
