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
)

// Pause/resume lifecycle: parking a session (commit dirty work, detach tmux,
// remove the worktree, keep the branch) and bringing it back, plus the
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

// Pause stops the tmux session and removes the worktree, preserving the branch —
// with one deliberate exception: a pause whose auto-commit of dirty work fails keeps
// the worktree, so the WIP it could not commit is left on disk to be rescued (see
// pause). Callers that act on the removal — the terminal-shell reap in particular —
// must therefore ask WorkingDirGone rather than assume it from a nil error.
//
// A direct (non-git) session has no worktree to free and runs in the user's real
// directory, so "pausing" it would only detach a still-running agent while the UI
// claims it is parked — misleading. Pause therefore refuses a direct session. (A
// direct session whose pane actually dies is still parked via RecoverLostSession.)
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
// Two of pause's branches make that last clause conditional. One is reachable only
// from here: it does not refuse a direct session the way Pause does, and a direct
// session has no worktree at all — that branch detaches the pane and stops the run
// command without committing or removing anything, in what is the user's own
// checkout. The other, the failed-WIP-commit branch that keeps the worktree on
// purpose, it shares with Pause. Hence WorkingDirGone for anything that acts on the
// removal.
func (i *Instance) RecoverLostSession() error {
	return i.pause()
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

// pause stops the tmux session and removes the worktree, preserving the branch.
//
// "Removes the worktree" holds for every branch below except two: the direct-session
// branch, which has no worktree at all, and the failed-WIP-commit branch, which keeps
// one on purpose. The returned error does not distinguish them — that WIP branch
// errors while keeping the worktree, and the orphaned-worktree branch frees the
// directory while returning a tc.Err() that is nil whenever its own steps succeeded.
// A caller that needs the outcome must measure it — WorkingDirGone — rather than read
// it off err or off Paused().
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

	// The managed port (#389) survives a pause whenever the pane does — which is every
	// path below, since all of them detach rather than close. tmux freezes
	// $ATRIUM_PORT at `new-session -e` and a resume re-attaches (Session.Restore is
	// attach-session and nothing more), so the shell the user types in keeps the number
	// it was born with. Releasing it here would let the next session created be handed
	// that number while the parked pane still exports it — the collision the port
	// exists to prevent, arrived at by way of freeing it.
	//
	// Deferred, so it covers the orphaned-worktree and direct branches below, which
	// return early and park a session just as thoroughly.
	defer i.releasePortIfPaneGone(ts)

	// The run command does NOT survive, and for the opposite reason to the port: the
	// worktree below is about to be removed, and that worktree is the command's working
	// directory — a dev server left running there is running in a deleted tree (#389).
	// The wanted flag is kept, so Resume restarts it, on the port this pause just kept.
	// Before the removal, not deferred: the server should be gone by the time its
	// directory is.
	i.pauseRunCommand()

	// Direct session: no worktree to commit/remove. User-initiated Pause is refused
	// for direct sessions (see Pause), so this branch is only reached via
	// RecoverLostSession when the pane has died — park it so the poll loop stops and
	// the user can Resume, without ever touching the user's real directory.
	if wt == nil {
		if err := ts.DetachSafely(); err != nil {
			log.ErrorLog.Print(err)
			i.SetStatus(Paused)
			return fmt.Errorf("failed to detach tmux session: %w", err)
		}
		i.SetStatus(Paused)
		return nil
	}

	var tc teardown.Errors

	// If the worktree is orphaned (path or .git missing), git cannot operate
	// on it. Skip dirty check and Remove, prune any lingering metadata, then
	// transition to Paused so the user can recover via Resume.
	if valid, err := wt.IsValidWorktree(); err != nil {
		tc.Record("validate worktree", err)
	} else if !valid {
		log.WarningLog.Printf("worktree at %s is orphaned; skipping dirty check and remove",
			wt.GetWorktreePath())
		tc.Record("detach tmux session", ts.DetachSafely())
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

	// Detach from tmux session instead of closing to preserve session output.
	// Continue with the pause process even if detach fails.
	tc.Record("detach tmux session", ts.DetachSafely())

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

	// Direct session: no worktree to recreate. Reattach to the still-running tmux
	// session (or recreate it in the real directory if it died).
	if wt == nil {
		if ts.DoesSessionExist() {
			if err := ts.Restore(); err != nil {
				log.ErrorLog.Print(err)
				if closeErr := ts.Close(); closeErr != nil {
					log.ErrorLog.Printf("failed to close stale session %s: %v", i.Title, closeErr)
				}
				if err := i.recreateSession(false); err != nil {
					return err
				}
			}
		} else if err := i.recreateSession(false); err != nil {
			return err
		}
		i.SetStatus(Running)
		// The resumed agent boots back into its old conversation — the first
		// poll's settle to Ready is not new output, so don't flag unread.
		i.ArmReadySuppression()
		i.resumeRunCommand()
		return nil
	}

	// If our own worktree is still materialized on disk, something parked this session
	// without removing it. Reuse it as-is: running Setup would clearStaleWorktree and
	// re-add from the branch, discarding any uncommitted work — and BranchCheckoutPath
	// would see our own worktree as a foreign checkout and refuse the resume outright.
	// A normal pause removes the worktree, so a valid one here means a park that left it
	// on disk. Several do — a pause whose WIP commit failed (see pause), a startup
	// recovery whose relaunch failed after the worktree validated (recoverInPlace), one
	// the host session budget deferred (parkOverBudget) — so the test is the property,
	// not a list: none of them ran pause's auto-commit, so a materialized worktree here
	// never has one to unwind. Otherwise materialize it fresh, first guarding against
	// the branch being checked out elsewhere (base repo or a sibling worktree).
	valid, err := wt.IsValidWorktree()
	if err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to validate worktree: %w", err)
	}
	// Whether this call is the one that put the worktree on disk. It decides whether a
	// failed launch below may tear it down: rolling back our own Setup is safe, while
	// tearing down a worktree we merely found would destroy work we did not create and
	// delete the branch holding it (see recreateSession).
	materializedHere := !valid
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
		// re-running `npm ci` for it would be a cost with nothing to buy.
		i.RunSetupScript(wt.GetWorktreePath())
	}

	// Check if tmux session still exists from pause, otherwise create new one
	if ts.DoesSessionExist() {
		// Session exists, just restore the PTY connection to it.
		if err := ts.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// Restore failed — the stale session must be killed before we can
			// recreate it (Start guards against duplicate session names).
			if closeErr := ts.Close(); closeErr != nil {
				log.ErrorLog.Printf("failed to close stale session %s: %v", i.Title, closeErr)
			}
			if err := i.recreateSession(materializedHere); err != nil {
				return err
			}
		}
	} else {
		// The tmux session is gone, so the agent process died with it; recreate
		// it, resuming the prior conversation rather than starting blank. This is the
		// path every budget-parked session takes, materializedHere false.
		if err := i.recreateSession(materializedHere); err != nil {
			return err
		}
	}

	i.SetStatus(Running)
	// As above: the resumed agent's post-boot idle is not a genuine completion.
	i.ArmReadySuppression()

	// The worktree is back and the agent is launched, so the run command the pause
	// stopped can have its directory again (#389). It restarts on the port the pause
	// kept, so nothing renumbers underneath a browser tab.
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
