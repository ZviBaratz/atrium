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
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			Title: fmt.Sprintf("%s-%d", stem, i), Path: dir, Batch: batch,
			BatchSize: n, BatchIndex: i, Force: force,
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
	assert.Contains(t, reason, "3 sessions from this atrium new are still queued")
	assert.Contains(t, reason, "room for 2")
	assert.Contains(t, reason, "refused together")
	// Never "the whole batch was refused", which is a claim about members this drain may
	// never have seen: the count in the receipt is what is still PENDING, and a batch one
	// of whose members was already created carries a smaller number than the command line
	// asked for. TestCreateDrainRefusesTheRemainderWhenCapacityIsTakenMidBatch is that
	// batch; this assertion is what stops its receipt from asserting the opposite.
	assert.NotContains(t, reason, "whole batch")
	assert.NotContains(t, reason, "asked for 3 sessions")
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
		assert.Contains(t, reason, "3 sessions from this atrium new are still queued")
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
	for _, record := range records[1:] {
		reason := refusalFor(t, record)
		assert.Contains(t, reason, "max_sessions")
		// The receipt counts what is still queued — two — and never claims the whole
		// batch was refused, because one member of it is a live session with a worktree
		// and a branch. A script sizing its retry off "asked for 3" would free the wrong
		// number of slots and leak bake-1.
		assert.Contains(t, reason, "2 sessions from this atrium new are still queued")
		// Not "3", which is what the command line asked for. The limit's own "more than
		// 3 sessions" is in this same string, so the clause is named rather than the
		// number: a bare NotContains("3 sessions") would fail on the cap's own wording.
		assert.NotContains(t, reason, "3 sessions from this atrium new")
		assert.NotContains(t, reason, "whole batch")
	}
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

// spoolBatchMembers spools the members of one batch named by their 1-based indices,
// against a declared size — the shape a batch has while it is still being written, or
// after the drain has taken some of it. Separate from spoolBatchOf, which always writes a
// complete one, because "which members are on disk" is the only variable the assembly
// hold reads.
func spoolBatchMembers(t *testing.T, dir, batch, stem string, size int, created time.Time, idx ...int) []string {
	t.Helper()
	records := make([]string, 0, len(idx))
	for _, i := range idx {
		// Staggered per index, because writeRecord builds the record NAME from CreatedAt
		// and ListCreates is oldest-first: members sharing one timestamp are ordered by
		// their random nonce instead, so which member the walk gates would be a coin
		// toss and the head rule under test would be exercised at random.
		records = append(records, spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("%s-%d", stem, i), Path: dir, Batch: batch,
			BatchSize: size, BatchIndex: i,
			CreatedAt: created.Add(time.Duration(i) * time.Millisecond),
		}))
	}
	return records
}

// TestCreateDrainHoldsABatchThatIsStillArriving is the publish race, and it is written to
// fail on a CREATION rather than on a wrong verdict.
//
// A batch becomes visible one atomic rename at a time, so a tick landing mid-spool sees a
// batch smaller than the one being written. Charged for what it can see, the head of a
// three-member batch fits under a cap with room for one — and is created: the head of a
// batch the whole-batch gate would have refused, which is the outcome that gate exists to
// prevent. Held instead, nothing is decided until the batch has finished arriving.
func TestCreateDrainHoldsABatchThatIsStillArriving(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	arrived := spoolBatchMembers(t, dir, "b1", "bake", 3, time.Now(), 1, 2)

	assert.Nil(t, h.drainCreateRequests(), "an assembling batch is held, and a hold is not an outcome")
	assert.Equal(t, 1, h.list.NumInstances(), "nothing is created from a batch that is still arriving")
	assert.Equal(t, 2, createSpoolCount(t), "and nothing is answered either — both members wait")
	for _, record := range arrived {
		_, rejected := outbox.Rejection(record)
		assert.False(t, rejected, "a hold writes no receipt: the caller has not been refused yet")
	}

	// The last member lands, and the batch is judged as the batch it was written to be.
	rest := spoolBatchMembers(t, dir, "b1", "bake", 3, time.Now(), 3)
	refuseDrain(t, h)
	assert.Equal(t, 1, h.list.NumInstances())
	for _, record := range append(arrived, rest...) {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
	}
}

// TestCreateDrainGatesAnAssemblingBatchOnceItsWindowPasses is the other end of the hold. A
// batch that will NEVER reach its declared size — a rollback whose withdrawal failed, a
// sibling too corrupt to decode — must not wait for its TTL, so the hold is bounded and
// what is actually there is then charged.
func TestCreateDrainGatesAnAssemblingBatchOnceItsWindowPasses(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	stale := time.Now().Add(-createBatchAssemblyWindow - time.Minute)
	records := spoolBatchMembers(t, dir, "b1", "bake", 3, stale, 1, 2)

	refuseDrain(t, h)
	assert.Equal(t, 1, h.list.NumInstances())
	for _, record := range records {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
	}
}

// TestCreateDrainDoesNotHoldABatchMissingItsHead pins the half of batchStillAssembling a
// count alone cannot express.
//
// A batch is short of its declared size for most of its own life, because the drain builds
// one member per tick; holding for that would stall the feature rather than protect it.
// Members are written in order, so an incomplete batch still has its head, and a batch
// being consumed does not — which is what BatchIndex is read for. Here the head is gone
// and the tail is charged immediately.
func TestCreateDrainDoesNotHoldABatchMissingItsHead(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	records := spoolBatchMembers(t, dir, "b1", "bake", 3, time.Now(), 2, 3)

	refuseDrain(t, h)
	for _, record := range records {
		assert.Contains(t, refusalFor(t, record), "max_sessions")
	}
}

// TestCreateDrainChargesOneForAnAdoptingGatedMember is a regression guard, and the
// behaviour it pins is what this line did before batches existed.
//
// The batch charge exists to produce a refusal, and refusing an adopting request is
// destructive beyond the record: applyCreateClaim has already released its worktree
// registration and the recovery is one-shot, so the branch would be invisible to
// `atrium ls`, to `atrium reap` and to `git worktree list` permanently. Charged for its
// batch the head here is blocked; charged for itself it fits, which is the answer the
// same request got before it had siblings. refuseBatchSiblings makes the same exception
// for a sibling — this is the gated member's half of it, and #782 records what it costs.
func TestCreateDrainChargesOneForAnAdoptingGatedMember(t *testing.T) {
	h := drainHome(t)
	limit := 3
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	repo := gitRepoWithBranch(t, h.appConfig.BranchPrefix+"bake-1")
	adopting := adoptedRequest(t, h, "bake-1", repo)
	adopting.Batch, adopting.BatchSize, adopting.BatchIndex = "b1", 3, 1
	head := spoolCreate(t, adopting)
	for i := 2; i <= 3; i++ {
		spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("bake-%d", i), Path: repo, Batch: "b1", BatchSize: 3, BatchIndex: i,
		})
	}

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-1"),
		"charged for itself the adopting head fits; charged for its batch of three it would not")
	_, rejected := outbox.Rejection(head)
	assert.False(t, rejected, "and a fit is not a refusal, so its one-shot recovery is unspent")
}

// TestCreateDrainBatchRefusalKeepsAMarkedSiblingsAccount: whoever gave up on a member
// wrote the account of what it left behind — a branch, a worktree, a tmux session — and
// overwriting that with "the cap was full" would replace a report of real debris with a
// reason this drain invented for a request it never executed.
//
// The mark is on the LAST member, so the walk has not reached it when the head is gated:
// this is refuseBatchSiblings' own skip under test, not the walk's mark arm.
func TestCreateDrainBatchRefusalKeepsAMarkedSiblingsAccount(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	// Stale, because a marked member is invisible to the charge and so makes its batch
	// look short of its declared size — which is the assembly hold's cue, not this test's.
	stale := time.Now().Add(-createBatchAssemblyWindow - time.Minute)
	records := spoolBatchMembers(t, dir, "b1", "bake", 3, stale, 1, 2, 3)
	require.NoError(t, outbox.Disclose(records[2], &outbox.Disclosure{
		Title: "bake-3", Repo: dir, Branch: "zvi/bake-3",
		Reason: "an earlier atrium gave up after making the branch",
	}))

	refuseDrain(t, h)
	assert.Contains(t, refusalFor(t, records[0]), "max_sessions")
	assert.Contains(t, refusalFor(t, records[1]), "max_sessions")

	kept := refusalFor(t, records[2])
	assert.Contains(t, kept, "gave up after making the branch",
		"the marked member keeps the account whoever gave up on it wrote")
	assert.NotContains(t, kept, "max_sessions",
		"a batch refusal must not overwrite it with a reason this drain never executed")
}

// TestCreateDrainDoesNotRefuseAMemberItDroppedThisTick is the same skip from the other
// side, and it is the case the mark stat alone cannot cover.
//
// The walk's own mark arm drops a record and then calls clearMarkOverADroppedRecord — so a
// member dropped EARLIER in this tick reads NoDisclosure by the time a batch gated later
// fans its refusal out, and the stat that is supposed to protect it finds nothing. Only
// the walk knows, which is why it records what it answered.
func TestCreateDrainDoesNotRefuseAMemberItDroppedThisTick(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	stale := time.Now().Add(-createBatchAssemblyWindow - time.Minute)
	records := spoolBatchMembers(t, dir, "b1", "bake", 3, stale, 1, 2, 3)
	// On the FIRST member, so the walk drops it before it gates anything.
	require.NoError(t, outbox.Disclose(records[0], &outbox.Disclosure{
		Title: "bake-1", Repo: dir, Branch: "zvi/bake-1",
		Reason: "an earlier atrium gave up after making the branch",
	}))

	refuseDrain(t, h)
	dropped := refusalFor(t, records[0])
	assert.Contains(t, dropped, "gave up after making the branch")
	assert.NotContains(t, dropped, "max_sessions",
		"a member this tick already answered must not be answered again by its batch")
	assert.Equal(t, outbox.NoDisclosure, outbox.DisclosureMark(records[0]),
		"and no fresh mark is minted at a path whose record this tick destroyed")
	assert.Contains(t, refusalFor(t, records[1]), "max_sessions")
	assert.Contains(t, refusalFor(t, records[2]), "max_sessions")
}

// TestCreateDrainDoesNotChargeForAMemberItDroppedThisTick is the count's half of the same
// problem TestCreateDrainDoesNotRefuseAMemberItDroppedThisTick covers for the refusal.
//
// A member the walk gave up on earlier in this tick is still in the listing the fan-out is
// built from, and its mark is gone by then — clearMarkOverADroppedRecord ran. Counted, it
// charges the cap for a session nobody will ever create, and that over-charge is not the
// safe direction: the refusal it produces unlinks a record and writes a receipt, so it
// does not withhold a session, it destroys a request that had room for one (#701).
//
// Here the room is exact. Charged for the two members that are really pending the batch
// fits and the next one is created; charged for three it is refused, and the caller loses
// two sessions to a member that was already answered.
func TestCreateDrainDoesNotChargeForAMemberItDroppedThisTick(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit

	dir := t.TempDir()
	records := spoolBatchMembers(t, dir, "b1", "bake", 3, time.Now(), 1, 2, 3)
	require.NoError(t, outbox.Disclose(records[0], &outbox.Disclosure{
		Title: "bake-1", Repo: dir, Branch: "zvi/bake-1",
		Reason: "an earlier atrium gave up after making the branch",
	}))

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-2"),
		"charged for the two members still pending the batch fits; charged for three it does not")
	_, rejected := outbox.Rejection(records[1])
	assert.False(t, rejected)
}

// TestCreateDrainDoesNotChargeTheCapForAnAdoptingSibling: the charge and the refusal have
// to describe the same members. refuseBatchSiblings cannot answer an adopting member —
// refusing one spends a one-shot recovery — so counting it charges the cap for a member
// that is not refused with the batch, and is then admitted alone on its own tick.
//
// A cap of two with one live session is the arithmetic that tells them apart: charged for
// the pending member alone the plain head fits, charged for the batch it does not. Refused,
// it is destroyed and unlinked while the member its charge was for goes on to be created.
func TestCreateDrainDoesNotChargeTheCapForAnAdoptingSibling(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	dir := t.TempDir()
	plain := spoolCreate(t, outbox.Request{Title: "bake-1", Path: dir, Batch: "b1"})
	spoolCreate(t, outbox.Request{
		Title: "bake-2", Path: dir, Batch: "b1", Adopt: true, AdoptTip: "deadbeef",
	})

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-1"),
		"charged for the members that can be refused, the head fits in the room left")
	reason, rejected := outbox.Rejection(plain)
	assert.False(t, rejected, "so it is not destroyed for a sibling nobody will refuse: %s", reason)
}

// TestCreateDrainChargesOneForAGatedAdoptionWhosePinIsGone: the exception for an adopting
// member is about the record on disk, not about the copy the walk has been editing.
//
// recheckAdoption clears req.Adopt in place when the pin no longer holds, and that happens
// ABOVE the batch charge — so a copy-reading test of "is this member adopting" answers no
// for the one member whose refusal is still destructive. Its worktree registration was
// released before the crash that re-queued it, and rejectCreateRequest spends the single
// recovery whether or not the pin survived.
func TestCreateDrainChargesOneForAGatedAdoptionWhosePinIsGone(t *testing.T) {
	h := drainHome(t)
	limit := 2
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	repo := gitRepoWithBranch(t, "") // the pinned branch was deleted by hand
	req := adoptedRequest(t, h, "bake-1", repo)
	req.AdoptTip, req.Batch = strings.Repeat("0", 40), "b1"
	head := spoolCreate(t, req)
	spoolCreate(t, outbox.Request{Title: "bake-2", Path: repo, Batch: "b1"})

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-1"),
		"charged for itself the withdrawn adoption fits; charged for its batch it would not")
	reason, rejected := outbox.Rejection(head)
	assert.False(t, rejected, "and its one recovery is not spent on a cap it never crossed: %s", reason)
}

// TestPendingBatchMembersAlwaysCountsTheGatedMember pins the count's floor as a property of
// how the list is BUILT, because the alternative is an accept path past a hard cap.
//
// capVerdict tests count+adding <= Limit, so an adding of zero allows at exactly the cap —
// the one verdict hardCapMessage tells the caller exists nowhere. The gated member reaches
// the charge having already been judged by the walk; every filter here is a SECOND answer
// about it, and any of them disagreeing empties its batch. This one is handed a gated entry
// that fails all of them at once.
func TestPendingBatchMembersAlwaysCountsTheGatedMember(t *testing.T) {
	h := drainHome(t)
	gated := filepath.Join(t.TempDir(), "0000000000000000001-ab.json")
	// A zero CreatedAt is past every TTL, so this entry is expired, poisoned and already
	// answered — three of the four exclusions, without touching the disk for the fourth.
	entries := []outbox.CreateEntry{{Path: gated, Request: outbox.Request{Title: "bake-1", Batch: "b1"}}}
	h.outboxPoisoned = map[string]bool{gated: true}

	members, unreadable := h.pendingBatchMembers(
		entries, "b1", gated, time.Now(), map[string]bool{gated: true})

	require.Len(t, members, 1, "the gated member is counted whatever a re-judgement says")
	assert.Equal(t, gated, members[0].Path)
	assert.False(t, unreadable, "and it is not the member the batch cannot judge")
}

// TestCreateDrainDoesNotHoldABatchStampedInTheFuture: now.Sub is signed, so a head stamped
// ahead of this clock satisfies "younger than the window" for as long as it stays ahead —
// while Request.Expired, the same difference against the TTL, stays false for exactly as
// long. The hold is a continue above every disposal arm, so together they are a record that
// gets no session, no receipt and no verdict from this launch or any later one.
func TestCreateDrainDoesNotHoldABatchStampedInTheFuture(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	spoolBatchMembers(t, dir, "b1", "bake", 3, time.Now().Add(time.Hour), 1)

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "bake-1"),
		"a head the clock cannot place is charged for its real membership, not waited on")
}
