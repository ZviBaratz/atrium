package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
)

// Bringing a killed session back.
//
// The restore itself is Resume's: recreate the branch from the retention ref, and
// the existing pause/resume machinery does the rest — `worktree add` from the
// branch, a soft reset that unwinds the auto-commit, and an agent relaunched with
// its resume flag. What is new here is deciding whether it is safe.
//
// It usually is not safe for one reason: kill frees a name, so nothing stops the
// user recreating the same session minutes later. That single cause produces three
// different collisions, and one of them is silent — see restoreBlocker.

// undoDoneMsg carries a finished restore back to the Update loop, which is where
// the list and storage may be touched. instances are the sessions that came back,
// in the order they were killed.
type undoDoneMsg struct {
	instances []*session.Instance
	entries   []undo.Entry
	failures  []undoFailure
	// refusedIDs are the records that could not be restored, so the next press can
	// step past them instead of re-offering the same refusal forever.
	refusedIDs []string
}

type undoFailure struct {
	title  string
	reason string
}

// undoLastKill offers to bring back the most recent kill — the whole batch when
// the last thing the user did was a visual-mode kill, because that was one action
// and undo should reverse one action.
func (m *home) undoLastKill() tea.Cmd {
	group, ok := undo.LatestBatch(time.Now(), m.undoRefused)
	if !ok {
		return m.handleInfoNotice("nothing to undo")
	}
	return m.confirmUndoKill(group)
}

// confirmUndoKill arms the restore.
//
// The group is captured here, when the dialog opens, and not read again when the
// user answers: the sweep runs on the poll tick, so "the newest entry" is a
// different question a second later, and a confirmation must act on the thing it
// named.
func (m *home) confirmUndoKill(group []undo.Entry) tea.Cmd {
	// Both inputs are captured here, on the UI thread, and neither is read again
	// when the user answers. The group, because the sweep runs on the poll tick and
	// "the newest entry" is a different question a second later. The live tmux names,
	// because the action runs in a goroutine and m.list belongs to Update — reading
	// the fleet from there would be the race every off-thread action in this file
	// is written to avoid.
	live := m.liveSessionNames()
	message := undoConfirmMessage(group)
	cmd := m.confirmAsyncAction(message, undoBusyLabel(len(group)), func() tea.Msg {
		return m.restoreKilled(group, live)
	})
	m.confirmationOverlay.SetConfirmLabel(undoConfirmLabel(len(group)))
	return cmd
}

// restoreKilled runs off the UI thread: it recreates each branch and resumes each
// session. It touches nothing on the model — live is the fleet's tmux names as they
// were when the dialog opened, and the Update loop inserts the rows.
func (m *home) restoreKilled(group []undo.Entry, live map[string]struct{}) undoDoneMsg {
	var res undoDoneMsg
	branchPrefix := config.LoadConfig().BranchPrefix

	for _, e := range group {
		if reason := m.restoreBlocker(e, live); reason != "" {
			res.failures = append(res.failures, undoFailure{e.Display, reason})
			res.refusedIDs = append(res.refusedIDs, e.ID)
			continue
		}
		inst, err := m.restoreOne(e, branchPrefix)
		if err != nil {
			res.failures = append(res.failures, undoFailure{e.Display, err.Error()})
			res.refusedIDs = append(res.refusedIDs, e.ID)
			continue
		}
		// Reserve the restored session's tmux name for the rest of this batch, so
		// two entries that somehow share one cannot both claim it.
		live[inst.TmuxSessionName()] = struct{}{}
		res.instances = append(res.instances, inst)
		res.entries = append(res.entries, e)
	}
	return res
}

// restoreOne recreates one session's branch and brings the session back.
func (m *home) restoreOne(e undo.Entry, branchPrefix string) (*session.Instance, error) {
	var data session.InstanceData
	if err := json.Unmarshal(e.Snapshot, &data); err != nil {
		return nil, fmt.Errorf("its record could not be read: %w", err)
	}

	if !e.Direct {
		// Recreate the branch only when it is actually gone. A session flagged as
		// sitting on a pre-existing branch never lost it, and restoreBlocker has
		// already refused the case where such a branch has moved on.
		if _, exists := git.BranchTip(m.ctx, e.RepoPath, e.Branch); !exists {
			if err := git.CreateBranchAt(m.ctx, e.RepoPath, e.Branch, e.Ref); err != nil {
				return nil, fmt.Errorf("its branch could not be recreated: %w", err)
			}
		}
	}

	inst, err := session.RestoreFromData(m.ctx, data, branchPrefix)
	if err != nil {
		return nil, fmt.Errorf("its session could not be rebuilt: %w", err)
	}
	if err := inst.Resume(); err != nil {
		return nil, err
	}
	return inst, nil
}

// restoreBlocker reports why e cannot be restored right now, or "" when it can.
//
// Every refusal names the retention ref. The commits are still there — this is a
// "not automatically", not a "gone" — and one `git branch <name> <ref>` recovers
// them by hand. A refusal that withheld that would fail silently rather than
// closed.
func (m *home) restoreBlocker(e undo.Entry, live map[string]struct{}) string {
	// The sharp one. TmuxName is persisted and repo-qualified, and Resume branches
	// on whether that tmux session exists: if the name is live again it calls
	// Restore() and binds the rebuilt instance to *another session's pane*. Two
	// instances then drive one tmux session and one hook directory, and killing
	// either destroys the other's agent. Nothing about that is visible until it is.
	if e.TmuxName != "" {
		if _, taken := live[e.TmuxName]; taken {
			return fmt.Sprintf("a session named '%s' is running again%s", e.Title, recoverByHand(e))
		}
	}

	if e.Direct {
		return ""
	}

	if _, ok := git.RefExists(m.ctx, e.RepoPath, e.Ref); !ok {
		if !git.IsGitRepo(m.ctx, e.RepoPath) {
			return fmt.Sprintf("its repository is no longer at %s", e.RepoPath)
		}
		return "the retained commits are gone"
	}

	if tip, exists := git.BranchTip(m.ctx, e.RepoPath, e.Branch); exists {
		if tip != e.SHA {
			// The branch is back but somewhere else. `worktree add` would materialize
			// today's tip and the soft reset would find no auto-commit to unwind, so
			// the "restored" tree would quietly not be the one that was killed.
			return fmt.Sprintf("branch '%s' has moved on since%s", e.Branch, recoverByHand(e))
		}
		// Same tip: this is the pre-existing-branch case kill never deleted. Nothing
		// to recreate, and the worktree rebuild below is all that is needed.
		return ""
	}
	return ""
}

// liveSessionNames is the set of tmux names the running fleet owns — including
// the ones it has not taken yet.
//
// `TmuxSessionName()` is minted inside Start, so a session between creation and
// the end of its Start goroutine reports "". That is precisely the window a
// restore must not walk into, so reading the field alone would make the fleet
// invisible exactly when it matters. The name is derived the same way Start
// derives it instead.
//
// This is not covered by supersedeUndoFor, which matches the (Title, Path) pair:
// two clones of one repository share a group key, so the same title in
// ~/a/atrium and ~/b/atrium produces the same tmux name from different paths.
// Deriving here closes both holes at once.
func (m *home) liveSessionNames() map[string]struct{} {
	live := make(map[string]struct{})
	for _, inst := range m.list.GetInstances() {
		name := inst.TmuxSessionName()
		if name == "" {
			name = tmux.QualifiedSessionName(inst.GroupKey(), inst.Title)
		}
		if name != "" {
			live[name] = struct{}{}
		}
	}
	return live
}

// handleUndoDone lands a finished restore on the Update loop: rows in, state
// persisted, consumed records dropped, and an honest report of what came back.
func (m *home) handleUndoDone(msg undoDoneMsg) tea.Cmd {
	for _, inst := range msg.instances {
		finalize := m.list.AddInstance(inst)
		finalize()
		m.list.SelectInstance(inst)
	}
	if len(msg.instances) > 0 {
		if err := m.persistInstances(); err != nil {
			// The records stay, so the restore is still on offer. Rows and branches
			// exist but nothing is on disk, and leaving the journal intact is what
			// keeps that recoverable rather than merely reported.
			return m.handleError(err)
		}
		// Only once the restore is durable is a record spent — and it is spent
		// whole: entry and retained ref together, or the ref is stranded with
		// nothing left that names it.
		for _, e := range msg.entries {
			m.retireUndoRecord(e)
		}
	}

	if len(msg.failures) > 0 {
		// Step past what was refused. A refusal is not a state the journal can see:
		// an entry whose repository has been deleted is refused every time and
		// removed never, so without this the newest record wedges the stack and
		// hides every older one until the horizon passes. In memory only — a
		// relaunch re-offers them, because by then the world may have changed.
		if m.undoRefused == nil {
			m.undoRefused = map[string]bool{}
		}
		for _, id := range msg.refusedIDs {
			m.undoRefused[id] = true
		}
		return tea.Batch(m.instanceChanged(), m.showInfo(undoFailureReport(msg)))
	}
	return tea.Batch(m.instanceChanged(),
		m.flashNotice(restoredNotice(msg.instances, msg.entries), ui.NoticeInfo))
}

// --- copy ---------------------------------------------------------------------
//
// Every string below names the undo key through the registry rather than spelling
// it, so a rebinding can never leave the prose lying.

// undoKeyLabel is the undo key as the user sees it on the cheatsheet.
func undoKeyLabel() string {
	return keys.GlobalKeyBindings[keys.KeyUndoKill].Help().Key
}

// killedNotice acknowledges a kill and, when there is something to come back to,
// says so. The advert is the whole point: a recovery nobody can see is not a
// recovery — the mistake superfile shipped with its invisible trash.
func killedNotice(display string, undoable bool) string {
	if !undoable {
		return fmt.Sprintf("killed '%s'", display)
	}
	return fmt.Sprintf("killed '%s' · %s to undo", display, undoKeyLabel())
}

// batchKilledNotice is killedNotice for a batch. It advertises undo only when
// every session in the run was recorded: "U to undo" after a partial record would
// promise back more than comes back.
func batchKilledNotice(killed, undoable int) string {
	base := fmt.Sprintf("killed %d session%s", killed, plural(killed))
	if killed > 0 && undoable == killed {
		return fmt.Sprintf("%s · %s to undo", base, undoKeyLabel())
	}
	return base
}

// undoConfirmMessage asks with the verb, then names what the user cannot see.
//
// What it deliberately does not promise is the conversation. Resume asks the agent
// adapter whether a transcript is resumable and quietly starts fresh when it is
// not, so promising it here would be a coin flip printed as a fact; the notice
// after the restore reports what actually happened instead.
func undoConfirmMessage(group []undo.Entry) string {
	var subject string
	if len(group) == 1 {
		subject = fmt.Sprintf("Restore session '%s'?", group[0].Display)
	} else {
		subject = fmt.Sprintf("Restore %d killed session%s?", len(group), plural(len(group)))
	}
	return subject + " (rebuilds the branch and worktree; the terminal scrollback" +
		" and any gitignored files the agent left behind are gone for good)"
}

func undoConfirmLabel(n int) string {
	if n == 1 {
		return "restore"
	}
	return fmt.Sprintf("restore %d session%s", n, plural(n))
}

func undoBusyLabel(n int) string {
	if n == 1 {
		return "restoring…"
	}
	return fmt.Sprintf("restoring %d session%s…", n, plural(n))
}

// restoredNotice reports what came back — and, when the teardown could not fold
// the working tree into the retained commits, that it did not.
//
// Entry.Committed is false when the dirty check or the commit itself failed at
// kill time, which are the two ways `git worktree remove -f` destroys uncommitted
// work despite everything else here. Saying nothing would report that restore
// exactly like a whole one.
func restoredNotice(instances []*session.Instance, entries []undo.Entry) string {
	lost := 0
	for _, e := range entries {
		if !e.Direct && !e.Committed && e.Dirty {
			lost++
		}
	}
	var base string
	if len(instances) == 1 {
		base = fmt.Sprintf("restored '%s'", instances[0].DisplayName())
	} else {
		base = fmt.Sprintf("restored %d session%s", len(instances), plural(len(instances)))
	}
	if lost > 0 {
		return base + " — uncommitted changes could not be saved and are gone"
	}
	return base
}

// undoFailureReport is the modal a partial (or wholly refused) restore earns. A
// refusal the user cannot read is a refusal they will hit again.
func undoFailureReport(msg undoDoneMsg) string {
	var b strings.Builder
	total := len(msg.instances) + len(msg.failures)
	if len(msg.instances) == 0 {
		fmt.Fprintf(&b, "Could not restore %d session%s:", total, plural(total))
	} else {
		fmt.Fprintf(&b, "Restored %d of %d session%s. %d could not come back:",
			len(msg.instances), total, plural(total), len(msg.failures))
	}
	for _, f := range msg.failures {
		fmt.Fprintf(&b, "\n  • %s — %s", f.title, f.reason)
	}
	return b.String()
}

// recoverByHand is the escape hatch every refusal carries. The commits are still
// pinned under the retention ref, so a refusal means "not automatically" rather
// than "gone" — and this names the one command that recovers them anyway. A
// refusal that withheld it would fail silently instead of closed.
func recoverByHand(e undo.Entry) string {
	if e.Ref == "" || e.RepoPath == "" {
		return ""
	}
	return fmt.Sprintf(" — its commits are still retained: git -C %s branch <name> %s", e.RepoPath, e.Ref)
}
