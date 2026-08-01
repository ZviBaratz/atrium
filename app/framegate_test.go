package app

import (
	"errors"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/require"
)

// The quiet-pane gate stops the 100ms capture chain once the watched pane returns
// the same bytes over and over. Measured on a live fleet, one capture-pane costs
// ~3.0ms of client CPU plus ~0.5ms in the tmux server, and the chain also spends a
// full frame rebuild (6-9ms) delivering each result — so at idle it was the single
// largest verb in the command log and about a third of the messages (#546).
//
// These are structural claims: subprocesses counted through frameSpy, arms counted
// by whether a Cmd came back. Nothing here measures time, and nothing sleeps.
//
// Every fixture is newCaptureHome, which starts its instance. An unstarted one
// makes resolveFrameTarget return the zero target anyway, so a hand-built instance
// would let each of these pass for the wrong reason.

// settleFrames feeds n identical observations of text for target into the run, the
// way the capture chain does.
func settleFrames(h *home, target frameTarget, text string, n int) {
	for range n {
		h.noteFrameSeen(target, text)
	}
}

// A run counts identical captures and restarts on anything else.
func TestNoteQuietFrame(t *testing.T) {
	a := frameTarget{instance: &session.Instance{}}
	b := frameTarget{instance: &session.Instance{}}

	for _, tc := range []struct {
		name   string
		prev   quietRun
		target frameTarget
		text   string
		want   quietRun
	}{
		{
			name: "the first observation is a run of one",
			prev: quietRun{}, target: a, text: "x",
			want: quietRun{target: a, text: "x", seen: 1},
		},
		{
			name: "an identical capture extends the run",
			prev: quietRun{target: a, text: "x", seen: 3}, target: a, text: "x",
			want: quietRun{target: a, text: "x", seen: 4},
		},
		{
			// The whole signal: one differing byte is the pane moving.
			name: "a single differing byte restarts it",
			prev: quietRun{target: a, text: "x", seen: 19}, target: a, text: "y",
			want: quietRun{target: a, text: "y", seen: 1},
		},
		{
			// Same bytes, different session: two panes showing the same thing must
			// not pool their stillness.
			name: "a different target restarts it even on identical text",
			prev: quietRun{target: a, text: "x", seen: 19}, target: b, text: "x",
			want: quietRun{target: b, text: "x", seen: 1},
		},
		{
			// An empty pane is a real observation, not a missing one — SetPaneFrame
			// records it for the same reason.
			name: "an empty capture is an observation like any other",
			prev: quietRun{target: a, text: "", seen: 2}, target: a, text: "",
			want: quietRun{target: a, text: "", seen: 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, noteQuietFrame(tc.prev, tc.target, tc.text))
		})
	}
}

// settled answers about a named target, not about the run's own length.
func TestQuietRun_Settled(t *testing.T) {
	a := frameTarget{instance: &session.Instance{}}
	b := frameTarget{instance: &session.Instance{}}

	for _, tc := range []struct {
		name   string
		run    quietRun
		target frameTarget
		want   bool
	}{
		{"a fresh run has settled nothing", quietRun{}, a, false},
		{"one short of the threshold is still moving", quietRun{target: a, text: "x", seen: 4}, a, false},
		{"exactly the threshold has settled", quietRun{target: a, text: "x", seen: 5}, a, true},
		{"past the threshold stays settled", quietRun{target: a, text: "x", seen: 500}, a, true},
		{
			// The run outlives a tab switch and is fed at two different rates, so a
			// caller can ask about a target it has never described. Answering from
			// seen alone would gate one pane on another pane's stillness.
			"a long run about another target says nothing about this one",
			quietRun{target: b, text: "x", seen: 500}, a, false,
		},
		{"the zero target is not settled by an empty run", quietRun{}, frameTarget{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.run.settled(tc.target, 5))
		})
	}
}

// A pane that has stopped moving stops being targeted.
func TestResolveFrameTarget_StillPaneStopsBeingCaptured(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("agent output"))
	target := frameTarget{instance: inst}

	require.Equal(t, target, h.resolveFrameTarget(),
		"precondition: a started, selected session on the preview tab is captured")

	settleFrames(h, target, "agent output", frameQuietRuns-1)
	require.Equal(t, target, h.resolveFrameTarget(),
		"one capture short of the threshold, the pane is still being watched")

	h.noteFrameSeen(target, "agent output")
	require.True(t, h.resolveFrameTarget().empty(),
		"a pane that has returned frameQuietRuns identical captures must stop costing one")
}

// The terminal tab is never gated — the negative control for the tab boundary.
//
// The gate is safe on the preview tab only because the 500ms sweep harvests that
// pane for free and keeps painting it at 2Hz. The terminal tab has no such
// fallback, so gating it would freeze its pane outright rather than slow it down.
func TestResolveFrameTarget_TerminalTabIsNeverGated(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("shell output"))

	// Settle the preview target hard, then switch tabs: the run is at its most
	// tempting exactly here.
	settleFrames(h, frameTarget{instance: inst}, "shell output", frameQuietRuns*3)
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)

	require.False(t, h.resolveFrameTarget().empty(),
		"the terminal tab has no harvest to fall back on, so it must keep capturing")
}

// The cost claim itself: a still pane stops forking capture-pane.
//
// The baseline half is what makes the zero mean anything. A pane whose content
// keeps changing must go on forking captures through the same loop — otherwise
// "it stopped capturing" would also pass on a harness that never captured.
func TestFrameChain_StopsForkingCapturePaneOnAStillPane(t *testing.T) {
	// Baseline: content that changes every round never settles, so the chain runs.
	t.Run("a moving pane keeps being captured", func(t *testing.T) {
		h, inst := newCaptureHome(t, newFrameSpy("frame 0"))
		target := frameTarget{instance: inst}
		for i := range frameQuietRuns * 2 {
			h.noteFrameSeen(target, string(rune('a'+i%26)))
		}
		require.Equal(t, target, h.resolveFrameTarget(),
			"a pane whose bytes keep changing must never settle")

		spy2 := newFrameSpy("frame 1")
		h2, _ := newCaptureHome(t, spy2)
		before := countVerb(spy2.seen(), "capture-pane")
		drainFrameChain(t, h2, 5)
		require.Positive(t, countVerb(spy2.seen(), "capture-pane")-before,
			"precondition: driving the chain does fork capture-pane, so a zero below is real")
	})

	t.Run("a still pane stops", func(t *testing.T) {
		spy := newFrameSpy("agent output")
		h, _ := newCaptureHome(t, spy)

		// Drive the real chain: each round captures the spy's fixed content, so the
		// run builds from the chain's own frames rather than from a hand-fed string.
		before := countVerb(spy.seen(), "capture-pane")
		rounds := drainFrameChain(t, h, frameQuietRuns*3)

		// The count pins where it stopped, and pins that it ran at all. A helper that
		// silently drove one round would satisfy every assertion below.
		require.Equal(t, frameQuietRuns, rounds,
			"the chain must run exactly until the run reaches the threshold, then end")
		require.Equal(t, rounds, countVerb(spy.seen(), "capture-pane")-before,
			"one capture-pane per round and not one more; saw %v", spy.seen()[before:])

		after := countVerb(spy.seen(), "capture-pane")
		require.Nil(t, h.armFrameCapture(0), "the settled pane must not be armed at all")
		require.Equal(t, after, countVerb(spy.seen(), "capture-pane"),
			"and no further capture-pane may reach tmux; saw %v", spy.seen()[after:])
	})
}

// drainFrameChain runs the capture chain on the main thread for at most n rounds,
// stopping as soon as it declines to arm, and reports how many rounds it ran. It
// leaves the in-flight slot free so the caller can arm once more and see for
// itself whether the chain is up.
//
// The slot release is load-bearing, not tidying. handlePaneFrame re-arms inside
// Update and Bubble Tea would run the Cmd it returns, clearing the flag on the way
// back; this loop discards that Cmd, so without releasing the slot by hand every
// arm after the first is a no-op — the loop runs exactly one round and any
// "it stopped capturing" assertion afterwards passes for that reason instead of
// the gate's. Which is exactly what the first version of this helper did.
func drainFrameChain(t *testing.T, h *home, n int) int {
	t.Helper()
	rounds := 0
	for range n {
		h.frameInFlight = false
		cmd := h.armFrameCapture(0)
		if cmd == nil {
			return rounds
		}
		msg, ok := cmd().(paneFrameMsg)
		require.True(t, ok, "the chain must always deliver a paneFrameMsg")
		h.Update(msg)
		rounds++
	}
	h.frameInFlight = false
	return rounds
}

// The harvest re-opens the gate: it is the only thing still watching once the
// chain has stopped.
func TestFrameChain_ResumesWhenTheHarvestSeesAChange(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, inst := newCaptureHome(t, spy)

	require.Equal(t, frameQuietRuns, drainFrameChain(t, h, frameQuietRuns*3),
		"precondition: the chain ran until the pane settled, rather than stalling early")
	require.Nil(t, h.armFrameCapture(0), "precondition: the chain ended")

	// What the 500ms sweep does when the agent starts printing again. The stamp has
	// to be newer than the cached frame or the harvest is dropped as a rewind.
	_, cachedAt, _ := inst.PaneFrame()
	h.applyMetadataResults([]instanceMetaResult{{
		instance:    inst,
		paneFrame:   "the agent said something new",
		paneFrameAt: cachedAt.Add(time.Second),
		paneFrameOK: true,
	}}, false)

	require.NotNil(t, h.armFrameCapture(0),
		"a harvest that differs must re-open the gate, or the pane is stuck at the 2Hz fallback")
}

// A harvest that matches keeps the gate shut, so a genuinely idle pane does not
// oscillate between 2Hz and 10Hz forever.
func TestHarvest_MatchingFrameKeepsTheGateShut(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("agent output"))

	require.Equal(t, frameQuietRuns, drainFrameChain(t, h, frameQuietRuns*3),
		"precondition: the chain ran until the pane settled, rather than stalling early")
	require.Nil(t, h.armFrameCapture(0), "precondition: the chain ended")

	cached, cachedAt, _ := inst.PaneFrame()
	h.applyMetadataResults([]instanceMetaResult{{
		instance:    inst,
		paneFrame:   cached,
		paneFrameAt: cachedAt.Add(time.Second),
		paneFrameOK: true,
	}}, false)

	require.Nil(t, h.armFrameCapture(0),
		"an unchanged harvest is evidence the pane is still still — the gate must hold")
}

// The gate must still close while the 500ms sweep keeps arriving.
//
// In production the two writers interleave: roughly five chain frames, then a
// sweep whose harvest is OLDER than the frame the chain just stored, so
// applyHarvestedFrame drops it as a rewind. Noting a dropped harvest would fold an
// empty string into the run twice a second and restart it every sweep — the gate
// would never close, the whole change would be inert, and every other test in this
// file would stay green, because none of the others lets a sweep land mid-drain.
// A mutation found this gap; the test is what keeps it shut.
func TestQuietRun_ClosesTheGateWhileSweepsInterleave(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("agent output"))
	h.instanceChanged() // settle the selection; see the revive table for why

	sweeps, dropsVerified := 0, false
	for round := range frameQuietRuns * 3 {
		h.frameInFlight = false
		cmd := h.armFrameCapture(0)
		if cmd == nil {
			break
		}
		msg, ok := cmd().(paneFrameMsg)
		require.True(t, ok)
		h.Update(msg)

		if round%5 != 4 {
			continue
		}
		// A sweep carrying a capture that predates the frame the chain just stored,
		// which is what every sweep looks like while the chain is alive.
		_, cachedAt, _ := inst.PaneFrame()
		h.applyMetadataResults([]instanceMetaResult{{
			instance: inst, paneFrame: "agent output",
			paneFrameAt: cachedAt.Add(-time.Second), paneFrameOK: true,
		}}, false)
		sweeps++
		if _, at, _ := inst.PaneFrame(); at.Equal(cachedAt) {
			dropsVerified = true
		}
	}
	h.frameInFlight = false

	require.Positive(t, sweeps, "precondition: sweeps actually landed during the drain")
	require.True(t, dropsVerified,
		"precondition: those sweeps were dropped as rewinds — that is the case under test")
	require.True(t, h.resolveFrameTarget().empty(),
		"a dropped harvest must not disturb the run, or the gate never closes in production")
}

// A gated pane must never announce itself stale.
//
// This is the whole reason the gate skips the capture instead of slowing it:
// previewStaleAfter is 1.2s and keys off the capture stamp, so a design that
// lengthened the cadence would stamp "— stale 1s" on every healthy idle pane. The
// 500ms sweep restamps regardless of content (Poll stamps lastCaptureAt on every
// successful poll), which is what keeps the marker silent here.
func TestQuietPane_NeverGoesStale(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("agent output"))

	require.Equal(t, frameQuietRuns, drainFrameChain(t, h, frameQuietRuns*3),
		"precondition: the chain ran until the pane settled, rather than stalling early")
	require.Nil(t, h.armFrameCapture(0), "precondition: the chain ended")

	// The marker is silent on a fallback pane, so prove it can speak at all here
	// before asserting that it does not.
	cached, _, _ := inst.PaneFrame()
	inst.SetPaneFrame(cached, time.Now().Add(-time.Hour))
	require.NoError(t, h.tabbedWindow.UpdatePreview(inst))
	require.Contains(t, h.tabbedWindow.String(), "stale",
		"precondition: this pane does render a staleness marker when a frame ages out")

	// Now the sweeps the gate relies on, each carrying the same bytes and a fresh
	// stamp — a session that has not moved for two seconds of wall clock.
	at := time.Now().Add(-time.Hour)
	for range 4 {
		at = at.Add(500 * time.Millisecond)
		h.applyMetadataResults([]instanceMetaResult{{
			instance: inst, paneFrame: cached, paneFrameAt: at, paneFrameOK: true,
		}}, false)
	}
	inst.SetPaneFrame(cached, time.Now())
	require.NoError(t, h.tabbedWindow.UpdatePreview(inst))
	require.NotContains(t, h.tabbedWindow.String(), "stale",
		"the 2Hz harvest must keep the frame fresh while the chain is gated")
}

// The chain ends on every zero-target reason, and the preview tick brings it back.
//
// A table over the reasons rather than one of them, because the revive is a single
// general self-heal and the risk is the opposite of a missing case: a reason that
// somehow keeps arming costs the messages this change exists to remove, and a
// revive that does not fire leaves the preview at the 2Hz fallback forever.
func TestFrameChain_DiesOnAnEmptyTargetAndRevivesFromThePreviewTick(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(h *home, inst *session.Instance)
		open  func(h *home, inst *session.Instance)
	}{
		{
			name:  "a paused selection",
			close: func(_ *home, inst *session.Instance) { inst.SetStatus(session.Paused) },
			open:  func(_ *home, inst *session.Instance) { inst.SetStatus(session.Ready) },
		},
		{
			name:  "the diff tab, which renders from cached git metadata",
			close: func(h *home, _ *session.Instance) { h.tabbedWindow.SetActiveTab(ui.DiffTab) },
			open:  func(h *home, _ *session.Instance) { h.tabbedWindow.SetActiveTab(ui.PreviewTab) },
		},
		{
			name:  "the screensaver, which discards every frame taken under it",
			close: func(h *home, _ *session.Instance) { h.state = stateScreensaver },
			open:  func(h *home, _ *session.Instance) { h.dismissScreensaver() },
		},
		{
			name: "a pane that has stopped moving",
			close: func(h *home, inst *session.Instance) {
				settleFrames(h, frameTarget{instance: inst}, "agent output", frameQuietRuns)
			},
			open: func(h *home, inst *session.Instance) {
				h.noteFrameSeen(frameTarget{instance: inst}, "something new")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newFrameSpy("agent output")
			h, inst := newCaptureHome(t, spy)
			require.NotNil(t, h.armFrameCapture(0), "precondition: the chain arms with something to capture")
			h.frameInFlight = false

			// Settle the selection first. instanceChanged restamps freshness — and so
			// drops the quiet run — the first time it sees a selection, which in
			// production has happened long before a pane can go quiet (that takes
			// frameQuietRuns ticks) but here would land in the middle of the table.
			h.instanceChanged()

			tc.close(h, inst)
			before := spy.count()
			require.Nil(t, h.armFrameCapture(0), "with nothing to capture the chain must end")
			require.False(t, h.frameInFlight, "a declined arm must not hold the in-flight slot")

			// The negative control: the tick must not resurrect it while the reason
			// still holds, or the loop never actually stopped.
			_, cmd := h.Update(previewTickMsg{})
			require.NotNil(t, cmd, "the preview tick always re-arms itself")
			require.False(t, h.frameInFlight, "the tick must not arm a capture there is no reason to take")
			require.Equal(t, before, spy.count(),
				"and nothing may reach tmux while the chain is down; saw %v", spy.seen()[before:])

			tc.open(h, inst)
			h.Update(previewTickMsg{})
			require.True(t, h.frameInFlight,
				"the preview tick is the only revive there is: without it the chain never restarts")
		})
	}
}

// Every deliberate re-point drops the run.
//
// A run describing the pane the user just looked away from must not decide whether
// the new one is captured. The reset lives in noteFrameTargetChange, which is
// already the shared tail of all three paths — so this is also the guard that a
// fourth exit inherits it.
func TestNoteFrameTargetChange_ResetsTheQuietRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repoint func(h *home)
	}{
		{"a tab switch", func(h *home) { h.tabChanged() }},
		{"a screensaver wake", func(h *home) { h.state = stateScreensaver; h.dismissScreensaver() }},
		{"a selection change", func(h *home) {
			h.lastStatusPollSelection = nil
			h.instanceChanged()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := newCaptureHome(t, newFrameSpy("agent output"))
			target := frameTarget{instance: inst}
			settleFrames(h, target, "agent output", frameQuietRuns)
			require.True(t, h.frameQuiet.settled(target, frameQuietRuns), "precondition: the run is settled")

			tc.repoint(h)

			require.Zero(t, h.frameQuiet.seen, "pointing the preview somewhere new must drop the run")
		})
	}
}

// Both writers of the pane cache feed the run.
//
// The forgotten-site guard. Each writer is enough on its own for one half of the
// cycle — the chain closes the gate, the harvest re-opens it — so a version that
// fed only one would still look healthy from the other's tests: gate-never-closes
// or gate-never-reopens, each invisible to the wrong test.
func TestBothWritersFeedTheQuietRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(h *home, inst *session.Instance, text string)
	}{
		{
			name: "the 100ms capture chain",
			write: func(h *home, inst *session.Instance, text string) {
				h.Update(paneFrameMsg{target: frameTarget{instance: inst}, text: text, at: time.Now()})
			},
		},
		{
			name: "the 500ms sweep's free harvest",
			write: func(h *home, inst *session.Instance, text string) {
				_, cachedAt, _ := inst.PaneFrame()
				h.applyMetadataResults([]instanceMetaResult{{
					instance: inst, paneFrame: text, paneFrameAt: cachedAt.Add(time.Second), paneFrameOK: true,
				}}, false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := newCaptureHome(t, newFrameSpy("agent output"))
			target := frameTarget{instance: inst}

			tc.write(h, inst, "first")
			require.Equal(t, quietRun{target: target, text: "first", seen: 1}, h.frameQuiet,
				"a stored frame must be noted, or the gate can never close")

			tc.write(h, inst, "first")
			require.Equal(t, 2, h.frameQuiet.seen, "and an identical one must extend the run")

			tc.write(h, inst, "second")
			require.Equal(t, quietRun{target: target, text: "second", seen: 1}, h.frameQuiet,
				"a differing one must restart it, or the gate can never re-open")
		})
	}
}

// A failed capture is not evidence either way, so it leaves the run alone.
func TestFailedCapture_LeavesTheQuietRunAlone(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("agent output"))
	target := frameTarget{instance: inst}
	settleFrames(h, target, "agent output", 3)
	before := h.frameQuiet

	h.Update(paneFrameMsg{target: target, err: errors.New("capture-pane failed"), at: time.Now()})

	require.Equal(t, before, h.frameQuiet,
		"a capture that failed says nothing about whether the pane moved")
}

// A frame stored against a session the user has already left must not be counted
// toward the selected pane's stillness.
func TestHarvest_OnlyTheSelectedSessionFeedsTheRun(t *testing.T) {
	h, selected := newCaptureHome(t, newFrameSpy("agent output"))

	other, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(other)()

	h.applyMetadataResults([]instanceMetaResult{{
		instance: other, paneFrame: "a background session's pane", paneFrameAt: time.Now(), paneFrameOK: true,
	}}, false)

	require.Zero(t, h.frameQuiet.seen,
		"only the watched pane's frames are evidence about the watched pane")
	require.Equal(t, frameTarget{instance: selected}, h.resolveFrameTarget(),
		"and the selected session must still be captured")

	text, _, ok := other.PaneFrame()
	require.True(t, ok, "precondition: the background harvest was still stored — only the noting is scoped")
	require.Equal(t, "a background session's pane", text)
}

// The tick's revive must not fork a second capture while one is in flight.
func TestPreviewTickRevive_NeverForksASecondCapture(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, _ := newCaptureHome(t, spy)

	require.NotNil(t, h.armFrameCapture(0), "precondition: one capture is armed and in flight")
	before := spy.count()

	h.Update(previewTickMsg{})

	require.True(t, h.frameInFlight)
	require.Equal(t, before, spy.count(),
		"the tick must be a no-op while a capture is in flight; saw %v", spy.seen()[before:])
}
