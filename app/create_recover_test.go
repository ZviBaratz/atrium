package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strandPrefix is the branch prefix these tests derive session branches under. An
// explicit value rather than config.DefaultConfig()'s, because the branch NAME is what
// several of these assertions turn on and reading it out of the code under test would
// make a wrong derivation agree with itself.
const strandPrefix = "zvi/"

// strand spools a request, claims it with meta, and returns the record path — a data
// dir in the state a process killed mid-Start leaves behind.
func strand(t *testing.T, r outbox.Request, m outbox.ClaimMeta) string {
	t.Helper()
	record, err := outbox.WriteCreate(r)
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(record, m))
	return record
}

// strandedIn is strand for the ordinary case: a request against repo whose session
// branch the interrupted build was about to mint, claimed when that branch did not
// exist.
func strandedIn(t *testing.T, title, repo string) string {
	t.Helper()
	return strand(t, outbox.Request{Title: title, Path: repo}, outbox.ClaimMeta{
		At: time.Now(), SessionBranch: strandPrefix + title,
	})
}

// sandboxSpool gives the test its own data dir, so the spool it writes is not the
// developer's (CLAUDE.md: tests must never reach the real data dir).
func sandboxSpool(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// recovered returns the request the reconcile put back in the spool, requiring there to
// be exactly one.
func recovered(t *testing.T) outbox.Request {
	t.Helper()
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1, "the reconcile must leave exactly one queued request")
	require.NoError(t, entries[0].Err)
	return entries[0].Request
}

func reconcile(t *testing.T, instances ...*session.Instance) int {
	t.Helper()
	return reconcileCreateClaims(context.Background(), instances, strandPrefix, time.Now())
}

// rowFrom builds a loaded session row, standing in for what LoadInstances hands
// newHome. record is the spool path it was created for ("" for a session that came
// from the form or a fork).
func rowFrom(t *testing.T, title, path, branch, record string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: path, Program: "echo"})
	require.NoError(t, err)
	inst.Branch = branch
	inst.CreateRequest = record
	return inst
}

// TestReconcileNoClaimsIsSilent: the steady state. Every launch but the one after a
// crash finds nothing, and must not write, probe or log for it.
func TestReconcileNoClaimsIsSilent(t *testing.T) {
	sandboxSpool(t)
	assert.Zero(t, reconcile(t))
}

// TestReconcileSettlesAClaimWhoseSessionWasRecorded is case 1, and the fix for the
// worst-sounding half of the old behaviour: the build finished and the row was
// persisted, and only the unlink was lost. The next launch used to re-read the request,
// find the title taken by the very session it had just made, and hand the caller
// "already used" — naming their own session as the obstacle.
//
// Settled as a SUCCESS: no receipt, both files gone, which is exactly what awaitSpool
// reads as done and what waitForCreate then confirms by reading the branch back.
func TestReconcileSettlesAClaimWhoseSessionWasRecorded(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "")
	record := strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t, rowFrom(t, "fix-auth", repo, strandPrefix+"fix-auth", record)))

	assertCreateSettled(t, record)
	reason, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a completed create is not a refusal: %s", reason)
}

// TestReconcileMatchesTheStampNotTheTitle is the negative control for the arm above,
// and the reason InstanceData carries a request path at all.
//
// A (Title, Path) match is the same comparison the drain's conflict gate already made
// and PASSED before the crash, so a row bearing that identity now is not evidence the
// crash produced it — someone may have created it since. Matching on it would report a
// stranger's session to a waiting --wait as the one it asked for, exit 0, and leave a
// script working in a session it does not own.
func TestReconcileMatchesTheStampNotTheTitle(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "")
	record := strandedIn(t, "fix-auth", repo)

	// Same title, same repo, different provenance.
	stranger := rowFrom(t, "fix-auth", repo, strandPrefix+"fix-auth", "")

	require.Equal(t, 1, reconcile(t, stranger))

	assert.NotEqual(t, claimSucceeded, mustClassify(t, record, stranger),
		"a row this request did not make must not settle it as a success")
}

// mustClassify re-reads the single stranded claim and returns the verdict for it, for
// tests that assert on the decision rather than on what it wrote.
func mustClassify(t *testing.T, record string, instances ...*session.Instance) claimVerdict {
	t.Helper()
	claims, err := outbox.ListClaims()
	require.NoError(t, err)
	for _, c := range claims {
		if c.Path == record {
			v, _ := classifyCreateClaim(context.Background(), c, instances, strandPrefix, time.Now())
			return v
		}
	}
	// Already applied: re-derive from what is on disk instead.
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	for _, e := range entries {
		if e.Path == record {
			if e.Request.Adopt {
				return claimAdopt
			}
			return claimRequeue
		}
	}
	if _, rejected := outbox.Rejection(record); rejected {
		return claimRefused
	}
	return claimSucceeded
}

// TestReconcileRequeuesAClaimThatBuiltNothing is case 2: the process died before
// Worktree.Setup made anything, so there is nothing to adopt and nothing in the way.
// Re-queued unmarked, so the ordinary gates judge it exactly as they would a fresh
// request.
func TestReconcileRequeuesAClaimThatBuiltNothing(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "") // no session branch: nothing was built
	record := strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	got := recovered(t)
	assert.Equal(t, "fix-auth", got.Title)
	assert.False(t, got.Adopt, "nothing was built, so nothing needs adopting")
	assert.NoFileExists(t, outbox.ClaimPath(record))
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a re-queue is not a refusal")
}

// TestReconcileAdoptsAnOrphanBranch is atrium#716 itself: Worktree.Setup created the
// branch and persistInstances never ran, so state.json has no row while the repo has a
// branch. That combination used to be a permanent refusal — "branch already exists",
// receipt written, record unlinked, so every retry under the same title met the same
// answer while the branch and worktree belonged to no row `atrium ls` could show.
func TestReconcileAdoptsAnOrphanBranch(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch) // the half-built session's own branch
	record := strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	got := recovered(t)
	assert.True(t, got.Adopt, "the orphan is this build's own work; finishing on it is the outcome asked for")
	assert.Equal(t, "fix-auth", got.Title)
	assert.NoFileExists(t, outbox.ClaimPath(record))
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected)
}

// TestReconcileRefusesABranchALiveSessionOwns is the adopt arm's first negative
// control. A branch that exists is not automatically an orphan: with a row holding it,
// adopting would put a second agent on one branch. The predicate that separates the two
// is "belongs to no loaded row", not "exists".
//
// Without this, "adopt every stranded request whose branch exists" scores full marks on
// the test above.
func TestReconcileRefusesABranchALiveSessionOwns(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	record := strandedIn(t, "fix-auth", repo)

	owner := rowFrom(t, "someone-else", repo, branch, "")

	require.Equal(t, 1, reconcile(t, owner))

	assertCreateSettled(t, record)
	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected, "the caller is owed the reason")
	assert.Contains(t, reason, branch)
	assert.Contains(t, reason, "someone-else", "and the session standing in the way")
}

// TestReconcileScopesBranchOwnershipByRepo is that predicate's own negative control. A
// branch name is unique only within one repository, so two sessions of the same title
// in two checkouts derive the same slug — and matching on the name alone would let an
// unrelated repo's live session veto this one's recovery, turning the fix back into the
// permanent refusal it replaced.
func TestReconcileScopesBranchOwnershipByRepo(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	record := strandedIn(t, "fix-auth", repo)

	elsewhere := rowFrom(t, "fix-auth", gitRepoWithBranch(t, branch), branch, "")

	require.Equal(t, 1, reconcile(t, elsewhere))

	assert.True(t, recovered(t).Adopt, "another repo's session does not own this repo's branch")
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "and must not refuse it on that session's account")
}

// TestReconcileRefusesABranchThatWasNotOurs is the adopt arm's second negative control,
// and the one that keeps BranchExisted honest as evidence rather than decoration.
//
// Adoption is a hole in a guard git.Worktree.Setup depends on: Setup reads a
// pre-existing branch as a resume, so a create that reaches it with a taken slug takes
// that branch's work. What licenses the hole is that the branch appeared AFTER the
// claim, which makes the interrupted build the only thing that can have made it. A
// claim recording that the branch was already there says the opposite, and must refuse.
func TestReconcileRefusesABranchThatWasNotOurs(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	record := strand(t, outbox.Request{Title: "fix-auth", Path: repo}, outbox.ClaimMeta{
		At: time.Now(), SessionBranch: branch, BranchExisted: true,
	})

	require.Equal(t, 1, reconcile(t))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected)
	assert.Contains(t, reason, "already existed")
	assert.Contains(t, reason, branch)
	assertCreateSettled(t, record)
}

// TestReconcileReadsTheRecordedBranchNotADerivedOne is why ClaimMeta carries the branch
// name at all. Recovery could recompute it from the title and the configured prefix —
// but the prefix is a config value, and one edited between the crash and this launch
// would have the probe look for a branch nobody made, read the orphan as "nothing was
// built", and create a SECOND session beside the first.
//
// Staged by reconciling under a prefix that disagrees with the claim's.
func TestReconcileReadsTheRecordedBranchNotADerivedOne(t *testing.T) {
	sandboxSpool(t)
	branch := "old-prefix/fix-auth"
	repo := gitRepoWithBranch(t, branch)
	strand(t, outbox.Request{Title: "fix-auth", Path: repo},
		outbox.ClaimMeta{At: time.Now(), SessionBranch: branch})

	// strandPrefix is deliberately NOT the prefix the branch was minted under.
	require.Equal(t, 1, reconcile(t))

	assert.True(t, recovered(t).Adopt,
		"the recorded branch is what was built; a re-derived one would miss the orphan")
}

// TestReconcileRequeuesADirectClaim: a direct (non-git) session has no branch, so
// nothing durable can be stranded whatever else the interrupted build got through.
func TestReconcileRequeuesADirectClaim(t *testing.T) {
	sandboxSpool(t)
	record := strand(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()},
		outbox.ClaimMeta{At: time.Now()}) // no SessionBranch

	require.Equal(t, 1, reconcile(t))

	assert.False(t, recovered(t).Adopt)
	assert.NoFileExists(t, outbox.ClaimPath(record))
}

// TestReconcileRefusesAnExpiredClaim. Expiry did not disappear when the claim took
// requests out of the drain's listing (see
// TestCreateDrainDoesNotExpireARequestItIsStillStarting) — it moved here, which is the
// one place that can tell an abandoned build from a running one. A request spooled two
// days ago names a branch point the tree has moved on from.
func TestReconcileRefusesAnExpiredClaim(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "")
	record := strand(t, outbox.Request{
		Title: "fix-auth", Path: repo, CreatedAt: time.Now().Add(-2 * outbox.TTL),
	}, outbox.ClaimMeta{At: time.Now(), SessionBranch: strandPrefix + "fix-auth"})

	require.Equal(t, 1, reconcile(t))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected)
	assert.Contains(t, reason, "horizon")
	assertCreateSettled(t, record)
}

// TestReconcileRefusesAnUndecodableClaim: nothing to match to a row or a branch, and a
// file nobody discards is re-read on every launch forever.
func TestReconcileRefusesAnUndecodableClaim(t *testing.T) {
	sandboxSpool(t)
	record := strandedIn(t, "fix-auth", gitRepoWithBranch(t, ""))
	require.NoError(t, config.WriteFileAtomic(outbox.ClaimPath(record), []byte("{not json"), 0o644))

	require.Equal(t, 1, reconcile(t))

	_, rejected := outbox.Rejection(record)
	assert.True(t, rejected, "an unreadable claim still owes its caller an answer")
	assertCreateSettled(t, record)
}

// TestReconcileSurvivesAClaimWithNoEvidence. outbox.Claim always writes the evidence
// block, so a claim without one is a hand-dropped file — and the alternative to
// checking is a nil dereference on the startup path, which takes the whole TUI down for
// a stray file in a spool directory.
func TestReconcileSurvivesAClaimWithNoEvidence(t *testing.T) {
	sandboxSpool(t)
	record, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	require.NoError(t, err)
	// Rename without enriching: a claim carrying no ClaimMeta.
	require.NoError(t, config.WriteFileAtomic(outbox.ClaimPath(record), mustRead(t, record), 0o644))
	require.NoError(t, outbox.Remove(record))

	require.Equal(t, 1, reconcile(t))

	assert.False(t, recovered(t).Adopt, "with no evidence there is no licence to adopt")
}

// TestReconcileLeavesEveryClaimSettled is the sweep-level invariant, and the one a
// per-case test cannot state: whatever a claim is, the reconcile must not leave it for
// the next launch to reach the same conclusion about — a claim that survives blocks its
// caller's --wait and is re-judged forever.
func TestReconcileLeavesEveryClaimSettled(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, strandPrefix+"orphan")
	clean := gitRepoWithBranch(t, "")

	strandedIn(t, "orphan", repo)   // adopt
	strandedIn(t, "nothing", clean) // re-queue
	done := strandedIn(t, "done", clean)
	strand(t, outbox.Request{Title: "old", Path: clean, CreatedAt: time.Now().Add(-2 * outbox.TTL)},
		outbox.ClaimMeta{At: time.Now()}) // expired

	require.Equal(t, 4, reconcile(t, rowFrom(t, "done", clean, strandPrefix+"done", done)))

	assert.Zero(t, createClaimCount(t), "no claim may survive a reconcile")
}

// TestNewHomeReconcilesStrandedClaims is the wiring, and nothing else asserts it: every
// test above calls reconcileCreateClaims directly, so all of them stay green against a
// build where newHome never calls it — and a reconcile nobody runs fixes nothing.
//
// It has to be newHome rather than Init, and that is also under test here: the verdict
// needs the loaded instances, and it must land before the first drain tick, which would
// otherwise judge the re-queued request by the ordinary gates and refuse the orphan.
func TestNewHomeReconcilesStrandedClaims(t *testing.T) {
	defer theme.Set(config.DefaultConfig().Theme)()
	t.Setenv("HOME", t.TempDir())

	repo := gitRepoWithBranch(t, config.DefaultConfig().BranchPrefix+"fix-auth")
	record := strand(t, outbox.Request{Title: "fix-auth", Path: repo}, outbox.ClaimMeta{
		At: time.Now(), SessionBranch: config.DefaultConfig().BranchPrefix + "fix-auth",
	})

	_, err := newHome(context.Background(), "echo", false, "v", "atr")
	require.NoError(t, err)

	assert.NoFileExists(t, outbox.ClaimPath(record), "the claim must be reconciled at construction")
	assert.True(t, recovered(t).Adopt, "and the orphan re-queued for the drain to finish")
}

// TestCreateRequestStampSurvivesAReload is the other half of the stamp, and the half
// that decides whether case 1 works at all: the reconcile reads it off instances
// LoadInstances rehydrated, so a value written to state.json but not read back makes
// every recorded session look like one this request did not make.
func TestCreateRequestStampSurvivesAReload(t *testing.T) {
	data := session.InstanceData{
		Title: "fix-auth", Path: "/repo/web", Program: "echo",
		CreateRequest: "/data/outbox/create/0000000000000000001-abcd.json",
	}
	inst, err := session.FromInstanceData(context.Background(), data, strandPrefix)
	require.NoError(t, err)
	assert.Equal(t, data.CreateRequest, inst.CreateRequest)
	assert.Equal(t, data.CreateRequest, inst.ToInstanceData().CreateRequest,
		"and round-trips, so a reload does not erase it from the next save")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
