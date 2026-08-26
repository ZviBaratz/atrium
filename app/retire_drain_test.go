package app

import (
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolRetire writes a retirement into the spool and returns its record path.
func spoolRetire(t *testing.T, r outbox.Retire) string {
	t.Helper()
	record, err := outbox.WriteRetire(r)
	require.NoError(t, err)
	return record
}

func killRecord(t *testing.T, title, path string) string {
	t.Helper()
	return spoolRetire(t, outbox.Retire{Title: title, Path: path, Mode: outbox.ModeKill})
}

// retireRecords is what the spool still holds, so a test can say "the record was
// answered" and "the record is still queued" as different things.
func retireRecords(t *testing.T) []outbox.RetireEntry {
	t.Helper()
	entries, err := outbox.ListRetires()
	require.NoError(t, err)
	return entries
}

// rejectionFor is the reason a producer blocked in --wait would read back.
func rejectionFor(t *testing.T, record string) string {
	t.Helper()
	reason, ok := outbox.Rejection(record)
	require.True(t, ok, "the record must leave a receipt, or --wait reads the unlink as success")
	return reason
}

// retirable makes inst look like the one shape a kill clears: idle, and carrying
// computed stats that show nothing at risk.
func retirable(inst *session.Instance) {
	inst.SetStatus(session.Ready)
	inst.SetDiffStats(&git.DiffStats{})
}

// TestRetireDrainDispatchesAKillForACleanIdleSession is the happy path, observed
// where it can be observed without a real teardown: the record is consumed and the
// instance is marked retiring, which is what armTeardown does and what keeps the
// poll loop from reading the dying pane as a lost session.
func TestRetireDrainDispatchesAKillForACleanIdleSession(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	killRecord(t, "fix-auth", "/repo/web")

	cmd := h.drainRetireRequests()

	require.NotNil(t, cmd, "a dispatched teardown returns a command to run it")
	assert.True(t, h.retiring[inst], "the row must be marked retiring for the length of the teardown")
	assert.Empty(t, retireRecords(t), "the record is consumed, not left for the next tick")
}

// TestRetireDrainDispatchesAPauseWithoutGating: pause is the escape valve, so the
// drain must not re-gate it either. A session with uncommitted work and a working
// agent is exactly what an orchestrator reaches for pause to reclaim.
func TestRetireDrainDispatchesAPauseWithoutGating(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	inst.SetStatus(session.Running)
	inst.SetDiffStats(&git.DiffStats{Dirty: true, Unpushed: 4})
	spoolRetire(t, outbox.Retire{Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModePause})

	cmd := h.drainRetireRequests()

	require.NotNil(t, cmd)
	assert.True(t, h.actionInFlight,
		"a pause must run behind the busy gate: its pane dies seconds before the status flips")
	assert.Empty(t, retireRecords(t))
}

// TestRetireDrainRegatesBeforeTearingDown is the TOCTOU half of the gate, and the
// reason the producer's check is not enough on its own. At least a poll tick passes
// between the spool and this walk, and the target agent keeps working through it — so
// a session that was clean when `atrium kill` looked can be dirty by now.
func TestRetireDrainRegatesBeforeTearingDown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status session.Status
		stats  *git.DiffStats
		says   string
	}{
		{"went dirty", session.Ready, &git.DiffStats{Dirty: true}, "uncommitted"},
		{"has unpushed commits", session.Ready, &git.DiffStats{Unpushed: 2}, "2 unpushed"},
		{"started working again", session.Running, &git.DiffStats{}, "still working"},
		{"has background work outstanding", session.Pending, &git.DiffStats{}, "still working"},
		{"is still starting up", session.Loading, &git.DiffStats{}, "still working"},
		{"has no computed stats at all", session.Ready, nil, "could not be established"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			inst := addInstance(t, h, "fix-auth", "/repo/web")
			inst.SetStatus(tc.status)
			inst.SetDiffStats(tc.stats)
			record := killRecord(t, "fix-auth", "/repo/web")

			cmd := h.drainRetireRequests()

			assert.False(t, h.retiring[inst], "a refused kill must leave the session alone")
			assert.Contains(t, rejectionFor(t, record), tc.says)
			assert.Empty(t, retireRecords(t))
			_ = cmd // a refusal may still return a notice command
		})
	}
}

// TestRetireDrainRefusesASessionItCannotFind: a session killed between the spool and
// this tick is the realistic case, and the receipt has to say so — otherwise
// `atrium kill --wait` reads the unlink as a successful teardown of a session that
// something else retired.
func TestRetireDrainRefusesASessionItCannotFind(t *testing.T) {
	h := drainHome(t)
	record := killRecord(t, "gone", "/repo/web")

	h.drainRetireRequests()

	assert.Contains(t, rejectionFor(t, record), "no session")
	assert.Empty(t, retireRecords(t))
}

// TestRetireDrainMatchesOnTheIdentityPairNotTheTitle: titles are unique only within a
// repo group, so a record naming one repo must never retire the same-titled session
// in another. This is the assertion that fails if the walk ever matches on Title.
func TestRetireDrainMatchesOnTheIdentityPairNotTheTitle(t *testing.T) {
	h := drainHome(t)
	other := addInstance(t, h, "fix-auth", "/repo/other")
	retirable(other)
	record := killRecord(t, "fix-auth", "/repo/web")

	h.drainRetireRequests()

	assert.False(t, h.retiring[other], "a session in a different repo is not the target")
	assert.Contains(t, rejectionFor(t, record), "no session")
}

// TestRetireDrainDiscardsAnExpiredRecord: a retirement spooled a day ago describes a
// session that has moved on, so acting on it is worse than dropping it.
func TestRetireDrainDiscardsAnExpiredRecord(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	record := spoolRetire(t, outbox.Retire{
		Title: "fix-auth", Path: "/repo/web", Mode: outbox.ModeKill,
		CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})

	h.drainRetireRequests()

	assert.False(t, h.retiring[inst], "an expired record must not tear anything down")
	assert.Contains(t, rejectionFor(t, record), "horizon")
}

// TestRetireDrainHoldsWhileATeardownIsInFlight: one teardown at a time. A kill is
// several subprocesses plus a recursive worktree delete, and a second dispatched
// underneath it would race the first for the same list and storage.
func TestRetireDrainHoldsWhileATeardownIsInFlight(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	h.retiring = map[*session.Instance]bool{addInstance(t, h, "other", "/repo/web"): true}
	record := killRecord(t, "fix-auth", "/repo/web")

	assert.Nil(t, h.drainRetireRequests(), "the tick holds rather than acting")
	assert.False(t, h.retiring[inst])
	entries := retireRecords(t)
	require.Len(t, entries, 1, "a held record stays queued")
	assert.Equal(t, record, entries[0].Path)
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "holding is not refusing — no receipt")
}

// TestRetireDrainHoldsWhileAConfirmationIsOpen closes the gap none of the four
// inherited holds cover. confirmKill captures its instance when the dialog is staged
// and nothing marks that instance retiring until the dialog is accepted, so a
// teardown dispatched underneath an open kill dialog leaves its accept to act on a
// session that is already gone.
func TestRetireDrainHoldsWhileAConfirmationIsOpen(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "fix-auth", "/repo/web")
	retirable(inst)
	h.state = stateConfirm
	killRecord(t, "fix-auth", "/repo/web")

	assert.Nil(t, h.drainRetireRequests())
	assert.False(t, h.retiring[inst])
	assert.Len(t, retireRecords(t), 1, "the record waits for the dialog")
}

// TestRetireDrainDispatchesOneTeardownPerTick: refusals are cheap and can all be
// answered at once, but a teardown is seconds of I/O. The second record must be left
// queued rather than dispatched alongside the first.
func TestRetireDrainDispatchesOneTeardownPerTick(t *testing.T) {
	h := drainHome(t)
	first := addInstance(t, h, "fix-auth", "/repo/web")
	second := addInstance(t, h, "add-cache", "/repo/web")
	retirable(first)
	retirable(second)
	killRecord(t, "fix-auth", "/repo/web")
	killRecord(t, "add-cache", "/repo/web")

	require.NotNil(t, h.drainRetireRequests())

	assert.True(t, h.retiring[first], "the older record goes first")
	assert.False(t, h.retiring[second])
	entries := retireRecords(t)
	require.Len(t, entries, 1, "the second record waits for the next tick")
	assert.Equal(t, "add-cache", entries[0].Retire.Title)
}

// TestRetireDrainAnswersEveryRefusalOnOneTick is the other side of that budget: a
// backlog of records the drain will never act on must not take one tick each, or a
// spool full of stale requests starves the one good record behind them.
func TestRetireDrainAnswersEveryRefusalOnOneTick(t *testing.T) {
	h := drainHome(t)
	first := killRecord(t, "gone-a", "/repo/web")
	second := killRecord(t, "gone-b", "/repo/web")
	third := killRecord(t, "gone-c", "/repo/web")

	h.drainRetireRequests()

	assert.Empty(t, retireRecords(t))
	for _, record := range []string{first, second, third} {
		assert.Contains(t, rejectionFor(t, record), "no session")
	}
}

// TestRetireDrainDiscardsAnUnreadableRecord: a file nobody can decode and nobody
// deletes is re-read on every tick forever. ListRetires only ever surfaces files
// matching the spool's own name format, so this can only discard our own.
func TestRetireDrainDiscardsAnUnreadableRecord(t *testing.T) {
	h := drainHome(t)
	record := killRecord(t, "fix-auth", "/repo/web")
	require.NoError(t, os.WriteFile(record, []byte(`{not json`), 0o644))

	h.drainRetireRequests()

	assert.Empty(t, retireRecords(t))
	assert.Contains(t, rejectionFor(t, record), "could not be read")
}
