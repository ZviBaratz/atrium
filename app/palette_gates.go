package app

import (
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
)

// Which palette rows can act on the current selection (#374).
//
// The hint bar cannot answer this. hintsFor picks a hand-curated key list per
// session *mode* (paused / needs-input / mergeable / dirty), not a verdict per
// action, and every other precondition in the app is inline in its own handler.
// So this table is new, and the risk it must not incur is becoming a second copy
// of the truth that drifts from the handlers.
//
// Three rules keep it honest:
//
//  1. The handler stays authoritative. A gate never lets an action run that the
//     handler would refuse; it only pre-empts the refusal on screen.
//  2. A gate dims only when it is CERTAIN. Anything unknown — a PR status not
//     fetched yet — reports runnable, and the handler's own notice explains the
//     refusal if one comes. A wrong dim is a lie; a missing dim is just the
//     status quo.
//  3. TestPaletteGatesAgreeWithDispatch drives every action against every
//     scenario and fails if a gate blocks something dispatch would happily do,
//     or if a blocked action refuses without saying anything.
//
// Rule 3 is why this can exist at all. Without it the table is a second opinion;
// with it, it is a checked one.
//
// On the reasons: they are chip-sized, because a palette row's tail column is
// ~27 columns at 80. The handlers' full sentences stay the toast. That split
// mirrors the settings panel's inertReasons (short chip) beside its expanded help
// (prose), which is the precedent this whole table copies — activeWhen +
// inertReason + a mandatory reason + FaintStyle, "dimmed, not hidden".
//
// Why the palette must speak at all, when app_feedback.go's rule says "a refusal
// the user can SEE stays silent": from the palette, no refusal is visible. The
// user picked a row from a list and the overlay closed. Every refusal on this
// path is invisible by construction, so it is the half of the rule that must
// explain itself.
const (
	noSelectionReason    = "no session selected"
	stillStartingReason  = "still starting"
	pausedReason         = "paused"
	alreadyPausedReason  = "already paused"
	notPausedReason      = "not paused"
	directReason         = "not a git session"
	noBranchReason       = "no branch yet"
	noQueueReason        = "nothing queued"
	noPRReason           = "no PR yet"
	noRunCommandReason   = "no run_command for this repo"
	deadPaneReason       = "terminal has exited"
	pausedWorktreeReason = "worktree freed — resume first"
	notClaudeReason      = "not a claude session"
	// The two below are used by the custom-commands menu rather than the palette, but
	// they live here so every chip-sized refusal in the app is written in one place
	// and in one voice.
	noWorktreeReason  = "no worktree yet"
	noDirectoryReason = "no directory to run in"
)

// paletteGate says whether an action can run against the current selection. A
// zero gate (no blocked func) is an action that never depends on the selection —
// stated explicitly, so that every registered action is classified rather than
// defaulting into "always fine" by omission.
type paletteGate struct {
	blocked func(inst *session.Instance) string
}

// global classifies an action with no selection-dependent precondition.
func global() paletteGate { return paletteGate{} }

// needsSelection gates only on there being something selected — the floor for
// any action that reads the selection at all.
func needsSelection(extra func(inst *session.Instance) string) paletteGate {
	return paletteGate{blocked: func(inst *session.Instance) string {
		if inst == nil {
			return noSelectionReason
		}
		if extra == nil {
			return ""
		}
		return extra(inst)
	}}
}

// perSession adds the still-starting guard, for the actions whose handlers go
// through selectedActionable (or inline the same check).
//
// Which wrapper an action gets is not a judgement call — it is a reading of the
// handler. Gating on Loading where the handler does not is a FALSE DIM: mute is
// deliberately built on the raw selection ("a lightweight state toggle with no
// readiness dependency, so it works even on a still-starting session", says
// toggleMuteSelected), and gating it here would have made the palette refuse
// something the key does. TestPaletteGatesAgreeWithDispatch caught exactly that.
func perSession(extra func(inst *session.Instance) string) paletteGate {
	return needsSelection(func(inst *session.Instance) string {
		if inst.GetStatus() == session.Loading {
			return stillStartingReason
		}
		if extra == nil {
			return ""
		}
		return extra(inst)
	})
}

// paletteGates classifies every dispatchable action. Membership is mandatory and
// checked in both directions (TestEveryPaletteActionHasAGate), so a new action
// forces a decision here instead of silently arriving un-gated.
var paletteGates = map[keys.KeyName]paletteGate{
	// Navigation, view and global surfaces: nothing about the selection can stop
	// them, including on an empty list.
	keys.KeyUp:             global(),
	keys.KeyDown:           global(),
	keys.KeyShiftUp:        global(),
	keys.KeyShiftDown:      global(),
	keys.KeyNextUnread:     global(),
	keys.KeyNextNeedsInput: global(),
	keys.KeyTab:            global(),
	keys.KeyShiftTab:       global(),
	keys.KeyTabPreview:     global(),
	keys.KeyTabDiff:        global(),
	keys.KeyTabTerminal:    global(),
	keys.KeyShrinkList:     global(),
	keys.KeyGrowList:       global(),
	keys.KeyLayoutPreset:   global(),
	keys.KeyCollapse:       global(),
	keys.KeyExpand:         global(),
	keys.KeyCollapseAll:    global(),
	keys.KeyFilter:         global(),
	keys.KeyMultiSelect:    global(),
	keys.KeyNew:            global(),
	keys.KeyPrompt:         global(),
	keys.KeySmartDispatch:  global(),
	keys.KeyHelp:           global(),
	keys.KeySettings:       global(),
	keys.KeyAccounts:       global(),
	keys.KeyCmdLog:         global(),
	keys.KeyCommandPalette: global(),
	// The leader opens with or without a selection: its rows carry their own
	// per-command reasons, and an empty list still has a pointer at the docs to
	// show. This must stay global() — TestPaletteGatesAgreeWithDispatch skips rows
	// with no reason, so a gate that dimmed the leader would have that test dispatch
	// it and then fail on the state openCustomCommands sets unconditionally.
	keys.KeyCustomCommands: global(),
	keys.KeyQuit:           global(),

	// Batch and reorder actions act on the view, not the selection. Their own
	// refusals (a filter-hidden neighbour, a sort that disables the ladder) are
	// explained by the handlers and are not selection-derived, so they are not
	// pre-empted here.
	keys.KeyPauseAll:  global(),
	keys.KeyResumeAll: global(),
	// Undo reads the journal, not the list: its target is a session that no longer
	// exists, so nothing about the current selection — including there being none —
	// can stop it. undoLastKill explains the empty case itself.
	keys.KeyUndoKill:        global(),
	keys.KeyMoveUp:          global(),
	keys.KeyMoveDown:        global(),
	keys.KeyMoveGroupUp:     global(),
	keys.KeyMoveGroupDown:   global(),
	keys.KeyMoveAccountUp:   global(),
	keys.KeyMoveAccountDown: global(),

	// Handlers that go through selectedActionable, so still-starting is theirs too.
	keys.KeyRename:   perSession(nil),
	keys.KeyAutoName: perSession(nil),
	keys.KeyKill:     perSession(nil),

	// Handlers built on the raw selection: they work while a session is still
	// starting, so gating them on Loading would refuse what the key allows.
	keys.KeyMute:        needsSelection(nil),
	keys.KeyApprove:     needsSelection(nil),
	keys.KeyHints:       needsSelection(nil),
	keys.KeyDiffComment: needsSelection(nil),

	keys.KeyEnter:        needsSelection(attachBlocked),
	keys.KeyAttachToggle: needsSelection(attachBlocked),

	keys.KeyQuickSend: perSession(func(inst *session.Instance) string {
		if inst.Paused() {
			return pausedReason
		}
		return ""
	}),

	keys.KeyPause: perSession(func(inst *session.Instance) string {
		if inst.IsDirect() {
			return directReason
		}
		if inst.Paused() {
			return alreadyPausedReason
		}
		return ""
	}),

	keys.KeyResume: perSession(func(inst *session.Instance) string {
		if !inst.Paused() {
			return notPausedReason
		}
		return ""
	}),

	// The dev command (#389). Stopping one is always available while it runs, so the
	// configured check is only asked of the start direction — and it is
	// RunCommandUnavailable rather than !RunConfigured, so a session the poll has not
	// yet looked at is offered rather than dimmed. toggleRunCommand refuses on exactly
	// this predicate, which is what keeps this row's claim and the key in step.
	keys.KeyRunCommand: perSession(func(inst *session.Instance) string {
		if inst.RunLive() {
			return ""
		}
		// A direct session runs in the user's own checkout, so it never hosts one — the
		// same refusal setup_script makes, and checked before the config, since it holds
		// however the repo is configured.
		if inst.IsDirect() {
			return directReason
		}
		if inst.Paused() {
			return pausedWorktreeReason
		}
		if inst.RunCommandUnavailable() {
			return noRunCommandReason
		}
		return ""
	}),

	// Push, like create-PR, runs git from the worktree a pause has freed; merge and
	// open-PR run gh from the repo root and survive one. That split is the reason
	// these four are not gated alike.
	keys.KeySubmit: perSession(func(inst *session.Instance) string {
		if inst.IsDirect() {
			return directReason
		}
		if inst.Paused() {
			return pausedWorktreeReason
		}
		return ""
	}),

	keys.KeyCreate: perSession(func(inst *session.Instance) string {
		if inst.IsDirect() {
			return directReason
		}
		if inst.Paused() {
			return pausedWorktreeReason
		}
		// Nil until the first poll fetches it: unknown is not blocked.
		if pr := inst.GetPRStatus(); pr != nil {
			return pr.CreateBlockedReason()
		}
		return ""
	}),

	keys.KeyMerge: perSession(func(inst *session.Instance) string {
		if inst.IsDirect() {
			return directReason
		}
		if pr := inst.GetPRStatus(); pr != nil {
			return pr.MergeBlockedReason()
		}
		return ""
	}),

	keys.KeyOpenPR: perSession(func(inst *session.Instance) string {
		if inst.IsDirect() {
			return directReason
		}
		if pr := inst.GetPRStatus(); pr != nil && !pr.HasPR {
			return noPRReason
		}
		return ""
	}),

	keys.KeyQueue: needsSelection(func(inst *session.Instance) string {
		if !inst.HasQueuedPrompt() {
			return noQueueReason
		}
		return ""
	}),

	// Deliberately needsSelection rather than perSession, even though the handler
	// does guard on Loading: perSession would report the still-starting reason
	// first, and for a non-claude session that is the wrong half of the truth —
	// openCheckpoints answers "different agent" whatever the status. The order here
	// is the handler's order.
	keys.KeyCheckpoints: needsSelection(func(inst *session.Instance) string {
		if !inst.SupportsCheckpoints() {
			return notClaudeReason
		}
		if inst.GetStatus() == session.Loading {
			return stillStartingReason
		}
		return ""
	}),

	keys.KeyCopyBranch: needsSelection(func(inst *session.Instance) string {
		if inst.Branch() == "" {
			return noBranchReason
		}
		return ""
	}),

	// Copying the active tab reads the PANE, not the session. The diff pane can
	// hold a frozen comment-mode selection that outlives the selection moving, and
	// the action declines by name ("nothing to copy from this tab yet") when there
	// is genuinely nothing — so gating it on a live selection would dim a key that
	// works, to prevent a case that already explains itself.
	keys.KeyCopyContent: global(),
}

// attachBlocked is shared by enter and the attach toggle, which are the same
// action reached two ways — a separate copy could drift into disagreeing about
// the same key.
func attachBlocked(inst *session.Instance) string {
	if inst.Paused() {
		return pausedReason
	}
	if !inst.TmuxAlive() {
		return deadPaneReason
	}
	return ""
}

// paletteInertReason reports why the named action cannot run against the current
// selection, or "" when it can.
func (m *home) paletteInertReason(name keys.KeyName) string {
	gate, ok := paletteGates[name]
	if !ok || gate.blocked == nil {
		return ""
	}
	return gate.blocked(m.list.GetSelectedInstance())
}
