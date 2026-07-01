package app

import (
	"testing"

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

// A confirmed batch pause tears down each parked session's preview terminal (the
// single-session pause path does the same after Pause).
func TestBatchOutcome_PauseTearsDownTerminals(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchPauseDoneMsg{paused: 1, pausedInstances: []*session.Instance{inst}})

	require.Equal(t, []*session.Instance{inst}, *captured,
		"a batch pause must tear down each parked session's preview terminal")
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
	addPaused(t, h, "alpha")
	captured := withCapturingCleanup(t)

	_, _ = h.Update(batchResumeDoneMsg{resumed: 1})

	assert.Empty(t, *captured, "a batch resume must not tear down any preview terminal")
}
