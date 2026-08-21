package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strandPrefix is the branch prefix these tests derive session branches under. An
// explicit value rather than config.DefaultConfig()'s, because the branch NAME is what
// several of these assertions turn on and reading it out of the code under test would
// make a wrong derivation agree with itself.
//
// It follows that a fixture which also starts a REAL session must not use both: that
// session's branch comes from the real derivation, which is the lowercased OS username, so
// the two agree on one machine and nowhere else. Read it off the instance there —
// TestReconcileRefusesWhileTheAgentIsStillRunning is the one such fixture.
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
	return reconcileCreateClaims(context.Background(), instances, time.Now())
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
			return classifyCreateClaim(context.Background(), c, instances, time.Now()).verdict
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

// worktreeOn checks branch out at a worktree under path, standing in for what
// `git worktree add` leaves when the process building a session is killed after it.
func worktreeOn(t *testing.T, repo, branch, at string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "worktree", "add", at, branch)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git worktree add: %s", out)
	return at
}

// TestReconcileFreesTheOrphanWorktreeBeforeRequeueing is the half a branch check cannot
// see, and only a live kill surfaced: an interrupted build leaves a registered WORKTREE
// as well as a branch, and that registration is what actually blocks the retry.
// resolveWorktreePaths stamps every worktree path with the current nanosecond, so the
// second attempt asks for a different directory, its clearStaleWorktree clears a path
// that never existed, and `git worktree add` fails with "already used by worktree"
// against the first attempt's. Adoption without this is a re-queue straight into a
// refusal.
//
// Before the re-queue, not after: the drain can pick the request up on its next tick.
func TestReconcileFreesTheOrphanWorktreeBeforeRequeueing(t *testing.T) {
	sandboxSpool(t)
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	stale := worktreeOn(t, repo, branch, filepath.Join(root, "fix-auth_deadbeef"))
	strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	require.True(t, recovered(t).Adopt)
	assert.NoDirExists(t, stale, "the stale worktree must be gone by the time the drain runs")
	held, _, err := git.StrandedWorktreeFor(context.Background(), repo, branch)
	require.NoError(t, err)
	assert.Empty(t, held, "and git must no longer report the branch as checked out")
	assert.True(t, git.LocalBranchExists(context.Background(), repo, branch),
		"while the branch itself — the interrupted build's actual work — survives")
}

// TestReconcileRefusesAnOrphanHeldByAHandMadeWorktree is that release's negative
// control, and the one that keeps it from being a recursive delete of somebody's work.
//
// A worktree under the data dir's worktrees/ tree carries a name only Atrium mints. A
// checkout a person made holds the branch just as firmly and is not ours to remove — so
// the claim is refused with a reason naming it, rather than adopted into a Setup that
// would fail with git's own wording anyway.
func TestReconcileRefusesAnOrphanHeldByAHandMadeWorktree(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	mine := worktreeOn(t, repo, branch, filepath.Join(t.TempDir(), "my-own-checkout"))
	record := strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected, "the caller is owed the reason rather than a late Setup failure")
	assert.Contains(t, reason, mine, "which names the checkout in the way")
	assert.DirExists(t, mine, "and the person's checkout is untouched")
	assertCreateSettled(t, record)
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

// TestReconcileRefusesABranchAnotherRepoSessionHolds pins the direction branchOwner
// deliberately errs in, and it is not the intuitive one.
//
// A branch name is unique only within one repository, so a row holding this branch name
// in a DIFFERENT checkout is, strictly, not evidence about this one — and refusing on it
// costs a recovery that would have been correct. It is refused anyway, because the two
// mistakes are not the same size. inst.Path is the directory a session was created FROM
// (resolveNewTarget stores the caller's cwd through filepath.Abs, which does not resolve
// symlinks), so a same-repo row reached through /repo/backend or a symlinked path fails
// the path comparison and reads as "another repo" — and the fall-through from there is
// claimAdopt, whose first act is `git worktree remove -f` plus os.RemoveAll on that live
// session's worktree.
//
// So: over-refusing writes a receipt naming the branch and the session, and the caller
// retries under another title. Under-refusing deletes somebody's working directory. This
// test asserts the receipt.
func TestReconcileRefusesABranchAnotherRepoSessionHolds(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	record := strandedIn(t, "fix-auth", repo)

	elsewhere := rowFrom(t, "fix-auth", gitRepoWithBranch(t, branch), branch, "")

	require.Equal(t, 1, reconcile(t, elsewhere))

	rejection, rejected := outbox.Rejection(record)
	require.True(t, rejected, "a row holding the branch must stop the adoption, wherever it is filed")
	assert.Contains(t, rejection, branch, "and the receipt must name the branch it left alone")
	assert.Contains(t, rejection, "another repository",
		"saying which kind of holder it found, so the message is not simply wrong for this case")

	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "the request is not re-queued to take a branch somebody else holds")
	assert.True(t, git.LocalBranchExists(context.Background(), repo, branch),
		"and nothing is deleted on the strength of a match that might be a false one")
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

// TestReconcileConsultsTheRowBeforeTheExpiryHorizon pins the arm ORDER, which is a
// claim classifyCreateClaim's own docstring makes ("the recorded row is consulted first
// because it is the only evidence that settles the question outright") and which the
// first cut of this file did not honour.
//
// The expiry and unreadable arms used to run ahead of the row lookup. A create that had
// fully succeeded — row persisted, only the settle interrupted — and then sat unnoticed
// past the 24h horizon was therefore handed a receipt saying it was "discarded rather
// than rebuilt", for a session `atrium ls` was showing as running. The row is durable
// evidence and does not go stale; the horizon is about rebuilding, and there is nothing
// here left to rebuild.
func TestReconcileConsultsTheRowBeforeTheExpiryHorizon(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "")
	record := strandedIn(t, "fix-auth", repo)
	row := rowFrom(t, "fix-auth", repo, strandPrefix+"fix-auth", record)

	// Well past the horizon, judged by a clock the test owns.
	future := time.Now().Add(outbox.TTL + 48*time.Hour)
	require.Equal(t, 1, reconcileCreateClaims(context.Background(), []*session.Instance{row}, future))

	reason, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a session that exists is not refused for being old: %s", reason)
	assertCreateSettled(t, record)
}

// TestReconcileNamesTheOrphanBranchItAbandonsOnExpiry is the other half of that
// reordering, and the reason expiry is applied to the VERDICT rather than up front.
//
// An expired claim whose build got as far as a branch is discarded on top of an orphan.
// Refusing it in the same words as one that built nothing would leave the single
// artifact the user has to clean up by hand as the one thing nobody mentions — atrium
// #716's complaint, re-entered through the door marked "too old to rebuild".
func TestReconcileNamesTheOrphanBranchItAbandonsOnExpiry(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	record := strandedIn(t, "fix-auth", repo)

	future := time.Now().Add(outbox.TTL + 48*time.Hour)
	require.Equal(t, 1, reconcileCreateClaims(context.Background(), nil, future))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected, "past the horizon it is not rebuilt")
	assert.Contains(t, reason, branch, "and the branch it leaves behind must be named")
	assert.Contains(t, reason, "belongs to no session",
		"together with what that means for the reader")
	assert.True(t, git.LocalBranchExists(context.Background(), repo, branch),
		"the branch is left in place rather than deleted — it may hold work")
}

// TestReconcileExpiresAClaimThatBuiltNothingWithoutNamingABranch is that message's
// negative control: the branch clause must be earned by an actual branch, not appended
// to every expiry. An expiry that always claimed to have left a branch behind would
// send a user hunting for one that was never made.
func TestReconcileExpiresAClaimThatBuiltNothingWithoutNamingABranch(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "") // nothing was built
	record := strandedIn(t, "fix-auth", repo)

	future := time.Now().Add(outbox.TTL + 48*time.Hour)
	require.Equal(t, 1, reconcileCreateClaims(context.Background(), nil, future))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected)
	assert.NotContains(t, reason, strandPrefix+"fix-auth",
		"no branch was made, so none may be named")
	assert.NotContains(t, reason, "belongs to no session")
}

// TestReconcileLeavesTheClaimWhenGitCannotBeAsked is the claimDefer arm, and it exists
// because both of the other answers are one-way.
//
// git.LocalBranchExists is `err == nil`, so git off PATH, a fork failure under memory
// pressure or a cancelled context all read as "the branch does not exist" — and that
// answer is acted on destructively: the request is re-queued, git recovers, the drain
// refuses it for the branch the failed probe could not see, and the receipt-plus-unlink
// leaves the orphan permanently. Deferring costs one launch and keeps the recovery.
//
// Staged by pointing the request at a directory that is not a repository at all, which
// is what a failed `for-each-ref` looks like from here.
func TestReconcileLeavesTheClaimWhenGitCannotBeAsked(t *testing.T) {
	sandboxSpool(t)
	notARepo := t.TempDir()
	record := strandedIn(t, "fix-auth", notARepo)

	assert.Zero(t, reconcile(t), "nothing was decided, so nothing is counted as acted on")

	assert.FileExists(t, outbox.ClaimPath(record), "the claim survives for the next launch")
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "it is not re-queued into gates that would refuse it")
	reason, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "and no receipt is written on evidence that was never gathered: %s", reason)
}

// TestReconcileRefusesWhileTheAgentIsStillRunning is the leftover this file missed
// entirely until a review pointed at it, and the one with the worst consequence.
//
// tmux runs on its own server on a dedicated socket, so the session Instance.Start
// creates outlives the TUI that created it — and Start creates it LAST, which is exactly
// the window between "everything is built" and "the row is persisted" that a claim
// exists to cover. Without this arm the reconcile sees a branch no row owns, calls it an
// orphan, and applyCreateClaim's release runs `git worktree remove -f` plus os.RemoveAll
// over the working directory of a live agent. The re-queue that follows could not
// succeed either: the tmux name is a pure function of (repo group, title), so every
// retry meets "tmux session already exists".
//
// The session here is real, started the way the app starts one, and its row is
// deliberately withheld — the state a SIGKILL in that window leaves on disk.
func TestReconcileRefusesWhileTheAgentIsStillRunning(t *testing.T) {
	sandboxSpool(t)
	testutil.RequireTmux(t)
	repo := gitRepoWithBranch(t, "")

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "fix-auth", Path: repo, Program: "sleep 300",
	})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)
	live := wt.GetWorktreePath()
	require.DirExists(t, live)

	// The claim's branch read off the instance, not strandedIn's fixed strandPrefix. That
	// constant is right for the tests where no session was ever built — it keeps a wrong
	// derivation from agreeing with itself — but here a real one WAS built, and its branch
	// comes from Config.BranchPrefix, which DefaultConfig derives from the lowercased OS
	// username. The two coincide on a machine whose account is "zvi" and nowhere else, so a
	// fixture using both describes two different branches, and the only assertion that
	// noticed is the one below: the receipt check above it names a tmux session, which
	// comes from (repo group, title) and carries no prefix at all.
	require.NotEmpty(t, inst.Branch, "precondition: the interrupted build minted a branch")
	record := strand(t, outbox.Request{Title: "fix-auth", Path: repo},
		outbox.ClaimMeta{At: time.Now(), SessionBranch: inst.Branch})

	// No instances: the row is what the crash lost.
	require.Equal(t, 1, reconcile(t))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected, "a running agent is not an orphan")
	assert.Contains(t, reason, inst.TmuxSessionName(),
		"and the receipt must name the session standing in the way")
	// The receipt reaches the caller and is then consumed and swept; the disclosure is
	// what the next TUI reads to tell the person at the terminal that a live agent is
	// running with nothing in atrium's records pointing at it.
	d := disclosed(t, record)
	assert.Equal(t, inst.TmuxSessionName(), d.TmuxName)
	assert.Equal(t, inst.Branch, d.Branch)
	// The worktree too, and this is the one arm where omitting it was a real hole. It is
	// the arm where the interrupted build got FURTHEST — branch, worktree and a running
	// agent — and the one arm that deliberately frees none of them, so the user kills the
	// tmux session, runs `git branch -d`, and meets "already used by worktree" against a
	// path nothing had named. The probe is here rather than inherited from the walk below
	// it, which this arm returns before reaching.
	assertSamePath(t, live, d.Worktree, "the directory that will block the retry")
	assert.DirExists(t, live, "the live agent's worktree must not be removed")
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "nor re-queued into a retry that could never succeed")
}

// assertSamePath compares a worktree path the test created against one git reported.
//
// Not assert.Equal, because the two spellings differ on macOS and only there: git reports
// the path it registered, which arrives as /private/var where t.TempDir gives /var — the
// asymmetry underManagedWorktrees documents and handles for the containment check.
//
// assert.Contains does not stand in for this, and passes for a reason that has nothing to
// do with the paths matching: "/var/…/x" is a literal substring of "/private/var/…/x", so a
// containment assertion is satisfied by the very mismatch it is meant to tolerate. It would
// be satisfied by an unrelated /var path with the same tail, too.
func assertSamePath(t *testing.T, want, got, msg string) {
	t.Helper()
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	assert.Equal(t, resolve(want), resolve(got), msg)
}

// disclosed returns the disclosure the reconcile left for record, requiring there to be
// one.
func disclosed(t *testing.T, record string) outbox.Disclosure {
	t.Helper()
	d, ok := outbox.DisclosureFor(record)
	require.True(t, ok, "a refusal has to record what the interrupted build left behind")
	return d
}

// TestReconcileDisclosesTheOrphanItRefusesFor is #732's complaint applied to the arm that
// reaches it soonest: a refusal answers the caller and destroys the claim, and the claim
// was the only durable thing naming the branch and the worktree.
//
// The hand-made-checkout arm is the fixture because it is the one refusal where the branch
// is certain to be there and to stay there — a branch nothing owns, held by a checkout that
// is the whole subject of the refusal — so the assertion cannot pass on an empty inventory.
//
// It is also the one arm where the inventory deliberately stops at the branch. The
// directory holding it is somebody's own checkout at a path Atrium never minted, and every
// reader renders the worktree field as a leftover to be removed; naming it there would tell
// the user to delete their own working tree. The reason says what is holding the branch,
// which is what they act on.
func TestReconcileDisclosesTheOrphanItRefusesFor(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	mine := worktreeOn(t, repo, branch, filepath.Join(t.TempDir(), "my-own-checkout"))
	record := strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	d := disclosed(t, record)
	assert.Equal(t, "fix-auth", d.Title)
	assert.Equal(t, repo, d.Repo)
	assert.Equal(t, branch, d.Branch)
	assert.Empty(t, d.Worktree, "a checkout the user made is not a leftover to remove")
	assert.Contains(t, d.Reason, filepath.Base(mine), "the reason names what holds the branch")
	assert.Contains(t, d.Reason, "not a worktree Atrium manages",
		"and says whose it is, which is the half a clip must not take")
	assert.True(t, d.Leftovers(), "so the reader has something to report")
}

// TestReconcileWillNotRebuildAClaimItAlreadyRefused is #731's third hole, and the reason the
// disclosure is written BEFORE the discard rather than after.
//
// A refusal writes the receipt, marks the request spent, and unlinks. If the unlink fails —
// EACCES on the spool, EIO — the claim survives, and the caller has already read the receipt
// and exited non-zero. Judged again on the next launch it meets LIVE git and a freshly
// loaded instance list, and both have moved on: the session that held the branch may since
// have been killed, the hand-made worktree removed. Without the disclosure this fixture
// classifies claimAdopt and builds the session its caller was told it would not get.
//
// The claim and the disclosure are placed by hand because the state they represent is a
// failed unlink, which is not reachable through a sandbox that can write.
func TestReconcileWillNotRebuildAClaimItAlreadyRefused(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch) // an orphan branch: claimAdopt's own fixture
	record := strandedIn(t, "fix-auth", repo)
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Repo: repo, Branch: branch,
		Reason: "a previous atrium was interrupted while creating this session",
	}))

	assert.Equal(t, claimAnswered, mustClassify(t, record),
		"the caller was answered; nothing here may reopen that")
	require.Equal(t, 1, reconcile(t))

	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "and it must not be re-queued for the drain to build")
	assertCreateSettled(t, record)
	_, ok := outbox.DisclosureFor(record)
	assert.True(t, ok, "the disclosure stays for the reader that has not shown it yet")
}

// TestReconcileOutranksTheRowWithADisclosure is the ordering half of the arm above. The row
// check is the one piece of evidence that settles a claim outright, and it deliberately
// comes first — except after a refusal, where a row appearing under this record would mean
// something built the session anyway. Read in the other order the request would be reported
// to its caller as a success it was already told it did not get.
func TestReconcileOutranksTheRowWithADisclosure(t *testing.T) {
	sandboxSpool(t)
	repo := t.TempDir()
	record := strandedIn(t, "fix-auth", repo)
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Repo: repo, Reason: "could not record it"}))
	row := rowFrom(t, "fix-auth", repo, "", record)

	assert.Equal(t, claimAnswered, mustClassify(t, record, row))
}

// TestReconcilePinsTheBranchItAdopts: Adopt licenses skipping the branch gate, and the pin
// is what lets the drain re-earn that skip instead of inheriting it. A re-queue that loses
// the pin reads as "no pin", which fails closed — the request is then refused for the very
// branch the adoption exists to finish on.
func TestReconcilePinsTheBranchItAdopts(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	strandedIn(t, "fix-auth", repo)

	require.Equal(t, 1, reconcile(t))

	got := recovered(t)
	require.True(t, got.Adopt)
	want := branchTip(t, repo, branch)
	require.NotEmpty(t, want, "precondition: the orphan branch has a tip to pin")
	assert.Equal(t, want, got.AdoptTip, "the commit the drain re-checks against")
}

// TestApplyAdoptRequeuesPlainWhenTheBranchWentAway covers the window between the verdict and
// the hand-off: the branch is probed for the pin after the worktree release, and it can be
// gone by then. Pinning nothing would leave an Adopt the drain fails closed on; re-queueing
// plain is what the ordinary gates already judge correctly, since there is no branch left
// for them to refuse.
//
// Driven through applyCreateClaim with a hand-built judgement, because the window it covers
// is a race no fixture can arrange.
func TestApplyAdoptRequeuesPlainWhenTheBranchWentAway(t *testing.T) {
	sandboxSpool(t)
	repo := gitRepoWithBranch(t, "") // no orphan branch at all
	record := strandedIn(t, "fix-auth", repo)
	claims, err := outbox.ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)

	applied := applyCreateClaim(context.Background(), claims[0],
		claimJudgement{verdict: claimAdopt, branch: strandPrefix + "fix-auth"})

	require.True(t, applied)
	got := recovered(t)
	assert.False(t, got.Adopt, "there is nothing left to adopt")
	assert.Empty(t, got.AdoptTip)
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "and this is not a refusal: an ordinary create is exactly right")
}

// TestNewHomeBuffersADisclosureAnEarlierProcessLeft wires the two producers to the one
// reader. A disclosure written by a process that then died has no frame to be shown on, so
// the construction that reads the spool has to buffer it for the first preview tick — and it
// has to read AFTER the reconcile, or a refusal this very launch reached lands in the next
// launch's report instead of this one's.
func TestNewHomeBuffersADisclosureAnEarlierProcessLeft(t *testing.T) {
	defer theme.Set(config.DefaultConfig().Theme)()
	t.Setenv("HOME", t.TempDir())

	branch := config.DefaultConfig().BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	mine := worktreeOn(t, repo, branch, filepath.Join(t.TempDir(), "my-own-checkout"))
	record := strand(t, outbox.Request{Title: "fix-auth", Path: repo},
		outbox.ClaimMeta{At: time.Now(), SessionBranch: branch})

	h, err := newHome(context.Background(), "echo", false, "v", "atr")
	require.NoError(t, err)

	require.Len(t, h.pendingCreateDisclosures, 1,
		"the refusal this launch just reached belongs in this launch's report")
	assert.Equal(t, record, h.pendingCreateDisclosures[0].Path)
	got := h.pendingCreateDisclosures[0].Disclosure
	assert.Equal(t, branch, got.Branch, "with the orphan branch it is about")
	assert.Contains(t, got.Reason, filepath.Base(mine), "and what is holding it")
	assert.False(t, got.CreatedAt.IsZero(),
		"stamped in place by Disclose, or the report cannot date it")
}

// TestReconcileAnswersAClaimNothingEverAnswered is claimAnswered's premise, checked rather
// than assumed. The arm's whole justification is "the receipt went out on an earlier launch",
// and the crash window Disclose is ordered ahead of Reject to survive is precisely the one
// where it did not: disclosure written, receipt not, claim still there.
//
// Reached with no receipt, an unlink and nothing else leaves the record, the claim and the
// receipt all absent — which is exactly the state awaitSpool reads as SUCCESS (see the
// claimSucceeded arm, whose only signal that is). `atrium new --wait` would then exit 0 and
// send its caller looking in state.json for a branch that is not there.
func TestReconcileAnswersAClaimNothingEverAnswered(t *testing.T) {
	sandboxSpool(t)
	record := strand(t, outbox.Request{Title: "fix-auth", Path: "/repo/web"},
		outbox.ClaimMeta{At: time.Now(), SessionBranch: "zvi/fix-auth"})
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Repo: "/repo/web", Branch: "zvi/fix-auth",
		Reason: "the session was created but atrium could not record it: disk full"}))
	_, rejected := outbox.Rejection(record)
	require.False(t, rejected, "precondition: the crash landed between the two writes")

	require.Equal(t, 1, reconcile(t))

	assert.NoFileExists(t, outbox.ClaimPath(record), "the unlink that failed is still owed")
	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected, "and so is the receipt, or the absence reads as success")
	assert.Contains(t, reason, "disk full", "in the disclosure's own words")
}

// TestReconcileAnswersAnUnreadableMarkWithWords is the same arm's edge: DisclosureFor reports
// a file it cannot decode as a disclosure, because the question is whether the request is
// terminal and an unreadable mark answers that. What it cannot supply is a reason, and an
// empty receipt is what `atrium new --wait` would print.
func TestReconcileAnswersAnUnreadableMarkWithWords(t *testing.T) {
	sandboxSpool(t)
	record := strand(t, outbox.Request{Title: "fix-auth", Path: "/repo/web"},
		outbox.ClaimMeta{At: time.Now(), SessionBranch: "zvi/fix-auth"})
	require.NoError(t, os.WriteFile(record+".disclosure", []byte("{not json"), 0o644))

	require.Equal(t, 1, reconcile(t))

	reason, rejected := outbox.Rejection(record)
	require.True(t, rejected)
	assert.NotEmpty(t, reason, "a caller reading this has to be told something")
}

// TestExpiredAdoptableClaimDisclosesWhatItAbandons pins expireVerdict's artifact carry-over,
// which is a line of its own and so can be deleted on its own.
//
// The downgrade turns claimAdopt into claimRefused, and the branch and worktree it was about
// to adopt become exactly what the refusal has to disclose. Dropped, this arm's caller is
// long gone — the request was re-queued by an earlier launch, so no --wait is blocked and
// the receipt is swept at the horizon — and Leftovers() goes false, so the reader filters the
// entry out and an orphan branch plus a still-registered managed worktree are never mentioned
// again. Which is verbatim the failure the function's own docstring names.
func TestExpiredAdoptableClaimDisclosesWhatItAbandons(t *testing.T) {
	sandboxSpool(t)
	branch := strandPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	managed := worktreeOn(t, repo, branch, filepath.Join(root, "fix-auth_deadbeef"))
	record := strand(t, outbox.Request{Title: "fix-auth", Path: repo,
		CreatedAt: time.Now().Add(-2 * outbox.TTL)},
		outbox.ClaimMeta{At: time.Now().Add(-2 * outbox.TTL), SessionBranch: branch})

	require.Equal(t, 1, reconcile(t))

	d := disclosed(t, record)
	assert.Equal(t, branch, d.Branch, "the branch it is abandoning")
	assertSamePath(t, managed, d.Worktree, "and the registration that blocks the retry")
	assert.Contains(t, d.Reason, "past the")
	assert.True(t, d.Leftovers(), "or the reader drops it and nobody is told")
}
