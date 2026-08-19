package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolCreate writes a create request and returns its spool path.
func spoolCreate(t *testing.T, r outbox.Request) string {
	t.Helper()
	path, err := outbox.WriteCreate(r)
	require.NoError(t, err)
	return path
}

func createSpoolCount(t *testing.T) int {
	t.Helper()
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	return len(entries)
}

// createClaimCount is createSpoolCount for the other half of the spool. The two are
// disjoint by construction — a claim is not in the record name format ListCreates
// screens on — so a test that means "still there" has to say which half it means.
func createClaimCount(t *testing.T) int {
	t.Helper()
	entries, err := outbox.ListClaims()
	require.NoError(t, err)
	return len(entries)
}

// refuseDrain runs the drain and asserts it refused rather than created. Both halves
// matter: the caller learns why from a rejection receipt (asserted per-test below),
// and the person at the TUI — the only one who can raise a cap or free a title — from
// a notice. A tick that refuses in silence is indistinguishable to them from no
// request at all.
func refuseDrain(t *testing.T, h *home) {
	t.Helper()
	require.NotNil(t, h.drainCreateRequests(), "a refusal is an outcome, not a no-op")
	assert.Contains(t, h.menu.NoticeText(), "refused",
		"a refused create request must say so at the TUI, not only in the receipt")
}

// disposeDrain is refuseDrain's counterpart for the disposal arms — an expired or
// undecodable record. It asserts the opposite of the notice, and that is the point: the
// notice exists so the person at the TUI can raise a cap or free a title, and neither is
// what an expired file needs. Counting one would repaint a red error every ~500ms tick
// for as long as a cron backlog takes to clear at createDisposalBudget a tick, and each
// one overwrites whatever drainOutbox flashed. The caller still gets its receipt (each
// test asserts that itself) and the log still gets its line.
func disposeDrain(t *testing.T, h *home) {
	t.Helper()
	assert.Nil(t, h.drainCreateRequests(), "a disposal is nobody's to act on at the TUI")
	assert.NotContains(t, h.menu.NoticeText(), "refused",
		"and must not raise the refusal notice")
}

// assertCreateHeld asserts that a request the drain accepted is being held across its
// start, in the two-file shape #716 gave that hold. Both halves are load-bearing and
// neither implies the other:
//
//   - the record has left the record name format, so no later tick can re-execute it or
//     expire it out from under the session it is building;
//   - a claim has taken its place, so a process killed mid-Start leaves the next launch
//     something to finish, and a caller blocked in --wait keeps waiting instead of
//     reading the record's absence as a created session.
//
// A test that asserted only the first would pass against a drain that simply unlinked
// on accept, which is the bug this replaced.
func assertCreateHeld(t *testing.T, record string) {
	t.Helper()
	assert.NoFileExists(t, record,
		"an accepted request must leave the record format, or a later tick re-executes or expires it")
	assert.FileExists(t, outbox.ClaimPath(record),
		"and must leave a claim, or a crash mid-Start strands it with nothing to reconcile")
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a request in flight is not a rejected one")
}

// assertCreateSettled asserts a request has reached a terminal state. Both files, not
// just the record: the claim is half of what awaitSpool now reads as "done", so one
// left behind blocks the caller's --wait and is re-judged by every later launch.
func assertCreateSettled(t *testing.T, record string) {
	t.Helper()
	assert.NoFileExists(t, record)
	assert.NoFileExists(t, outbox.ClaimPath(record),
		"a settled request leaves no claim for the next launch to reconcile")
}

// assertCreateQueued asserts a request is still queued and untouched — the shape of a
// tick that HELD rather than refused. Distinct from assertCreateHeld: nothing has been
// accepted, so the record is exactly where `atrium new` put it and there is no claim.
func assertCreateQueued(t *testing.T, record string) {
	t.Helper()
	assert.FileExists(t, record, "the request waits rather than being refused")
	assert.NoFileExists(t, outbox.ClaimPath(record),
		"and is not claimed either — nothing accepted it")
	_, rejected := outbox.Rejection(record)
	assert.False(t, rejected, "a held request is not a rejected one")
}

// titled returns the instance with this title, or nil.
func titled(h *home, title string) *session.Instance {
	for _, inst := range h.list.GetInstances() {
		if inst.Title == title {
			return inst
		}
	}
	return nil
}

// gitRepoWithBranch initialises a repo with one commit and, when branch is
// non-empty, an extra branch — the orphan a killed session leaves behind.
func gitRepoWithBranch(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		// A hermetic identity: the developer's global config may set neither.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	if branch != "" {
		run("branch", branch)
	}
	return dir
}

// TestCreateDrainCreatesSessionAndHoldsTheFile is the end-to-end contract, and
// the "holds" half is the whole reason `atrium new --wait` can be truthful: at
// the moment the drain returns, the worktree and the agent do not exist yet, so
// unlinking here would let --wait report success for a create that then failed.
func TestCreateDrainCreatesSessionAndHoldsTheFile(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: dir})

	require.NotNil(t, h.drainCreateRequests(), "a drained request must return its boot command")

	inst := titled(h, "fix-auth")
	require.NotNil(t, inst, "the session must be in the list")
	assert.Equal(t, session.Loading, inst.GetStatus())
	assertCreateHeld(t, path)
}

// TestCreateDrainRemovesFileOnStartSuccess: the file's absence is what a waiting
// `atrium new --wait` reads as "created", so it must not go until the start
// actually succeeded.
func TestCreateDrainRemovesFileOnStartSuccess(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	h.drainCreateRequests()

	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	h.settleCreateRequest(inst, nil)

	assertCreateSettled(t, path)
	_, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "a successful create leaves no receipt")
}

// TestCreateDrainRejectsOnStartFailure: a request that was accepted and then died
// building its worktree must not read as a success. Without this the caller waits
// out its whole --wait timeout and is told the request is "still queued", which is
// both wrong and unactionable.
func TestCreateDrainRejectsOnStartFailure(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	h.drainCreateRequests()

	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	h.settleCreateRequest(inst, errors.New("worktree is dirty"))

	reason, ok := outbox.Rejection(path)
	require.True(t, ok, "a failed start must leave a receipt")
	assert.Contains(t, reason, "worktree is dirty")
	assertCreateSettled(t, path)
}

// TestCreateDrainSkipsRequestAlreadyInFlight: the request stays on disk while the
// session starts, so the next tick must leave it alone rather than re-executing it and
// writing an "already used" receipt over a create that is going fine.
//
// It pins the OUTCOME, and until #716 that outcome had two independent causes — a
// linear scan of createsInFlight, and createStartBudget, seeded from that same map and
// so already spent while a start runs. Either alone delivered a green run here, which
// is why deleting the scan left this test and the whole package passing: it named the
// cause that was not doing the work.
//
// One cause now, and it is neither of those. holdCreateRequest renames the record out
// of the format ListCreates screens on, so the second tick cannot see the request at
// all; the scan is gone, and the assertion below on the two spool halves is what
// distinguishes "left alone" from "consumed". createStartBudget still covers the same
// ground it always did, which is why this remains an outcome test rather than a
// mechanism one — see TestCreateDrainDoesNotExpireARequestItIsStillStarting for the
// arm the budget genuinely cannot reach.
func TestCreateDrainSkipsRequestAlreadyInFlight(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	assert.Nil(t, h.drainCreateRequests(), "the second tick must find nothing to do")

	assert.Equal(t, 1, h.list.NumInstances())
	assert.Zero(t, createSpoolCount(t), "the second tick has no request to re-execute")
	assertCreateHeld(t, path)
	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "a request in flight must not be rejected by its own session: %s", reason)
}

// TestCreateDrainDoesNotExpireARequestItIsStillStarting pins the hazard the old
// in-flight scan existed for, against the mechanism that replaced it.
//
// The hazard: createStartBudget covers the default arm, because it is seeded from the
// in-flight map and so is already spent while a start runs. The EXPIRY arm draws on
// createDisposalBudget instead, is evaluated ahead of the default arm, and therefore
// sees none of that — so a request whose Start crosses the 24h horizon mid-flight was
// rejected and unlinked underneath its own running session. The caller's --wait, which
// reads the record's disappearance as "created and recorded", was handed a receipt
// saying the request expired, for a session that at that moment exists. Reachable
// without contrivance: a request spooled just under the horizon and a repo whose setup
// script takes a minute.
//
// What changed with #716 is that the guard is now structural rather than a skip. An
// accepted request is renamed out of the record name format, so ListCreates does not
// return it and no arm of the loop — expiry included — can reach it at all. That is
// what the middle of this test measures: the aged request reads as expired to anything
// that CAN see it, and the drain's own listing cannot.
//
// Expiry is not lost, only moved to where it is safe: a claim that outlives its process
// is judged by reconcileCreateClaims, which has the evidence to tell an expired
// abandoned build from a running one (see TestReconcileRefusesAnExpiredClaim).
//
// Staged by ageing the record on disk between two ticks, because the drain re-decodes
// every file on every tick and there is no clock to move.
func TestCreateDrainDoesNotExpireARequestItIsStillStarting(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests(), "the first tick starts it")
	require.NotNil(t, titled(h, "fix-auth"))
	assertCreateHeld(t, path)

	// Age the held request past the horizon, as a slow Start would.
	claim := outbox.ClaimPath(path)
	raw, err := os.ReadFile(claim)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(raw, &record))
	record["created_at"] = time.Now().Add(-2 * outbox.TTL).Format(time.RFC3339Nano)
	aged, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(claim, aged, 0o600))

	claims, err := outbox.ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.True(t, claims[0].Request.Expired(time.Now()),
		"precondition: the request now reads as expired to anything that can see it")
	assert.Zero(t, createSpoolCount(t),
		"and the drain's own listing cannot see it, which is what makes the expiry arm unreachable")

	h.drainCreateRequests()

	assertCreateHeld(t, path)
	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "nor handed its caller an expiry receipt: %s", reason)
}

// TestCreateDrainRunsInEveryUIState is a regression from driving a real first-run
// TUI. An earlier version of the drain skipped anything but stateDefault, which
// looked harmless until the welcome modal — a state a fresh install sits in until
// someone answers it — made `atrium new` unable to create the first session on a
// machine nobody had used interactively. That is precisely the deadlock the
// feature exists to remove, and no unit test saw it because they all set the state
// themselves.
//
// stateWelcome is the case that shipped, but the failure was "a state nobody thought
// about", so this walks the enum instead of naming the states that came to mind. An
// earlier version listed six of the then-21 by hand and omitted stateScreensaver —
// literally the state an unattended TUI sits in. numStates is the bound, so a state
// added later is covered without anyone remembering to add it here.
func TestCreateDrainRunsInEveryUIState(t *testing.T) {
	for st := stateDefault; st < numStates; st++ {
		t.Run(strconv.Itoa(int(st)), func(t *testing.T) {
			h := drainHome(t)
			h.state = st

			spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
			require.NotNil(t, h.drainCreateRequests())
			assert.NotNil(t, titled(h, "fix-auth"), "a request must not wait on a modal being answered")
		})
	}
}

// TestCreateDrainDefersToAStagedSpawnPlan is the one exception, and it is keyed on
// the staged plan rather than on a state.
//
// Accepting either capacity confirm goes straight to spawnVariants, which re-validates
// neither the title nor the cap the plan was staged against. Creating in between would
// let the accepted plan spawn a duplicate title — two sessions deriving one branch
// slug, which Worktree.Setup reads as a resume — or spawn past the cap the user was
// shown. Unlike a state gate this cannot deadlock: a staged plan means a human is
// looking at a dialog, and the request is retried on the next tick regardless.
func TestCreateDrainDefersToAStagedSpawnPlan(t *testing.T) {
	for name, stage := range map[string]func(*home, spawnPlan){
		"over cap":  func(h *home, p spawnPlan) { h.pendingOverCap = &p },
		"exhausted": func(h *home, p spawnPlan) { h.pendingExhausted = &p },
	} {
		t.Run(name, func(t *testing.T) {
			h := drainHome(t)
			dir := t.TempDir()
			stage(h, spawnPlan{titles: []string{"fix-auth"}, path: dir, direct: true, programs: []string{"echo"}})
			path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: dir})

			assert.Nil(t, h.drainCreateRequests(), "a staged plan holds the drain")
			assert.Zero(t, h.list.NumInstances(), "nothing may be created under a pending confirm")
			assertCreateQueued(t, path)
		})
	}
}

// TestCreateDrainKeepsTheUsersSelection: startNewSession selects the row it
// creates, which is right for a keypress and wrong for a request that arrived from
// another terminal. #439 settled that a background event does not move a cursor a
// human placed.
//
// It is also the observable half of the spawnBackground wiring: that origin is what
// suppresses the cursor move, the fold reset (below) and — the one with no cheap test,
// since it needs a live tmux pane — auto-attach. Flip the drain to spawnInteractive and
// this fails, which is the point.
func TestCreateDrainKeepsTheUsersSelection(t *testing.T) {
	h := drainHome(t)
	mine := addInstance(t, h, "watching-this", t.TempDir())
	h.list.SelectInstance(mine)

	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())

	require.NotNil(t, titled(h, "fix-auth"), "the session is still created")
	assert.Same(t, mine, h.list.GetSelectedInstance(), "the cursor must not move")
}

// TestCreateDrainKeepsFoldedGroupsFolded: AddInstance unfolds the new row's repo
// group unconditionally, which is right for a keypress — a session you just made must
// not land hidden — and wrong here. A fold is a layout choice the user made and the
// next collapse keypress persists, so a background create that opened one would make
// its own unfold durable.
func TestCreateDrainKeepsFoldedGroupsFolded(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	existing := addInstance(t, h, "already-here", dir)
	folded := existing.GroupKey()
	h.list.SetCollapsedRepos([]string{folded})
	require.Equal(t, []string{folded}, h.list.CollapsedRepos(), "precondition: the group is folded")

	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: dir})
	require.NotNil(t, h.drainCreateRequests())

	require.NotNil(t, titled(h, "fix-auth"), "the session is still created")
	assert.Equal(t, []string{folded}, h.list.CollapsedRepos(), "a background create may not unfold a group")
}

// TestCreateDrainSelectsTheFirstSession: with an empty list the new row ends up
// selected — not because the drain selected it, but because it is the only row and
// the cursor index is already 0. Pinned so a reader does not mistake the outcome for
// the cursor move TestCreateDrainKeepsTheUsersSelection proves does not happen.
func TestCreateDrainSelectsTheFirstSession(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	assert.Same(t, titled(h, "fix-auth"), h.list.GetSelectedInstance())
}

// TestCreateDrainRejectsTitleAlreadyUsed pins that a headless create refuses a
// collision exactly as the form does rather than suffixing it. A caller that asked
// for "fix-auth" and silently got "fix-auth-2" would push to a branch it never
// named.
func TestCreateDrainRejectsTitleAlreadyUsed(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	addInstance(t, h, "fix-auth", dir)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: dir})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, titleErrAlreadyUsed, "the receipt carries the TUI's own verdict")
	assert.Contains(t, reason, "fix-auth", "and names the title it refused")
	assert.Equal(t, 1, h.list.NumInstances(), "nothing new was created")
}

// TestCreateDrainRejectsExistingBranch is the contract git.Worktree.Setup relies
// on. Setup treats a pre-existing branch as a *resume*, so a create that skipped
// this check would not fail — it would silently adopt someone else's branch.
func TestCreateDrainRejectsExistingBranch(t *testing.T) {
	h := drainHome(t)
	// The branch a session titled "fix-auth" would mint, already present.
	repo := gitRepoWithBranch(t, h.appConfig.BranchPrefix+"fix-auth")
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, titleErrBranchExists)
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainAdoptsAnExistingBranchWhenTheReconcileSaysSo is the other end of
// atrium#716: the startup reconcile re-queues an interrupted build's request marked
// Adopt, and the drain has to let that one request past the branch check the test above
// pins — otherwise the recovery hands the caller the same permanent refusal.
//
// The same fixture as TestCreateDrainRejectsExistingBranch, one field apart. That is
// deliberate: the pair is what shows the flag is the whole difference, rather than the
// check having been loosened for everyone.
func TestCreateDrainAdoptsAnExistingBranchWhenTheReconcileSaysSo(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, h.appConfig.BranchPrefix+"fix-auth")
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo, Adopt: true})

	require.NotNil(t, h.drainCreateRequests(), "an adopting request must be created, not refused")
	assert.Equal(t, 1, h.list.NumInstances())
	assertCreateHeld(t, path)
	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "and must not be handed a receipt: %s", reason)
}

// TestCreateDrainStillRefusesATakenTitleWhenAdopting is the Adopt arm's negative
// control, and the reason it skips only HALF of the conflict gate.
//
// Adopt licenses taking a branch no session owns; it says nothing about a title a live
// session holds. Without this, "skip the whole conflict check when Adopt is set" scores
// full marks on the test above — and would mint a second session on top of a running
// one, sharing its tmux name.
func TestCreateDrainStillRefusesATakenTitleWhenAdopting(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, h.appConfig.BranchPrefix+"fix-auth")
	addInstance(t, h, "fix-auth", repo)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo, Adopt: true})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, titleErrAlreadyUsed)
	assert.Equal(t, 1, h.list.NumInstances(), "the live session is the only one")
}

// TestCreateDrainRecordsWhatWasTrueOfTheBranchWhenItClaimed pins the evidence half of
// the claim, at the one moment it can be measured. Recorded false here and true for the
// adopting request, because those are the facts — and the startup reconcile reads
// exactly this field to tell an orphan it may finish from a foreign branch it may not.
//
// Measured, not inferred, and the third row is what says so. The first two agree with
// `BranchExisted = r.Adopt` exactly, so a claim that copied its answer off the flag
// would pass them both while recording a guess. The third splits them: a request the
// reconcile marked Adopt, whose branch someone deleted before this tick ran, has no
// existing branch — and recording "true" there would tell the NEXT reconcile the branch
// it finds was foreign, turning a recoverable build into a permanent refusal.
func TestCreateDrainRecordsWhatWasTrueOfTheBranchWhenItClaimed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch string
		adopt  bool
		want   bool
	}{
		{name: "fresh branch", branch: "", want: false},
		{name: "adopting an orphan", branch: "fix-auth", adopt: true, want: true},
		{name: "adopting a branch that has since gone", branch: "", adopt: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			existing := ""
			if tc.branch != "" {
				existing = h.appConfig.BranchPrefix + tc.branch
			}
			repo := gitRepoWithBranch(t, existing)
			path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo, Adopt: tc.adopt})

			require.NotNil(t, h.drainCreateRequests())
			assertCreateHeld(t, path)

			claims, err := outbox.ListClaims()
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.NotNil(t, claims[0].Request.Claim)
			assert.Equal(t, tc.want, claims[0].Request.Claim.BranchExisted)
			assert.Equal(t, h.appConfig.BranchPrefix+"fix-auth", claims[0].Request.Claim.SessionBranch,
				"the branch recorded must be the one Setup would mint")
		})
	}
}

// TestCreateDrainStampsTheRequestOnTheSession: the stamp is what lets a later reconcile
// recognise this row as the one this request produced, so it has to be on the instance
// before Start persists it — not written afterwards by a process that may not survive.
func TestCreateDrainStampsTheRequestOnTheSession(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	assert.Equal(t, path, inst.CreateRequest)
	assert.Equal(t, path, inst.ToInstanceData().CreateRequest, "and must survive serialization")
}

// TestFormCreateCarriesNoRequestStamp is the stamp's negative control: only a spooled
// create has a request behind it, so a session from the form or a fork must carry "".
// A stamp on everything would make the reconcile's "did THIS request make a session"
// question meaningless.
func TestFormCreateCarriesNoRequestStamp(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "typed-by-hand", t.TempDir())
	assert.Empty(t, inst.CreateRequest)
	assert.Empty(t, inst.ToInstanceData().CreateRequest)
}

// TestCreateDrainCreatesInAGitRepo is TestCreateDrainRejectsExistingBranch's
// negative control: the same repo without the branch creates, so the refusal above
// is the branch check firing and not the git target being rejected outright.
func TestCreateDrainCreatesInAGitRepo(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "")
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})

	require.NotNil(t, h.drainCreateRequests())
	assert.Equal(t, 1, h.list.NumInstances())
}

// TestCreateDrainRejectsMissingDirectory: a path that is gone by the time the TUI
// drains is the realistic case (the CLI checked it, then someone moved the repo).
func TestCreateDrainRejectsMissingDirectory(t *testing.T) {
	h := drainHome(t)
	gone := filepath.Join(t.TempDir(), "no-such-dir")
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gone})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "is not a directory")
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainHoldsWhenTmuxUnusable: an unusable tmux must not consume the request.
//
// It is the one condition checked here that is about the MACHINE rather than about the
// request, and that is what separates it from every gate in executeCreateRequest. A
// taken title or a full cap stays true until a person changes it, so spending the record
// on a receipt tells the caller something durable. tmux off PATH — the window of a `brew
// upgrade tmux`, say — is true for a second and then is not, and tmux.Available re-runs
// exec.LookPath on every call rather than caching, so the next tick can already see it
// come back. Refusing would destroy a fire-and-forget create for a condition that
// cleared before anyone could read the receipt.
//
// The recovery half is the one that makes this a hold rather than a silent drop, so it
// is asserted rather than assumed: same home, same record, tmux back, session created.
func TestCreateDrainHoldsWhenTmuxUnusable(t *testing.T) {
	h := drainHome(t)
	orig := tmuxAvailable
	tmuxAvailable = func() error { return errors.New("tmux 2.9 is older than the 3.0 minimum") }
	t.Cleanup(func() { tmuxAvailable = orig })

	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	assert.Nil(t, h.drainCreateRequests(), "a held tick creates nothing and says nothing")
	assertCreateQueued(t, path)
	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "and must not be spent on a receipt: %s", reason)
	assert.Zero(t, h.list.NumInstances())

	tmuxAvailable = orig
	require.NotNil(t, h.drainCreateRequests(), "and is created once tmux is usable again")
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestCreateDrainStillDisposesWhileTmuxIsUnusable: the hold covers starts, not the
// disposal arms. An expired or undecodable record needs no tmux to discard, and its
// caller is owed the receipt whatever the machine is doing — otherwise a --wait blocked
// on a record that can never be built would time out instead of being told why.
func TestCreateDrainStillDisposesWhileTmuxIsUnusable(t *testing.T) {
	h := drainHome(t)
	orig := tmuxAvailable
	tmuxAvailable = func() error { return errors.New("tmux is not installed") }
	t.Cleanup(func() { tmuxAvailable = orig })

	path := spoolCreate(t, outbox.Request{
		Title:     "stale",
		Path:      t.TempDir(),
		CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})
	disposeDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok, "an expired record is discarded even with tmux down")
	assert.Contains(t, reason, "horizon")
	assertCreateSettled(t, path)
}

// TestCreateDrainRefusesADirectTargetItCouldNotConfirm is the guard on the worst thing
// this drain can do.
//
// targetValidity reads "is this a git repo" off git.IsGitRepo, which is `err == nil` and
// so answers false for a git that could not run — off PATH mid-upgrade, a fork failure
// under memory pressure, gitLocalTimeout on a cold repo, a cancelled context — exactly as
// it answers false for a plain directory. That verdict is what decides `direct`, and
// direct means NO worktree and NO branch: the agent runs in the target itself. So a git
// hiccup during one tick would silently hand a caller who asked for an isolated session
// an agent editing their own checkout, print `created "fix-auth"` with no branch clause
// (byte-identical to a legitimate direct session), and leave nobody a way to notice.
//
// The target here is a REAL repo. That is the whole point: the request is not asking for
// a direct session, and nothing about it is wrong — only the probe is unavailable, and
// the drain must refuse rather than assume. A cancelled context stands in for the class,
// being the one member of it a hermetic test can produce on demand.
func TestCreateDrainRefusesADirectTargetItCouldNotConfirm(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.ctx = ctx

	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})
	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "could not determine whether")
	assert.Zero(t, h.list.NumInstances(),
		"and above all: no session created loose in the caller's own repo")
}

// The control for the test above, and the one that keeps it from passing for the wrong
// reason. A directory that genuinely is not a repo still becomes a direct session — git
// ran and said no, which is a verdict rather than a silence. Without this, refusing every
// direct create would score identically and would have broken the documented behaviour
// that a non-git target is not an error.
func TestCreateDrainStillCreatesDirectlyWhenGitAnswersNo(t *testing.T) {
	h := drainHome(t)
	plain := t.TempDir() // a directory, deliberately not a repo

	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: plain})
	require.NotNil(t, h.drainCreateRequests())

	inst := titled(h, "fix-auth")
	require.NotNil(t, inst, "a non-git target is a direct session, not a refusal")
	assert.Empty(t, inst.ToInstanceData().Worktree.RepoPath, "and direct means no worktree")
}

// TestCreateDrainHoldsWhileATeardownIsInFlight: actionInFlight alone does not cover a
// kill, and this drain cannot afford the gap that leaves.
//
// asyncActionDoneMsg clears actionInFlight one message BEFORE killDoneMsg reaches the
// reap in applyKillDone, and messages are processed in between (app_frames.go says so,
// and pairs it with retiring for exactly this reason). A tick landing in that window
// sees a list that still holds the row being torn down: a request reusing its title is
// told "already used", and any other request is gated against a capCount that still
// counts it. Both are REFUSALS — receipt written, record unlinked — for a condition that
// is false one message later, which is why a key gate can be relaxed here and this one
// cannot.
func TestCreateDrainHoldsWhileATeardownIsInFlight(t *testing.T) {
	h := drainHome(t)
	dying := addInstance(t, h, "dying", t.TempDir())
	h.retiring = map[*session.Instance]bool{dying: true}
	require.False(t, h.actionInFlight, "the window under test is precisely the one where it is clear")

	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	assert.Nil(t, h.drainCreateRequests(), "a tick mid-teardown creates nothing")
	assertCreateQueued(t, path)
	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "a deferral leaves no receipt: %s", reason)

	h.endTeardown([]*session.Instance{dying})
	require.NotNil(t, h.drainCreateRequests(), "and it is created once the reap has landed")
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestCreateDrainRejectsHardCapEvenWithForce: an explicit max_sessions is the one
// gate with no accept path anywhere. --force answers the two *confirmations*; it
// must not answer a refusal, or the CLI would have a bypass the TUI does not.
func TestCreateDrainRejectsHardCapEvenWithForce(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Force: true})
	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "max_sessions")
	assert.Equal(t, 1, h.list.NumInstances())
}

// TestCreateDrainRejectsSoftCapWithoutForce: the host-derived cap raises a
// confirmation in the TUI, and a headless request has nobody to ask. Refusing with
// the reason is the honest answer — spawning past host capacity from a script is
// exactly what that confirmation exists to prevent.
func TestCreateDrainRejectsSoftCapWithoutForce(t *testing.T) {
	h := drainHome(t)
	h.hostCap = 1 // soft: max_sessions unset
	addInstance(t, h, "already-here", t.TempDir())

	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "--force", "the receipt must say how to proceed")
	assert.Equal(t, 1, h.list.NumInstances())
}

// TestCreateDrainForceCrossesSoftCap is the other half: --force is what makes the
// refusal above a choice rather than a wall on any busy machine.
func TestCreateDrainForceCrossesSoftCap(t *testing.T) {
	h := drainHome(t)
	h.hostCap = 1
	addInstance(t, h, "already-here", t.TempDir())

	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Force: true})
	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestCreateDrainRejectsExpiredRequest: a request spooled a day ago names a branch
// point that has moved on, so creating from it is worse than dropping it.
func TestCreateDrainRejectsExpiredRequest(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: t.TempDir(),
		CreatedAt: time.Now().Add(-outbox.TTL - time.Hour),
	})

	disposeDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "horizon")
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainRejectsUnreadableRequest: a file nobody can decode and nobody
// deletes would be re-read on every tick for the life of the TUI.
func TestCreateDrainRejectsUnreadableRequest(t *testing.T) {
	h := drainHome(t)
	dir, err := outbox.CreateDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "0000000000000000001-abcdabcd.json")
	require.NoError(t, os.WriteFile(path, []byte(`{not json`), 0o644))

	disposeDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "could not be read")
	assert.Zero(t, createSpoolCount(t))
}

// TestCreateDrainUsesTheTUIsProgramWhenUnset is what makes an unflagged
// `atrium new` equivalent to pressing the new-session key: the request carries no
// program, so the draining TUI supplies its own.
func TestCreateDrainUsesTheTUIsProgramWhenUnset(t *testing.T) {
	h := drainHome(t)
	h.program = "claude --dangerously-skip-permissions"
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	assert.Equal(t, "claude --dangerously-skip-permissions", inst.Program)
}

// TestCreateDrainHonoursAnExplicitProgram is the negative control for the above:
// without it, a drain that ignored the field entirely would still pass.
func TestCreateDrainHonoursAnExplicitProgram(t *testing.T) {
	h := drainHome(t)
	h.program = "claude"
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Program: "codex"})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	assert.Equal(t, "codex", inst.Program)
}

// TestCreateDrainQueuesTheFirstPrompt: the prompt rides the same field the create
// form fills, so it is delivered on the form's terms — queued now, typed in once
// the agent is past its startup screen.
func TestCreateDrainQueuesTheFirstPrompt(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Prompt: "start on the parser"})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	assert.Equal(t, 1, inst.QueueLen())
}

// TestCreateDrainBudgetIsOnePerTick: creating a session builds a worktree, runs
// the repo's setup script and launches a program, so a backlog is spread across
// ticks rather than started all at once inside one.
func TestCreateDrainBudgetIsOnePerTick(t *testing.T) {
	h := drainHome(t)
	for _, title := range []string{"one", "two", "three"} {
		spoolCreate(t, outbox.Request{Title: title, Path: t.TempDir()})
	}

	require.NotNil(t, h.drainCreateRequests())
	assert.Equal(t, 1, h.list.NumInstances(), "one tick starts one session")
	// Two, not three: the started one has been claimed, and a claim is out of the
	// record name format ListCreates screens on. Both counts are asserted because
	// either alone would pass against a drain that lost a request — three queued and
	// no claim would mean nothing was started, one queued and one claim would mean one
	// was dropped.
	assert.Equal(t, 2, createSpoolCount(t), "and leaves the unstarted ones queued")
	assert.Equal(t, 1, createClaimCount(t), "with the started one held as a claim")
}

// TestCreateDrainBudgetCountsStartsStillInFlight is what the budget is actually for,
// and the single-tick test above cannot see it: a per-tick budget looks identical
// there while delivering only a ~500ms stagger in production, because the next tick
// skips the still-running request without spending anything and starts the next one
// regardless. Twenty spooled requests would then have twenty `git worktree add`
// processes contending on one index.lock.
func TestCreateDrainBudgetCountsStartsStillInFlight(t *testing.T) {
	h := drainHome(t)
	for _, title := range []string{"one", "two", "three"} {
		spoolCreate(t, outbox.Request{Title: title, Path: t.TempDir()})
	}

	require.NotNil(t, h.drainCreateRequests())
	require.Equal(t, 1, h.list.NumInstances(), "precondition: the first tick started one")

	// The first start has not settled, so it is still in flight.
	assert.Nil(t, h.drainCreateRequests(), "no second start while the first is still running")
	assert.Equal(t, 1, h.list.NumInstances())

	// Settle it, and the budget frees up.
	h.settleCreateRequest(titled(h, "one"), nil)
	require.NotNil(t, h.drainCreateRequests())
	assert.Equal(t, 2, h.list.NumInstances(), "a settled start releases the budget")
}

// TestCreateDrainRefusalsDoNotSpendTheStartBudget: disposals and starts draw on
// separate budgets. Sharing one would let a backlog of expired requests — a cron job
// that ran while nothing was draining — spend the tick's only start, so a fresh
// request behind them would wait one tick per stale entry and a --wait would time out
// reporting "still queued" with a TUI running and draining the whole time.
func TestCreateDrainRefusalsDoNotSpendTheStartBudget(t *testing.T) {
	h := drainHome(t)
	stale := spoolCreate(t, outbox.Request{
		Title: "expired", Path: t.TempDir(), CreatedAt: time.Now().Add(-2 * outbox.TTL),
	})
	spoolCreate(t, outbox.Request{Title: "fresh", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())

	_, rejected := outbox.Rejection(stale)
	assert.True(t, rejected, "the expired request is disposed of")
	assert.NotNil(t, titled(h, "fresh"), "and the live one still starts in the same tick")

	// The disposal is silent at the TUI even beside a create: an expired file is not
	// something the person here can act on, so it earns no clause. Its caller still
	// learns why from the receipt asserted above.
	notice := h.menu.NoticeText()
	assert.Contains(t, notice, "created")
	assert.NotContains(t, notice, "refused", "a disposal is not a refusal")
}

// TestCreateDrainGivesOneTickOneGateOutcome pins what lets the create notice drop its
// "and N refused" clause: reaching either outcome costs a gate evaluation, and
// createGateBudget allows one per tick, so a create and a gate refusal can never share
// a tick. Asserted on the two outcomes rather than on the unexported counter, so raising
// the budget without restoring the clause fails here rather than silently dropping the
// half the person at the TUI can act on.
func TestCreateDrainGivesOneTickOneGateOutcome(t *testing.T) {
	h := drainHome(t)
	long := spoolCreate(t, outbox.Request{
		Title: strings.Repeat("a", session.MaxTitleLen+1), Path: t.TempDir(),
	})
	spoolCreate(t, outbox.Request{Title: "fresh", Path: t.TempDir()})

	// Tick one gates the older entry — the overlong title — and stops there.
	require.NotNil(t, h.drainCreateRequests())
	_, rejected := outbox.Rejection(long)
	require.True(t, rejected, "the overlong title is refused at the gate")
	assert.Contains(t, h.menu.NoticeText(), "refused")
	assert.Nil(t, titled(h, "fresh"), "and the tick's one gate evaluation is spent")

	// Tick two gates the survivor, and reports only the create.
	require.NotNil(t, h.drainCreateRequests())
	require.NotNil(t, titled(h, "fresh"))
	notice := h.menu.NoticeText()
	assert.Contains(t, notice, "created")
	assert.NotContains(t, notice, "refused", "the refusal belonged to the previous tick")
}

// TestCreateDrainRejectsAnOverlongTitle: the CLI bounds the title, so this is for
// what the CLI cannot speak for — a hand-written spool file, or one from a build whose
// limit differs. The drain's contract is that it runs every gate the form runs, and
// the form's title field is bounded by construction (CharLimit).
func TestCreateDrainRejectsAnOverlongTitle(t *testing.T) {
	h := drainHome(t)
	long := strings.Repeat("a", session.MaxTitleLen+1)
	path := spoolCreate(t, outbox.Request{Title: long, Path: t.TempDir()})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, strconv.Itoa(session.MaxTitleLen))
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainForceAcceptsAnExhaustedPoolByPinningAMember is the half of --force
// that was documented in four places and provably unreachable: the drain skipped its
// own gate and then handed startNewSession a nil selection, which fails closed on an
// unpinned all-limited multi-member pool (#483) and answers "pick a member explicitly
// to override" — a flag `atrium new` does not have. Accepting has to pin, exactly as
// accepting the confirmation dialog does.
func TestCreateDrainForceAcceptsAnExhaustedPoolByPinningAMember(t *testing.T) {
	h := drainHome(t)
	exhaustedPool(t, h)

	// Without --force the same request is refused, which is the control: it proves the
	// pool really is exhausted and that the accept below is not passing vacuously.
	refused := spoolCreate(t, outbox.Request{Title: "no-force", Path: t.TempDir()})
	refuseDrain(t, h)
	reason, ok := outbox.Rejection(refused)
	require.True(t, ok)
	assert.Contains(t, reason, "rate-limited")
	require.Zero(t, h.list.NumInstances(), "precondition: the pool blocks an unforced create")

	path := spoolCreate(t, outbox.Request{Title: "forced", Path: t.TempDir(), Force: true})
	require.NotNil(t, h.drainCreateRequests(), "--force must accept, not refuse")

	inst := titled(h, "forced")
	require.NotNil(t, inst, "the session must exist")
	assert.NotEmpty(t, inst.ClaudeAccountName(),
		"and be pinned to a member, or startNewSession would have refused it")
	assertCreateHeld(t, path)
}

// exhaustedPool configures a two-member claude pool with every member rate-limited —
// the state that raises the all-exhausted confirm in the TUI and has nobody to ask
// here. Two members, because gateAllExhausted deliberately exempts a singleton pool:
// one account has nothing to rotate to.
func exhaustedPool(t *testing.T, h *home) {
	t.Helper()
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
}

// TestCreateDrainRunsOnTheMetadataTick is the wiring guard. Every test above calls
// drainCreateRequests directly, so all of them would still pass with the call
// missing from Update — the command would be registered, documented and dead.
func TestCreateDrainRunsOnTheMetadataTick(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	h.Update(metadataUpdateDoneMsg{})

	assert.NotNil(t, titled(h, "fix-auth"), "the tick must reach the create drain")
}

// TestCreateDrainRunsOnAStaleAttachTick is the fact #760 rests on, and the one thing
// nothing here asserted: the create drain sits OUTSIDE the attachGen guard, so the tick
// message parked on Bubble Tea's unbuffered channel for the whole of an attach still
// creates the session when the loop resumes.
//
// That is what bounds the wait to the length of one attach rather than to a relaunch,
// and it is what `atrium new`'s "it lands when you detach" is a claim about. Inside the
// guard the request would sit until the NEXT tick — or, with the loop parked at the
// moment the app exits, until the next launch. The prompt spool has had
// TestDrainRunsOnStaleAttachTick for this since it was written; the create spool
// inherited the property and not the proof.
func TestCreateDrainRunsOnAStaleAttachTick(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	h.attachGen = 7
	h.Update(metadataUpdateDoneMsg{attachGen: 3}) // stale: captured before the attach

	assert.NotNil(t, titled(h, "fix-auth"),
		"a request spooled during an attach is created on the first tick after the detach")
}

// TestCreateDrainEmptySpoolIsQuiet: the common case by far, twice a second for the
// life of the TUI.
func TestCreateDrainEmptySpoolIsQuiet(t *testing.T) {
	h := drainHome(t)
	assert.Nil(t, h.drainCreateRequests())
	assert.Zero(t, h.list.NumInstances())
	assert.False(t, h.menu.HasNotice(), "nothing to report is not something to report")
}

// TestCreateSettlesOnlyAfterTheRowIsPersisted is the ordering drainOutbox documents
// as load-bearing and this path originally inverted: it unlinked first and persisted
// forty lines later.
//
// Two things go wrong in that window, and --wait sees both. A failed persist leaves a
// live worktree, branch and tmux session recorded nowhere, reported to the caller as a
// success; and even on the happy path --wait polls every 100ms and reads state.json
// the instant the file goes away, so a read landing first finds no row and prints a
// created session with no branch — byte-identical to the direct-session case.
func TestCreateSettlesOnlyAfterTheRowIsPersisted(t *testing.T) {
	h := drainHome(t)
	cs := withCapturingStore(t, h)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)

	cs.saveErr = errors.New("disk full")
	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "a create that could not be recorded is not a create that succeeded")
	assert.Contains(t, reason, "disk full")
}

// TestCreateSettlesOnSuccessfulPersist is the positive control for the above: with the
// save working, the same message clears the request rather than rejecting it.
func TestCreateSettlesOnSuccessfulPersist(t *testing.T) {
	h := drainHome(t)
	withCapturingStore(t, h)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)

	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})

	assertCreateSettled(t, path)
	_, rejected := outbox.Rejection(path)
	assert.False(t, rejected)
}

// TestForgetInstanceRejectsAnUnsettledCreateRequest: createsInFlight is a third map
// keyed by *session.Instance, and forgetInstance exists so a removed session does not
// pin one for the process lifetime. Dropping the entry silently would be worse than
// the leak: the spool file would have nothing left to settle it, so it would be
// re-read and re-created on the next launch while the caller's --wait timed out.
func TestForgetInstanceRejectsAnUnsettledCreateRequest(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)

	h.forgetInstance(inst)

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "a removed session owes its caller an answer")
	assert.Contains(t, reason, "removed before it finished starting")
	assert.Empty(t, h.createsInFlight, "and the map must not pin the instance")
}

// TestReconcileSettlesAnAdoptedCreateRequest: an ordinary SIGTERM lands in the adopt
// branch, which flips the session to Running and persists it without ever going
// through handleInstanceStarted. Unsettled, the request survives, the next launch
// re-reads it, and the title now collides — so the caller is handed "already used"
// for a session that exists and is running.
//
// Needs a real tmux session, because the adopt branch is gated on Started().
func TestReconcileSettlesAnAdoptedCreateRequest(t *testing.T) {
	testutil.RequireTmux(t)

	h := drainHome(t)
	withCapturingStore(t, h)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "adopt-me", Path: t.TempDir(), Program: "sleep 300", Direct: true,
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	require.True(t, inst.Started())
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading) // the dropped instanceStartedMsg

	path := spoolCreate(t, outbox.Request{Title: "adopt-me", Path: inst.Path})
	h.createsInFlight = map[*session.Instance]string{inst: path}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal shutdown
	h.reconcileInFlightStarts(ctx)

	require.Equal(t, session.Running, inst.GetStatus(), "precondition: the session was adopted")
	assertCreateSettled(t, path)
	_, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "adoption is a success, not a refusal")
}

// TestReconcileRejectsATornDownCreateRequest is the other half: a start that never
// completed is killed at shutdown, and its caller must be told rather than left to
// time out against a request nothing will ever pick up again.
func TestReconcileRejectsATornDownCreateRequest(t *testing.T) {
	h := drainHome(t)
	withCapturingStore(t, h)
	inst := addInstance(t, h, "never-finished", t.TempDir())
	inst.SetStatus(session.Loading) // Loading but never Started: the partial case

	path := spoolCreate(t, outbox.Request{Title: "never-finished", Path: inst.Path})
	h.createsInFlight = map[*session.Instance]string{inst: path}

	h.reconcileInFlightStarts(context.Background()) // live ctx: the force-quit abandon

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "an abandoned start owes its caller an answer")
	assert.Contains(t, reason, "atrium exited before it finished starting")
}

// TestCreateDrainHoldsWhileAQuitIsPending: a deferred quit completes only when
// nothing is Loading, so every session this drain starts postpones it by another
// Start. Before #703 that was bounded by what the user had submitted themselves —
// Loading had one producer, a keypress. A spool is bounded by nothing, so a queue of
// twenty would keep building worktrees well after the user pressed q, re-arming the
// "waiting for startup" notice at each completion.
func TestCreateDrainHoldsWhileAQuitIsPending(t *testing.T) {
	h := drainHome(t)
	h.quitRequested = true
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	assert.Nil(t, h.drainCreateRequests(), "a pending quit holds the drain")
	assert.Zero(t, h.list.NumInstances(), "nothing may be created after the user asked to leave")
	assertCreateQueued(t, path)

	// The control: clear the quit and the same request creates, so the hold above is
	// the quit and not some other refusal.
	h.quitRequested = false
	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestCreateDrainGatesOneRequestPerTick is the budget the start budget cannot supply.
// A refusal spends no start, so a backlog refused for a full cap would run every
// request through the gates inside one Update — and those gates are three git
// subprocesses each (targetValidity, RepoGroupKey, the branch-slug check), executed
// synchronously on the Bubble Tea update goroutine. Fifty of them is a frozen UI every
// 500ms for as long as the backlog lasts.
//
// Asserted as receipts written, because that is what a completed gate evaluation
// leaves behind: with the cap full, one tick may answer exactly one request.
func TestCreateDrainGatesOneRequestPerTick(t *testing.T) {
	h := drainHome(t)
	limit := 1
	h.appConfig.MaxSessions = &limit
	addInstance(t, h, "already-here", t.TempDir())

	paths := make([]string, 0, 5)
	for i := range 5 {
		paths = append(paths, spoolCreate(t, outbox.Request{Title: "q" + strconv.Itoa(i), Path: t.TempDir()}))
	}

	refuseDrain(t, h)

	answered := 0
	for _, p := range paths {
		if _, ok := outbox.Rejection(p); ok {
			answered++
		}
	}
	assert.Equal(t, 1, answered, "one tick may run the gates once; the rest wait for the next")
	assert.Equal(t, 4, createSpoolCount(t))

	// The control: the backlog does drain, one per tick, rather than being stuck.
	refuseDrain(t, h)
	assert.Equal(t, 3, createSpoolCount(t))
}

// TestCreateDrainDiscardsExpiredRequestsInBulk is the negative control for the gate
// budget: an expired or unreadable request costs a receipt and an unlink, no git at
// all, so those are NOT held to one a tick. Without this the two budgets could be
// collapsed into one and a cron backlog would clear at 2 records a second.
func TestCreateDrainDiscardsExpiredRequestsInBulk(t *testing.T) {
	h := drainHome(t)
	for i := range 5 {
		spoolCreate(t, outbox.Request{
			Title: "old" + strconv.Itoa(i), Path: t.TempDir(),
			CreatedAt: time.Now().Add(-2 * outbox.TTL),
		})
	}

	disposeDrain(t, h)
	assert.Zero(t, createSpoolCount(t), "expired requests are cheap and go in one tick")
}

// TestCreateDrainHoldsWhileAnActionIsInFlight: handleKeyPress refuses every mutating
// key while an async action runs (beginAsyncAction), so the drain must not be held to
// a weaker bar than pressing the new-session key. The case with teeth is the deep
// rename: renameIOCmd does the tmux rename, the `git branch -m` and the worktree move
// off-thread, and AdoptRename lands only afterwards — so mid-flight the instance still
// answers with its OLD title, the title check sees no conflict for the new one, and a
// create that wins the branch check but loses the rename adopts the branch it created.
func TestCreateDrainHoldsWhileAnActionIsInFlight(t *testing.T) {
	h := drainHome(t)
	h.actionInFlight = true
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	assert.Nil(t, h.drainCreateRequests(), "an in-flight action holds the drain")
	assert.Zero(t, h.list.NumInstances())
	assertCreateQueued(t, path)

	// The control: clear the action and the same request creates.
	h.actionInFlight = false
	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestCreateDrainRejectsABlankRequest: readCreate refuses the same (title, path) pair
// WriteCreate refuses to write, so a hand-written spool file cannot reach the gates.
// Nothing downstream would stop it — titleConflictIn deliberately answers "no conflict"
// for a blank title, and filepath.Abs("") is the draining TUI's own working directory
// with a nil error, so the request would build a worktree wherever atrium was launched.
func TestCreateDrainRejectsABlankRequest(t *testing.T) {
	for _, tc := range []struct{ name, title, path string }{
		// A real directory for the target, so targetValidity cannot be what refuses
		// it — without the decoder's check, a blank title reaches the list as a row
		// nothing can render.
		{"blank title", "  ", "%DIR%"},
		// And no path at all, which filepath.Abs turns into the draining TUI's own
		// working directory with a nil error: a worktree wherever atrium was launched.
		{"no path", "fix-auth", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			body, err := json.Marshal(map[string]any{
				"version": 1, "title": tc.title,
				"path": strings.ReplaceAll(tc.path, "%DIR%", t.TempDir()),
				// Stamped now, or the TTL horizon refuses it before the decoder does
				// and this proves nothing.
				"created_at": time.Now(),
			})
			require.NoError(t, err)
			dir, err := outbox.CreateDir()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			path := filepath.Join(dir, "1700000000000000000-abc.json")
			require.NoError(t, os.WriteFile(path, body, 0o644))

			disposeDrain(t, h)

			assert.Zero(t, h.list.NumInstances(), "nothing may be created from it")
			reason, rejected := outbox.Rejection(path)
			require.True(t, rejected, "and it must not be left to be re-read forever")
			assert.Contains(t, reason, "could not be read")
		})
	}
}

// TestHoldCreateRequestKeysOnTheInstanceItCreated: the hold is keyed by the object
// startNewSession built, never by a (Title, Path) lookup — those are different
// questions. titleConflictIn scopes its "already used" arm to a repo group, so a
// stored session whose GroupKey has diverged from git.RepoGroupKey for the same
// directory passes the conflict check and is still the FIRST identity match in the
// list. Keyed on that older row, the settle below is a silent miss: the entry never
// clears, createStartBudget is seeded from the map's length, and every later `atrium
// new` on the machine is skipped with no notice, no log line and no receipt.
func TestHoldCreateRequestKeysOnTheInstanceItCreated(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	decoy := addInstance(t, h, "fix-auth", dir) // same identity, added first
	created := addInstance(t, h, "fix-auth", dir)
	require.NotSame(t, decoy, created)

	r := outbox.Request{Title: "fix-auth", Path: dir}
	path := spoolCreate(t, r)
	h.holdCreateRequest(path, r, created)

	h.settleCreateRequest(created, nil)
	assert.Empty(t, h.createsInFlight, "the session that started is the one that settles")
	assertCreateSettled(t, path)
}

// TestFailedBackgroundCreateKillsOnlyItself. The failure path tears the new session
// down, and list.Kill() destroys whatever the CURSOR is on — which SelectInstance
// cannot be trusted to aim, because it ends in clampSelectionToNavigable: a row hidden
// inside a folded group (which a background create's row is, by design) snaps the
// selection to the group anchor. Aiming first therefore killed a live session, with its
// tmux pane and worktree, silently and with no confirmation — while the failed row
// stayed Loading forever, so `q` deferred indefinitely and the drain never ran again.
func TestFailedBackgroundCreateKillsOnlyItself(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	victim := addInstance(t, h, "victim", dir)
	addInstance(t, h, "other-repo", t.TempDir()) // a second group: folding needs one
	h.list.SelectInstance(victim)
	require.True(t, h.list.Collapse(), "precondition: victim's group is folded")

	spoolCreate(t, outbox.Request{Title: "doomed", Path: dir})
	require.NotNil(t, h.drainCreateRequests())
	doomed := titled(h, "doomed")
	require.NotNil(t, doomed)
	require.NotSame(t, doomed, h.list.GetSelectedInstance(),
		"precondition: the new row is hidden, so the cursor is elsewhere")

	h.handleInstanceStarted(instanceStartedMsg{
		instance: doomed, err: errors.New("worktree is dirty"), origin: spawnBackground,
	})

	assert.Nil(t, titled(h, "doomed"), "the session that failed is gone")
	assert.NotNil(t, titled(h, "victim"), "and the one that did not is still there")
	assert.Same(t, victim, h.list.GetSelectedInstance(), "the cursor never moved")
}

// TestBackgroundCreateLeavesTheHintBarAlone: the post-start SetState is a bare write,
// so unlike Menu.SetInstance — which rewrites only StateDefault/StateEmpty, precisely
// so the 100ms instanceChanged cannot do this — it overwrites a bar whose mode is still
// active. Marking sessions in visual mode and having a spooled `atrium new` land is
// enough to lose the gesture hints, and with hint_bar off the row goes blank.
func TestBackgroundCreateLeavesTheHintBarAlone(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)

	h.menu.SetState(ui.StateVisual)
	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnBackground})
	assert.Equal(t, ui.StateVisual, h.menu.State(), "a create nobody asked for may not reset the bar")

	// The control: a keypress-created session does reset it, which is what the write is
	// there for (StateEmpty -> StateDefault on the first session).
	h.menu.SetState(ui.StateVisual)
	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnInteractive})
	assert.Equal(t, ui.StateDefault, h.menu.State())
}

// TestBackgroundCreateAsksForNoResize: tea.RequestWindowSize's message reaches the
// WindowSizeMsg handler, which exits hint mode outright — the mode's frozen geometry is
// invalid after a resize. Nothing about the terminal changed when a list row appeared,
// so a background create must not ask for one, from either of the two places that do.
//
// Asserted on the two functions that decide rather than on the drain's return, because
// the drain only forwards what startNewSession built — and because reaching it means
// walking past a leaf that really starts a session.
func TestBackgroundCreateAsksForNoResize(t *testing.T) {
	h := drainHome(t)
	inst, spawned, err := h.startNewSession("fix-auth", t.TempDir(), true, false, "echo", "", "", nil, spawnBackground, nil)
	require.NoError(t, err)
	assert.False(t, requestsWindowSize(spawned), "startNewSession asked for a resize")

	_, started := h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnBackground})
	assert.False(t, requestsWindowSize(started), "the start handler asked for a resize")

	// The controls: the interactive origin does ask at both sites, so neither assertion
	// above can be passing vacuously.
	_, interactive, err := h.startNewSession("other", t.TempDir(), true, false, "echo", "", "", nil, spawnInteractive, nil)
	require.NoError(t, err)
	assert.True(t, requestsWindowSize(interactive))
	_, startedInteractive := h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnInteractive})
	assert.True(t, requestsWindowSize(startedInteractive))
}

// requestsWindowSize reports whether cmd asks for a resize, without running a single
// leaf. That restraint is the point: the batch startNewSession returns carries the
// closure that really runs `tmux new-session`, so a walk that invoked what it descended
// into would start a session — on the developer's own socket, if the sandbox
// TMUX_TMPDIR were ever absent (#581) — to answer a question about a command list.
//
// Identity, not messages: tea.RequestWindowSize is a package-level function, so its code
// pointer is stable, while calling it yields a message type bubbletea keeps unexported.
// One level of descent is the whole question, because both producers add it as a direct
// member of the batch they return; calling a tea.Batch closure yields those members
// rather than executing them.
func requestsWindowSize(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	if sameCmd(cmd, tea.RequestWindowSize) {
		return true
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, member := range batch {
		if sameCmd(member, tea.RequestWindowSize) {
			return true
		}
	}
	return false
}

// sameCmd compares two commands by function identity.
func sameCmd(a, b tea.Cmd) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// TestBackgroundCreateSpendsNoOneTimeState: the recent-path MRU feeds the create
// form's picker and the welcome's seen-bit is #381's "until the user has actually
// created a session". Both are about what the person at the keyboard did, so a CI job's
// create must write neither — a fresh install whose welcome is still on screen would
// otherwise have it burned by a session the user never asked for and never see it again.
func TestBackgroundCreateSpendsNoOneTimeState(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)

	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnBackground})
	assert.Zero(t, h.appState.GetHelpScreensSeen(), "the welcome bit is the user's to spend")
	assert.Empty(t, h.appState.GetRecentPaths(), "and so is the recent-path list")

	// The control: a keypress-created session spends both, which is what they are for.
	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnInteractive})
	assert.NotZero(t, h.appState.GetHelpScreensSeen())
	assert.Contains(t, h.appState.GetRecentPaths(), inst.Path)
}

// TestReconcileNamesAPersistFailureForWhatItIs: these instances reached the adopt
// branch through Started(), so the worktree, the branch and the agent all exist and
// what failed is the record of them. Told "the session could not be started", a
// retrying script re-runs `atrium new` with the same title and collides with the live
// tmux session and orphan branch the first run really did leave behind.
func TestReconcileNamesAPersistFailureForWhatItIs(t *testing.T) {
	testutil.RequireTmux(t)

	h := drainHome(t)
	cs := withCapturingStore(t, h)
	cs.saveErr = errors.New("no space left on device")
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "adopt-me", Path: t.TempDir(), Program: "sleep 300", Direct: true,
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading)

	path := spoolCreate(t, outbox.Request{Title: "adopt-me", Path: inst.Path})
	h.createsInFlight = map[*session.Instance]string{inst: path}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.reconcileInFlightStarts(ctx)

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected)
	assert.Contains(t, reason, "could not record it")
	assert.NotContains(t, reason, "could not be started", "the session did start; the record did not")
}

// TestBackgroundCreateDoesNotReflowTheFrameUnderAnOverlay: menuVisible is false in all
// fifteen modal states, so flashNotice falls back to the errBox row and calls
// recomputeLayout — the panes shrink by a row under the open overlay and grow back when
// the toast expires. That is the #518 shape, and a create nobody asked for must not
// cause it: the row now in the list is evidence enough. A refusal is the opposite case,
// invisible at the TUI otherwise and actionable only by the person there, so it keeps
// the fallback — asserted here too, because a fix that silenced both would look green.
func TestBackgroundCreateDoesNotReflowTheFrameUnderAnOverlay(t *testing.T) {
	h := drainHome(t)
	h.state = stateWelcome // the state a fresh install sits in; menuVisible is false
	require.False(t, h.menuVisible(), "precondition: the hint bar row is not available")

	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())
	require.NotNil(t, titled(h, "fix-auth"), "the create still happens behind the modal")
	assert.False(t, h.errBox.HasContent(),
		"a create nobody asked for must not take the errBox row under an open overlay")

	// The control: a refusal in the same state does take it, so the assertion above is
	// not passing merely because notices never reach the errBox from here.
	spoolCreate(t, outbox.Request{
		Title: strings.Repeat("a", session.MaxTitleLen+1), Path: t.TempDir(),
	})
	h.settleCreateRequest(titled(h, "fix-auth"), nil)
	require.NotNil(t, h.drainCreateRequests())
	assert.True(t, h.errBox.HasContent(), "a refusal is actionable and keeps the fallback row")
}

// TestCreateDrainStartsFromTheRequestedBaseBranch: --branch is carried across the wire
// and then handed to startNewSession by hand, positionally, alongside ten other
// arguments. Nothing else asserted it end to end, so executeCreateRequest could have
// passed "" and every headless session would have branched off HEAD instead of the base
// the caller named — silently, with a green suite. This is the "registered, documented
// and dead" shape one layer in from a keybinding.
//
// Asserted through the worktree's recorded BaseRef rather than the unexported
// baseBranch field: it is what Start actually used, and what state.json keeps. The
// drain's own command is deliberately not run — startNewSession has already called
// SetBaseBranch by the time it returns, so starting the instance here exercises the
// same wiring without executing a batch that also carries a notice timer.
func TestCreateDrainStartsFromTheRequestedBaseBranch(t *testing.T) {
	testutil.RequireTmux(t)

	h := drainHome(t)
	h.program = "sleep 300" // a real Start: the session has to outlive the wait for it
	repo := gitRepoWithBranch(t, "release/2.0")
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo, Branch: "release/2.0"})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	require.NoError(t, inst.Start(true))

	assert.Equal(t, "release/2.0", inst.ToInstanceData().Worktree.BaseRef,
		"the session must be based on the branch the request named")
}

// TestCreateDrainWithoutABaseBranchUsesHead is the control for the test above: without
// --branch the recorded base is empty, which is what makes "release/2.0" evidence that
// the request's value arrived rather than something every create records anyway.
func TestCreateDrainWithoutABaseBranchUsesHead(t *testing.T) {
	testutil.RequireTmux(t)

	h := drainHome(t)
	h.program = "sleep 300" // a real Start: the session has to outlive the wait for it
	repo := gitRepoWithBranch(t, "release/2.0")
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})

	require.NotNil(t, h.drainCreateRequests())
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst)
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	require.NoError(t, inst.Start(true))

	assert.Empty(t, inst.ToInstanceData().Worktree.BaseRef,
		"an unflagged create bases on HEAD, recording no explicit base")
}

// TestCreateDrainRefusesABaseBranchForANonGitTarget: startNewSession drops the base
// branch for a direct session, so without this the request would succeed, produce no
// worktree and no branch, and report back as `created "fix-auth"` with no branch clause
// — which is byte-identical to a legitimate direct create. Refusing is the only signal
// that separates them, and it is also the only signal available when `direct` is wrong:
// targetValidity infers it from git.IsGitRepo, which answers false for a transient
// failure exactly as it does for "not a repo".
func TestCreateDrainRefusesABaseBranchForANonGitTarget(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: t.TempDir(), Branch: "release/2.0", // a plain dir, no git
	})

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "release/2.0", "the refusal names the branch that could not be used")
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainStillCreatesADirectSessionWithoutABranch is the control: the refusal
// above is about the combination, not about non-git targets, which `atrium new` supports.
func TestCreateDrainStillCreatesADirectSessionWithoutABranch(t *testing.T) {
	h := drainHome(t)
	spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())
	assert.NotNil(t, titled(h, "fix-auth"))
}

// TestBackgroundCreateSizesItsOwnPane is the other half of "asks for no resize".
// updateHandleWindowSizeEvent ends in SetSessionPreviewSize, the only production caller
// that gives a detached tmux session the preview's geometry, and it skips any instance
// that is not yet Started. tea.RequestWindowSize is what used to trigger it at exactly
// the moment a new session became Started — so dropping that request for a background
// create left the pane at its `new-session -d` default (measured: 80 columns against a
// 116-column preview) until some unrelated resize happened to fix it. Every capture
// taken meanwhile is wrapped at a width the pane never had, which is what every
// width-sensitive classifier in session/agent then reads.
//
// Asserted at the seam rather than by reading the width back out of tmux. See
// sizeStartedPane: the width tmux reports is the outcome of its own SIGWINCH handling
// and client-size policy, so a test that read it was pinning tmux's behaviour and
// raced its propagation. The two things this branch is actually responsible for are
// that the call happens and that it carries the preview's geometry.
func TestBackgroundCreateSizesItsOwnPane(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "sizeme", t.TempDir())
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 44})
	wantW, wantH := h.tabbedWindow.GetPreviewSize()

	var gotInst *session.Instance
	var gotW, gotH int
	restore := sizeStartedPane
	t.Cleanup(func() { sizeStartedPane = restore })
	sizeStartedPane = func(i *session.Instance, w, h int) error {
		gotInst, gotW, gotH = i, w, h
		return nil
	}

	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnBackground})

	require.Same(t, inst, gotInst, "the pane sized must be the one this message is about")
	assert.Equal(t, wantW, gotW, "sized to the preview's width")
	assert.Equal(t, wantH, gotH, "and its height")

	// The control: the interactive origin sizes nothing here, because the resize it
	// asks for reaches every started instance through SetSessionPreviewSize. Without
	// this, moving the call outside the origin check would still pass above.
	gotInst = nil
	h.handleInstanceStarted(instanceStartedMsg{instance: inst, origin: spawnInteractive})
	assert.Nil(t, gotInst, "the interactive origin resizes through its WindowSizeMsg instead")
}

// TestHoldCreateRequestPoisonsARecordItCouldNotClaim closes the hole deleting
// createRequestInFlight left.
//
// The old drain skipped a request whose instance was in createsInFlight. That scan was
// removed on the argument that holdCreateRequest renames an accepted request out of the
// record name format, so ListCreates cannot return one that is still starting — true of
// the rename that SUCCEEDS. holdCreateRequest deliberately builds the session anyway
// when the claim fails (refusing one whose worktree is already going up would be worse),
// and the record then keeps its own name with nothing left to skip it.
//
// The consequence is not theoretical: the next tick runs the gates against the row
// startNewSession has already inserted, titleConflictIn finds it, and the caller's
// --wait is handed "already used" naming their own session — the exact #716 symptom this
// PR exists to remove — for a session that is about to come up fine. The expiry arm can
// also unlink the record out from under a build still running its setup script.
//
// Staged by making the claim rename fail: the spool directory is stripped of write
// permission, so os.Rename inside outbox.Claim cannot create the new name.
func TestHoldCreateRequestPoisonsARecordItCouldNotClaim(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the claim would not fail")
	}
	h := drainHome(t)
	dir := t.TempDir()
	created := addInstance(t, h, "fix-auth", dir)

	r := outbox.Request{Title: "fix-auth", Path: dir}
	path := spoolCreate(t, r)

	spoolDir := filepath.Dir(path)
	require.NoError(t, os.Chmod(spoolDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(spoolDir, 0o700) })

	h.holdCreateRequest(path, r, created)

	require.FileExists(t, path, "the record kept its name, which is the premise of this test")
	assert.True(t, h.outboxPoisoned[path],
		"so the drain must be told to skip it for the rest of this run")
}

// TestHoldCreateRequestDoesNotPoisonARecordItClaimed is that guard's negative control.
// Poisoning unconditionally would pass the test above and quietly disable the skip's
// real job — a record whose unlink failed — as well as making every settle's
// bookkeeping meaningless.
func TestHoldCreateRequestDoesNotPoisonARecordItClaimed(t *testing.T) {
	h := drainHome(t)
	dir := t.TempDir()
	created := addInstance(t, h, "fix-auth", dir)

	r := outbox.Request{Title: "fix-auth", Path: dir}
	path := spoolCreate(t, r)
	h.holdCreateRequest(path, r, created)

	assertCreateHeld(t, path)
	assert.False(t, h.outboxPoisoned[path], "a claim that worked needs no skip; the rename is the skip")
}
