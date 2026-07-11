package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStatus_String pins the log/diagnostic rendering of each status word.
func TestStatus_String(t *testing.T) {
	for s, want := range map[Status]string{
		Running:    "running",
		Ready:      "ready",
		Loading:    "loading",
		Paused:     "paused",
		NeedsInput: "needs-input",
		Status(99): "unknown",
	} {
		require.Equal(t, want, s.String())
	}
}

// TestStatusHistory_RecordsRealTransitions asserts every From≠To change lands in the
// ring buffer in order, and a repeated same-status write (the idle poll tick) does not.
func TestStatusHistory_RecordsRealTransitions(t *testing.T) {
	inst := &Instance{Title: "s", status: Running}

	inst.SetStatus(Ready)      // Running → Ready
	inst.SetStatus(NeedsInput) // Ready → NeedsInput
	inst.SetStatus(NeedsInput) // no-op: same status, not recorded
	inst.SetStatus(Running)    // NeedsInput → Running

	hist := inst.StatusHistory()
	require.Len(t, hist, 3, "only the three real transitions are recorded")
	require.Equal(t, StatusTransition{From: Running, To: Ready}.From, hist[0].From)
	require.Equal(t, Ready, hist[0].To)
	require.Equal(t, Ready, hist[1].From)
	require.Equal(t, NeedsInput, hist[1].To)
	require.Equal(t, NeedsInput, hist[2].From)
	require.Equal(t, Running, hist[2].To)
	for _, tr := range hist {
		require.False(t, tr.At.IsZero(), "each transition is timestamped")
	}
}

// TestStatusChangedAt_StampsOnChangeNotOnNoOp asserts the clock advances on a real
// transition and is left untouched by a repeated same-status write, so a session that
// stays idle across many poll ticks keeps a stable "held since" time.
func TestStatusChangedAt_StampsOnChangeNotOnNoOp(t *testing.T) {
	inst := &Instance{Title: "s", status: Running}
	require.True(t, inst.StatusChangedAt().IsZero(), "unset before the first SetStatus")

	inst.SetStatus(Ready)
	t1 := inst.StatusChangedAt()
	require.False(t, t1.IsZero(), "a real change stamps the clock")

	inst.SetStatus(Ready) // idle poll tick: same status
	require.Equal(t, t1, inst.StatusChangedAt(), "a no-op change must not advance the clock")

	inst.SetStatus(Running)
	require.False(t, inst.StatusChangedAt().Before(t1), "a later change advances the clock forward")
}

// TestStatusChangedAt_FirstObservationStampsClock asserts that even a first write that
// matches the zero-value status (Running) stamps the clock, so StatusChangedAt is
// meaningful from launch rather than reading as the zero time.
func TestStatusChangedAt_FirstObservationStampsClock(t *testing.T) {
	inst := &Instance{Title: "s"} // zero-value status == Running
	inst.SetStatus(Running)
	require.False(t, inst.StatusChangedAt().IsZero(), "first observation stamps the clock")
	require.Empty(t, inst.StatusHistory(), "a Running→Running first write records no transition")
}

// TestStatusHistory_RingBufferBounded asserts the buffer is capped at statusHistoryMax,
// dropping the oldest entries and keeping the newest transition.
func TestStatusHistory_RingBufferBounded(t *testing.T) {
	inst := &Instance{Title: "s", status: Running}
	// Alternate Ready/Running well past the cap; each write is a real transition.
	toggles := statusHistoryMax*2 + 5
	for n := 0; n < toggles; n++ {
		if n%2 == 0 {
			inst.SetStatus(Ready)
		} else {
			inst.SetStatus(Running)
		}
	}
	hist := inst.StatusHistory()
	require.Len(t, hist, statusHistoryMax, "the ring buffer is bounded")
	last := hist[len(hist)-1]
	// toggles is odd, so the final index (toggles-1) is even → the last write is Ready,
	// transitioning Running→Ready.
	require.Equal(t, Running, last.From)
	require.Equal(t, Ready, last.To)
}

// TestStatusHistory_ReturnsCopy asserts callers cannot mutate the instance's internal
// history through the returned slice.
func TestStatusHistory_ReturnsCopy(t *testing.T) {
	inst := &Instance{Title: "s", status: Running}
	inst.SetStatus(Ready)
	got := inst.StatusHistory()
	require.Len(t, got, 1)
	got[0].To = Paused // scribble on the copy

	require.Equal(t, Ready, inst.StatusHistory()[0].To, "internal history is insulated from caller mutation")
}
