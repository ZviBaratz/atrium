package app

// create_drain_batch_test.go — the whole-batch session cap (#761).
//
// A fan-out spools N ordinary records sharing a batch id. Every gate below the cap
// stays per member and is covered in create_drain_test.go; what is here is the one gate
// a batch is charged for as a batch, and the fan-out of its refusal — which is the half
// a suite that only checked the charge would let rot, because the charge alone still
// creates a batch from its tail.

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolBatchOf spools n members of one batch against dir, returning their record paths
// in variant order — the shape `atrium new --variants` writes.
func spoolBatchOf(t *testing.T, dir, batch, stem string, n int, force bool) []string {
	t.Helper()
	records := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		records = append(records, spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("%s-%d", stem, i), Path: dir, Batch: batch, Force: force,
		}))
	}
	return records
}

// refusalFor is the receipt a member was left, or a failure naming the member that has
// none — "no receipt" is the outcome a caller's --wait sits through until its deadline,
// so it must never be reported as an ordinary assertion miss.
func refusalFor(t *testing.T, record string) string {
	t.Helper()
	reason, ok := outbox.Rejection(record)
	require.True(t, ok, "every member of a refused batch is owed a receipt: %s", record)
	return reason
}

// TestCreateDrainRefusesAWholeBatchOverTheCap is #761's second criterion, and it is
// written to fail on a PARTIAL spawn rather than on a wrong verdict.
//
// Charged for itself, each member of a three-session batch fits under a cap of two with
// one live session — so a per-variant charge creates the first, and the second, and only
// then refuses. Charged for the batch, none of them is created and all three are
// answered.
func TestCreateDrainRefusesAWholeBatchOverTheCap(t *testing.T) {
	h := drainHome(t)
	limit := 3
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)
	refuseDrain(t, h)

	assert.Equal(t, 1, h.list.NumInstances(),
		"nothing is created: a batch that does not fit is refused whole")
	for _, record := range records {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
	}
	assert.Equal(t, 0, createSpoolCount(t), "and no member is left to be re-gated later")
	assert.Equal(t, 0, createClaimCount(t))
}

// TestCreateDrainBatchRefusalNamesTheBatch: a receipt saying only "you can't create more
// than 3 sessions" cannot be told from a refusal of one variant, and the caller's next
// move differs — free two slots, or free one.
func TestCreateDrainBatchRefusalNamesTheBatch(t *testing.T) {
	h := drainHome(t)
	limit := 3
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)
	refuseDrain(t, h)

	reason := refusalFor(t, records[0])
	assert.Contains(t, reason, "asked for 3 sessions")
	assert.Contains(t, reason, "room for 2")
	assert.Contains(t, reason, "whole batch")
}

// TestCreateDrainRefusesAWholeBatchOverTheSoftCap: the host-derived cap has an accept
// path, and it has to be offered for the batch rather than for a variant — a caller told
// to pass --force for one session would get the same refusal again for the rest.
func TestCreateDrainRefusesAWholeBatchOverTheSoftCap(t *testing.T) {
	h := drainHome(t)
	h.hostCap = 3 // soft: max_sessions unset
	addInstance(t, h, "already-here", t.TempDir())

	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)
	refuseDrain(t, h)

	assert.Equal(t, 1, h.list.NumInstances())
	for _, record := range records {
		reason := refusalFor(t, record)
		assert.Contains(t, reason, "--force")
		assert.Contains(t, reason, "asked for 3 sessions")
	}
}

// TestCreateDrainForceCrossesTheSoftCapForAWholeBatch is the other half: --force is one
// answer for the whole batch, so the members that follow the first are not asked again.
func TestCreateDrainForceCrossesTheSoftCapForAWholeBatch(t *testing.T) {
	h := drainHome(t)
	h.hostCap = 1
	dir := t.TempDir()
	spoolBatchOf(t, dir, "b1", "bake", 2, true)

	// One member per tick: the start budget is on concurrency, so each has to settle
	// before the next is gated.
	require.NotNil(t, h.drainCreateRequests())
	require.NotNil(t, titled(h, "bake-1"))
	h.settleCreateRequest(titled(h, "bake-1"), nil)

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-2"), "--force answered the cap for the batch, not for a variant")
}

// TestCreateDrainRefusesOnlyTheMemberAConflictBelongsTo pins the scope of "whole": the
// cap is a fact about the fleet and is shared, while a taken title is a fact about one
// variant. Fanning a title conflict out would refuse two perfectly good sessions.
func TestCreateDrainRefusesOnlyTheMemberAConflictBelongsTo(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	addInstance(t, h, "bake-2", dir)
	records := spoolBatchOf(t, dir, "b1", "bake", 3, false)

	require.NotNil(t, h.drainCreateRequests())
	require.NotNil(t, titled(h, "bake-1"))
	h.settleCreateRequest(titled(h, "bake-1"), nil)

	refuseDrain(t, h)
	assert.Contains(t, refusalFor(t, records[1]), "bake-2")
	_, rejected := outbox.Rejection(records[2])
	assert.False(t, rejected, "the third member is not answered by the second's conflict")

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-3"), "and is created on its own tick")
}

// TestCreateDrainChargesTheCapForWhatIsStillPending: as members are created the charge
// shrinks with them, so a batch that fit when it was gated still fits at its last
// member. Without that, the second tick would charge for the whole batch again against a
// count that has already grown by one and refuse a batch it just admitted.
func TestCreateDrainChargesTheCapForWhatIsStillPending(t *testing.T) {
	h := drainHome(t)
	limit := 3
	h.appConfig.MaxSessions = &limit
	dir := t.TempDir()
	spoolBatchOf(t, dir, "b1", "bake", 3, false)

	for _, title := range []string{"bake-1", "bake-2", "bake-3"} {
		require.NotNil(t, h.drainCreateRequests(), title)
		require.NotNil(t, titled(h, title), "%s must be created", title)
		h.settleCreateRequest(titled(h, title), nil)
	}
	assert.Equal(t, 3, h.list.NumInstances())
}

// TestCreateDrainRefusesTheRemainderWhenCapacityIsTakenMidBatch is the residue of
// charging live rather than remembering a verdict, and it is the behaviour this repo
// wants: an explicit max_sessions has no accept path anywhere, so a batch that fitted
// when it started and does not now is refused for the rest — as a unit, with a receipt
// each, rather than creating variants until the cap closes.
func TestCreateDrainRefusesTheRemainderWhenCapacityIsTakenMidBatch(t *testing.T) {
	h := drainHome(t)
	limit := 3
	h.appConfig.MaxSessions = &limit
	dir := t.TempDir()
	records := spoolBatchOf(t, dir, "b1", "bake", 3, false)

	require.NotNil(t, h.drainCreateRequests())
	require.NotNil(t, titled(h, "bake-1"))
	h.settleCreateRequest(titled(h, "bake-1"), nil)

	// Somebody at the keyboard takes the room the rest of the batch was counting on.
	addInstance(t, h, "from-the-keyboard", t.TempDir())

	refuseDrain(t, h)
	assert.Nil(t, titled(h, "bake-2"))
	assert.Nil(t, titled(h, "bake-3"))
	assert.Contains(t, refusalFor(t, records[1]), "max_sessions")
	assert.Contains(t, refusalFor(t, records[2]), "max_sessions")
}

// TestCreateDrainBatchRefusalLeavesAnAdoptSiblingAlone.
//
// Refusing an adopting request is destructive beyond the record: its worktree
// registration has already been released and the recovery is one-shot, which is why the
// gated member's own path re-checks the pin before it refuses. A sibling swept up by the
// fan-out has had no such re-check, so it is left for its own gate.
func TestCreateDrainBatchRefusalLeavesAnAdoptSiblingAlone(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	plain := spoolCreate(t, outbox.Request{Title: "bake-1", Path: dir, Batch: "b1"})
	adopting := spoolCreate(t, outbox.Request{
		Title: "bake-2", Path: dir, Batch: "b1", Adopt: true, AdoptTip: "deadbeef",
	})

	refuseDrain(t, h)
	assert.Contains(t, refusalFor(t, plain), "max_sessions")
	_, rejected := outbox.Rejection(adopting)
	assert.False(t, rejected,
		"an adopting member keeps its one-shot recovery until its own gate re-checks the pin")
	assert.FileExists(t, adopting)
}

// TestCreateDrainSkipsAPoisonedMemberInTheBatchCharge: a poisoned record's session is
// already being built and already counts toward the cap, and the poisoning lasts for the
// life of the process — so counting it would over-charge every later member of that
// batch on every tick until the record's TTL.
func TestCreateDrainSkipsAPoisonedMemberInTheBatchCharge(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	dir := t.TempDir()
	records := spoolBatchOf(t, dir, "b1", "bake", 2, false)

	// Stand in for a claim that could not be renamed: the record is still in the spool
	// and its session is already in the list.
	h.outboxPoisoned = map[string]bool{records[0]: true}
	addInstance(t, h, "bake-1", dir)

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-2"),
		"charged for one pending member, the survivor fits; charged for two it would not")
}

// TestCreateDrainBatchRefusalRaisesTheBatchNotice: the person at the TUI is the one who
// can raise a cap, and a refusal that answered five requests reading as "a create
// request" understates what just happened to them.
func TestCreateDrainBatchRefusalRaisesTheBatchNotice(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())
	spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)

	require.NotNil(t, h.drainCreateRequests())
	assert.Contains(t, h.menu.NoticeText(), "batch of create requests")
}

// TestCreateDrainSingleRefusalKeepsTheSingularNotice is the negative control for the
// one above: an ordinary refusal must not start reading as a batch, or the batch
// spelling stops carrying information.
func TestCreateDrainSingleRefusalKeepsTheSingularNotice(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	notice := h.menu.NoticeText()
	assert.Contains(t, notice, "refused a create request")
	assert.NotContains(t, notice, "batch")
}

// TestCreateDrainBoundsABatchRefusalPerTick: nothing on the wire says how big a batch
// is, so a hand-written one can exceed anything this atrium's own CLI would mint.
// Answering all of it inside one Update is the freeze createDisposalBudget exists to
// prevent — and spreading the remainder over later ticks is safe here in a way spreading
// a creation would not be, because a refusal is never a spawn and each later tick
// re-charges the cap for what is still pending.
func TestCreateDrainBoundsABatchRefusalPerTick(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	oversized := createBatchRefusalBudget + 5
	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", oversized, false)

	require.NotNil(t, h.drainCreateRequests())
	answered := 0
	for _, record := range records {
		if _, ok := outbox.Rejection(record); ok {
			answered++
		}
	}
	assert.Equal(t, createBatchRefusalBudget+1, answered,
		"the gated member plus one budget's worth of siblings")
	assert.Equal(t, 1, h.list.NumInstances(),
		"and nothing is created while the rest wait: only the session seeded above is there")

	// The remainder is answered on later ticks and never created. Bounded so a batch
	// that stopped converging fails here rather than hanging.
	for i := 0; i < oversized && createSpoolCount(t) > 0; i++ {
		h.drainCreateRequests()
	}
	assert.Equal(t, 0, createSpoolCount(t), "every member is answered in the end")
	assert.Equal(t, 1, h.list.NumInstances())
}

// TestCreateDrainBatchRefusalStillSpendsOneGate.
//
// The gate budget is on git — five subprocess round trips before a verdict exists — and
// a batch refusal reaches N records on one verdict, so it must not become a way to run
// the gates twice in a tick. TestCreateDrainGivesOneTickOneGateOutcome makes this claim
// for two unrelated singletons and would pass unchanged over a batch that broke it,
// which is why the case is spelled out here rather than left to it.
func TestCreateDrainBatchRefusalStillSpendsOneGate(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)
	unrelated := spoolCreate(t, outbox.Request{Title: "fresh", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	for _, record := range records {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
	}
	_, rejected := outbox.Rejection(unrelated)
	assert.False(t, rejected, "the tick's one gate evaluation was spent on the batch")
	assert.FileExists(t, unrelated)
}

// TestCreateDrainDoesNotReJudgeTheSiblingsItJustRefused.
//
// The listing a tick walks is taken before any refusal in it, so a batch's siblings are
// still in it after the fan-out has answered them. Reaching one again finds the terminal
// mark the fan-out just wrote and takes the arm meant for a request an EARLIER launch
// gave up on: it re-writes the receipt — over one the caller may already have read and
// cleared — spends disposal budget undoing this tick's own work, and logs that atrium
// "had already given up on" a refusal it is still in the middle of making.
//
// The mark surviving the tick is what says the arm was not taken: that arm clears it.
func TestCreateDrainDoesNotReJudgeTheSiblingsItJustRefused(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	records := spoolBatchOf(t, t.TempDir(), "b1", "bake", 3, false)
	refuseDrain(t, h)

	for _, record := range records {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
		assert.Equal(t, outbox.HasDisclosure, outbox.DisclosureMark(record),
			"the mark this tick wrote is left for the next launch's flush, as every "+
				"inventory-less mark is — clearing it here means the walk re-judged the member")
	}
}
