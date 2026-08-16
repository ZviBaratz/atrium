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

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
)

// claimVerdict is what reconcileCreateClaims decided about one stranded claim. Named
// values rather than a bool pair because the four outcomes are not two axes: "finish
// it" and "give up on it" differ in what they write, and "re-queue" and "re-queue to
// adopt" differ in what the next drain is allowed to skip.
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
)

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
// Every path writes something. A verdict that neither settles nor re-queues would leave
// the claim for the next launch to reach the same conclusion about, and leave a --wait
// blocked on it until its deadline.
func reconcileCreateClaims(ctx context.Context, instances []*session.Instance, branchPrefix string, now time.Time) int {
	claims, err := outbox.ListClaims()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool for stranded requests: %v", err)
		return 0
	}
	if len(claims) == 0 {
		return 0
	}

	var acted int
	for _, e := range claims {
		verdict, reason := classifyCreateClaim(ctx, e, instances, branchPrefix, now)
		if applyCreateClaim(e, verdict, reason) {
			acted++
		}
	}
	return acted
}

// classifyCreateClaim reads one stranded claim into a verdict, plus the reason a
// caller is owed when that verdict is claimRefused.
//
// The order of the arms is the argument. The recorded row is consulted first because it
// is the only evidence that settles the question outright; the branch is consulted next
// because a branch nobody owns is the case this whole file exists for; and everything
// that is neither is refused rather than guessed at.
func classifyCreateClaim(ctx context.Context, e outbox.CreateEntry, instances []*session.Instance,
	branchPrefix string, now time.Time) (claimVerdict, string) {
	// An undecodable claim cannot be matched to a row or a branch, so there is nothing
	// to finish and nothing to re-queue — the same dead end ListCreates' Err arm hits,
	// and the same answer.
	if e.Err != nil {
		return claimRefused, fmt.Sprintf("a previous atrium was interrupted while creating this "+
			"session and the request it left behind could not be read (%v)", e.Err)
	}
	if e.Request.Expired(now) {
		age := now.Sub(e.Request.CreatedAt).Round(time.Minute)
		return claimRefused, fmt.Sprintf("a previous atrium was interrupted while creating this "+
			"session; the request was spooled %s ago, past the %s horizon, so it names a branch "+
			"point the tree has moved on from and is discarded rather than rebuilt", age, outbox.TTL)
	}

	// The row names the request, not the other way round. A (Title, Path) match would
	// be the same comparison the drain's conflict gate already made and passed before
	// the crash, so it cannot distinguish this request's session from one somebody else
	// created under that title since — and reporting a stranger's session to a waiting
	// --wait as its own is worse than the refusal this replaces.
	if rowFor(instances, e.Path) != nil {
		return claimSucceeded, ""
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
		return claimRequeue, ""
	}

	branch := e.Request.Claim.SessionBranch
	if branch == "" {
		// A direct (non-git) session has no branch to strand, so an unrecorded one
		// built nothing durable whatever else it got through. Nothing to adopt, and
		// nothing in the way of a clean second attempt.
		return claimRequeue, ""
	}
	if !git.LocalBranchExists(ctx, e.Request.Path, branch) {
		return claimRequeue, ""
	}

	// From here the branch exists and no row bears this request's stamp. Two things
	// still have to be true before it can be treated as this build's own leftovers.
	if owner := branchOwner(instances, e.Request.Path, branch); owner != "" {
		// A live session holds it. That is not an orphan at all, and adopting it would
		// put a second agent on one branch.
		return claimRefused, fmt.Sprintf("a previous atrium was interrupted while creating this "+
			"session, and branch %q now belongs to session %q; pick another title", branch, owner)
	}
	if e.Request.Claim.BranchExisted && !e.Request.Adopt {
		// The branch was already there when the build claimed the request, so it is
		// somebody else's and always was. This is unreachable through the drain's own
		// gate, which refuses such a request before it can be claimed; it is here
		// because the field is evidence and a guard that trusts evidence has to check
		// the case where the evidence says no.
		return claimRefused, fmt.Sprintf("a previous atrium was interrupted while creating this "+
			"session, and branch %q already existed before it started, so it is not this "+
			"session's to take; delete it or pick another title", branch)
	}
	return claimAdopt, ""
}

// applyCreateClaim carries out a verdict and reports whether it was applied. A failure
// is logged and left alone: the claim survives, and the next launch reaches the same
// verdict from the same evidence.
func applyCreateClaim(e outbox.CreateEntry, verdict claimVerdict, reason string) bool {
	name := e.Request.Title
	switch verdict {
	case claimSucceeded:
		// No receipt. The absence of both the record and its claim, with no rejection
		// beside them, is what awaitSpool reads as success — and waitForCreate then
		// reads the branch back out of the row that made this verdict true.
		if err := outbox.DiscardCreate(e.Path); err != nil {
			log.ErrorLog.Printf("failed to close out a completed create request for %q: %v", name, err)
			return false
		}
		log.InfoLog.Printf("create request for %q completed before an earlier atrium exited; "+
			"its session is recorded", name)
	case claimRequeue, claimAdopt:
		adopt := verdict == claimAdopt
		if err := outbox.Requeue(e.Path, adopt); err != nil {
			log.ErrorLog.Printf("failed to re-queue the interrupted create request for %q: %v", name, err)
			return false
		}
		if adopt {
			log.WarningLog.Printf("create request for %q was interrupted after it made branch %q; "+
				"re-queued to finish on that branch", name, e.Request.Claim.SessionBranch)
		} else {
			log.WarningLog.Printf("create request for %q was interrupted before it built anything; "+
				"re-queued", name)
		}
	case claimRefused:
		// Reject then release, in that order and both regardless: the receipt has to be
		// on disk before the file a --wait is watching goes away, and a claim left
		// behind would be re-judged on every launch forever.
		if err := outbox.Reject(e.Path, reason); err != nil {
			log.ErrorLog.Printf("failed to write a receipt for the interrupted create request "+
				"for %q: %v", name, err)
		}
		if err := outbox.DiscardCreate(e.Path); err != nil {
			log.ErrorLog.Printf("failed to drop the interrupted create request for %q: %v", name, err)
			return false
		}
		log.WarningLog.Printf("giving up on an interrupted create request for %q: %s", name, reason)
	}
	return true
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

// branchOwner returns the title of the loaded session holding branch in repo, or "" if
// none does — the "belongs to no row `atrium ls` can show" half of the orphan test.
//
// Scoped by repo as well as by branch name, because a branch name is only unique within
// one repository: two sessions of the same title in two checkouts derive the same slug,
// and matching on the name alone would let one repo's live session veto the other's
// recovery.
func branchOwner(instances []*session.Instance, repo, branch string) string {
	want := filepath.Clean(repo)
	for _, inst := range instances {
		if inst.Branch == branch && filepath.Clean(inst.Path) == want {
			return inst.Title
		}
	}
	return ""
}
