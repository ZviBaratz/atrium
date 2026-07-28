package app

import (
	"encoding/json"

	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// Making a kill reversible.
//
// journalKill runs inside the teardown, just before the session stops existing:
// it records everything a restore would need, then makes the branch's commits
// survive `branch -D` by pinning them under a ref. Both kill paths call it, which
// matters because visual-mode `x` — a batch kill of everything marked — is the
// most destructive action in the app and the one an undo is most wanted for.
//
// Nothing here may block the kill. The user asked for a session to go away; if
// the journal cannot be written, or the repository has moved out from under us,
// the teardown proceeds and the caller simply does not advertise an undo that
// would not work.

// journalKill records inst so it can be restored, and returns whether an undo is
// actually on offer.
//
// The entry is written before the ref exists, not after. A crash in between
// leaves a record naming a ref that was never created, which the restore detects
// and the sweep cleans up; the other order would leave a ref with nothing left in
// the world that names it — a leak of the user's objects with no way to find or
// expire them.
//
// batchID is empty for a single kill and shared across one visual-mode batch.
func (m *home) journalKill(inst *session.Instance, batchID string) (undo.Entry, bool) {
	// An unstarted or still-Loading session has nothing to come back to: it has no
	// branch yet, SaveInstances filters it out of state.json, and its Start
	// goroutine may still be writing the worktree we would be snapshotting. A
	// journal entry for one would be a row that cannot be restored. This is the
	// drift-proof place for the check — confirmKill and killMarked each refuse
	// Loading on their own, and killInstances does not.
	if !inst.Started() || inst.GetStatus() == session.Loading {
		return undo.Entry{}, false
	}

	snapshot, err := json.Marshal(inst.ToInstanceData())
	if err != nil {
		log.WarningLog.Printf("undo %s: cannot snapshot session, kill will not be undoable: %v", inst.Title, err)
		return undo.Entry{}, false
	}

	entry, err := undo.Write(undo.Entry{
		BatchID:  batchID,
		Title:    inst.Title,
		Display:  inst.DisplayName(),
		Path:     inst.Path,
		Direct:   inst.IsDirect(),
		TmuxName: inst.TmuxSessionName(),
		Snapshot: snapshot,
	})
	if err != nil {
		log.WarningLog.Printf("undo %s: cannot write journal entry, kill will not be undoable: %v", inst.Title, err)
		return undo.Entry{}, false
	}

	// A direct session runs in the user's own directory: there is no worktree to
	// remove and no branch to delete, so the snapshot alone is the whole record.
	if entry.Direct {
		return entry, true
	}

	wt, err := inst.GetGitWorktree()
	if err != nil {
		log.WarningLog.Printf("undo %s: cannot resolve worktree, kill will not be undoable: %v", inst.Title, err)
		return entry, false
	}
	entry.RepoPath = wt.GetRepoPath()
	entry.Branch = wt.GetBranchName()
	entry.ExistingBranch = wt.IsExistingBranch()

	captured, err := inst.PrepareUndo(entry.Ref)
	if err != nil {
		// The commits could not be pinned — a repository the user moved or deleted
		// is the usual cause. Record what we know and rewrite the entry without a
		// SHA, which is what marks it unrestorable, then let the kill proceed.
		log.WarningLog.Printf("undo %s: cannot retain branch, kill will not be undoable: %v", inst.Title, err)
		if written, werr := undo.Write(entry); werr == nil {
			entry = written
		}
		return entry, false
	}
	entry.SHA = captured.SHA
	entry.Committed = captured.Committed

	written, err := undo.Write(entry)
	if err != nil {
		// The ref exists but the record naming it does not carry the SHA. The
		// restore refuses rather than guessing, and the sweep still expires the
		// entry, so the objects are not stranded forever.
		log.WarningLog.Printf("undo %s: cannot record retained branch: %v", inst.Title, err)
		return entry, false
	}
	return written, true
}
