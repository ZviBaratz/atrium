package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A resume is the one action that grows the live session population without creating
// anything, so it was the one way past the host-derived soft cap without being asked
// (#463). These cover both entry points — the batch confirmation, which gains a
// paragraph, and single resume, which gains a dialog it only ever shows over budget.
//
// Test homes leave hostCap at 0 (an unlimited soft cap), which is why every resume
// test written before this one still sees no dialog and no clause.

// Over the derived budget, the batch dialog names the capacity alongside its usual
// question — and still resumes nothing until the user confirms. 2 live + a batch of 3
// against a capacity of 4: resuming one would fit, so the clause is proof the batch
// size is what was weighed.
func TestResumeAll_OverSoftCapNamesHostBudget(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 4
	h.appConfig.MaxSessions = nil // unset → host-derived soft cap
	addActive(t, h, "live-1")
	addActive(t, h, "live-2")
	paused := []string{"alpha", "bravo", "charlie"}
	for _, title := range paused {
		addPaused(t, h, title)
	}

	_, _ = h.handleKeyPress(keyMsg("ctrl+r"))

	require.Equal(t, stateConfirm, h.state, "ctrl+r must confirm before resuming")
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	// The literal itself is pinned once, in dialog_voice_test.go; what this test is
	// about is that the capacity clause joins the ordinary resume question rather
	// than replacing it or opening a dialog of its own.
	assert.Contains(t, rendered, resumeConfirmMessage("paused", 3))
	assert.Contains(t, rendered,
		"Host capacity is 4, with 2 already running — 3 more will queue rather than parallelize.")
	assert.Contains(t, rendered, "Press y (or ctrl+r) to resume 3 sessions, n or esc to cancel")
	for _, inst := range h.list.GetInstances() {
		if inst.Title() == "live-1" || inst.Title() == "live-2" {
			continue
		}
		assert.True(t, inst.Paused(), "nothing resumes until the user confirms")
	}
}

// Within budget the dialog is exactly what it was before this change: paused sessions
// impose no host load, so 1 live + 3 paused resuming all three lands on the capacity
// of 4, not past it (counting all four sessions would).
func TestResumeAll_WithinBudgetKeepsPlainQuestion(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 4
	h.appConfig.MaxSessions = nil
	addActive(t, h, "live-1")
	for _, title := range []string{"alpha", "bravo", "charlie"} {
		addPaused(t, h, title)
	}

	_, _ = h.handleKeyPress(keyMsg("ctrl+r"))

	require.Equal(t, stateConfirm, h.state)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, rendered, "Resume 3 paused sessions?")
	assert.NotContains(t, rendered, "Host capacity",
		"a resume that fits the budget says nothing about capacity")
}

// The clause is computed in the shared core, but the count it weighs is derived
// differently by each entry point: resumeAll takes PausedInstancesInView() whole,
// while resumeMarked filters the marked set down to its paused subset. So the marked
// batch needs its own guard on the number — the resumeAll test cannot reach that
// filter. 2 live, 4 marked of which 3 are paused, 4 paused in the view: the clause
// must weigh 3, which is neither the marked count nor the view's paused count (both 4,
// and both would still be over budget — a fleet where the wrong reading merely stayed
// silent would prove nothing) and not 1 either (2+1 fits capacity 4, so weighing one
// session drops the clause entirely).
func TestResumeMarked_OverSoftCapWeighsTheMarkedPausedSubset(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 4
	h.appConfig.MaxSessions = nil
	live := addActive(t, h, "live-1")
	addActive(t, h, "live-2")
	paused := make([]*session.Instance, 0, 4)
	for _, title := range []string{"alpha", "bravo", "charlie", "delta"} {
		paused = append(paused, addPaused(t, h, title))
	}

	pressRune(h, 'v')
	h.list.ToggleMark(live) // live — filtered out of a resume
	for _, inst := range paused[:3] {
		h.list.ToggleMark(inst)
	}
	pressRune(h, 'r')

	require.Equal(t, stateConfirm, h.state, "the marked batch must confirm before resuming")
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, rendered, "Resume 3 marked sessions?")
	assert.Contains(t, rendered,
		"Host capacity is 4, with 2 already running — 3 more will queue rather than parallelize.")
	for _, inst := range paused {
		assert.True(t, inst.Paused(), "nothing resumes until the user confirms")
	}
}

// An explicit max_sessions counts every session, paused included, so the sessions a
// resume brings back are already inside its limit — it cannot be crossed by resuming,
// and the dialog stays silent. Same fleet as the over-budget case above (2 live + 3
// paused, limit 4 vs capacity 4), so the only difference is that the cap is explicit.
func TestResumeAll_HardCapNeverWarns(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 4
	four := 4
	h.appConfig.MaxSessions = &four // explicit hard cap
	addActive(t, h, "live-1")
	addActive(t, h, "live-2")
	for _, title := range []string{"alpha", "bravo", "charlie"} {
		addPaused(t, h, title)
	}

	_, _ = h.handleKeyPress(keyMsg("ctrl+r"))

	require.Equal(t, stateConfirm, h.state, "the batch still asks its usual question")
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.NotContains(t, rendered, "Host capacity",
		"an explicit cap counts paused sessions, so a resume never crosses it")
}

// Single resume asks too when it crosses — twelve presses of r are the same overshoot
// as one "resume all". The dialog names the session and the capacity, and nothing else:
// r has never explained what resume does, and the batch dialog is where those semantics
// are named.
func TestResumeSelected_OverSoftCapConfirmsFirst(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addActive(t, h, "live-1")
	addActive(t, h, "live-2")
	inst := addPaused(t, h, "alpha")
	h.list.SelectInstance(inst)
	require.Equal(t, inst, h.list.GetSelectedInstance())

	_, _ = h.handleKeyPress(textMsg("r"))

	require.Equal(t, stateConfirm, h.state, "r must confirm before resuming over budget")
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, rendered, "Resume session 'alpha'?")
	assert.Contains(t, rendered,
		"Host capacity is 2, with 2 already running — another will queue rather than parallelize.")
	assert.Contains(t, rendered, "Press y (or r) to resume, n or esc to cancel")
	assert.NotContains(t, rendered, "rebuilds",
		"the capacity dialog stays on capacity")
	assert.Equal(t, "resuming…", h.pendingConfirmBusyLabel,
		"accepting runs the same off-thread resume, behind the same progress row")
	assert.True(t, inst.Paused(), "nothing resumes until the user confirms")
	assert.False(t, h.actionInFlight, "the resume has not started")
}

// The paused siblings of a resumed session impose no load, so they must not push it
// over: 1 live + 3 paused against a capacity of 4 resumes one with no dialog at all
// (counting all four sessions would ask).
func TestResumeSelected_PausedSiblingsDontCount(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 4
	h.appConfig.MaxSessions = nil
	addActive(t, h, "live-1")
	inst := addPaused(t, h, "alpha")
	addPaused(t, h, "bravo")
	addPaused(t, h, "charlie")
	h.list.SelectInstance(inst)
	require.Equal(t, inst, h.list.GetSelectedInstance())

	_, _ = h.handleKeyPress(textMsg("r"))

	assert.NotEqual(t, stateConfirm, h.state, "a resume that fits the budget is not questioned")
	assert.Nil(t, h.confirmationOverlay)
	assert.True(t, h.actionInFlight, "r goes straight to the resume")
}

// Declining the capacity question leaves the session parked and the app idle.
func TestResumeSelected_DeclineResumesNothing(t *testing.T) {
	h := newCreateFormHome(t)
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addActive(t, h, "live-1")
	addActive(t, h, "live-2")
	inst := addPaused(t, h, "alpha")
	h.list.SelectInstance(inst)

	_, _ = h.handleKeyPress(textMsg("r"))
	require.Equal(t, stateConfirm, h.state)

	h.Update(textMsg("n")) // decline

	assert.True(t, inst.Paused(), "declining resumes nothing")
	assert.Equal(t, stateDefault, h.state, "the confirm closes")
	assert.False(t, h.actionInFlight, "no resume was started")
}
