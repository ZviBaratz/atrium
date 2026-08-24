package app

import (
	"encoding/json"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withCapturingCleanup swaps the package terminal-cleanup seam for a fake that
// records which instances a batch outcome tears down, restoring the real one when
// the test ends. Same seam idiom as withFakeClipboard. It returns a pointer to the
// capture slice so assertions see the appends the driven msg makes.
func withCapturingCleanup(t *testing.T) *[]*session.Instance {
	t.Helper()
	orig := cleanupTerminalForInstance
	t.Cleanup(func() { cleanupTerminalForInstance = orig })
	var captured []*session.Instance
	cleanupTerminalForInstance = func(_ *ui.TabbedWindow, inst *session.Instance) {
		captured = append(captured, inst)
	}
	return &captured
}

// withStorage gives a test home a working in-memory storage so the batch
// pause/resume Update handlers can persist (they moved persistence onto the Update
// loop when the batch actions went off-thread).
func withStorage(t *testing.T, h *home) {
	t.Helper()
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st
}

// A confirmed batch pause tears down each parked session's preview terminal (the
// single-session pause path does the same after Pause).
func TestBatchOutcome_PauseTearsDownTerminals(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchPauseDoneMsg{paused: 1, pausedInstances: []*session.Instance{inst}})

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a batch pause must tear down each parked session's preview terminal")
}

// recordingState is a config.InstanceStorage that logs each save, so a test can assert WHEN
// a handler persists relative to what else it does. It embeds a real State, so everything
// but the recorded call behaves normally — and an instance that is not Started still drives
// a save (SaveInstances marshals an empty set and writes it), which is what lets this run on
// the package's hermetic fixtures instead of a live session.
type recordingState struct {
	config.InstanceStorage
	log *[]string
}

func (r recordingState) SaveInstances(data json.RawMessage) error {
	*r.log = append(*r.log, "persist")
	return r.InstanceStorage.SaveInstances(data)
}

// A batch pause must persist AFTER both of its reaps, which is the order single-session
// pause has always had (handlePauseDone). Since #708 a reap releases the shell's owned tmux
// name, so persisting first writes a term_session naming a shell the same handler goes on to
// kill: the next run claims that dead name for a live shell rather than minting one from the
// title the session has by then, and goes on reserving the freed title against new sessions.
//
// Both reaps, deliberately: the failures loop here and the successes loop inside finishBatch
// are separate call sites, and the persist used to sit above both.
//
// The seam records the teardown rather than performing it — that the teardown releases the
// name is ui's property (TestReapReleasesTheNameSoTheNextShellFollowsTheRename), and this is
// only about which happens first.
func TestBatchOutcome_PausePersistsAfterTearingDownTerminals(t *testing.T) {
	h := newCreateFormHome(t)
	var order []string
	st, err := session.NewStorage(recordingState{InstanceStorage: config.DefaultState(), log: &order})
	require.NoError(t, err)
	h.storage = st

	parked := addActive(t, h, "alpha")
	stranded := addActive(t, h, "bravo")
	orig := cleanupTerminalForInstance
	t.Cleanup(func() { cleanupTerminalForInstance = orig })
	cleanupTerminalForInstance = func(_ *ui.TabbedWindow, inst *session.Instance) {
		order = append(order, "reap "+inst.Title())
	}

	_, _ = h.Update(batchPauseDoneMsg{
		paused:          1,
		pausedInstances: []*session.Instance{parked},
		failures: []pauseFailure{{
			inst: stranded, title: "bravo", err: assert.AnError, worktreeGone: true,
		}},
	})

	assert.Equal(t, []string{"reap bravo", "reap alpha", "persist"}, order,
		"a batch pause must persist after its reaps, not before")
}

// A confirmed batch kill tears down each killed session's preview terminal.
func TestBatchOutcome_KillTearsDownTerminals(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchKillDoneMsg{killed: 1, killedInstances: []*session.Instance{inst}})

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a batch kill must tear down each killed session's preview terminal")
}

// A confirmed batch resume only flips in-memory status; it must NOT tear down any
// preview terminal. A naive single shared cleanup field later fed resume's
// instances would silently regress this — the invariant finishBatch enforces by
// making resume pass no cleanup slice at all.
func TestBatchOutcome_ResumeTearsDownNothing(t *testing.T) {
	h := newCreateFormHome(t)
	withStorage(t, h)
	addPaused(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchResumeDoneMsg{resumed: 1})

	assert.Empty(t, *captured, "a batch resume must not tear down any preview terminal")
}
