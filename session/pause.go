package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/internal/teardown"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// Pause/resume lifecycle: parking a session (commit dirty work, close the tmux
// session, remove the worktree, keep the branch) and bringing it back, plus the
// auto-commit markers Resume unwinds so a pause/resume round-trips transparently.

// Paused reports whether the instance is parked: no agent process, branch preserved.
// See the Status constant for why the worktree is usually — but not always — gone.
func (i *Instance) Paused() bool {
	return i.GetStatus() == Paused
}

// WorkingDirGone reports whether the directory this session's agent and its shells
// run in — WorkingDir(): the worktree for a git session, the user's own checkout for
// a direct one — is no longer on disk.
//
// It is the discriminator a teardown's caller needs before reaping the session's
// cached terminal shell (#707), and neither fact such a caller already holds answers
// it. pause()'s error does not: a failed WIP commit keeps the worktree and returns
// non-nil, while the orphaned-worktree branch below removes the directory and returns
// tc.Err() — which is nil when its own teardown steps happen to succeed. So the two
// do not correlate in either direction. Paused() cannot be read for it either — see
// the Paused Status constant, which says outright that nothing may infer from it that
// the directory is gone.
//
// It is a measurement rather than a report of intent because the two differ in both
// directions: pause()'s removeWorktree local is not even in scope for the orphan
// branch, and it stays true when the removal it authorises fails outright.
//
// Only fs.ErrNotExist counts. A permission or I/O error leaves the shell alone: the
// cost of a false "gone" is killing a live shell in a directory that is still there,
// and possibly the work the user was about to rescue with it.
func (i *Instance) WorkingDirGone() bool {
	dir := i.WorkingDir()
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	ts := i.tmux()
	return ts != nil && ts.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch. That
// removal is not a postcondition. One branch skips it deliberately — a pause whose
// auto-commit of dirty work fails keeps the worktree, so the WIP it could not commit
// is left on disk to be rescued — and the removal is best-effort besides, so it can
// simply fail with the directory still there (see pause). Callers
// that act on the removal — the terminal-shell reap in particular — must therefore
// ask WorkingDirGone rather than assume it from a nil error.
//
// A direct (non-git) session has no worktree to free and runs in the user's real
// directory, so pausing it would buy nothing and cost the agent: the park would stop
// a live agent standing in the user's own checkout, with no disk or branch reclaimed
// in exchange. Pause therefore refuses a direct session. (A direct session whose pane
// actually dies is still parked via RecoverLostSession.)
func (i *Instance) Pause() error {
	if i.direct {
		return fmt.Errorf("cannot pause a direct (non-git) session: it runs in place with no worktree to free")
	}
	return i.pause()
}

// RecoverLostSession transitions an instance whose tmux pane has died (server
// restart, agent exit, external kill) into Paused, so the metadata loop stops
// polling it and the user can bring it back with Resume. It reuses the Pause path —
// committing any uncommitted work and removing the worktree.
//
// That last clause is conditional here for the same reasons it is on Pause, plus one
// reachable only from this entry point: it does not refuse a direct session the way
// Pause does, and a direct session has no worktree at all — that branch closes the
// session and stops the run command without committing or removing anything, in what
// is the user's own checkout. Hence WorkingDirGone for anything that acts on the
// removal.
func (i *Instance) RecoverLostSession() error {
	return i.pause()
}

// RepairResumingLaunch relaunches the agent BLANK when the launch that just died was a
// resuming one made within `within`, and reports whether it did. It is the caller's
// first move on a lost session: a repair here means the session stays Running and
// RecoverLostSession is never reached.
//
// The failure it repairs is #699's, generalised (#712). Atrium relaunches a dead agent
// with its resume flag, and only claude is asked first whether a conversation exists —
// transcript.HasResumable has an adapter for nothing else, and ResumeProbe answers a
// different question (does the binary support the flag). So agy, codex and gemini are
// launched with a resume flag whether or not there is anything to resume, and that this
// is harmless is a property of their CLIs rather than of anything here. All three were
// driven and all three survive; this is what keeps a vendor changing its mind from
// costing a session, without modelling any vendor's on-disk conversation store.
//
// Every condition is a refusal to relaunch on a guess:
//
//   - lastLaunchResumed, because a blank relaunch of a command the adapter did not
//     REWRITE is the same command again. Note what that excludes: a program already
//     carrying its own resume flag — a forked claude pinned to a conversation id, which
//     session/fork.go writes — is launched unchanged, so the flag reads false and this
//     declines. Declining is right (dropping nothing would change nothing), but it does
//     mean a pin whose conversation has been garbage-collected is a #699 death nothing
//     here reaches;
//   - DiedAtLaunch, because a session that ran for hours and then died did not die OF
//     its launch — the user quit it, or the machine did — and relaunching that would be
//     a resurrection nobody asked for;
//   - a worktree git still recognises, not merely a directory that stats: a base repo
//     that moved or was deleted leaves the directory behind with a dangling .git file,
//     and an agent launched there fails every git operation it makes. That is the park's
//     case — it keeps the branch — and recoverInPlace asks the same question for the same
//     reason. A directory that is simply gone is RecoverLostSession's;
//   - a tmux session that is DEFINITIVELY gone (Session.Gone), not merely one that did
//     not answer, because what follows is a kill and a relaunch: on a false positive
//     this would close a live agent and start a second over it. The caller decides WHEN
//     to try, with a debounce that already refuses to read a transient probe failure as
//     a death (the #270 shape); this re-asks immediately before acting, so an
//     inconclusive probe here declines rather than destroys;
//   - and a session that can still be closed, since the relaunch would otherwise meet
//     Start's duplicate-name guard.
//
// Once, per launch: the relaunch it makes carries no resume flag, so lastLaunchResumed
// is false afterwards and a second death parks in the ordinary way.
//
// What it CANNOT tell is why the launch died, and the window is the whole of the
// diagnosis. An agent the user quit deliberately seconds after a resume is relaunched
// rather than parked, and a typo'd program/profile is relaunched once before its second
// death reaches showLaunchCrash's modal — both cost one relaunch and a notice, which is
// why the notice says the launch died rather than naming a cause it has not established.
// A narrower gate would have to model each vendor's exit codes; the ceiling on being
// wrong here is one blank agent in the worktree the session already owned.
func (i *Instance) RepairResumingLaunch(within time.Duration) bool {
	if !i.isStarted() || i.Paused() {
		return false
	}
	i.mu.RLock()
	resuming := i.lastLaunchResumed
	i.mu.RUnlock()
	if !resuming || !i.DiedAtLaunch(within) {
		return false
	}
	ts := i.tmux()
	if ts == nil {
		return false
	}
	workDir := i.WorkingDir()
	if workDir == "" || i.WorkingDirGone() {
		return false
	}
	// A directory that stats is not a worktree git still recognises, and every other
	// relaunch path asks the stronger question — recoverInPlace degrades to Paused when
	// this fails, and pause() has a whole branch for the orphan. WorkingDirGone counts
	// only fs.ErrNotExist by design, so it passes for a worktree whose base repo has
	// moved or been deleted: the agent would come up in a tree where every git operation
	// made in it fails, where the park would have kept the branch.
	// Skipped for a direct session, which has no worktree and runs in the user's own
	// checkout (worktree() is nil there).
	if wt := i.worktree(); wt != nil {
		valid, err := wt.IsValidWorktree()
		if err != nil {
			log.ErrorLog.Printf("cannot validate the worktree for %s, leaving it to be parked: %v", i.Title, err)
		}
		if err != nil || !valid {
			return false
		}
	}
	if !ts.Gone() {
		return false
	}

	// Detach before close, the pairing closeParkedSession owns and for its reason: Close
	// kills the session and closes ptmx but never clears attachCh, so closing without
	// detaching would strand the goroutines of an attach it raced (#701). A failed close
	// ABORTS — unlike a park, which must finish regardless, this is optional repair, and
	// relaunching over a session that would not die is how you get two agents or a
	// launch refused by name.
	if err := ts.DetachSafely(); err != nil {
		log.ErrorLog.Printf("failed to detach the dead session for %s: %v", i.Title, err)
	}
	if err := ts.Close(); err != nil {
		log.ErrorLog.Printf("cannot repair the resuming launch for %s: %v", i.Title, err)
		return false
	}

	// startResuming re-applies this before every relaunch, and for the same reason: a
	// tmux session's environment can only be set as it is born.
	i.applySessionEnv(workDir)
	if err := ts.Start(workDir); err != nil {
		log.ErrorLog.Printf("blank relaunch failed for %s, leaving it to be parked: %v", i.Title, err)
		return false
	}
	log.InfoLog.Printf("%q exited at launch while resuming its conversation; relaunched without resuming", i.Title)

	i.mu.Lock()
	// Stamped so DiedAtLaunch keeps describing THIS launch: a blank agent that also dies
	// at birth is a crash-at-launch the caller must still be able to name (#270).
	i.startedAt = time.Now()
	i.lastLaunchResumed = false
	// The conversation did not come back. conversationKnown is left alone — whatever the
	// transcript adapter could say about this agent it can still say; what changed is the
	// answer, not whether there is one.
	i.conversationResumed = false
	i.mu.Unlock()

	// Running, then the arm: the pairing both other relaunch paths use — Resume and
	// recoverInPlace — and which ArmReadySuppression's own doc names as its contract.
	// Both halves are load-bearing here.
	//
	// The write, because nothing else replaces the dead agent's status. applyMetadataResults
	// skips a sessionLost result, so without this the row keeps whatever the dying agent
	// last painted — Ready, NeedsInput, a held Pending — while a brand-new agent boots
	// underneath it.
	//
	// The order, because ArmReadySuppression is a one-shot consumed by an edge INTO Ready
	// (setStatusLocked). Arming on a row that is already Ready leaves it unconsumed until
	// some later edge eats it, which for a chip-held Pending is a genuine turn-end going
	// silent — no ding, no unread glyph, skipped by NextUnread. A non-Ready write clears
	// any stale arm first, so the one set below is this launch's.
	i.SetStatus(Running)
	// The relaunched agent's boot idle is a boot artifact, not a finished turn — the
	// suppression Resume and recoverInPlace arm for the same reason.
	i.ArmReadySuppression()
	return true
}

// Auto-commit marker. Pause commits a dirty worktree under this message so work
// is not lost when the worktree is removed; Resume recognizes it by these
// affixes and soft-resets it away, making pause/resume round-trip transparently.
// The writer (pause) and reader (resume) share these so the format can't drift.
const (
	autoPauseCommitPrefix = "[atrium] update from "
	autoPauseCommitSuffix = "(paused)"
)

// isAutoPauseCommit reports whether a commit subject is one of pause's
// auto-commits. A genuine, user-authored commit never matches, so Resume only
// ever unwinds Atrium's own markers.
func isAutoPauseCommit(subject string) bool {
	s := strings.TrimSpace(subject)
	return strings.HasPrefix(s, autoPauseCommitPrefix) && strings.HasSuffix(s, autoPauseCommitSuffix)
}

// closeParkedSession stops a session's agent: it tears the attach down and THEN kills
// the tmux session. Every park below goes through it, so the pairing is one unit with
// one rationale rather than three copies of two lines.
//
// The order is the whole of its correctness, and it is not a swap. Close kills the
// session and closes ptmx, but it never clears attachCh, cancels the context or
// disables the stdout pump, so a park that closed without detaching would strand the
// goroutines of an attach it raced, with no channel close to release the reader
// (#701). Detach owns the attach teardown; close owns the session.
//
// Neither failure aborts the park: every caller ends the instance Paused, and Paused
// means no agent process, so a park that gave up halfway would leave exactly the
// contradiction #710 was filed about. Close is itself a teardown.Errors path that logs
// its own aggregate, so it goes through Wrap rather than Record (as Instance.Kill
// does); DetachSafely does not log, so it keeps Record.
func closeParkedSession(tc *teardown.Errors, ts *tmux.Session) {
	tc.Record("detach tmux session", ts.DetachSafely())
	tc.Wrap("close tmux session", ts.Close())
}

// pause closes the tmux session — ending the agent process with it — and removes the
// worktree, preserving the branch.
//
// Read "removes the worktree" as an intent, not a postcondition: it is conditional
// and best-effort, and paths below leave the directory standing on purpose (the
// failed-WIP-commit branch, which keeps the work it could not commit), by
// construction (the direct-session branch has no worktree; the two guard returns at
// the top touch nothing), and by failure (wt.Remove() and its os.RemoveAll fallback
// can both fail, leaving the directory exactly where it was). The returned error
// discriminates none of that — the WIP branch errors while keeping the worktree, and
// the orphaned-worktree branch frees the directory while returning a tc.Err() that is
// nil whenever its own steps succeeded. A caller that needs the outcome must measure
// it — WorkingDirGone — rather than read it off err or off Paused().
func (i *Instance) pause() error {
	if !i.isStarted() {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Paused() {
		return fmt.Errorf("instance is already paused")
	}
	// The cached pane frame is dropped by the flip to Paused itself (SetStatus), not
	// here: every path below ends Paused, and dropping before the commit/worktree I/O
	// would only be undone by a capture landing during it.

	ts := i.tmux()
	wt := i.worktree()

	// The managed port (#389) is deliberately NOT released here, on any path below.
	// Nothing exports it while the session is parked — the pane is closed with the
	// session — so this is a promise rather than a collision guard: the session comes
	// back on the number it left with, and the run command below restarts on it, so
	// nothing renumbers under a browser tab or a rendered template. What does release
	// one is Kill, and a config edit that stops routing this repo to a port range
	// (resolveSetupRun) — neither of which is a park, which is the point.
	//
	// Resume re-reaches reservePort through applySessionEnv, and reservePort keeps a
	// port the session already holds, so the number survives the round trip without
	// anything here re-allocating it.

	// The run command does NOT survive, and for the opposite reason to the port: the
	// worktree below is about to be removed, and that worktree is the command's working
	// directory — a dev server left running there is running in a deleted tree (#389).
	// The wanted flag is kept, so Resume restarts it, on the port this pause just kept.
	// Before the removal, not deferred: the server should be gone by the time its
	// directory is.
	i.pauseRunCommand()

	var tc teardown.Errors

	// Direct session: no worktree to commit/remove. User-initiated Pause is refused
	// for direct sessions (see Pause), so this branch is only reached via
	// RecoverLostSession when the pane has died — park it so the poll loop stops and
	// the user can Resume, without ever touching the user's real directory.
	//
	// The close is not redundant with the dead pane that got us here: it is what
	// reaps the tmux session the pane left behind, and Close treats an already-gone
	// session as the goal met rather than a failure (sessionAlreadyGone), so this
	// branch does not start reporting an error for the ordinary case.
	if wt == nil {
		closeParkedSession(&tc, ts)
		i.SetStatus(Paused)
		return tc.Err()
	}

	// If the worktree is orphaned (path or .git missing), git cannot operate
	// on it. Skip dirty check and Remove, prune any lingering metadata, then
	// transition to Paused so the user can recover via Resume.
	if valid, err := wt.IsValidWorktree(); err != nil {
		tc.Record("validate worktree", err)
	} else if !valid {
		log.WarningLog.Printf("worktree at %s is orphaned; skipping dirty check and remove",
			wt.GetWorktreePath())
		closeParkedSession(&tc, ts)
		// Drop any leftover directory so a future Resume's `git worktree add` won't conflict.
		tc.Record("remove orphaned worktree directory", os.RemoveAll(wt.GetWorktreePath()))
		tc.Record("prune git worktrees", wt.Prune())
		// The worktree is gone and any uncommitted changes it held are
		// unrecoverable, so the cached dirty flag (still maintained for paused
		// instances, which the poll loop skips) must not keep claiming there are
		// uncommitted changes.
		i.clearCachedDirty()
		i.SetStatus(Paused)
		return tc.Err()
	}

	// Whether it is safe to remove the worktree. Committing dirty work is a
	// precondition: if the commit fails we must NOT remove the worktree — that
	// would destroy the very WIP pause exists to protect. We still park the
	// session as Paused below (leaving the WIP on disk for the user to rescue) so
	// a lost-session recovery doesn't loop and the row doesn't freeze at Running.
	removeWorktree := true
	if dirty, err := wt.IsDirty(); err != nil {
		// removeWorktree deliberately stays true, and it is NOT the same judgement as
		// the commit failure below: a failed check has not met the precondition, it has
		// only failed to ask, so this branch removes a worktree that may hold WIP.
		// Filed as #740 rather than inverted here, because "unknown" has two causes that
		// want opposite answers and only one of them is transient. A contended
		// index.lock wants the worktree kept; a base repo deleted out from under the
		// session reaches this same branch (git reports "not a git repository" through
		// the worktree's gitdir), and there keeping it strands a directory with no
		// branch left to rescue anything onto — which is the #270 orphan fallback
		// below, and what TestPause_RemoveFailureFallsBackToParkedPaused holds.
		tc.Record("check if worktree is dirty", err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub)
		commitMsg := fmt.Sprintf("%s'%s' on %s %s", autoPauseCommitPrefix, i.Title, time.Now().Format(time.RFC822), autoPauseCommitSuffix)
		if tc.Record("commit changes", wt.CommitChanges(commitMsg)) {
			removeWorktree = false
		} else {
			// The metadata poll skips paused instances, so fold this WIP commit into
			// the cached/persisted commit count now — otherwise the kill dialog would
			// not warn before `branch -D` destroys its only ref.
			i.noteAutoPauseCommit()
		}
	}

	// Stop the agent, and do it BEFORE the worktree below goes: a process whose cwd is
	// unlinked out from under it keeps running with a `(deleted)` working directory it
	// can neither read nor write, which is what a paused session used to be left as
	// (#710). The ordering is this site's; why the two calls travel together is
	// closeParkedSession's.
	//
	// Unconditional, including on the branch above that keeps the worktree. The
	// deleted-cwd hazard is not the only reason to stop the agent — the session ends
	// Paused either way, and Paused means no agent process, which is the invariant the
	// poll skip, the session cap, `peek` and Preview all already read that way.
	closeParkedSession(&tc, ts)

	if removeWorktree {
		// Check if worktree exists before trying to remove it
		if _, err := os.Stat(wt.GetWorktreePath()); err == nil {
			// Remove worktree but keep branch. If git can't remove it (the base repo
			// moved or was deleted), fall back to the orphan handling the
			// invalid-worktree branch uses — best-effort directory removal + prune —
			// so the session still ends Paused instead of stuck Running with a dead
			// pane. The Remove error is still recorded so the failure surfaces once.
			if tc.Record("remove git worktree", wt.Remove()) {
				tc.Record("remove orphaned worktree directory", os.RemoveAll(wt.GetWorktreePath()))
			}
			// Prune stale metadata even if this fails — the worktree directory is
			// gone now, so the session must be marked Paused regardless.
			tc.Record("prune git worktrees", wt.Prune())
		}
		// The worktree is gone and its work is committed on the branch, so the
		// cached dirty flag must not keep claiming uncommitted changes. When the
		// commit failed above we deliberately leave it set (the WIP is real and
		// still on disk) so the kill dialog keeps warning before branch -D.
		i.clearCachedDirty()
	}

	i.SetStatus(Paused)

	return tc.Err()
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.isStarted() {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if !i.Paused() {
		return fmt.Errorf("can only resume paused instances")
	}

	ts := i.tmux()
	wt := i.worktree()

	// Direct session: no worktree to recreate, so relaunch the agent in the real
	// directory — the normal path, since a park closed the session. A live session
	// here is one an older, detach-only pause left behind, and reattaching it is
	// still right: a direct session's directory is the user's own checkout, which no
	// pause ever removed, so its agent's cwd is exactly where it always was. That is
	// the whole difference from the worktree branch below.
	//
	// "Reattaches" is what this branch does when the probe ANSWERS. DoesSessionExist is
	// liveness() == sessionAlive, so an indeterminate answer relaunches instead, and a
	// session that was in fact alive then fails the launch on Start's duplicate-name
	// guard. The worktree branch below stopped gating on that predicate for a hazard this
	// branch does not share; the residual here is a loud failure rather than a pane over a
	// deleted directory, so it is filed as #748 rather than changed alongside the fix.
	if wt == nil {
		if ts.DoesSessionExist() {
			if err := ts.Restore(); err != nil {
				log.ErrorLog.Print(err)
				if closeErr := ts.Close(); closeErr != nil {
					log.ErrorLog.Printf("failed to close stale session %s: %v", i.Title, closeErr)
				}
				if err := i.recreateSession(); err != nil {
					return err
				}
			}
		} else if err := i.recreateSession(); err != nil {
			return err
		}
		i.SetStatus(Running)
		// The resumed agent boots back into its old conversation — the first
		// poll's settle to Ready is not new output, so don't flag unread.
		i.ArmReadySuppression()
		i.resumeRunCommand()
		return nil
	}

	// Stop the session this park left behind — BEFORE anything below materializes or
	// rewrites the worktree. Unconditional, and deliberately above the valid-worktree
	// block, so it governs every park that reaches here rather than only the usual one.
	//
	// The worst case is the park that removed the worktree, which is the ordinary one: a
	// session that survived that has an agent whose cwd is the inode the removal
	// unlinked, so restoring the PTY would hand the user a pane printing the right $PWD
	// over a directory it can neither read nor write (#710). That is the hazard the
	// direct branch above does not have — its directory is the user's own checkout, which
	// no pause ever removed — and it is why that branch may still reattach and this one
	// never may. For the parks that left the worktree standing (the block below names
	// them) the deleted-cwd reasoning does not apply at all; closing is still right there,
	// for closeParkedSession's reason instead — Paused means no agent process, and the
	// relaunch below is what brings one back. A live session on either path is one an
	// older, detach-only pause left behind, so this is both the fix's belt-and-braces and
	// its upgrade path; after a park this build made there is nothing left to kill, and
	// Close reports that as the goal already met (sessionAlreadyGone).
	//
	// NOT gated on DoesSessionExist, and that is the substance rather than a tidy-up: it
	// reports liveness() == sessionAlive, so every INDETERMINATE answer — a socket that
	// cannot be opened for any reason but ENOENT, a probe the context cancelled — reads
	// as "no session" and would skip the close for precisely the servers that cannot be
	// killed. Close forgives an already-gone session on its own, so the guard bought
	// nothing except that hole and a second has-session round trip. (The direct branch
	// above still carries that gate. It is not the same hazard — a missed close there
	// costs a duplicate-name launch failure, not a pane over a deleted directory — and
	// changing it is filed as #748 rather than done inside this one.)
	//
	// Detach before close, the pairing closeParkedSession owns and for its reason: Close
	// kills the session and closes ptmx but never clears attachCh, cancels the context or
	// disables the stdout pump, so closing without detaching would strand the goroutines
	// of an attach it raced (#701). Nothing can be attached to a Paused instance today
	// (attachSelected refuses one), which makes this a no-op — detachSafelyLocked returns
	// immediately on a nil attachCh — rather than a redundancy: the ordering holds here by
	// construction instead of by a fact about a gate in another package. It cannot reuse
	// closeParkedSession itself, whose contract is the opposite of this one: a park may
	// never abort, and this must.
	//
	// A failure ABORTS the resume rather than being logged and walked past, and it runs
	// here so the abort has nothing to undo. Walking past it reaches a relaunch that
	// Start's duplicate-name guard is bound to refuse, by which point this call would
	// have re-added the worktree and unwound the pause auto-commit for a launch that
	// never had a chance — leaving a park that is no longer a park. Until #741 it was
	// worse than that: the failed launch answered by tearing the worktree down through
	// Worktree.Cleanup — `git worktree remove -f` and `git branch -D`, the KILL
	// teardown, with no retention ref, because only Kill records one. It no longer tears
	// anything down, which is why the cost of walking past is now the state above rather
	// than the loss. Ordered above the block below, a park is still a park when
	// this returns — the worktree is wherever the pause left it, the branch holds every
	// commit, and no auto-commit has been unwound — so retrying costs nothing and is
	// simply a second Resume. That is a statement about the state left behind, not a
	// promise the retry will fare better: a socket that is unreachable rather than empty
	// fails the same way every time, which is why the error names where the work is.
	//
	// What returning does not undo is Close's own first half: cleanupHookSession and
	// resetPaneID run before the kill that failed, so a session still alive here has lost
	// its hook directory. Nothing is reading it — the instance stays Paused, which the
	// poll loop skips — and a later successful resume re-arms one under a freshly frozen
	// name (freezeHookName). The close's error is not logged here — Close records its own
	// through teardown.Errors, and the wrap below carries it to the caller — while the
	// detach's is, because nothing else would: it is deliberately not folded into the
	// returned error, since a detach that failed on an already-dead attach must not be
	// what refuses a resume. That is the same Record/Wrap split closeParkedSession makes,
	// spelled out here because this site cannot use teardown.Errors to make it.
	if err := ts.DetachSafely(); err != nil {
		log.ErrorLog.Printf("failed to detach the session %s was parked with: %v", i.Title, err)
	}
	if err := ts.Close(); err != nil {
		// One line, no newlines: a batch resume renders each failure as a bullet in a
		// summary modal (batchResumeDoneMsg.summary), so a wrapped remedy would break the
		// list it sits in. The remedy is named because this failure can be permanent —
		// an unreachable socket fails identically on every retry — and `atrium doctor`
		// is the reader that can see it, reporting servers no socket lookup reaches
		// (doctor.CheckOrphans).
		return fmt.Errorf("failed to close the session %s was parked with: %w "+
			"(it stays paused, with its work on branch %s; run `atrium doctor` to check the "+
			"tmux server, then resume again)", i.Title, err, wt.GetBranchName())
	}

	// If our own worktree is still materialized on disk, reuse it as-is: running Setup
	// would clearStaleWorktree and re-add from the branch, discarding any uncommitted
	// work — and BranchCheckoutPath would see our own worktree as a foreign checkout and
	// refuse the resume outright.
	//
	// A normal pause removes the worktree, so two populations arrive here with one, and
	// the skipped unwind below is right for both:
	//
	//   - a park that left it on disk: a pause whose WIP commit failed (see pause), a
	//     startup recovery whose relaunch failed after the worktree validated
	//     (recoverInPlace), one the host session budget deferred (parkOverBudget). None
	//     of them ran pause's auto-commit, so there is none to unwind.
	//   - an earlier Resume of this same session that materialized the worktree and then
	//     failed to launch the agent. Since #741 that failure tears nothing down, so the
	//     directory stays for the retry — and that attempt already ran the unwind below.
	//
	// The test is therefore the property rather than the list. What the second population
	// costs is disclosed rather than handled, and it is one shape rather than two items:
	// the retry skips this entire block, so ANYTHING in it that failed on the attempt
	// before is not attempted again (#791). Both halves are best-effort by design — an
	// unwind that errored leaves its auto-commit in history, and a setup script that
	// failed or was aborted leaves the worktree half-provisioned — and neither aborts a
	// resume, which is exactly why neither gets a second chance here.
	//
	// Otherwise materialize it fresh, first guarding against the branch being checked
	// out elsewhere (base repo or a sibling worktree).
	valid, err := wt.IsValidWorktree()
	if err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to validate worktree: %w", err)
	}
	if !valid {
		// Naming the holding path makes the error actionable and lets the app layer
		// offer to detach the base repo automatically.
		if heldBy, err := wt.BranchCheckoutPath(); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to check if branch is checked out: %w", err)
		} else if heldBy != "" {
			return &git.BranchCheckedOutError{Branch: wt.GetBranchName(), Path: heldBy}
		}

		// Setup git worktree
		if err := wt.Setup(); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to setup git worktree: %w", err)
		}

		// Reverse the auto-commit pause made (if any), so the worktree comes back
		// exactly as it was left — changes restored, no history artifact. Best-effort:
		// the WIP content is safe inside the commit regardless, so a failure here must
		// not abort resume; worst case is the prior behavior (the commit stays).
		if n, err := i.unwindAutoPauseCommits(wt); err != nil {
			log.ErrorLog.Print(err)
		} else {
			// The unwound commits are pending changes again, so walk the count pause
			// bumped back down; otherwise the kill dialog would over-count after a
			// resume (durably if the session is re-paused before the next poll).
			i.noteAutoPauseUnwind(n)
		}

		// The worktree is new, so the per-repo setup script runs again (#389). Pause
		// removed the directory and with it every gitignored path the last run
		// installed — node_modules, a built binary, a generated .env — so "once per new
		// worktree" is the rule, and this IS a new worktree. Deliberately inside the
		// !valid branch: a park that left its worktree materialized skips Setup, and
		// re-running `npm ci` for it would be a cost with nothing to buy. That is sound
		// for a park, which never ran this; it is the weaker half for a resume retrying
		// after a failed launch, which did — and possibly failed at it (#791).
		i.RunSetupScript(wt.GetWorktreePath())
	}

	// Relaunch the agent, resuming its prior conversation rather than starting blank —
	// never reattaching, for the reason the close above carries. A failure TEARS NOTHING
	// DOWN (see recreateSession) — which is not the same as leaving the session as it was
	// found: the block above has already re-added the worktree and moved the branch tip
	// back off pause's auto-commit. What survives is every commit and the restored work,
	// so the retry is a second Resume rather than a rescue.
	if err := i.recreateSession(); err != nil {
		return err
	}

	i.SetStatus(Running)
	// As above: the resumed agent's post-boot idle is not a genuine completion.
	i.ArmReadySuppression()

	// The worktree is back and the agent is launched, so the run command the pause
	// stopped can have its directory again (#389). It restarts on the port the pause
	// deliberately held rather than released (see pause), so nothing renumbers
	// underneath a browser tab.
	//
	// Last, and specifically AFTER the flip out of Paused: StartRunCommand refuses a
	// paused session — rightly, since a parked one has no worktree to run in — so a
	// call placed before this line silently restarts nothing at all.
	i.resumeRunCommand()
	return nil
}

// maxAutoPauseUnwind caps how many leading commit subjects we inspect when
// undoing pause auto-commits. A run longer than this would need that many paused
// reboots without an intervening real commit — far beyond anything realistic —
// and is safely left partially coalesced rather than read of unbounded history.
const maxAutoPauseUnwind = 64

// unwindAutoPauseCommits soft-resets past every consecutive leading auto-commit
// pause made, landing on the first real ancestor so the worktree returns exactly
// as it was left (changes re-staged, no history artifact). Walking the whole run
// — not just HEAD~1 — also coalesces legacy stacks from multiple reboots. It is a
// no-op when HEAD is not an auto-commit, so a genuine user commit is never reset.
// Returns how many commits were actually unwound (0 when nothing was reset) so the
// caller can walk the cached commit count back down by the same amount.
func (i *Instance) unwindAutoPauseCommits(wt *git.Worktree) (int, error) {
	subjects, err := wt.CommitSubjects(maxAutoPauseUnwind)
	if err != nil {
		return 0, err
	}
	n := 0
	for n < len(subjects) && isAutoPauseCommit(subjects[n]) {
		n++
	}
	// n == len(subjects) means the whole inspected run is auto-commits with no real
	// ancestor in view (history shorter than the cap → down to the root, or a run
	// longer than the cap). Either way there's nothing safe to land on, so leave
	// history untouched rather than soft-reset below the first commit.
	if n == 0 || n == len(subjects) {
		return 0, nil
	}
	if err := wt.ResetSoft(fmt.Sprintf("HEAD~%d", n)); err != nil {
		return 0, err
	}
	return n, nil
}
