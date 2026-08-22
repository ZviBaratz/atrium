package app

// create_recover.go — finishing the `atrium new` requests a dead process left
// mid-build (#716).
//
// A create is held for the whole of Start, which is not short: a `git worktree add`,
// the repo's setup script (unbounded — an `npm ci` on a cold cache), a pty launch. Kill
// the process inside that window and the request outlives it while the link to the
// session it was building does not, because that link is a map in memory
// (home.createsInFlight). holdCreateRequest's claim is the durable half; this is the
// side that reads it.
//
// Three outcomes are reachable from one interrupted build, and before the claim existed
// the next launch could not tell them apart. It re-read an ordinary request and let the
// creation gates judge it, which got the first two right by luck and the third wrong
// permanently:
//
//   - the row was persisted → titleConflictIn refused it as "already used", naming the
//     caller's own session as the obstacle;
//   - nothing was built → re-creating it was exactly right;
//   - Worktree.Setup created the branch and persistInstances never ran → state.json had
//     no row, so titleConflictIn saw nothing, but branchSlugConflict's LocalBranchExists
//     did, and the request was refused with "branch already exists". Once: a refusal
//     unlinks the record along with writing its receipt, so the request was destroyed
//     while the branch and worktree it had made were not. They belonged to no row
//     `atrium ls` could show and no `atrium reap` could find (that scans tmux servers),
//     and every retry under the same title met the same refusal, forever. Deleting the
//     branch by hand was the only way out.
//
// What makes reading a claim safe is not local to this file: tui.lock (main.go) admits
// one interactive atrium per data dir and is held across the whole of app.Run, and the
// kernel frees an flock on process death. So a claim on disk when this runs was left by
// a process that is gone — never by a live peer mid-build. The autoyes daemon is not a
// second claimant either; it never touches the create spool.
//
// Giving up on a claim leaves a disclosure rather than nothing (#731, #732). A refusal
// answers the caller and then destroys the claim, which used to destroy the only mention
// of the branch, worktree and tmux session the interrupted build had already made — the
// pre-#716 orphan, reached through the door marked "we told the caller". outbox.Disclose
// records those leftovers before the receipt and before the unlink, and that ordering
// does two jobs: the next TUI can say what is stranded, and a claim that outlives a
// failed unlink can no longer be re-judged into a verdict that builds the session its
// caller was already told had failed. See claimAnswered.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// claimVerdict is what reconcileCreateClaims decided about one stranded claim. Named
// values rather than a bool pair because the outcomes are not two axes: "finish it" and
// "give up on it" differ in what they write, "re-queue" and "re-queue to adopt" differ
// in what the next drain is allowed to skip, and one of them deliberately writes
// nothing at all.
type claimVerdict int

const (
	// claimSucceeded: the session exists and is recorded. The caller asked for a
	// session and has one, so this is a success even though the process that made it
	// died before it could say so.
	claimSucceeded claimVerdict = iota
	// claimRequeue: nothing durable was built, so the request goes back in the spool
	// and is created normally.
	claimRequeue
	// claimAdopt: a session branch exists that belongs to no row. The request goes
	// back marked to take it, which is the outcome the caller asked for rather than
	// the refusal-plus-orphan they used to get.
	claimAdopt
	// claimRefused: the request cannot be finished and the caller is owed the reason.
	claimRefused
	// claimDefer: git could not be asked, so there is no evidence to judge on. The
	// claim is left exactly as it is for a later launch, which is the only verdict
	// that costs nothing to be wrong about — see applyCreateClaim.
	claimDefer
	// claimAnswered: an earlier launch already refused this request, wrote its caller a
	// receipt and could not unlink the claim (#731). The disclosure it left is the proof,
	// and it is durable where the receipt is not — a --wait clears the receipt as it
	// reads it, and SweepRejections drops the rest.
	//
	// It is the one verdict about the PROTOCOL rather than about the request, which is
	// why it outranks every other: the evidence the others read is live git and a freshly
	// loaded instance list, and both have moved on since the refusal. A session the
	// caller was told it would not get, appearing on the next launch because a branch has
	// since been freed, is the outcome this forecloses. Being wrong about it costs the
	// claim its rebuild — which is exactly what the receipt already promised.
	//
	// "Already refused" is the premise and not an assumption: applyCreateClaim re-writes the
	// receipt on this arm, because the crash window Disclose is ordered ahead of Reject to
	// survive is the one where the receipt was never written at all.
	claimAnswered
)

// claimJudgement is what classifyCreateClaim decided about one stranded claim: the
// verdict, the reason a refusal owes its caller, and the artifacts that refusal has to
// disclose.
//
// The artifacts travel with the verdict because the classifier is where they are
// measured — the tmux name from liveAgentSession, the worktree from StrandedWorktreeFor,
// the branch from the claim's own evidence block — and re-deriving them at the point of
// writing the disclosure would mean asking git again about a state the answer has already
// been read off.
//
// They are set where a refusal could need them, which is not every judgement, and "not
// every" is three separate reasons rather than one. claimSucceeded, claimAnswered and
// claimDefer never had a use for them. An arm that returns before the branch is known — an
// undecodable claim, a claim with no evidence block — has nothing to put in them. And two
// refusals that DO know the branch leave them empty deliberately, because there the branch
// is not this build's leaving to report: one where a row in the same repository holds it,
// and one where the claim's own evidence says it was already there before the build started.
// Both name it in the reason, which says what is in the way rather than what to remove.
//
// Only a refusal writes them anywhere — including one expireVerdict downgrades from
// claimAdopt, which is why that carry-over is a line of its own rather than a consequence.
type claimJudgement struct {
	verdict claimVerdict
	reason  string
	// branch is the session branch the interrupted build made, empty when it made none
	// (a direct session, or a claim with no evidence block).
	branch string
	// worktree is the directory `git worktree add` registered for that branch, empty
	// when nothing holds it. It is the artifact that blocks a retry rather than merely
	// surviving one, so a disclosure that omits it names the wrong obstacle.
	worktree string
	// tmuxName is a session the agent is still running in.
	tmuxName string
}

// reconcileCreateClaims finishes or gives up on every `atrium new` request a previous
// process claimed and did not settle. It runs once at startup, before the event loop,
// and returns how many claims it acted on.
//
// Synchronous, and gated on there being a claim at all: the steady-state cost is one
// os.ReadDir of a directory drainCreateRequests already reads twice a second, and the
// git probes below are paid only by a data dir that actually holds a stranded build.
// It is here rather than in an Init command because the verdict needs the loaded
// instances, and reading the model's list from a tea.Cmd goroutine would race the
// update loop that owns it. Nothing here starts a session — a re-queued request is
// created by the ordinary drain on the first metadata tick, through the same gates as
// any other.
//
// Every path writes something except claimDefer, and that exception is the point of
// having it: leaving the claim costs a --wait the rest of its own deadline and nothing
// else, while writing a verdict read off a git call that failed spends the one recovery
// a stranded request gets. Any verdict that neither settles nor re-queues nor defers
// would be the bad version of this — the claim left behind with no reason recorded.
//
// A refusal now writes twice: the caller's receipt, and a disclosure of what the
// interrupted build left stranded. That is what claimAnswered reads on a later launch, so
// the one state this used to have no name for — claim and receipt coexisting because the
// unlink failed — is no longer judged on evidence that has since moved on.
//
// It also returns the disclosures it could not WRITE, and that return exists because this is
// a free function with no *home to buffer them on. Everything it discloses successfully
// reaches this launch's report through loadCreateDisclosures, which reads the directory a
// moment later; a Disclose that failed reaches nobody, and these are the refusals carrying
// the full inventory — a branch, a registered worktree, a running agent. The drain's own side
// buffers regardless for that reason (app.discloseCreateLeftovers), and a full disk is no
// more of a reason to withhold the report here than it is there.
func reconcileCreateClaims(ctx context.Context, instances []*session.Instance,
	now time.Time) (int, []outbox.DisclosureEntry) {
	claims, err := outbox.ListClaims()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool for stranded requests: %v", err)
		return 0, nil
	}
	if len(claims) == 0 {
		return 0, nil
	}

	var acted int
	var undisclosed []outbox.DisclosureEntry
	for _, e := range claims {
		applied, missed := applyCreateClaim(ctx, e, classifyCreateClaim(ctx, e, instances, now))
		if applied {
			acted++
		}
		if missed != nil {
			undisclosed = append(undisclosed, outbox.DisclosureEntry{Path: e.Path, Disclosure: *missed})
		}
	}
	return acted, undisclosed
}

// classifyCreateClaim reads one stranded claim into a verdict, plus the reason a
// caller is owed when that verdict is claimRefused.
//
// The order of the arms is the argument, and the recorded row genuinely comes first:
// it is the only evidence that settles the question outright, and it is read off the
// record PATH, so it answers even for a claim whose body could not be decoded and for
// one older than the spool's horizon. (It did not always. Putting the unreadable and
// expired arms ahead of it meant a create that fully succeeded — row persisted, settle
// interrupted — could be handed a receipt saying it was "discarded rather than rebuilt"
// while `atrium ls` showed the session running.)
//
// After that: a tmux session outliving its TUI is checked before anything is judged
// recoverable, because it is the one leftover that makes a retry impossible rather than
// merely awkward; then the branch, because a branch nobody owns is the case this whole
// file exists for; and everything that is neither is refused rather than guessed at.
// Expiry is applied last, to the verdict, so it can say what it is abandoning.
//
// One arm sits above even the row, and it is not evidence about the build: a disclosure
// beside the claim means an earlier launch already answered this request's caller. See
// claimAnswered for why that outranks everything measured here.
func classifyCreateClaim(ctx context.Context, e outbox.CreateEntry, instances []*session.Instance,
	now time.Time) claimJudgement {
	// Read off the record PATH, so it answers for a claim whose body could not be decoded
	// — which is most of the point, since the refusal that wrote the disclosure may have
	// been for exactly that.
	//
	// A state that could not be established defers instead of guessing, and this is the
	// arm where that matters most: the recovery is one-shot, so reading "I could not look"
	// as "there is no mark" spends a stranded claim's only chance on a stale NFS handle,
	// while reading it as "there is one" answers the caller with a reason nobody wrote.
	// Deferring costs one launch — claimDefer's own argument, one file out.
	switch d, state := outbox.DisclosureFor(e.Path); state {
	case outbox.HasDisclosure:
		return claimJudgement{verdict: claimAnswered, reason: answeredReason(d)}
	case outbox.DisclosureUnknown:
		return claimJudgement{verdict: claimDefer, reason: fmt.Sprintf(
			"could not check whether an earlier atrium had already given up on this request "+
				"(%s)", filepath.Base(e.Path))}
	}

	// The row names the request, not the other way round. A (Title, Path) match would
	// be the same comparison the drain's conflict gate already made and passed before
	// the crash, so it cannot distinguish this request's session from one somebody else
	// created under that title since — and reporting a stranger's session to a waiting
	// --wait as its own is worse than the refusal this replaces.
	if rowFor(instances, e.Path) != nil {
		return claimJudgement{verdict: claimSucceeded}
	}

	// An undecodable claim cannot be matched to a branch, so there is nothing to finish
	// and nothing to re-queue — the same dead end ListCreates' Err arm hits, and the
	// same answer. Below the row check because e.Path is readable either way.
	if e.Err != nil {
		return claimJudgement{verdict: claimRefused, reason: fmt.Sprintf(
			"a previous atrium was interrupted while creating this session and the request it "+
				"left behind could not be read (%v)", e.Err)}
	}

	// The agent is the leftover this file used to miss entirely. tmux runs on its own
	// server on a dedicated socket, so a session Instance.Start created survives the TUI
	// that created it — and Start creates it LAST, which is exactly the window between
	// "everything is built" and "the row is persisted" that a claim exists to cover.
	//
	// It is checked before the branch reasoning rather than beside it because it applies
	// to every unrecorded claim, including a direct session's, which has no branch to
	// reason about at all. And it is fatal to both live verdicts: the tmux name is
	// derived from (repo group, title) and nothing else (tmux.QualifiedSessionName), so
	// a retry meets `tmux session already exists` on every attempt, forever. Adopting
	// would be worse than useless — applyCreateClaim's release runs os.RemoveAll over the
	// worktree, which here is the working directory of a running agent.
	if name := liveAgentSession(ctx, e.Request); name != "" {
		// config.RuntimeName is the socket name tmux.socketName derives from, and the
		// one CLAUDE.md requires anything naming the socket to go through — a legacy
		// install is on "claudesquad" and a hardcoded "atrium" here would print a
		// command that finds nothing.
		//
		// The worktree is probed here rather than inherited from the walk below, which
		// this arm returns before ever reaching. It is the arm where the interrupted build
		// got FURTHEST — branch, worktree and a running agent — and it is the one arm that
		// deliberately frees none of them, so a report that omits the directory names the
		// wrong obstacle: the user kills the tmux session, tries `git branch -d`, and meets
		// "already used by worktree" against a path nothing told them about.
		return claimJudgement{verdict: claimRefused, tmuxName: name, branch: claimedBranch(e.Request),
			worktree: strandedWorktreePath(ctx, e.Request.Path, claimedBranch(e.Request)),
			reason: fmt.Sprintf("a previous atrium was interrupted while creating this "+
				"session, and its agent is still running in tmux session %q with nothing in "+
				"atrium's records pointing at it; attach to it or kill it (`tmux -L %s kill-session "+
				"-t %s`) before creating this title again", name, config.RuntimeName(), name)}
	}

	// A claim with no evidence block. Claim() always writes one, so this is a
	// hand-written file rather than anything this package produced — and it is checked
	// because the alternative is a nil dereference on the startup path, which would
	// take the whole TUI down for a stray file in a spool directory.
	//
	// Re-queued, not refused: without the block there is no branch name to probe, and
	// an unclaimable-looking record is exactly what the gates already judge correctly.
	// Whatever the interrupted build did or did not make, the drain refuses a taken
	// branch — the pre-#716 outcome, which is where a claim carrying no evidence
	// belongs.
	if e.Request.Claim == nil {
		return expireVerdict(e, claimJudgement{verdict: claimRequeue}, now)
	}

	branch := e.Request.Claim.SessionBranch
	if branch == "" {
		// A direct (non-git) session has no branch to strand, so an unrecorded one
		// built nothing durable whatever else it got through. Nothing to adopt, and
		// nothing in the way of a clean second attempt.
		return expireVerdict(e, claimJudgement{verdict: claimRequeue}, now)
	}
	// LookupLocalBranch, not LocalBranchExists: the latter is `err == nil`, so it reports
	// a git that could not be run as "no such branch", and here that answer is acted on
	// destructively. It sends the request back through the gates, where a recovered git
	// then refuses it for the branch this arm just failed to see — receipt written,
	// record unlinked, orphan kept. Deferring instead costs one more launch.
	exists, err := git.LookupLocalBranch(ctx, e.Request.Path, branch)
	if err != nil {
		return claimJudgement{verdict: claimDefer, reason: fmt.Sprintf(
			"could not check whether branch %q exists: %v", branch, err)}
	}
	if !exists {
		return expireVerdict(e, claimJudgement{verdict: claimRequeue}, now)
	}

	// From here the branch exists and no row bears this request's stamp. Three things
	// still have to be true before it can be treated as this build's own leftovers.
	if owner, sameRepo := branchOwner(instances, e.Request.Path, branch); owner != "" {
		// A live session holds it. That is not an orphan at all, and adopting it would
		// put a second agent on one branch.
		//
		// Unless the row is in another repository, which branchOwner reports on purpose and
		// calls the wrong answer in the harmless direction. Harmless for the VERDICT — a
		// refusal the caller can retry under another title, against a miss that runs
		// os.RemoveAll over a live session's worktree — and not harmless for the inventory:
		// on a false hit THIS repo's branch is a genuine orphan, and a refusal that names
		// nothing leaves it stranded with a mark that has nothing to report. So the
		// same-repo hit discloses nothing and the cross-repo one discloses what it found.
		where := ""
		j := claimJudgement{verdict: claimRefused}
		if !sameRepo {
			where = " (in another repository — refused anyway, see branchOwner)"
			j.branch = branch
			j.worktree = strandedWorktreePath(ctx, e.Request.Path, branch)
		}
		j.reason = fmt.Sprintf(
			"a previous atrium was interrupted while creating this session, and branch %q now "+
				"belongs to session %q%s; pick another title", branch, owner, where)
		return j
	}
	if e.Request.Claim.BranchExisted && !e.Request.Adopt {
		// The branch was already there when the build claimed the request, so it is
		// somebody else's and always was. This is unreachable through the drain's own
		// gate, which refuses such a request before it can be claimed; it is here
		// because the field is evidence and a guard that trusts evidence has to check
		// the case where the evidence says no.
		return claimJudgement{verdict: claimRefused, reason: fmt.Sprintf(
			"a previous atrium was interrupted while creating this session, and branch %q "+
				"already existed before it started, so it is not this session's to take; delete "+
				"it or pick another title", branch)}
	}

	// The branch is not the only thing an interrupted build leaves. `git worktree add`
	// registers a directory too, and that registration is what actually blocks the
	// retry: resolveWorktreePaths stamps every worktree path with the current nanosecond,
	// so the second attempt asks for a DIFFERENT directory, its clearStaleWorktree clears
	// a path that never existed, and `git worktree add` fails with "already used by
	// worktree" against the first attempt's. Found by driving a real kill; no unit test
	// saw it, because the branch check alone reads as sufficient.
	//
	// Whether it can be cleared is part of the verdict rather than of applying one. A
	// worktree under the data dir's worktrees/ tree carries a name only Atrium mints and
	// is this build's own leavings; anywhere else it is a checkout somebody made on
	// purpose, and adopting past it would only fail later with git's own wording.
	wt, managed, err := git.StrandedWorktreeFor(ctx, e.Request.Path, branch)
	if err != nil {
		// Same reason as the branch probe above, one step worse: folded into "nothing
		// holds it", a failed `git worktree list` yields claimAdopt with no release, and
		// the retry dies on `already used by worktree`.
		return claimJudgement{verdict: claimDefer, reason: fmt.Sprintf(
			"could not check what holds branch %q: %v", branch, err)}
	}
	if wt != "" && !managed {
		// The worktree is named in the reason and NOT in the judgement. Every path this
		// struct's worktree field reaches renders it as a leftover to be removed by hand,
		// and this directory is the opposite: a checkout somebody made on purpose, at a path
		// Atrium never minted. The branch IS this build's leaving and is disclosed; the
		// reason says what is holding it. strandedWorktreePath screens for the same thing on
		// the arm that reaches a directory without a verdict.
		return claimJudgement{verdict: claimRefused, branch: branch,
			reason: fmt.Sprintf("a previous atrium was interrupted while creating this session, "+
				"and branch %q is checked out at %s, which is not a worktree Atrium manages; "+
				"free the branch or pick another title", branch, wt)}
	}
	return expireVerdict(e, claimJudgement{verdict: claimAdopt, branch: branch, worktree: wt}, now)
}

// answeredReason is the wording a claim's re-written receipt carries when a disclosure
// beside it says the request was already given up on.
//
// The disclosure's own Reason, except for the one case where there isn't one:
// DisclosureFor reports a file it cannot decode as a disclosure all the same, because the
// question it answers is whether the request is terminal and an unreadable mark answers
// that. What it cannot supply is words for the caller, and an empty receipt is worse than
// a vague one — `atrium new --wait` prints it.
func answeredReason(d outbox.Disclosure) string {
	if d.Reason != "" {
		return d.Reason
	}
	return "a previous atrium gave up on this request and could not record why"
}

// strandedWorktreePath is StrandedWorktreeFor reduced to a directory Atrium minted, for a
// report rather than for a verdict: an error or an unreadable repo yields "" and the caller
// says one thing less, where a verdict must defer instead (see claimDefer).
//
// It screens for `managed`, and that screening is the whole of it. Every field this feeds
// renders as a leftover to go and remove, and parseWorktreeList returns the PRIMARY worktree
// too — so a branch the user has checked out in their own clone resolves to that clone's
// path. Unscreened, a create whose agent is still running reports the user's main checkout
// under a header saying nothing in atrium's records points at it, and whether it does so is
// decided only by whether a tmux session happened to outlive its TUI. The sibling arm that
// meets the same directory through a verdict refuses to put it in this field for exactly that
// reason (see classifyCreateClaim's unmanaged-worktree arm).
func strandedWorktreePath(ctx context.Context, repo, branch string) string {
	if branch == "" {
		return ""
	}
	wt, managed, err := git.StrandedWorktreeFor(ctx, repo, branch)
	if err != nil || !managed {
		return ""
	}
	return wt
}

// claimedBranch returns the session branch a claim recorded, tolerating a claim that has
// no evidence block at all — a hand-written file, which the arms below reject on its own
// terms but which the tmux arm above must not dereference.
func claimedBranch(r outbox.Request) string {
	if r.Claim == nil {
		return ""
	}
	return r.Claim.SessionBranch
}

// expireVerdict downgrades a live verdict to a refusal when the request has outlived the
// spool's horizon, and is a pass-through when it has not.
//
// Applied to the verdict rather than checked up front, because the wording an expiry
// owes depends on what the expired request had already built. A claim that built nothing
// is discarded with the same reason ListCreates' own expiry arm gives. A claim whose
// build got as far as a branch is discarded on top of an orphan, and the receipt has to
// say which branch, or the one artifact the user must clean up by hand is the one thing
// nobody is told about — the #716 complaint, re-entered through the door marked "too
// old to rebuild".
func expireVerdict(e outbox.CreateEntry, j claimJudgement, now time.Time) claimJudgement {
	if !e.Request.Expired(now) {
		return j
	}
	age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
	reason := fmt.Sprintf("a previous atrium was interrupted while creating this session; the "+
		"request was spooled %s ago, past the %s horizon, so it names a branch point the tree "+
		"has moved on from and is discarded rather than rebuilt", age, outbox.TTL)
	if j.branch != "" {
		reason += fmt.Sprintf(". It had already created branch %q, which is left in place and "+
			"belongs to no session: delete it or create a session on it yourself", j.branch)
	}
	// The artifacts carry over: the verdict is downgraded, but what the expired build
	// left behind is exactly what the refusal now has to disclose.
	j.verdict, j.reason = claimRefused, reason
	return j
}

// liveAgentSession returns the name of a tmux session already running for this request's
// (repo group, title), or "" if none is.
//
// It reproduces the name Instance.Start mints rather than searching, because that name
// is a pure function of those two values — tmux.QualifiedSessionName — and reproducing
// it is what makes the answer mean "the retry will collide with this" rather than "some
// session that looks related exists". git.RepoGroupKey computes the same group key
// Instance.GroupKey resolves to once the worktree exists (both are the repo root's
// basename), and falls back to the directory basename exactly as GroupKey's direct arm
// does.
//
// Over-reporting is the safe direction and is reachable: a recorded session under the
// same title in the same repo answers this probe. That yields a refusal naming the tmux
// session, where the drain would have refused the re-queued request for the taken title
// anyway — a clearer message for the same outcome, and never a delete.
func liveAgentSession(ctx context.Context, r outbox.Request) string {
	name := tmux.QualifiedSessionName(git.RepoGroupKey(ctx, r.Path), r.Title)
	if tmux.NewSessionWithName(ctx, name, r.Title, r.Program).DoesSessionExist() {
		return name
	}
	return ""
}

// applyCreateClaim carries out a verdict and reports whether it was applied. A failure
// is logged and left alone: the claim survives, and the next launch reaches the same
// verdict from the same evidence — which is what makes leaving it the safe response to
// a git or filesystem problem rather than a leak.
//
// The second return is the disclosure it meant to write and could not, for the caller to
// carry to the report — see reconcileCreateClaims. Nil in every other case, including a
// refusal whose write landed: that one is already on disk for loadCreateDisclosures to find.
func applyCreateClaim(ctx context.Context, e outbox.CreateEntry, j claimJudgement) (bool, *outbox.Disclosure) {
	name := e.Request.Title
	switch j.verdict {
	case claimSucceeded:
		// No receipt. The absence of both the record and its claim, with no rejection
		// beside them, is what awaitSpool reads as success — and waitForCreate then
		// reads the branch back out of the row that made this verdict true.
		if err := outbox.DiscardCreate(e.Path); err != nil {
			log.ErrorLog.Printf("failed to close out a completed create request for %q: %v", name, err)
			return false, nil
		}
		log.InfoLog.Printf("create request for %q completed before an earlier atrium exited; "+
			"its session is recorded", name)
	case claimAnswered:
		// Nothing is judged: the disclosure beside this claim is what says an earlier launch
		// already gave up on the request, and the unlink that failed then is what is owed.
		//
		// The receipt is re-written rather than assumed, because the crash window Disclose
		// is ordered to survive is exactly the one between the disclosure and the receipt.
		// A claim reaching this arm with no receipt beside it is a request whose caller was
		// never told anything, and the unlink below would leave record, claim and receipt
		// all absent — which awaitSpool reads as SUCCESS (see claimSucceeded, whose whole
		// signal that is). Re-writing a receipt some earlier --wait already read and
		// cleared costs a file the TTL sweep collects; not writing one costs a CI job that
		// proceeds against a session that does not exist. The wording is the disclosure's
		// own Reason, carried in j.reason for this.
		if err := outbox.Reject(e.Path, j.reason); err != nil {
			log.ErrorLog.Printf("failed to re-write the receipt for an already-refused create "+
				"request for %q: %v", name, err)
		}
		// And if this fails again the pair is still terminal, so the next launch reaches
		// this same arm rather than the evidence below.
		if err := outbox.DiscardCreate(e.Path); err != nil {
			log.ErrorLog.Printf("failed to drop an already-refused create request for %q: %v", name, err)
			return false, nil
		}
		log.WarningLog.Printf("dropped a create request for %q that an earlier atrium had already "+
			"refused: %s", name, j.reason)
	case claimRequeue, claimAdopt:
		adopt := j.verdict == claimAdopt
		var adoptTip string
		if adopt {
			// Before the re-queue, not after: the drain can pick the request up on its
			// very next tick, and a Setup that runs while the stale worktree still holds
			// the branch fails with git's "already used by worktree" — the same dead end
			// the adoption exists to avoid. classifyCreateClaim has already established
			// that any holder is one Atrium minted and that no agent is running in it.
			wt, managed, err := git.StrandedWorktreeFor(ctx, e.Request.Path,
				e.Request.Claim.SessionBranch)
			if err != nil {
				// Re-probed rather than carried over from the verdict, so it can fail
				// again here — and a failure must not fall through to the re-queue,
				// which would send the request at a branch whose registration is still
				// in place. Leaving the claim is the same answer classifyCreateClaim
				// gives the same failure.
				log.ErrorLog.Printf("failed to re-check what holds branch %q before adopting it; "+
					"leaving the claim for the next launch: %v", e.Request.Claim.SessionBranch, err)
				return false, nil
			}
			if managed {
				if err := git.ReleaseManagedWorktree(ctx, e.Request.Path, wt); err != nil {
					log.ErrorLog.Printf("failed to free branch %q from the interrupted build's "+
						"worktree %s: %v", e.Request.Claim.SessionBranch, wt, err)
					return false, nil
				}
				log.InfoLog.Printf("freed branch %q from the interrupted build's worktree %s",
					e.Request.Claim.SessionBranch, wt)
			}
			// The commit the adoption is pinned to, read here rather than carried over from
			// the verdict: this is the last instant before the branch leaves this process's
			// hands, and the drain re-checks the pin against it before taking the branch-gate
			// skip (recheckAdoption). Measuring it earlier would widen the window the pin
			// exists to close by exactly the work done in between.
			tip, err := git.LookupLocalBranchTip(ctx, e.Request.Path, j.branch)
			if err != nil {
				// The re-probe above's answer to the same failure, for the same reason: a
				// re-queue that cannot pin its branch is one the drain will refuse, which
				// spends the recovery. The claim keeps it available.
				log.ErrorLog.Printf("failed to pin branch %q before adopting it; leaving the "+
					"claim for the next launch: %v", j.branch, err)
				return false, nil
			}
			if tip == "" {
				// The branch went away between the verdict and here — deleted by hand, or
				// by whatever else has this repo open. There is nothing left to adopt, and
				// nothing in the way of an ordinary create either, so it is re-queued as
				// one rather than pinned to a branch that no longer exists.
				log.WarningLog.Printf("branch %q vanished before the interrupted create request "+
					"for %q could adopt it; re-queued as an ordinary create", j.branch, name)
				adopt = false
			}
			adoptTip = tip
		}
		if err := outbox.Requeue(e.Path, adoptTip); err != nil {
			log.ErrorLog.Printf("failed to re-queue the interrupted create request for %q: %v", name, err)
			return false, nil
		}
		if adopt {
			log.WarningLog.Printf("create request for %q was interrupted after it made branch %q at "+
				"%s; re-queued to finish on that branch", name, j.branch, adoptTip)
		} else {
			log.WarningLog.Printf("create request for %q was interrupted before it built anything; "+
				"re-queued", name)
		}
	case claimRefused:
		// Disclose, reject, discard, in that order, and the first two regardless of what
		// the one before them did.
		//
		// The disclosure comes first because it is the only durable record of what this
		// build left behind, and both of the steps after it destroy things: Reject unlinks
		// the record, DiscardCreate the claim. A crash in between would leave the branch
		// and the worktree with nothing that mentions them, which is the #716 orphan
		// re-entered through the door marked "we told the caller".
		//
		// It is also what makes the discard's failure survivable, which is why it is not
		// conditional on there being an artifact to name. A claim that outlives a failed
		// unlink is re-read on every later launch, and the evidence it is judged against —
		// live git, a freshly loaded instance list — moves on: a branch since freed, a
		// session since killed, and the same claim classifies as claimAdopt and is built,
		// for a caller that exited non-zero long ago. With the disclosure on disk it
		// reaches claimAnswered instead.
		//
		// Then the receipt, whose own ordering is Reject's: on disk before the file a
		// --wait is watching goes away.
		d := j.disclosure(e.Request)
		var undisclosed *outbox.Disclosure
		if err := outbox.Disclose(e.Path, &d); err != nil {
			log.ErrorLog.Printf("failed to record what the interrupted create request for %q left "+
				"behind: %v", name, err)
			// Carried back to the caller for this launch's report. There is no reader for it
			// on disk — the write is what failed — and this arm is where the inventory is
			// richest, so the person at the terminal is the only party who can act on it.
			undisclosed = &d
		}
		if err := outbox.Reject(e.Path, j.reason); err != nil {
			log.ErrorLog.Printf("failed to write a receipt for the interrupted create request "+
				"for %q: %v", name, err)
		}
		if err := outbox.DiscardCreate(e.Path); err != nil {
			log.ErrorLog.Printf("failed to drop the interrupted create request for %q: %v", name, err)
			return false, undisclosed
		}
		log.WarningLog.Printf("giving up on an interrupted create request for %q: %s", name, j.reason)
		return true, undisclosed
	case claimDefer:
		// Deliberately nothing on disk. The evidence this verdict needs was a git call
		// that failed, and both of the writes available here are one-way: a re-queue
		// hands the request to gates that refuse it for the very branch the failed probe
		// could not see, and a refusal unlinks it. The claim is idempotent — the next
		// launch re-reads it and reaches a verdict from a git that works — so waiting is
		// the only response that keeps the recovery available. Not counted as acted on,
		// because nothing was.
		log.WarningLog.Printf("leaving the interrupted create request for %q claimed for a later "+
			"launch: %s", name, j.reason)
		return false, nil
	}
	return true, nil
}

// disclosure assembles what outbox.Disclose records for a refusal: the request's own
// identity plus the artifacts this judgement measured.
//
// Reason is the same string the caller's receipt carries, duplicated deliberately. The
// receipt is consumed by whoever reads it (outbox.ClearRejection) and swept at the TTL
// horizon, so it cannot be the thing a later launch reads to explain the leftovers it is
// about to name.
func (j claimJudgement) disclosure(r outbox.Request) outbox.Disclosure {
	return outbox.Disclosure{
		Title:    r.Title,
		Repo:     r.Path,
		Branch:   j.branch,
		Worktree: j.worktree,
		TmuxName: j.tmuxName,
		Reason:   j.reason,
	}
}

// rowFor returns the loaded session a create request produced, or nil.
//
// The stamp is the whole match: session.InstanceData.CreateRequest records the spool
// path the session was built for, so this asks "did THIS request make a session" rather
// than "does a session of this name exist".
func rowFor(instances []*session.Instance, record string) *session.Instance {
	for _, inst := range instances {
		if inst.CreateRequest == record {
			return inst
		}
	}
	return nil
}

// branchOwner returns the title of a loaded session holding branch, and whether that
// session is in the same repository as repo — the "belongs to no row `atrium ls` can
// show" half of the orphan test. An empty title means no row holds the branch at all.
//
// A branch name is only unique within one repository, so the same-repo match is the
// meaningful one and is preferred: two sessions of the same title in two checkouts
// derive the same slug. But a row in another repo is still reported, rather than
// skipped, and the caller still refuses on it. That is deliberately the wrong answer in
// the harmless direction, because the two errors here are not comparable:
//
//   - a false hit costs a refusal carrying a receipt that names the branch and the
//     session holding it, and the caller can retry under another title;
//   - a miss falls through to claimAdopt, whose first act is ReleaseManagedWorktree —
//     `git worktree remove -f` plus os.RemoveAll — on a live session's worktree.
//
// And a miss is reachable, because inst.Path is the directory the session was created
// FROM, not its repository root: resolveNewTarget stores the caller's cwd through
// filepath.Abs, which does not resolve symlinks, so a session created from /repo/backend
// or through a symlinked path compares unequal to one holding the same branch. Every
// other repo-scoped comparison in the tree goes through GroupKey()/RepoGroupKey; those
// cost a subprocess per row, which this path (running before the first frame, over
// every loaded instance) will not spend to sharpen a match whose only job is to say no.
func branchOwner(instances []*session.Instance, repo, branch string) (title string, sameRepo bool) {
	want := filepath.Clean(repo)
	var elsewhere string
	for _, inst := range instances {
		if inst.Branch != branch {
			continue
		}
		if filepath.Clean(inst.Path) == want {
			return inst.Title, true
		}
		if elsewhere == "" {
			elsewhere = inst.Title
		}
	}
	return elsewhere, false
}
