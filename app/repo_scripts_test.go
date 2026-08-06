package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installRepoScripts writes config.json with the given repo_scripts section into the
// sandboxed HOME.
func installRepoScripts(t *testing.T, entries ...config.RepoScript) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".atrium"), 0o755))
	cfg := config.DefaultConfig()
	cfg.RepoScripts = entries
	require.NoError(t, config.SaveConfig(cfg))
}

// Shutdown must end a setup script a RESUME started, not only one a create started.
// Resume runs the script while the instance is still Paused — SetStatus(Running) comes
// after — from a goroutine startWG never learns about, so a status gate here left the
// whole resume half of the feature (including a "resume all" running one script per
// session) with nothing to stop it: Atrium exited and `npm ci` kept going in a worktree
// with no app left to report it.
func TestReconcileInFlightStarts_EndsASetupScriptAResumeStarted(t *testing.T) {
	dir := t.TempDir()
	installRepoScripts(t, config.RepoScript{Name: "any", SetupScript: "sleep 60 && echo done"})
	h := newTestHomeWithInstances(t, dir)
	inst := h.list.GetInstances()[0]
	inst.SetStatus(session.Paused) // where Resume runs the script from

	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.RunSetupScript(dir)
	}()
	require.Eventually(t, func() bool { return inst.SetupPhase() != "" }, 5*time.Second, 5*time.Millisecond,
		"the script never started")

	h.reconcileInFlightStarts(context.Background())

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown left a resume's setup script running")
	}
}

// The startup report for refused entries. A rejected entry is dropped rather than
// applied — one bad template must not cost the user their other repos — so without a
// surface the entire symptom is a setup script that never runs and says nothing about
// why.
func TestFlushRepoScriptProblems_ReportsRefusedEntries(t *testing.T) {
	h := newTestHomeWithInstances(t)
	h.state = stateDefault
	h.pendingRepoScriptProblems = []repocfg.Problem{
		{Index: 3, Name: "web", Msg: "setup_script: template does not render: no field Wortree"},
	}

	assert.Nil(t, h.flushRepoScriptProblems(), "the report is opened directly, not returned as a command")

	require.Equal(t, stateInfo, h.state)
	require.NotNil(t, h.textOverlay)
	rendered := h.textOverlay.Render()
	assert.Contains(t, rendered, "repo_scripts[3]", "the entry's real position is how the user finds it")
	assert.Contains(t, rendered, "Wortree", "and the reason is what makes it fixable")
}

// Reported once, not on every tick: the flush runs from the 100ms preview loop, so a
// buffer that did not clear itself would reopen the modal forever.
func TestFlushRepoScriptProblems_ReportsOnce(t *testing.T) {
	h := newTestHomeWithInstances(t)
	h.state = stateDefault
	h.pendingRepoScriptProblems = []repocfg.Problem{{Index: 0, Msg: "entry configures nothing"}}

	h.flushRepoScriptProblems()
	h.state = stateDefault
	h.textOverlay = nil

	assert.Nil(t, h.flushRepoScriptProblems())
	assert.Nil(t, h.textOverlay)
	assert.Empty(t, h.pendingRepoScriptProblems)
}

// The screen belongs to whoever owns it: a modal opened from under a live overlay
// recomputes the height budget beneath it.
func TestFlushRepoScriptProblems_WaitsForTheScreen(t *testing.T) {
	h := newTestHomeWithInstances(t)
	h.state = stateHelp
	h.pendingRepoScriptProblems = []repocfg.Problem{{Index: 0, Msg: "entry configures nothing"}}

	assert.Nil(t, h.flushRepoScriptProblems())

	assert.Equal(t, stateHelp, h.state)
	assert.NotEmpty(t, h.pendingRepoScriptProblems, "the report must survive to be shown later")
}

// A clean config says nothing.
func TestFlushRepoScriptProblems_SaysNothingWhenEveryEntryIsValid(t *testing.T) {
	h := newTestHomeWithInstances(t)
	h.state = stateDefault

	assert.Nil(t, h.flushRepoScriptProblems())
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.textOverlay)
}

// The report is bounded: a config can hold any number of broken entries and the modal
// is one overlay.
func TestRepoScriptProblemsReport_CapsTheList(t *testing.T) {
	var problems []repocfg.Problem
	for i := 0; i < customCommandProblemsShown+3; i++ {
		problems = append(problems, repocfg.Problem{Index: i, Msg: "entry configures nothing"})
	}

	report := repoScriptProblemsReport(problems)

	assert.Contains(t, report, "… and 3 more")
	assert.NotContains(t, report, "repo_scripts[7]")
}
