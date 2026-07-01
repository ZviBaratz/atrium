package session

import (
	"fmt"
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

// Paused reports whether the instance is paused (worktree removed, branch
// preserved).
func (i *Instance) Paused() bool {
	return i.GetStatus() == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	ts := i.tmux()
	return ts != nil && ts.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch.
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
func (i *Instance) pause() error {
	if !i.isStarted() {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Paused() {
		return fmt.Errorf("instance is already paused")
	}

	ts := i.tmux()
	wt := i.worktree()

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

	// Check if there are any changes to commit
	if dirty, err := wt.IsDirty(); err != nil {
		tc.Record("check if worktree is dirty", err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub)
		commitMsg := fmt.Sprintf("%s'%s' on %s %s", autoPauseCommitPrefix, i.Title, time.Now().Format(time.RFC822), autoPauseCommitSuffix)
		// Return early if we can't commit changes to avoid corrupted state
		if tc.Record("commit changes", wt.CommitChanges(commitMsg)) {
			return tc.Err()
		}
	}

	// Detach from tmux session instead of closing to preserve session output.
	// Continue with the pause process even if detach fails.
	tc.Record("detach tmux session", ts.DetachSafely())

	// Check if worktree exists before trying to remove it
	if _, err := os.Stat(wt.GetWorktreePath()); err == nil {
		// Remove worktree but keep branch
		if tc.Record("remove git worktree", wt.Remove()) {
			return tc.Err()
		}

		// Prune stale metadata even if this fails — the worktree directory is
		// gone after Remove(), so the session must be marked Paused regardless.
		tc.Record("prune git worktrees", wt.Prune())
	}

	// Pause committed any uncommitted work above and removed the worktree, so the
	// session now has nothing uncommitted. The metadata poll loop skips paused
	// instances, so clear the cached dirty flag here or it would stay stale until
	// the next Resume — surfacing a false "(has uncommitted changes)" in the kill
	// dialog and a stale pencil glyph in the list.
	i.clearCachedDirty()
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
		return nil
	}

	// Check if branch is checked out elsewhere (base repo or a sibling worktree).
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
	if err := i.unwindAutoPauseCommits(wt); err != nil {
		log.ErrorLog.Print(err)
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
			if err := i.recreateSession(); err != nil {
				return err
			}
		}
	} else {
		// The tmux session is gone, so the agent process died with it; recreate
		// it, resuming the prior conversation rather than starting blank.
		if err := i.recreateSession(); err != nil {
			return err
		}
	}

	i.SetStatus(Running)
	// As above: the resumed agent's post-boot idle is not a genuine completion.
	i.ArmReadySuppression()
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
func (i *Instance) unwindAutoPauseCommits(wt *git.Worktree) error {
	subjects, err := wt.CommitSubjects(maxAutoPauseUnwind)
	if err != nil {
		return err
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
		return nil
	}
	return wt.ResetSoft(fmt.Sprintf("HEAD~%d", n))
}
