package session

import (
	"context"
	"fmt"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// Undo seam: the two halves of making a kill reversible that have to live inside
// this package.
//
// PrepareUndo needs pause's private auto-commit markers, because Resume unwinds
// the WIP commit by matching them exactly. RestoreFromData needs the private
// `started` field, because Resume refuses anything that was never started and only
// the unexported reattach sets it. Everything else about undo — the journal, the
// refusals, the UI — lives outside.

// UndoCapture is what a teardown recorded so the session can be rebuilt.
type UndoCapture struct {
	// SHA is the branch head that was retained. Empty for a direct session, which
	// has no repository.
	SHA string
	// Dirty records that the worktree had uncommitted changes to deal with. Paired
	// with Committed it distinguishes "nothing to save" from "could not save it".
	Dirty bool
	// Committed reports that dirty work was folded into the retained commits. When
	// it is false the working tree at kill time is not in what was retained, and
	// the restore must say so rather than imply the tree comes back whole.
	Committed bool
}

// PrepareUndo makes this instance's work recoverable, then leaves the caller to
// tear it down: it commits anything uncommitted and points ref at the branch head.
//
// The order matters. A ref captured before the commit would retain a tip that
// predates the user's uncommitted edits, so undo would restore the branch and
// silently lose the work it exists to rescue.
//
// The commit reuses pause's marker verbatim (autoPauseCommitPrefix/Suffix), which
// is load-bearing rather than cosmetic: Resume unwinds it with a soft reset by
// matching those exact affixes, so a kill that invented its own marker would leave
// the WIP as a permanent commit in restored history.
//
// Two sessions get no commit. A direct session has no worktree. And a session
// sitting on a branch the user already owned keeps that branch through Cleanup
// (worktree_ops.go gates `branch -D` on the same flag), so a WIP commit written
// onto it could never be taken back — it would outlive the journal entry, survive
// expiry, and turn up in a branch the user may already have pushed. Its tip is
// still retained, so a branch that moves afterwards is still detectable.
func (i *Instance) PrepareUndo(ref string) (UndoCapture, error) {
	if i.direct {
		return UndoCapture{}, nil
	}
	wt := i.worktree()
	if wt == nil {
		return UndoCapture{}, fmt.Errorf("cannot prepare undo for %s: no worktree", i.Title())
	}

	// The dirty check is unconditional; only the commit is gated. Cleanup removes the
	// worktree with `git worktree remove -f` for every session alike — just `branch -D`
	// is skipped for a user-owned branch — so declining to commit does not spare the
	// working tree, it only means nothing preserved it. Recording Dirty either way is
	// what lets the post-restore notice tell "nothing to save" from "could not save
	// it"; gating the check too would report the second as the first.
	var capture UndoCapture
	dirty, err := wt.IsDirty()
	if err != nil {
		// Best-effort: an unreadable worktree must not stop the kill the user
		// asked for. The entry records that the tree was not preserved.
		log.WarningLog.Printf("undo %s: cannot check for uncommitted changes: %v", i.Title(), err)
	} else if dirty {
		capture.Dirty = true
		if !wt.IsExistingBranch() {
			msg := fmt.Sprintf("%s'%s' on %s %s",
				autoPauseCommitPrefix, i.Title(), time.Now().Format(time.RFC822), autoPauseCommitSuffix)
			if err := wt.CommitChanges(msg); err != nil {
				log.WarningLog.Printf("undo %s: cannot commit uncommitted changes: %v", i.Title(), err)
			} else {
				capture.Committed = true
			}
		}
	}

	sha, err := wt.RetainBranch(ref)
	if err != nil {
		return capture, err
	}
	capture.SHA = sha
	return capture, nil
}

// RestoreFromData rehydrates a killed instance into the one state Resume accepts.
//
// FromInstanceData is pure but leaves the instance neither started nor paused, and
// Resume requires both; today only the unexported reattach sets them, and it
// reattaches every session it touches. This is the same rehydration with those two
// facts applied and nothing else — no subprocess, no filesystem check — so the
// caller can decide on the UI thread whether a restore is possible and do the slow
// part (Resume: worktree add, soft reset, agent relaunch) off it.
//
// The instance keeps the stored worktree path byte for byte, which is not
// incidental: the agent's conversation is keyed by its working directory, so
// `--continue` finds it only when the worktree comes back at the identical path.
func RestoreFromData(ctx context.Context, data InstanceData, branchPrefix string) (*Instance, error) {
	data.Status = Paused
	inst, err := FromInstanceData(ctx, data, branchPrefix)
	if err != nil {
		return nil, err
	}
	inst.started = true
	return inst, nil
}
