package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// The spinner loop was self-chaining forever. It is one of three independent 10Hz
// loops, and Bubble Tea rebuilds the whole frame after every message — measured at
// 6-9ms and 1.2-2.3MB a rebuild — so with nothing on screen actually spinning, that
// was ~31% of an idle Atrium's render cost animating a counter nothing reads
// (#546).
//
// These are structural claims about whether the loop arms, in the shape
// app_splash_test.go established. Nothing here measures time.

// spinnerHome builds a home holding one instance parked at the given status.
func spinnerHome(t *testing.T, status session.Status) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	h.spinner = spinner.New()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "spun", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(0)
	inst.SetStatus(status)
	return h, inst
}

// With nothing spinning, a delivered tick kills the loop instead of re-arming it.
func TestSpinnerTick_DiesWhenNothingSpins(t *testing.T) {
	h, _ := spinnerHome(t, session.Ready)
	h.spinnerTicking = true

	_, cmd := h.Update(spinner.TickMsg{})

	require.Nil(t, cmd, "a tick with no spinning row must not re-arm the loop")
	require.False(t, h.spinnerTicking,
		"the flag must clear as the loop dies, or armSpinnerTick can never revive it")
}

// A spinning row keeps it alive — the negative control, without which the test
// above passes on a loop that simply never runs.
func TestSpinnerTick_KeepsRunningWhileARowSpins(t *testing.T) {
	for _, status := range []session.Status{session.Running, session.Loading} {
		t.Run(status.String(), func(t *testing.T) {
			h, _ := spinnerHome(t, status)
			h.spinnerTicking = true

			_, cmd := h.Update(spinner.TickMsg{})

			require.NotNil(t, cmd, "a spinning row must keep the loop alive")
			require.True(t, h.spinnerTicking)
		})
	}
}

// Statuses that draw a still glyph do not animate. Pending is the interesting one:
// it IS busy (a background sub-agent is still working, #290) but renders a steady
// marker precisely so it reads differently from Running — so it must not spin.
func TestSpinnerAnimating_OnlyRunningAndLoading(t *testing.T) {
	for status, want := range map[session.Status]bool{
		session.Running:    true,
		session.Loading:    true,
		session.Ready:      false,
		session.NeedsInput: false,
		session.Pending:    false,
		session.Paused:     false,
	} {
		t.Run(status.String(), func(t *testing.T) {
			h, _ := spinnerHome(t, status)
			require.Equal(t, want, h.spinnerAnimating())
		})
	}
}

// The 100ms tick revives a dead loop, which is what makes a row that starts
// spinning between metadata sweeps light up rather than sitting frozen.
func TestPreviewTick_RevivesTheSpinner(t *testing.T) {
	h, _ := spinnerHome(t, session.Running)
	h.spinnerTicking = false

	require.NotNil(t, h.armSpinnerTick(), "the tick's arm must start a loop for a spinning row")
	require.True(t, h.spinnerTicking)
}

// Arming twice cannot fork a second loop — the same no-double-arm guarantee
// splashTicking gives, and the reason the flag exists at all.
func TestArmSpinnerTick_NeverForksASecondLoop(t *testing.T) {
	h, _ := spinnerHome(t, session.Running)
	h.spinnerTicking = true

	require.Nil(t, h.armSpinnerTick(), "a live loop must not be armed again")
}

// A home with no list at all (the minimal shape some tests build) must not panic.
func TestSpinnerAnimating_NilListIsNotSpinning(t *testing.T) {
	h := &home{}
	require.False(t, h.spinnerAnimating())
	require.Nil(t, h.armSpinnerTick())
}

// The screensaver captures nothing.
//
// It draws the full-window splash and returns from viewContent before the panes are
// reached, so every capture taken under it is discarded on arrival — and unlike
// hint or scroll mode it can be left up indefinitely, which makes it the one
// discard state worth gating. The chain still ticks; only the tmux work stops.
//
// Built with newCaptureHome because the instance must actually be STARTED:
// resolveFrameTarget returns the zero target for an unstarted session anyway, so a
// hand-built instance would make this pass for the wrong reason.
func TestResolveFrameTarget_ScreensaverCapturesNothing(t *testing.T) {
	h, _ := newCaptureHome(t, newFrameSpy("pane"))

	h.state = stateDefault
	require.False(t, h.resolveFrameTarget().empty(),
		"precondition: a started, selected session on the preview tab is captured")

	h.state = stateScreensaver
	require.True(t, h.resolveFrameTarget().empty(),
		"the screensaver discards every frame, so none should be captured")
}

// Waking restamps pane freshness, so the first frame back does not report the age
// of the frame the screensaver left behind — a real number about the wrong
// question, and the same reason a tab change restamps.
//
// Every exit, not just the keyboard one. There are three ways out and they live in
// three different files, so a per-exit table is the only shape that catches the one
// that forgot: asserting this through the any-key path alone passed while a click
// still flashed the screensaver's age (which is exactly what it did).
//
// The pane needs a real frame for this to mean anything: staleMarker stays silent
// on a fallback pane, so a session without one would pass whatever the wake path
// did.
func TestScreensaverWake_RestampsPaneFreshness(t *testing.T) {
	for _, tc := range []struct {
		name string
		wake func(h *home)
	}{
		{"any key", func(h *home) { h.Update(keyMsg("a")) }},
		{"click", func(h *home) { h.Update(testutil.MouseClick(0, 0, tea.MouseLeft)) }},
		{"resized below the splash floor", func(h *home) {
			h.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := newCaptureHome(t, newFrameSpy("pane"))
			inst.SetPaneFrame("live pane content", time.Now().Add(-time.Hour))
			require.NoError(t, h.tabbedWindow.UpdatePreview(inst))
			require.Contains(t, h.tabbedWindow.String(), "stale",
				"precondition: an hour-old frame reads as stale before the wake restamp")

			h.state = stateScreensaver
			tc.wake(h)

			require.Equal(t, stateDefault, h.state, "precondition: this path woke it")
			require.NotContains(t, h.tabbedWindow.String(), "stale",
				"waking must restamp freshness, not flash the screensaver's age at the user")
		})
	}
}
