package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// backgroundPane is a settled claude pane whose footer still chips work the ended turn left
// running. footer parameterizes the mode line so a test can vary what the chip sits beside.
func backgroundPane(footer string) string {
	return strings.Join([]string{
		"● Kicked the suite off in the background; I'll report when it lands.",
		"",
		spinnerRule,
		"❯ ",
		spinnerRule,
		"  " + footer,
	}, "\n")
}

const (
	chipFooter      = "⏵⏵ auto mode on · 2 shells · ← for agents · ↓ to manage"
	chipBusyFooter  = "⏵⏵ auto mode on · 1 shell, 1 monitor · esc to interrupt · ← for agents"
	cleanIdleFooter = "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
)

// The bug repro. An authoritative turn-end — hook latched "ready" with an EMPTY in-flight
// set — used to commit PaneIdle → session.Ready while a background shell was still running,
// because a background Bash/Monitor is not a sub-agent and never enters that set. The
// footer chip is the evidence the hook record cannot carry.
func TestPollBackgroundChipHoldsPending(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady})

	require.Equal(t, PaneBackground, s.Poll(),
		"ready + empty set + a footer chip is background work, not a finished turn")
}

// And it clears itself: no watchdog, no latch — the chip is re-scraped every poll, so the
// work exiting is the whole reconciliation.
//
// With the hook latched "ready" that lands on the FIRST tick, not after the grace: the
// ready+empty arm is the sole "done" authority and returns above the grace, resetting
// idleStreak on its way. Asserting tick 1 is what pins that — a loop to the cap would pass
// on tick 1 too and quietly stop proving anything if the fast path ever moved.
// TestPollBackgroundExitHoldsThroughTheGrace covers the other shape, where no ready latch
// exists and the grace does run.
func TestPollBackgroundChipClearsToIdle(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady})
	require.Equal(t, PaneBackground, s.Poll())

	c = backgroundPane(cleanIdleFooter)
	require.Equal(t, PaneIdle, s.Poll(), "chip gone + an authoritative ready latch → done at once")
}

// The chip outranks the marker-absent grace, which is a statement about a pane that has
// gone quiet rather than a read of the current frame. Ordered the other way, a chip that is
// STILL up matches the grace's own hold set (PaneBackground is in it, for the cleared-chip
// case) and reports working — flipping the row Pending → Running → Pending while nothing on
// the pane changes.
func TestPollVisibleChipOutranksTheGrace(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateWorking}) // no ready latch → grace territory
	require.Equal(t, PaneBackground, s.Poll())

	for range idleConfirmTicks + 1 {
		require.Equal(t, PaneBackground, s.Poll(),
			"a chip that never clears stays background; it must not churn through working")
	}
}

// A chip beside a LIVE busy marker is a running turn that also has a background job, not a
// finished one — #332 established that the interrupt hint and the chips render side by side.
// The marker is positive proof of work and must keep outranking the chip.
func TestPollMarkerOutranksBackground(t *testing.T) {
	c := backgroundPane(chipBusyFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady})

	require.Equal(t, PaneWorking, s.Poll(), "a live interrupt marker outranks a background chip")
}

// The animating spinner is the other statement about the MAIN turn, and it outranks the
// chip for the same reason the marker does. Two ticks: the spinner is only trusted while
// the pane moves.
func TestPollSpinnerOutranksBackground(t *testing.T) {
	c := strings.Join([]string{
		"● Working.",
		"",
		"✽ Unravelling… (14s · ↓ 4.6k tokens)",
		"",
		spinnerRule,
		"❯ ",
		spinnerRule,
		"  " + chipFooter,
	}, "\n")
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateWorking})
	s.Poll()

	c = strings.Replace(c, "(14s ·", "(15s ·", 1) // the pane animates
	require.Equal(t, PaneWorking, s.Poll(), "an animating spinner outranks a background chip")
}

// A non-empty in-flight set keeps its own state even with a chip up. Both map to
// session.Pending so the row is identical, but only PanePending carries the wall-clock
// watchdog — and it must stay on the set, which is the thing that can leak.
func TestPollInflightOutranksBackground(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady, Inflight: []string{"aa"}})

	require.Equal(t, PanePending, s.Poll(), "a live in-flight set stays set-driven pending")
}

// A background shell that exits need not wake the agent — claude only notices when it reads
// BashOutput — so "chip vanishes, no working edge" is the common transition. The grace holds
// briefly rather than committing idle on the first quiet tick, which is what keeps a firing
// Monitor (whose last working edge may be hours old, far outside the heartbeat TTL) from
// blipping through Ready and ringing a false "finished".
func TestPollBackgroundExitHoldsThroughTheGrace(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateWorking})
	require.Equal(t, PaneBackground, s.Poll())

	c = backgroundPane(cleanIdleFooter)
	require.Equal(t, PaneWorking, s.Poll(), "the tick after the chip clears holds, it does not blip idle")
}

// PollNow re-implements the same precedence for the post-detach sweep. Without the demotion
// there, returning from an attach re-baselines a background-working session to Ready.
func TestPollNowBackground(t *testing.T) {
	c := backgroundPane(chipFooter)
	s := hookPollSession(t, "claude", &c)
	seedHookRecord(t, s, hookRecord{State: hookStateReady})

	require.Equal(t, PaneBackground, s.PollNow(), "the detach sweep classifies background work too")

	c = backgroundPane(cleanIdleFooter)
	require.Equal(t, PaneIdle, s.PollNow(), "and reports a genuinely finished turn at face value")
}

// Liveness is what bounds a stuck background hold in place of a watchdog: the has-session
// probe runs before the capture, so a dead pane can never sit Background.
func TestPollDeadSessionOutranksBackground(t *testing.T) {
	c := backgroundPane(chipFooter)
	deadExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("can't find session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(c), nil },
	}
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), deadExec)
	seedHookRecord(t, s, hookRecord{State: hookStateReady})

	require.Equal(t, PaneDead, s.Poll(), "a dead pane outranks a background chip")
	require.Equal(t, PaneDead, s.PollNow(), "PollNow agrees: liveness outranks the chip")
}
