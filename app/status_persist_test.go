package app

import (
	"context"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPersistHome builds a home whose metadata sweep can be driven directly, with a
// capturing store so saves are counted rather than written.
func newPersistHome(t *testing.T) (*home, *capturingStore, *session.Instance) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	_ = list.AddInstance(inst)

	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      list,
	}
	store := withCapturingStore(t, h)
	// The SetStatus above leaves the instance owing a write, which would put every
	// test below one save in debt before it starts. Settle it here so each asserts
	// only on the transitions it drives itself.
	h.flushStatusPersist(time.Now())
	store.saves = 0
	h.lastStatusPersist = time.Time{}
	return h, store, inst
}

// A status change writes state.json. This is the fix: before it, the file was
// written only by user and lifecycle events, so `atrium ls` served a status that had
// been true at some unknowable past moment — measured gaps of 5 and 26 minutes during
// active use, and about eight hours overnight.
func TestStatusChangePersists(t *testing.T) {
	h, store, inst := newPersistHome(t)

	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)

	require.Equal(t, session.Ready, inst.GetStatus(), "precondition: the sweep changed the status")
	assert.Equal(t, 1, store.saves, "a status change must reach disk")
}

// A sweep that changes nothing must not write. The tick runs twice a second for the
// life of the process; rewriting a 100KB file on every one of them would be a far
// worse bargain than the staleness this replaces.
func TestQuietSweepDoesNotPersist(t *testing.T) {
	h, store, inst := newPersistHome(t)

	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	require.Equal(t, 1, store.saves)

	// Same pane state twice: ApplyPaneState re-applies Ready, which is not a change.
	for range 5 {
		h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	}
	assert.Equal(t, 1, store.saves, "re-observing the same status must not rewrite the file")
}

// THE REASON THE DIRTY BIT LIVES ON THE INSTANCE. A status change applied by an
// observer other than this sweep — the off-cadence poll fired on every selection
// change (instancePolledMsg), or the attach keeper — is already in memory by the time
// the sweep runs. A sweep that detected changes by comparing each instance's status
// across its own ApplyPaneState would snapshot an "old" that is already the new value,
// see no change, and never write: the original staleness bug, in miniature and silent.
// Asking the instance what it owes catches it.
func TestStatusChangeAppliedOffTheSweepStillPersists(t *testing.T) {
	h, store, inst := newPersistHome(t)

	// Stand in for the selection poll's handler, which calls ApplyPaneState directly.
	inst.ApplyPaneState(tmux.PaneIdle)
	require.Equal(t, session.Ready, inst.GetStatus(), "precondition: the off-sweep observer moved it")
	require.Equal(t, 0, store.saves, "precondition: that path does not write for itself")

	// The next sweep re-observes the SAME pane state, so it applies no transition.
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)

	assert.Equal(t, 1, store.saves, "the sweep must still write the change it did not itself make")
}

// Bursts coalesce: a fleet resuming after a lull logs a dozen transitions inside one
// second, and each save rewrites the whole state file.
func TestStatusPersistCoalescesABurst(t *testing.T) {
	h, store, inst := newPersistHome(t)

	// Two changes inside one interval — Running→Ready, then Ready→Running.
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneWorking}}, false)

	require.Equal(t, session.Running, inst.GetStatus())
	assert.Equal(t, 1, store.saves, "a second change inside the interval must not fork a second write")
	assert.True(t, inst.StatusDirty(), "but it must be remembered")
}

// THE PART WORTH NOT LOSING. A burst's LAST transition is usually the interesting one
// — running→needs-input is what a fleet settles on — and it is exactly the one the
// coalescing interval suppresses. Without a trailing flush it would wait for the next
// unrelated change, reintroducing unbounded staleness at a smaller scale. So the next
// sweep to clear the interval writes it, whether or not anything changed on that sweep.
func TestSuppressedChangeIsWrittenByALaterQuietSweep(t *testing.T) {
	h, store, inst := newPersistHome(t)

	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneWorking}}, false)
	require.Equal(t, 1, store.saves)
	require.True(t, inst.StatusDirty())

	// Clear the interval without changing anything, then take a quiet sweep.
	h.lastStatusPersist = time.Now().Add(-2 * statusPersistInterval)
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneWorking}}, false)

	assert.Equal(t, 2, store.saves, "the suppressed change must land on a later sweep")
	assert.False(t, inst.StatusDirty(), "and the debt must clear once it has")
}

// The interval is a real gate, not a field that merely happens to be read: shrinking
// it lets through a second change the default second would have suppressed. This is
// what statusPersistInterval being a var rather than a const is for.
func TestStatusPersistHonoursTheInterval(t *testing.T) {
	h, store, inst := newPersistHome(t)
	original := statusPersistInterval
	t.Cleanup(func() { statusPersistInterval = original })

	statusPersistInterval = time.Nanosecond
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneWorking}}, false)

	assert.Equal(t, 2, store.saves, "with the interval shrunk, the second change writes immediately")
	assert.False(t, inst.StatusDirty())
}

// A write that fails must not be counted as done. The debt stays owed so the next
// sweep retries; clearing it would silently restore the staleness this replaces, on
// the one path — a full or read-only data dir — where the user has no other clue.
func TestFailedWriteIsRetriedNotDropped(t *testing.T) {
	h, _, inst := newPersistHome(t)
	st, err := session.NewStorage(failingStore{})
	require.NoError(t, err)
	h.storage = st

	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)
	require.Equal(t, session.Ready, inst.GetStatus(), "precondition: the sweep moved the status")
	assert.True(t, inst.StatusDirty(), "a failed write must leave the change owed")

	// The disk comes back; the next sweep past the interval writes what was owed,
	// without needing a fresh transition to re-arm it.
	store := withCapturingStore(t, h)
	h.lastStatusPersist = time.Now().Add(-2 * statusPersistInterval)
	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)

	assert.Equal(t, 1, store.saves, "the retry must happen on its own")
	assert.False(t, inst.StatusDirty())
}

// The very first change writes immediately rather than waiting out an interval it has
// no reference point for — a zero lastStatusPersist means "never written", not "just
// written".
func TestFirstStatusChangeIsNotDelayed(t *testing.T) {
	h, store, inst := newPersistHome(t)
	require.True(t, h.lastStatusPersist.IsZero(), "precondition")

	inst.SetStatus(session.NeedsInput)
	h.flushStatusPersist(time.Now())
	assert.Equal(t, 1, store.saves)
}

// A quiet sweep with nothing owed does nothing at all — the common case, twice a
// second, for as long as the fleet is idle.
func TestNothingPendingIsANoOp(t *testing.T) {
	h, store, inst := newPersistHome(t)
	require.False(t, inst.StatusDirty(), "precondition: nothing owed")

	for range 10 {
		h.flushStatusPersist(time.Now())
	}
	assert.Equal(t, 0, store.saves)
}

// The persisted payload carries the NEW status, not the one it replaced. A save that
// fires on the right edge but serializes the pre-change value would satisfy every
// count-based assertion above and still leave `atrium ls` one transition behind — the
// same symptom, from a different cause. Uses a started instance because SaveInstances
// filters unstarted ones out of the payload entirely.
func TestPersistedPayloadCarriesTheNewStatus(t *testing.T) {
	h, inst := newCaptureHome(t, newFrameSpy("output"))
	store := withCapturingStore(t, h)
	inst.SetStatus(session.Running)
	h.flushStatusPersist(time.Now()) // settle the debt that SetStatus just created
	store.saves = 0
	h.lastStatusPersist = time.Time{}
	require.True(t, inst.Started(), "precondition: an unstarted instance is not persisted at all")

	h.applyMetadataResults([]instanceMetaResult{{instance: inst, state: tmux.PaneIdle}}, false)

	require.Equal(t, session.Ready, inst.GetStatus())
	require.Equal(t, 1, store.saves)
	// Status serializes as its enum ordinal; Ready is 1 (Running is 0).
	assert.Contains(t, string(store.last), `"status":1`,
		"the write must carry the status the sweep just set")
}
