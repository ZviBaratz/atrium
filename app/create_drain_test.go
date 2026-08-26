package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
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
		if inst.Title() == title {
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

// gitEnv is a hermetic committer identity: the developer's global config may set neither,
// and a commit without one fails rather than defaulting.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

// branchTip returns the commit refs/heads/<branch> points at, or "" when there is none.
//
// It shells out rather than calling git.LookupLocalBranchTip, because these fixtures pin
// what the ADOPTION is checked against: reading the value through the same function the
// check uses would make a pin that is wrong in both places agree with itself.
func branchTip(t *testing.T, repo, branch string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "--verify", "--quiet",
		"refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		return "" // no such branch; rev-parse --quiet exits 1
	}
	return strings.TrimSpace(string(out))
}

// commitOnto adds a commit to branch, moving its tip — the thing that happens to an
// orphan branch between the reconcile that vetted it and the drain that executes.
func commitOnto(t *testing.T, repo, branch string) {
	t.Helper()
	for _, args := range [][]string{
		{"-C", repo, "checkout", branch},
		{"-C", repo, "commit", "--allow-empty", "-m", "somebody else's work"},
		{"-C", repo, "checkout", "-"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Env = gitEnv()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// adoptedRequest is the request shape reconcileCreateClaims re-queues for an orphan
// branch: Adopt, the claim's evidence block naming the session branch, and the pin the
// drain re-checks before it honours the branch-gate skip (recheckAdoption).
//
// Hand-built rather than driven through the reconcile, so a drain test stays a drain test —
// but complete, because an Adopt request missing any of the three fails closed and would
// take the ordinary gate instead, which is the arm these tests are not about.
func adoptedRequest(t *testing.T, h *home, title, repo string) outbox.Request {
	t.Helper()
	branch := h.appConfig.BranchPrefix + title
	return outbox.Request{
		Title: title, Path: repo, Adopt: true, AdoptTip: branchTip(t, repo, branch),
		Claim: &outbox.ClaimMeta{At: time.Now(), SessionBranch: branch, BranchExisted: true},
	}
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
	path := spoolCreate(t, adoptedRequest(t, h, "fix-auth", repo))

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
	path := spoolCreate(t, adoptedRequest(t, h, "fix-auth", repo))

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
// Measured, not inferred — and no row here can prove that any more, which is worth saying
// plainly rather than leaving the docstring claiming one does. The third row used to split
// the field from the flag: a request the reconcile marked Adopt whose branch someone deleted
// before this tick ran had no existing branch, so recording "true" off the flag would have
// told the NEXT reconcile the branch it finds was foreign, turning a recoverable build into
// a permanent refusal.
//
// recheckAdoption closes that gap upstream. It withdraws Adopt before holdCreateRequest
// runs, for every branch that is absent or not the one that was vetted, so by the time the
// claim is written the flag and the tree agree in every state reachable here — a moved
// branch never gets this far, being refused at the gate. What these rows assert is the
// recorded VALUE, which is still the thing the reconcile reads; the guard against inferring
// it from the flag now lives in the withdrawal, and
// TestCreateDrainDisclosesAWithdrawnAdoption is what holds that.
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
			req := outbox.Request{Title: "fix-auth", Path: repo}
			if tc.adopt {
				req = adoptedRequest(t, h, "fix-auth", repo)
				if req.AdoptTip == "" {
					// The third row's whole point: the reconcile pinned a commit and the
					// branch was deleted before this tick ran, so the pin names something
					// no ref reaches. Left empty it would instead exercise the
					// no-pin-at-all arm, which is a different refusal.
					req.AdoptTip = strings.Repeat("0", 40)
				}
			}
			path := spoolCreate(t, req)

			require.NotNil(t, h.drainCreateRequests())
			assertCreateHeld(t, path)

			claims, err := outbox.ListClaims()
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.NotNil(t, claims[0].Request.Claim)
			assert.Equal(t, tc.want, claims[0].Request.Claim.BranchExisted)
			assert.Equal(t, h.appConfig.BranchPrefix+"fix-auth", claims[0].Request.Claim.SessionBranch,
				"the branch recorded must be the one Setup would mint")
			assert.Equal(t, tc.adopt && tc.want, claims[0].Request.Adopt,
				"and the licence recorded must be the one this build actually used")
		})
	}
}

// TestHoldCreateRequestMeasuresTheBranchRatherThanCopyingTheLicence is the field's own guard,
// and it is here rather than in the table above because no row of that table can separate the
// two: every request the drain reaches ends with BranchExisted equal to the Adopt it executed
// with, so `meta.BranchExisted = r.Adopt` passes the whole suite while writing a guess into
// the one field whose job is to be evidence.
//
// The state that separates them is reachable and is the one the pin exists for: the re-check
// says the branch is gone, the licence is withdrawn, and something recreates that name — a
// fetch, a rebase, another checkout — before the claim is written. Then BranchExisted is true
// and Adopt is false, which is exactly the pair classifyCreateClaim reads as "somebody else's
// branch is in the way" and refuses. Copied off the licence it reads as "nothing was there",
// and the next launch adopts a stranger's work.
func TestHoldCreateRequestMeasuresTheBranchRatherThanCopyingTheLicence(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	inst := addInstance(t, h, "fix-auth", repo)
	require.False(t, inst.IsDirect(), "precondition: a session with a branch to record")
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})

	// Adopt false, the branch present: the drain's own gates never produce this pair, so it
	// is driven directly.
	h.holdCreateRequest(path, outbox.Request{Title: "fix-auth", Path: repo}, inst)

	claims, err := outbox.ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotNil(t, claims[0].Request.Claim)
	assert.False(t, claims[0].Request.Adopt, "precondition: no licence was carried")
	assert.True(t, claims[0].Request.Claim.BranchExisted,
		"and the field says what git said, not what the licence said")
}

// TestCreateDrainPersistsAWithdrawnAdoption: outbox.Claim re-reads the record, so a withdrawal
// the drain keeps in its own copy never reaches the claim it writes. The next launch then reads
// a licence this build did not use, and classifyCreateClaim's "the branch already existed
// before it started, so it is not this session's to take" refusal never fires.
func TestCreateDrainPersistsAWithdrawnAdoption(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "") // the pinned branch is gone, so the licence is withdrawn
	req := adoptedRequest(t, h, "fix-auth", repo)
	req.AdoptTip = strings.Repeat("0", 40)
	path := spoolCreate(t, req)

	require.NotNil(t, h.drainCreateRequests())
	assertCreateHeld(t, path)

	claims, err := outbox.ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.False(t, claims[0].Request.Adopt, "the claim records the licence as withdrawn")
	assert.Empty(t, claims[0].Request.AdoptTip, "and drops the pin it was withdrawn against")
	assert.False(t, claims[0].Request.CreatedAt.IsZero(),
		"while the record's own fields survive the edit, or nothing can judge its age")
}

// TestCreateDrainAnswersAnAdoptionWhoseRepoIsGone: "git could not be asked" and "git says
// there is no repository here" arrive as one error, and only the first is a fact about the
// machine. Folded together, a re-queued adoption whose repo has been deleted is held for the
// full 24h TTL — `atrium new --wait` timing out with no receipt at all — and pays a
// for-each-ref fork on every one of those ticks. Answered, it goes through the same gates an
// ordinary request would and the caller is told in one tick.
func TestCreateDrainAnswersAnAdoptionWhoseRepoIsGone(t *testing.T) {
	h := drainHome(t)
	gone := filepath.Join(t.TempDir(), "deleted-repo")
	path := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: gone, Adopt: true, AdoptTip: strings.Repeat("a", 40),
		Claim: &outbox.ClaimMeta{At: time.Now(), SessionBranch: h.appConfig.BranchPrefix + "fix-auth"},
	})

	refuseDrain(t, h)

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "the caller is answered rather than left to time out")
	assert.Contains(t, reason, "is not a directory")
	assert.False(t, h.createAdoptHeld, "and nothing is being held on the machine's account")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state)
	assert.False(t, d.Leftovers(), "a repo that is gone took its branch with it")
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
	// Auto-attach off, and that is the difference between a control and a coin flip.
	// handleInstanceStarted returns attachExec for a session shouldAutoOpen approves —
	// a command that carries no resize — and shouldAutoOpen ends in TmuxAlive(), which
	// for a session running `echo` is a race against `echo` exiting. With auto-attach
	// on (the default) the interactive control below therefore failed whenever the
	// session happened to still be up, which under coverage instrumentation is often
	// enough to break a CI run. Auto-attach is a third behaviour this test is not about.
	noAutoAttach := false
	h.appConfig.AutoAttach = &noAutoAttach
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

// TestCreateDrainDisclosesTheOrphanWhenItRefusesAnAdoption is #731's first hole.
//
// applyCreateClaim releases the interrupted build's worktree BEFORE it re-queues, which is
// right on its own terms — the drain can pick the request up on the very next tick, and a
// Setup that runs while the stale worktree still holds the branch dies on git's "already
// used by worktree". But the re-queued request then meets gates that refuse destructively,
// and a title taken since the crash is one of them. Receipt written, record unlinked: the
// branch had no row, no claim, no request and, after the release, no worktree registration
// either — invisible to `atrium ls`, to `atrium reap` and to `git worktree list` alike. The
// recovery is one-shot, so that state was permanent.
func TestCreateDrainDisclosesTheOrphanWhenItRefusesAnAdoption(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	addInstance(t, h, "fix-auth", repo) // the title taken since the crash
	path := spoolCreate(t, adoptedRequest(t, h, "fix-auth", repo))

	refuseDrain(t, h)

	reason, ok := outbox.Rejection(path)
	require.True(t, ok, "precondition: the caller is still answered")
	assert.Contains(t, reason, titleErrAlreadyUsed)
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "and the branch the release freed must not go unmentioned")
	assert.Equal(t, branch, d.Branch)
	assert.Equal(t, repo, d.Repo)
	assert.Contains(t, d.Reason, titleErrAlreadyUsed)
}

// TestCreateDrainMarksAnOrdinaryRefusalWithoutReportingIt is that arm's other half, and the
// two jobs of a disclosure are what separate them. A request nothing has built yet leaves no
// INVENTORY, so there is nothing for a modal to name and one saying "a create failed" would
// be noise the caller's receipt already carried. It still leaves a MARK, because
// discardSpoolFile's unlink can fail and most of the gates are facts about the fleet rather
// than about the request — a cap that is raised, a title a kill frees — so a record that
// survives its own Reject is one a later launch lets through, for a caller that read the
// refusal and exited non-zero.
func TestCreateDrainMarksAnOrdinaryRefusalWithoutReportingIt(t *testing.T) {
	h := drainHome(t)
	warnings := captureWarnings(t)
	repo := gitRepoWithBranch(t, "")
	addInstance(t, h, "fix-auth", repo)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})

	refuseDrain(t, h)

	_, ok := outbox.Rejection(path)
	require.True(t, ok)
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "or a record that outlives its Reject is re-drained")
	assert.False(t, d.Leftovers(), "and there is nothing for a modal to name")
	assert.Contains(t, d.Reason, titleErrAlreadyUsed)

	// Not buffered at all, which is the other half of "not reported". A mark with nothing to
	// name has no reader, so the two things a buffered entry buys — a place in the report, and
	// clearMarkOverADroppedRecord's promise not to delete a file nobody has seen — are both
	// vacuous for it, while the slice it sat in has no ceiling and is emptied only by a flush
	// that returns early behind any overlay. Its guard job is held by ClearDisclosure's own
	// recordStillSpooled check instead, which is where it belongs.
	assert.Empty(t, h.pendingCreateDisclosures,
		"a mark nobody will read does not need a place in the report to keep guarding")
	h.flushCreateDisclosures()
	assert.Equal(t, stateDefault, h.state, "and no modal comes of it")
	// Nor does it claim, at WARNING, that a title collision left artifacts. One wording for
	// both cases printed `left artifacts belonging to no session (branch "", worktree "",
	// tmux "")` for the overwhelming majority of giving-ups — the opposite of what happened,
	// on the level an operator greps, burying the one case where it is true.
	assert.NotContains(t, warnings.String(), "left artifacts belonging to no session",
		"a refusal that built nothing did not leave artifacts")
	assert.Contains(t, warnings.String(), "refusing a create request",
		"precondition: this arm's own warning did reach the buffer")
}

// TestCreateDrainRechecksTheAdoptionPin is #731's second hole. The evidence licensing an
// Adopt is gathered by the startup reconcile and the skip is taken whenever the drain gets
// round to the request, which can be much later: tmux off PATH holds this drain
// indefinitely, so does createDrainHeld, so does another crash. Delete and recreate the
// branch in that window — by hand, by a fetch, by a rebase — and the name still matches
// while the commits are somebody else's, so Setup takes its existing-branch arm and the
// agent silently resumes on their work.
//
// A moved tip is the same evidence as a recreated branch and is what a test can arrange.
// Without the re-check both rows below adopt; with it, only the unmoved one does.
func TestCreateDrainRechecksTheAdoptionPin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		moveTip   bool
		wantAdopt bool
	}{
		{name: "pin still matches", wantAdopt: true},
		{name: "somebody else committed on it", moveTip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			branch := h.appConfig.BranchPrefix + "fix-auth"
			repo := gitRepoWithBranch(t, branch)
			req := adoptedRequest(t, h, "fix-auth", repo)
			if tc.moveTip {
				commitOnto(t, repo, branch)
			}
			path := spoolCreate(t, req)

			cmd := h.drainCreateRequests()

			reason, rejected := outbox.Rejection(path)
			if tc.wantAdopt {
				require.NotNil(t, cmd, "an adoption whose pin holds is still created")
				assert.False(t, rejected, "and is not handed a receipt: %s", reason)
				assert.Equal(t, 1, h.list.NumInstances())
				return
			}
			require.True(t, rejected, "a branch that is no longer ours must meet the branch gate")
			assert.Contains(t, reason, "branch already exists")
			assert.Zero(t, h.list.NumInstances(), "and no agent is put on somebody else's work")
		})
	}
}

// TestCreateDrainCreatesFreshWhenTheAdoptedBranchIsGone is the third outcome of that
// re-check, and the one that must NOT be a refusal: a pin naming a branch nobody can find
// means there is nothing to adopt and nothing in the way, which is exactly what the
// ordinary gates already handle. Refusing here would spend the recovery on a branch a
// person had already tidied up themselves.
func TestCreateDrainCreatesFreshWhenTheAdoptedBranchIsGone(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "") // the orphan branch was deleted by hand
	req := adoptedRequest(t, h, "fix-auth", repo)
	req.AdoptTip = strings.Repeat("0", 40)
	path := spoolCreate(t, req)

	require.NotNil(t, h.drainCreateRequests())

	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "nothing was in the way: %s", reason)
	assert.Equal(t, 1, h.list.NumInstances())
	_, state := outbox.DisclosureFor(path)
	assert.Equal(t, outbox.NoDisclosure, state, "and there was no orphan left to disclose")
}

// TestCreateDrainHoldsAnAdoptionItCannotRecheck: a repo git cannot read is a fact about the
// machine, not about the request — the distinction the tmux probe already makes here — and
// this request in particular is the second attempt at a build that already made a branch, so
// refusing it spends the one recovery a stranded create gets on a condition that may clear
// on the next tick. Held, the request keeps its TTL and its claim to that branch.
func TestCreateDrainHoldsAnAdoptionItCannotRecheck(t *testing.T) {
	h := drainHome(t)
	path := unaskableAdoption(t, h, "fix-auth")

	assert.Nil(t, h.drainCreateRequests(), "nothing was created and nothing was refused")

	reason, rejected := outbox.Rejection(path)
	assert.False(t, rejected, "a machine that cannot answer is not a reason to refuse: %s", reason)
	assertCreateQueued(t, path)
	assert.True(t, h.createAdoptHeld, "and the hold is logged once rather than twice a second")
}

// gitCannotAnswerFor makes the adoption re-check fail to RUN for the given repos, and only
// for those — the one condition recheckAdoption holds on rather than answers.
//
// Through the seam rather than through a broken path, because a broken path is no longer that
// condition: git reports a missing directory or a plain directory by exiting non-zero, and
// recheckAdoption reads an answer git gave as a fact about the request, withdraws the licence
// and lets the gates deal with it. What holds is git not running, and emptying PATH would do
// that to every repo in the process at once — including the readable one a starvation test
// needs beside the broken one.
func gitCannotAnswerFor(t *testing.T, repos ...string) {
	t.Helper()
	broken := make(map[string]bool, len(repos))
	for _, repo := range repos {
		broken[repo] = true
	}
	orig := lookupBranchTip
	lookupBranchTip = func(ctx context.Context, repo, branch string) (string, error) {
		if broken[repo] {
			return "", errors.New(`exec: "git": executable file not found in $PATH`)
		}
		return orig(ctx, repo, branch)
	}
	t.Cleanup(func() { lookupBranchTip = orig })
}

// unaskableAdoption spools a re-queued adoption whose repo is real, whose branch is real, and
// whose pin git cannot be asked about.
func unaskableAdoption(t *testing.T, h *home, title string) string {
	t.Helper()
	branch := h.appConfig.BranchPrefix + title
	repo := gitRepoWithBranch(t, branch)
	gitCannotAnswerFor(t, repo)
	return spoolCreate(t, outbox.Request{
		Title: title, Path: repo, Adopt: true, AdoptTip: strings.Repeat("a", 40),
		Claim: &outbox.ClaimMeta{At: time.Now(), SessionBranch: branch},
	})
}

// startedCreate is an `atrium new` whose session is genuinely built — worktree, branch,
// tmux session and agent — and whose row has not been written yet. It is the state the
// persist-failure paths act on, and it has to be real: an instance the drain constructed but
// never booted has an empty Branch, worktree path and tmux name, so every assertion about
// what the failure discloses compares "" to "" and passes with the inventory deleted.
func startedCreate(t *testing.T, h *home) (*session.Instance, string) {
	t.Helper()
	testutil.RequireTmux(t)
	repo := gitRepoWithBranch(t, "")
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "fix-auth", Path: repo, Program: "sleep 300",
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	require.NotEmpty(t, inst.Branch())
	require.NotEmpty(t, inst.GetWorktreePath())
	require.NotEmpty(t, inst.TmuxSessionName())

	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})
	h.createsInFlight = map[*session.Instance]string{inst: path}
	return inst, path
}

// TestCreateDrainDisclosesAPersistFailure is #732 on the in-process path. The session
// started fine — worktree, branch, tmux session and agent all exist — and what failed is
// persistInstances, the record of them. Telling the caller is right; dropping the claim as
// well left the next launch nothing to reconcile, so the branch and the worktree belonged to
// nothing and nothing ever mentioned them again.
//
// The file, and NOT this process's report. Nothing tore the session down — it is in the
// list, Running, one row away — so a modal naming its branch, its worktree and its tmux
// session under "nothing here will clean them up" would be an instruction to destroy a live
// agent. The file covers the only way these become orphans, which is this process dying
// before a later persist lands the row.
func TestCreateDrainDisclosesAPersistFailure(t *testing.T) {
	h := drainHome(t)
	cs := withCapturingStore(t, h)
	inst, path := startedCreate(t, h)

	cs.saveErr = errors.New("disk full")
	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "precondition: the caller still hears the failure")
	assert.Contains(t, reason, "disk full")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "and what the create left behind must survive the answer")
	assert.Equal(t, "fix-auth", d.Title)
	assert.Equal(t, inst.Branch(), d.Branch)
	assert.Equal(t, inst.GetWorktreePath(), d.Worktree)
	assert.Equal(t, inst.TmuxSessionName(), d.TmuxName)
	assert.Contains(t, d.Reason, "disk full")

	require.Equal(t, 1, h.list.NumInstances(), "precondition: nothing tore the session down")
	require.Equal(t, session.Running, inst.GetStatus())
	assert.Empty(t, h.pendingCreateDisclosures,
		"so no modal tells the user to delete a session that is in the list")
	require.Len(t, h.unrecordedCreates, 1, "it is remembered for withdrawal instead")
	assert.Equal(t, path, h.unrecordedCreates[0].record)
	assert.Same(t, inst, h.unrecordedCreates[0].inst,
		"with the session, because what makes it false is THIS row landing")
}

// TestASucceedingPersistWithdrawsTheDisclosure is the other half of that decision, and the
// reason the file can be written for a live session at all. The moment the row IS durable
// the disclosure is a false report — the next launch would name a branch, a worktree and a
// tmux session that state.json has a row for, under a header saying nothing points at them —
// and a transient ENOSPC or NFS stall is followed by a successful save within seconds.
func TestASucceedingPersistWithdrawsTheDisclosure(t *testing.T) {
	h := drainHome(t)
	cs := withCapturingStore(t, h)
	inst, path := startedCreate(t, h)

	cs.saveErr = errors.New("disk full")
	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})
	_, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "precondition: the failure left one")

	cs.saveErr = nil
	require.NoError(t, h.persistInstances())

	_, state = outbox.DisclosureFor(path)
	assert.Equal(t, outbox.NoDisclosure, state, "the row is durable, so the leftovers are not leftovers")
	assert.Empty(t, h.unrecordedCreates)
}

// TestAPersistWithoutTheRowKeepsTheDisclosure is what "a save landed" cannot stand in for, and
// it is #732 recreated by its own fix. A kill drops the row BEFORE it reports the outcome —
// applyKillDone removes the instance and then says "killed but teardown was incomplete", and
// killInstances does the same in the batch path — so a teardown that failed leaves a branch, a
// worktree and an agent standing while every save after it writes a list the session is not in.
// Withdrawing on the bare fact of success deletes the only file that named them.
//
// Storage.SaveInstances' own filter is the second reason: it skips any instance that has not
// Started(), so even a row still in the list is not necessarily on disk.
func TestAPersistWithoutTheRowKeepsTheDisclosure(t *testing.T) {
	h := drainHome(t)
	cs := withCapturingStore(t, h)
	inst, path := startedCreate(t, h)

	cs.saveErr = errors.New("disk full")
	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})
	require.Len(t, h.unrecordedCreates, 1, "precondition: the failure left one to withdraw")

	// The row goes, the artifacts do not — the state a failed teardown leaves.
	h.list.RemoveInstance(inst)
	cs.saveErr = nil
	require.NoError(t, h.persistInstances())

	_, state := outbox.DisclosureFor(path)
	assert.Equal(t, outbox.HasDisclosure, state,
		"nothing recorded this session, so the one file naming its leftovers has to stay")
	assert.Len(t, h.unrecordedCreates, 1, "and it is still owed a withdrawal if the row comes back")
}

// TestWithdrawalKeepsAnEntryClearDisclosureDeclined: ClearDisclosure returns nil both for
// "unlinked" and for "kept, because a record or claim beside it is still executable", and this
// caller is the one that has to tell them apart. Reading the kept case as done drops the only
// handle for a retry — leaving a disclosure that names a live session's branch, worktree and
// tmux session, which the next launch reports as an orphan and offers a kill-session command
// for.
func TestWithdrawalKeepsAnEntryClearDisclosureDeclined(t *testing.T) {
	h := drainHome(t)
	cs := withCapturingStore(t, h)
	inst, path := startedCreate(t, h)

	cs.saveErr = errors.New("disk full")
	h.Update(instanceStartedMsg{instance: inst, origin: spawnBackground})
	require.Len(t, h.unrecordedCreates, 1)
	// The claim back on disk: the state a failed removeClaim leaves, and the one
	// ClearDisclosure refuses to drop a mark over.
	require.NoError(t, os.WriteFile(outbox.ClaimPath(path), []byte("{}"), 0o644))

	cs.saveErr = nil
	require.NoError(t, h.persistInstances())

	_, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "precondition: the mark is still guarding the claim")
	assert.Len(t, h.unrecordedCreates, 1, "so the withdrawal is still owed and still remembered")

	// And it lands once the claim does go, which is what makes keeping the entry a retry
	// rather than a leak.
	require.NoError(t, os.Remove(outbox.ClaimPath(path)))
	require.NoError(t, h.persistInstances())
	_, state = outbox.DisclosureFor(path)
	assert.Equal(t, outbox.NoDisclosure, state)
	assert.Empty(t, h.unrecordedCreates)
}

// TestReconcileInFlightStartsDisclosesAPersistFailure is #732's worse call site: the same
// failure on the shutdown path, where there is no later persist attempt and no frame left to
// toast on, so before the disclosure it was terminal.
func TestReconcileInFlightStartsDisclosesAPersistFailure(t *testing.T) {
	testutil.RequireTmux(t)

	h := drainHome(t)
	cs := withCapturingStore(t, h)
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

	cs.saveErr = errors.New("read-only file system")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal shutdown
	h.reconcileInFlightStarts(ctx)

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "precondition: an adopted session whose row failed owes its caller")
	assert.Contains(t, reason, "read-only file system")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "and the live agent this exit leaves behind must be on disk for the next launch")
	assert.Equal(t, inst.TmuxSessionName(), d.TmuxName)
	assert.True(t, d.Leftovers())
}

// TestCreateDrainLiftsTheAdoptionHold is the other side of that hold's switch. The bool is
// state about the LOG LINE, not about the hold — the hold itself is re-decided every tick —
// so it has to clear when there is nothing held any more. Left set, the next request git
// cannot answer for is held in silence.
//
// The held request going away is how that happens in practice: it expires out of the spool
// at the horizon, or `atrium reset` clears it, and no probe ever succeeds to un-set the flag.
func TestCreateDrainLiftsTheAdoptionHold(t *testing.T) {
	h := drainHome(t)
	held := unaskableAdoption(t, h, "fix-auth")
	require.Nil(t, h.drainCreateRequests())
	require.True(t, h.createAdoptHeld, "precondition: the hold is on")

	require.NoError(t, outbox.DiscardCreate(held))
	path := spoolCreate(t, outbox.Request{Title: "other", Path: t.TempDir()})
	require.NotNil(t, h.drainCreateRequests())

	assert.False(t, h.createAdoptHeld, "or the next unanswerable re-check is held in silence")
	assertCreateHeld(t, path)
}

// TestCreateDrainDropsARecordItAlreadyGaveUpOn is #731's third hole in its RECORD shape,
// which the claim-side guard does not cover.
//
// rejectCreateRequest writes the disclosure and then Reject, and Reject writes the receipt
// before it unlinks. A failed unlink — EACCES on the spool, EIO — or a kill between the two
// leaves record + receipt + disclosure, and the only thing standing between that record and
// a session was the in-memory poison map, which the next launch does not inherit. So the
// caller reads its receipt and exits non-zero while the next launch builds the session
// anyway, and the modal opens naming the branch it is being built on.
func TestCreateDrainDropsARecordItAlreadyGaveUpOn(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "")
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})
	require.NoError(t, outbox.Disclose(path, &outbox.Disclosure{
		Title: "fix-auth", Repo: repo, Branch: "zvi/fix-auth",
		Reason: "the title is already used by another session"}))

	assert.Nil(t, h.drainCreateRequests(), "an answered request must not be built")

	assert.Zero(t, h.list.NumInstances())
	assertCreateSettled(t, path)
	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "and the receipt is re-written, since the crash may have been before it")
	assert.Contains(t, reason, "already used")
}

// TestCreateDrainDisclosesAWithdrawnAdoption is #731's first hole re-entered through the
// re-check that closes its second.
//
// applyCreateClaim releases the interrupted build's worktree registration BEFORE it
// re-queues, deliberately — a Setup that runs while the stale worktree holds the branch dies
// on git's "already used by worktree". So by the time the drain sees an Adopt request, the
// branch has no row, no claim and no worktree registration, and a refusal that unlinks the
// record leaves it with no request either: invisible to `atrium ls`, to `atrium reap` and to
// `git worktree list` alike, permanently, since the recovery is one-shot.
//
// Gating the disclosure on r.Adopt made every WITHDRAWAL one of those. The two rows here are
// the withdrawals where the branch is still standing: one whose tip moved under it, and one
// carrying no pin at all — the upgrade case internal/outbox's createVersion promises by name
// is covered.
func TestCreateDrainDisclosesAWithdrawnAdoption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, repo, branch string, r *outbox.Request)
	}{
		{"its tip moved under it", func(t *testing.T, repo, branch string, r *outbox.Request) {
			commitOnto(t, repo, branch)
		}},
		{"it carries no pin", func(t *testing.T, repo, branch string, r *outbox.Request) {
			r.AdoptTip = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := drainHome(t)
			branch := h.appConfig.BranchPrefix + "fix-auth"
			repo := gitRepoWithBranch(t, branch)
			req := adoptedRequest(t, h, "fix-auth", repo)
			tc.arrange(t, repo, branch, &req)
			path := spoolCreate(t, req)

			refuseDrain(t, h)

			reason, rejected := outbox.Rejection(path)
			require.True(t, rejected, "precondition: it is refused for the branch: %s", reason)
			assert.Zero(t, h.list.NumInstances(), "and no agent is put on somebody else's work")
			d, state := outbox.DisclosureFor(path)
			require.Equal(t, outbox.HasDisclosure, state, "but the branch the interrupted build made must still be named")
			assert.Equal(t, branch, d.Branch)
			assert.True(t, d.Leftovers())
			// And the buffered copy is the one that was WRITTEN, stamp included. Disclose
			// takes a pointer for this: stamping a copy leaves the entry this process
			// reports from carrying Version 0 and a zero CreatedAt, so the report dates a
			// live TUI's own orphan as undated while the next launch's — read back off
			// disk — is dated.
			require.Len(t, h.pendingCreateDisclosures, 1)
			buffered := h.pendingCreateDisclosures[0].Disclosure
			assert.Equal(t, d.CreatedAt.UnixNano(), buffered.CreatedAt.UnixNano())
			assert.False(t, buffered.CreatedAt.IsZero())
		})
	}
}

// TestCreateDrainNamesNoBranchWhenItWentAway is the one withdrawal that clears the branch as
// well as the flag. git has just said there is nothing at that name, so a refusal for some
// other reason has no leftovers of its own — and the report is the wrong place to name a
// branch nobody can act on. The mark is still written, because that is the guard rather than
// the report (see TestCreateDrainMarksAnOrdinaryRefusalWithoutReportingIt).
func TestCreateDrainNamesNoBranchWhenItWentAway(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "") // the orphan branch is gone
	req := adoptedRequest(t, h, "fix-auth", repo)
	req.AdoptTip = strings.Repeat("0", 40)
	// A row owning the title, so the request is refused for something OTHER than its
	// branch and the disclosure decision is the only thing under test.
	addInstance(t, h, "fix-auth", repo)
	path := spoolCreate(t, req)

	refuseDrain(t, h)

	_, rejected := outbox.Rejection(path)
	require.True(t, rejected, "precondition: refused for the taken title")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "the mark is owed either way")
	assert.False(t, d.Leftovers(), "there is no branch left to tell anyone about")
}

// TestCreateDrainDisclosesAnExpiredAdoption: the disposal arms answer their caller and unlink
// the record without ever reaching the pin re-check, and expiry is the one that destroys the
// record permanently. A branch it does not name is named nowhere ever again — where a branch
// named after somebody deleted it costs the reader one row saying so, which is the asymmetry
// outbox.Disclosure's header argues from.
func TestCreateDrainDisclosesAnExpiredAdoption(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	req := adoptedRequest(t, h, "fix-auth", repo)
	req.CreatedAt = time.Now().Add(-2 * outbox.TTL)
	path := spoolCreate(t, req)

	disposeDrain(t, h)

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "precondition: past the horizon it is discarded")
	assert.Contains(t, reason, "horizon")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state, "and this is the last chance anyone has to hear about the branch")
	assert.Equal(t, branch, d.Branch)
}

// TestCreateDrainBoundsRechecksWithoutStarvingWhatIsBehindThem: the re-check is a git
// subprocess, so it needs a budget — and charging it to createGateBudget is what starves
// everything behind it. A re-check that cannot be answered produces no verdict and `continue`s,
// so it spends the tick's one gate without gating anything, and the spool is walked
// oldest-first: one unreadable repo at the head of the queue then holds every ordinary
// `atrium new` behind it, with no receipt and no notice, for up to the 24h TTL.
//
// Asserted through the budget's observable consequences rather than by counting forks. Two
// requests, both of which the tick must handle: the re-check spends its OWN budget, so the
// ordinary request behind an unanswerable one is still created in the same tick.
func TestCreateDrainBoundsRechecksWithoutStarvingWhatIsBehindThem(t *testing.T) {
	h := drainHome(t)
	// Spooled first, so the oldest-first walk reaches it before the ordinary request.
	held := unaskableAdoption(t, h, "held")
	ordinary := spoolCreate(t, outbox.Request{Title: "ordinary", Path: t.TempDir()})

	require.NotNil(t, h.drainCreateRequests())

	assert.NotNil(t, titled(h, "ordinary"),
		"a repo git cannot read must not spend the gate the request behind it needed")
	assertCreateQueued(t, held)
	assertCreateHeld(t, ordinary)
	assert.True(t, h.createAdoptHeld)
}

// TestCreateDrainBoundsRechecksPerTick is the other half: the re-check is a git subprocess
// on the update goroutine, so a backlog of Adopt records must not spend one apiece on every
// ~500ms tick. Each is wrapped in gitLocalTimeout, so on a hung mount K unbounded re-checks
// block one Update for K×30s.
//
// Observed through the request the budget stops it reaching, since there is no seam to count
// forks through. Two unanswerable Adopt records fill createRecheckBudget, so the third — a
// perfectly good adoption, in a readable repo, with a pin that matches — is not re-checked
// and not created. Unbounded, it would be.
//
// "Not created", with no second tick asserted, and that is the cost the bound buys rather
// than a gap in the test: the walk is oldest-first and takes no note of which records it
// could not answer for, so two permanently-unreadable repos at the head of the queue hold
// the third behind them until one of the three expires. See createRecheckBudget — every
// request in that queue is recovery work, and an ordinary `atrium new` needs no re-check and
// is unaffected (TestCreateDrainBoundsRechecksWithoutStarvingWhatIsBehindThem).
func TestCreateDrainBoundsRechecksPerTick(t *testing.T) {
	h := drainHome(t)
	for _, title := range []string{"a", "b"} {
		unaskableAdoption(t, h, title)
	}
	repo := gitRepoWithBranch(t, h.appConfig.BranchPrefix+"fix-auth")
	good := spoolCreate(t, adoptedRequest(t, h, "fix-auth", repo))

	assert.Nil(t, h.drainCreateRequests(), "the tick's re-checks went to the two ahead of it")

	assert.Nil(t, titled(h, "fix-auth"))
	assertCreateQueued(t, good)
	assert.True(t, h.createAdoptHeld, "with the hold the first two produced reported once")
}

// TestCreateDrainKeepsTheAdoptionHoldWhileAnythingIsStillHeld: createAdoptHeld is state about
// the LOG LINE, and lifting it on "no error this tick" is wrong for every tick that never
// reached the re-check. tmux off PATH skips the whole default arm, so the hold error is nil
// while the held request is untouched — which logs a resume that did not happen and, worse,
// leaves the flag clear so the next genuine hold goes unlogged.
func TestCreateDrainKeepsTheAdoptionHoldWhileAnythingIsStillHeld(t *testing.T) {
	h := drainHome(t)
	path := unaskableAdoption(t, h, "fix-auth")
	h.drainCreateRequests()
	require.True(t, h.createAdoptHeld, "precondition: git could not answer for it")

	// The seam, not PATH: app_test.go stubs tmuxAvailable for the whole package, so
	// emptying PATH changes nothing the drain reads and the tick would look like an
	// ordinary one that simply found nothing to hold.
	orig := tmuxAvailable
	tmuxAvailable = func() error { return errors.New("tmux is not on PATH") }
	t.Cleanup(func() { tmuxAvailable = orig })

	h.drainCreateRequests()
	assert.True(t, h.createAdoptHeld, "tmux being gone is not the re-check starting to work")
	assertCreateQueued(t, path)
}

// TestCreateDrainHoldsARecordWhoseMarkCannotBeRead is the drain-side twin of
// TestClassifyDefersWhenItCannotLookForAMark. DisclosureFor answered every non-ENOENT read
// error as "there is a mark", and os.ReadFile opens a descriptor — so under fd exhaustion it
// answers EMFILE for a path that does not exist, and every queued request in the spool is
// answered with "a previous atrium gave up on this request and could not record why" and
// unlinked. Presence is a stat now, and a stat that fails for any other reason holds.
//
// Held rather than destroyed and rather than executed: the caller may already have been
// answered, and the record keeps its place and its TTL either way. The fixture also sets
// CreateEntry.Err, so this doubles as the ordering assertion — the mark arm outranks the
// undecodable-record arm, which would otherwise dispose it.
func TestCreateDrainHoldsARecordWhoseMarkCannotBeRead(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	dir := filepath.Dir(path)
	require.NoError(t, os.Chmod(dir, 0o444))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := os.Stat(path); err == nil {
		t.Skip("this filesystem (or this user) ignores the directory's execute bit")
	}

	assert.Nil(t, h.drainCreateRequests(), "nothing created, nothing refused")

	require.NoError(t, os.Chmod(dir, 0o755))
	assertCreateQueued(t, path)
	assert.True(t, h.createMarkHeld, "and the hold is logged once rather than twice a second")
	assert.Zero(t, h.list.NumInstances())
}

// TestCreateDrainClearsTheMarkOverTheRecordItDrops: the mark's second job is to stop that
// record being executed, and it is finished the moment the unlink that had failed lands. Left
// on disk the same orphan is reported on two consecutive launches — the startup read buffers
// it, the 100ms flush offers it up while the record is still there and ClearDisclosure rightly
// declines, and the drain removes the record 400ms later with nothing asking again.
func TestCreateDrainClearsTheMarkOverTheRecordItDrops(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	d := orphanDisclosure("fix-auth")
	require.NoError(t, outbox.Disclose(path, &d))
	// Already shown: the state after a flush that found the record still beside the mark.
	h.pendingCreateDisclosures = nil

	disposeDrain(t, h)

	assert.NoFileExists(t, path, "precondition: the record is gone")
	_, state := outbox.DisclosureFor(path)
	assert.Equal(t, outbox.NoDisclosure, state, "so the mark has nothing left to guard")
}

// TestCreateDrainKeepsAMarkNobodyHasSeenYet is that clear's one exception, and it is not an
// optimisation. flushCreateDisclosures waits for a frame with no overlay on it — a fresh
// install sits in the welcome modal until somebody answers it — while this walk runs
// regardless. Cleared here, the only account of an orphan is deleted before anyone saw it.
func TestCreateDrainKeepsAMarkNobodyHasSeenYet(t *testing.T) {
	h := drainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	d := orphanDisclosure("fix-auth")
	require.NoError(t, outbox.Disclose(path, &d))
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 1, "precondition: it is buffered and unshown")

	disposeDrain(t, h)

	_, state := outbox.DisclosureFor(path)
	assert.Equal(t, outbox.HasDisclosure, state, "the report has not happened yet")
}

// TestStartFailureMarksTheClaimItGivesUpOn: failing to start is a giving-up on a CLAIM, and
// outbox.Disclose is ordered before the discard for exactly the failure available here.
// DiscardCreate can fail — a full or read-only spool — and a claim that outlives it with no
// mark beside it is re-judged on the next launch against live git: claimRequeue if the teardown
// worked, claimAdopt if it did not, and either way a session built for a caller that read "the
// session could not be started" and exited non-zero.
//
// With no inventory, deliberately. The caller has just torn the session down, so naming its
// branch would put a modal in front of the user for artifacts that are gone — which is why the
// assertion is on the mark and on the modal's absence, not on a branch.
func TestStartFailureMarksTheClaimItGivesUpOn(t *testing.T) {
	h := drainHome(t)
	inst, path := startedCreate(t, h)

	h.settleCreateRequest(inst, errors.New("the repo is dirty"))

	reason, rejected := outbox.Rejection(path)
	require.True(t, rejected, "precondition: the caller is answered")
	assert.Contains(t, reason, "the repo is dirty")
	d, state := outbox.DisclosureFor(path)
	require.Equal(t, outbox.HasDisclosure, state,
		"or a claim that outlives its unlink is re-judged into a rebuild")
	assert.Contains(t, d.Reason, "the repo is dirty")
	assert.False(t, d.Leftovers(), "and the caller is tearing the session down, so nothing to name")

	h.flushCreateDisclosures()
	assert.Equal(t, stateDefault, h.state, "so no modal either")
}

// TestCreateDrainKeepsTheAdoptionHoldBehindASpentDisposalBudget is the last skip in the walk
// that did not feed the hold's "still pending" answer, and noteAdoptHold's stated rule is what
// makes that a defect rather than a detail: the lift needs BOTH nothing having failed and
// nothing having been left un-re-checked, because every skip arrives with the error nil. A
// tick that walks past an adoption without re-checking it and reports nothing pending logs a
// resume that did not happen — and clears the flag, so the next genuine hold goes unlogged.
//
// The skip here is the terminal-mark arm running out of disposal budget, which needs a spool
// with createDisposalBudget disposable records ahead of the adoption. Contrived, and the only
// arm left: the other four already fed it.
func TestCreateDrainKeepsTheAdoptionHoldBehindASpentDisposalBudget(t *testing.T) {
	h := drainHome(t)
	// Oldest first, so the walk spends its whole disposal budget before it reaches the
	// adoption behind them.
	for i := 0; i < createDisposalBudget; i++ {
		spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("expired-%02d", i), Path: t.TempDir(),
			CreatedAt: time.Now().Add(-2 * outbox.TTL),
		})
	}
	adopting := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: gitRepoWithBranch(t, ""), Adopt: true,
		AdoptTip: strings.Repeat("a", 40),
		Claim:    &outbox.ClaimMeta{At: time.Now(), SessionBranch: h.appConfig.BranchPrefix + "fix-auth"},
	})
	require.NoError(t, outbox.Disclose(adopting, &outbox.Disclosure{
		Title: "fix-auth", Reason: "an earlier launch gave up on this"}))
	// The flag as an earlier tick left it: this is state about the log line, so a test may
	// set it the way a hold would have.
	h.createAdoptHeld = true

	h.drainCreateRequests()

	assert.FileExists(t, adopting, "precondition: the budget ran out before this one")
	assert.True(t, h.createAdoptHeld,
		"an adoption this tick never re-checked is not the re-check starting to work")
}

// TestCreateDrainHoldsAnAdoptionWhoseRepoCannotBeRead is the worst outcome in this file, and
// it arrives through a probe that looks conservative.
//
// recheckAdoption folds "git could not be asked" and "git says there is no repository here"
// apart with git.ProbeGitRepo, so a re-queued adoption whose repo was DELETED is answered in
// one tick instead of held for the whole 24h horizon. But git prints the same sentence and the
// same 128 for a real repository whose .git it cannot read, so an unreadable repo took that
// same arm: adoptionGone, the licence withdrawn ON DISK by WithdrawAdoption, orphan struck off
// the disclosure, and the request handed to the gates. There targetValidity reads
// git.IsGitRepo — false — so direct is true, executeCreateRequest's isolation guard passes
// because git DID answer, and an agent is launched with no worktree in the user's own
// checkout, on the branch they had checked out, while the one recovery a stranded create gets
// is spent and the orphan branch is named nowhere.
//
// Held instead, which is what the pre-#731 drain did for this case and what tmux off PATH
// still does: a fact about the machine, no fault of the request, and the next tick can see it
// come back. TestProbeGitRepoWillNotCallAnUnreadableRepoAPlainDirectory is the unit half.
func TestCreateDrainHoldsAnAdoptionWhoseRepoCannotBeRead(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	tip, err := git.LookupLocalBranchTip(t.Context(), repo, branch)
	require.NoError(t, err)
	require.NotEmpty(t, tip, "precondition: the pin is a real commit, so nothing else withdraws it")
	record := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: repo, Adopt: true, AdoptTip: tip,
		Claim: &outbox.ClaimMeta{At: time.Now(), SessionBranch: branch},
	})
	gitDir := filepath.Join(repo, ".git")
	require.NoError(t, os.Chmod(gitDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })
	if _, err := git.LookupLocalBranchTip(t.Context(), repo, branch); err == nil {
		t.Skip("this user can read the repository through mode 000")
	}

	assert.Nil(t, h.drainCreateRequests(), "nothing created, nothing refused")

	assert.Zero(t, h.list.NumInstances(),
		"and above all no session in the user's own checkout, with no worktree of its own")
	assertCreateQueued(t, record)
	assert.True(t, h.createAdoptHeld, "the hold is logged once rather than twice a second")
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Request.Adopt,
		"and the licence is still on disk: a withdrawal here is permanent and the repo may come back")
}

// TestCreateDrainNamesTheBranchAfterAWithdrawalHasLanded: the branch a refusal discloses comes
// from the claim's evidence block and not from Request.Adopt, and it has to, because
// outbox.WithdrawAdoption persists the cleared flag.
//
// So a record that comes back round after a withdrawal landed — the tick's Claim rename failed,
// or the process died between the two writes — reads Adopt false with its branch still standing,
// and applyCreateClaim has already released that branch's worktree registration on this
// request's behalf. Guarded on the flag, that record named no branch anywhere: no row, no claim,
// no request, no registration, and nothing that mentions it. The recovery is one-shot, so the
// state was permanent — #731's hole 1, reached from the one direction the flag could not see.
func TestCreateDrainNamesTheBranchAfterAWithdrawalHasLanded(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	repo := gitRepoWithBranch(t, branch)
	// Adopt false with the evidence block intact: the state a landed withdrawal leaves.
	record := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo,
		Claim: &outbox.ClaimMeta{At: time.Now(), SessionBranch: branch}})
	addInstance(t, h, "fix-auth", repo) // so a gate refuses it for the title

	refuseDrain(t, h)

	d := disclosed(t, record)
	assert.Equal(t, branch, d.Branch,
		"the branch an interrupted build made outlives the licence to adopt it")
	assert.True(t, d.Leftovers(), "or the reader drops it and nobody is ever told")
}

// TestCreateDrainGivesAnUnreadableMarkAHorizon: the terminal-mark probe holds the record above
// every arm below it, so a record it covers reaches no disposal either — and nothing sweeps a
// record. sweepSuffixed only ever walks the two suffixed kinds and no arm poisons the path, so
// a spool the stat keeps failing on held it for the life of this TUI and of every one after,
// re-listed and re-probed twice a second, with `atrium reset` the only way out.
//
// Past the horizon the disposal wins: a second receipt for a caller that may already have one
// is a duplicate 24 hours late, against a file no atrium can ever let go. The symlink loop is
// how one file's stat is made to fail while the directory stays writable — the mark cannot be
// read, and the record beside it can.
func TestCreateDrainGivesAnUnreadableMarkAHorizon(t *testing.T) {
	h := drainHome(t)
	record := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, ""),
		CreatedAt: time.Now().Add(-2 * outbox.TTL)})
	loopTheMarkOf(t, record)

	h.drainCreateRequests()

	assert.NoFileExists(t, record, "a record nothing can ever answer for is not immortal")
	reason, ok := outbox.Rejection(record)
	require.True(t, ok, "and its caller is told why")
	assert.Contains(t, reason, "horizon")
}

// TestCreateDrainHoldsAFreshRecordWhoseMarkCannotBeRead is the other side of that horizon, and
// the control for it: under the horizon the hold is what it always was. Without both, the
// disposal above reads as "an unreadable mark is disposed of", which is the behaviour the whole
// probe exists to prevent.
func TestCreateDrainHoldsAFreshRecordWhoseMarkCannotBeRead(t *testing.T) {
	h := drainHome(t)
	record := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	loopTheMarkOf(t, record)

	assert.Nil(t, h.drainCreateRequests(), "nothing created, nothing refused")

	assertCreateQueued(t, record)
	assert.True(t, h.createMarkHeld)
	assert.Zero(t, h.list.NumInstances())
}

// loopTheMarkOf points a record's disclosure path at itself, so os.Stat on it fails with ELOOP
// while the record beside it and the directory around it stay perfectly readable. That
// separation is the point: a chmod on the spool directory breaks the record's own read too, so
// the entry arrives undecodable and exercises a different arm.
func loopTheMarkOf(t *testing.T, record string) {
	t.Helper()
	mark := record + ".disclosure"
	require.NoError(t, os.Symlink(filepath.Base(mark), mark))
	if _, err := os.Stat(mark); err == nil {
		t.Skip("this filesystem resolves a self-referential symlink")
	}
	require.Equal(t, outbox.DisclosureUnknown, outbox.DisclosureMark(record),
		"precondition: the mark cannot be looked for")
}

// TestCreateDrainKeepsTheMarkHoldBehindASpentDisposalBudget is
// TestCreateDrainKeepsTheAdoptionHoldBehindASpentDisposalBudget for the other flag, and it is
// the assertion that was missing when the flag was added.
//
// noteAdoptHold was given a `pending` companion because every skip in the walk arrives at the
// notices with err nil while the held request is still queued. createMarkHeld was given none —
// and the composite break above the mark probe is the largest such skip there is: it leaves the
// whole tail of the spool unprobed, so markUnreadable is "" for a record still held. Read as
// "the spool can be stat'd again", that logs a resume nobody earned and clears the flag the
// next genuine hold needed, which is the one thing an edge-triggered flag exists to prevent.
// The next tick re-logs the hold, so the ERROR/INFO pair flaps at 2Hz.
func TestCreateDrainKeepsTheMarkHoldBehindASpentDisposalBudget(t *testing.T) {
	h := drainHome(t)
	// Oldest first, so the walk spends its whole disposal budget, then a gate, before it
	// reaches the held record — which is what makes the composite break fire on it.
	for i := range createDisposalBudget {
		spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("expired-%02d", i), Path: t.TempDir(),
			CreatedAt: time.Now().Add(-2 * outbox.TTL),
		})
	}
	repo := gitRepoWithBranch(t, "")
	addInstance(t, h, "taken", repo)
	spoolCreate(t, outbox.Request{Title: "taken", Path: repo}) // charges the gate budget
	held := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})
	loopTheMarkOf(t, held)
	// The flag as an earlier tick left it: state about the log line, so a test may set it the
	// way a hold would have.
	h.createMarkHeld = true

	h.drainCreateRequests()

	require.FileExists(t, held, "precondition: the break came before this one was probed")
	assert.True(t, h.createMarkHeld,
		"a record this tick never probed is not the spool becoming readable again")
}

// TestCreateDrainLiftsTheMarkHoldWhenTheSpoolEmpties is the lift the pending companion must not
// break, and the place it reliably happens. drainCreateRequests returns early on an empty
// spool, before the walk that would otherwise set the flags, so the transition has to be driven
// through that return — which is where the adopt flag's lift already lives and where the mark
// flag's was missing entirely. Without it the flag stays true forever once set, and the next
// genuinely unreadable mark is held in total silence.
func TestCreateDrainLiftsTheMarkHoldWhenTheSpoolEmpties(t *testing.T) {
	h := drainHome(t)
	h.createMarkHeld = true

	h.drainCreateRequests()

	assert.False(t, h.createMarkHeld,
		"an empty spool holds nothing, so this is where a reset-away hold is lifted")
}

// TestCreateDrainKeepsTheAdoptionHoldWhenADropFails is the arm the adopt flag's own companion
// did not cover. The terminal-mark arm returns ABOVE the re-check, so a record it drops is
// never re-checked — and the drop is the very call that failed when the mark was written, so
// the record routinely survives it. Charged from the budget-spent skip alone, a failed drop
// logged "resuming" for a request still held and cleared the flag; on every later tick the
// poisoned path then sets pending with err nil, so no case matches and the genuine hold is
// never re-logged.
func TestCreateDrainKeepsTheAdoptionHoldWhenADropFails(t *testing.T) {
	h := drainHome(t)
	branch := h.appConfig.BranchPrefix + "fix-auth"
	record := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: gitRepoWithBranch(t, branch), Adopt: true,
		AdoptTip: strings.Repeat("a", 40),
		Claim:    &outbox.ClaimMeta{At: time.Now(), SessionBranch: branch},
	})
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Reason: "an earlier launch gave up on this"}))
	// The unlink fails for the reason the mark exists: a spool that cannot be written. Set
	// after the mark, so the mark itself is on disk.
	dir := filepath.Dir(record)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Remove(record); err == nil {
		t.Skip("this user can unlink through a read-only directory")
	}
	h.createAdoptHeld = true

	h.drainCreateRequests()

	require.FileExists(t, record, "precondition: the drop failed, so the record is still held")
	assert.True(t, h.createAdoptHeld,
		"a record the mark arm returns above is never re-checked, so it is never answered for")
}

// TestCreateDrainMintsNoMarkForAnExpiredRecordWithNothingToName: the two disposal arms are
// terminal however often they repeat — an expired record is expired again on the next tick and
// an undecodable one is still undecodable — so a mark buys them no guard at all. What it would
// cost is a file and two fsyncs per record per tick: createDisposalBudget allows 50, the walk
// runs twice a second, and a cron backlog cleared while the TUI sits behind the welcome modal
// (where flushCreateDisclosures returns early and clears nothing) mints them without bound, on
// the Bubble Tea update goroutine. That is the synchronous freeze createDisposalBudget exists
// to bound, one layer out from where it can see it.
//
// TestCreateDrainRejectsExpiredRequest is the other half: the same arm DOES write one when it
// has a branch to name, because that file is a report and the report is owed.
func TestCreateDrainMintsNoMarkForAnExpiredRecordWithNothingToName(t *testing.T) {
	h := drainHome(t)
	var records []string
	for i := range 3 {
		records = append(records, spoolCreate(t, outbox.Request{
			Title: fmt.Sprintf("expired-%d", i), Path: t.TempDir(),
			CreatedAt: time.Now().Add(-2 * outbox.TTL),
		}))
	}

	h.drainCreateRequests()

	for _, record := range records {
		assert.NoFileExists(t, record, "precondition: the arm ran")
		assert.Equal(t, outbox.NoDisclosure, outbox.DisclosureMark(record),
			"a mark that guards nothing is a file per record per tick")
	}
	assert.Empty(t, h.pendingCreateDisclosures, "and nothing buffered for a report either")
}

// TestCreateDrainNamesTheMarksTitleForAnUndecodableRecord: the log line in the terminal-mark
// arm is the only account of the drop that survives it — the record and the mark are both
// destroyed two calls later. It took its title from the RECORD, which is the zero value on the
// undecodable arm, while the mark beside it held the answer all along.
func TestCreateDrainNamesTheMarksTitleForAnUndecodableRecord(t *testing.T) {
	h := drainHome(t)
	record := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: gitRepoWithBranch(t, "")})
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Reason: "an earlier launch gave up on this"}))
	require.NoError(t, os.WriteFile(record, []byte("{not json"), 0o644))

	warnings := captureWarnings(t)

	h.drainCreateRequests()

	assert.NoFileExists(t, record, "precondition: the mark arm dropped it")
	assert.Contains(t, warnings.String(), `"fix-auth"`,
		"the only surviving account of the drop names which request it was")
}

// captureWarnings redirects the WARNING log for the duration of one test. Two of the fixes in
// this file are about what a line SAYS rather than about what a file contains, and nothing else
// in the package can see that.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.WarningLog.Writer()
	log.WarningLog.SetOutput(&buf)
	t.Cleanup(func() { log.WarningLog.SetOutput(prev) })
	return &buf
}
