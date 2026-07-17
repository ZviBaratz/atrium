// Package keys defines the logical key actions of the TUI and the single
// registry (Registry, registry.go) from which the dispatch and help maps are
// derived. Help is a projection of the registry, never authored beside it.
package keys

// KeyName identifies a logical key action in the TUI. The home model switches
// on KeyName rather than raw key strings, so a rebind only touches the
// registry in this package. A few names are documented-only (no dispatch):
// they exist so generated help can reference every key it documents.
type KeyName int

// The logical key actions. Their key strings and help entries live in
// Registry (registry.go); GlobalKeyStringsMap (string → action) and
// GlobalKeyBindings (action → help entry) are derived from it.
const (
	KeyUp KeyName = iota
	KeyDown
	KeyEnter
	KeyNew
	KeyKill
	KeyQuit
	KeyReview
	KeyPush
	KeySubmit
	KeyCreate // Open a pull request for the pushed branch via gh
	KeyMerge  // Squash-merge the selected session's pull request via gh
	KeyOpenPR // Open the selected session's pull request in the browser via gh

	KeyTab      // Tab is a special keybinding for switching between panes.
	KeyShiftTab // ShiftTab cycles between panes in reverse order.

	KeyPause    // Commit changes and pause the session, freeing its worktree
	KeyPauseAll // Pause every active session in the current view (batch park)
	KeyResume
	KeyResumeAll // Resume every paused session in the current view (batch restore)
	KeyPrompt    // Open the new-session form focused on the project picker
	KeyHelp      // Key for showing help screen

	// KeyShiftUp and KeyShiftDown scroll the diff/preview pane.
	KeyShiftUp
	KeyShiftDown

	// KeyNextUnread jumps the selection to the next unread Ready session;
	// KeyNextNeedsInput jumps to the next session blocked on input. Both wrap
	// around the list and cross repo-group boundaries.
	KeyNextUnread
	KeyNextNeedsInput

	// KeyMoveUp and KeyMoveDown reorder the selected session within its group.
	KeyMoveUp
	KeyMoveDown

	// KeyMoveGroupUp and KeyMoveGroupDown reorder a whole repo group.
	KeyMoveGroupUp
	KeyMoveGroupDown

	// KeyMoveAccountUp and KeyMoveAccountDown reorder a whole account cluster while
	// account-grouped — the widest step of the reorder ladder (session → repo group →
	// account cluster). Unlike the other two, the order they write is a stored
	// preference (config.State.AccountOrder), not the session order itself.
	KeyMoveAccountUp
	KeyMoveAccountDown

	// KeyCollapse and KeyExpand fold/unfold the selected repo group (tree-view
	// style); KeyCollapseAll folds/unfolds every group at once.
	KeyCollapse
	KeyExpand
	KeyCollapseAll

	KeyRename // Rename the selected session's display label

	KeyQuickSend // Open a compose box to send a message to the selected session without attaching

	KeyQueue // Open the pending-prompt management overlay for the selected session

	KeyCmdLog // Open the command-log overlay (tmux/git/gh subprocesses Atrium ran)

	KeyAutoName // Auto-generate a display name for the selected session via claude

	KeyFilter // Enter incremental filter mode to narrow the session list

	KeyCopyBranch // Copy the selected session's branch name to the clipboard

	// KeyShrinkList and KeyGrowList resize the session list relative to the
	// preview pane.
	KeyShrinkList
	KeyGrowList

	// KeyTabPreview/KeyTabDiff/KeyTabTerminal jump straight to a tab by number,
	// complementing Tab/Shift+Tab cycling.
	KeyTabPreview
	KeyTabDiff
	KeyTabTerminal

	KeySettings // Open the settings panel to view and edit the configuration

	KeyAccounts // Open the accounts panel to manage Claude/GitHub accounts

	// KeyAttachToggle mirrors the in-session detach key: on the list it attaches
	// the selected session, making ctrl+q a symmetric attach/detach toggle.
	KeyAttachToggle

	// KeyHints enters hint (fingers) mode: overlay copy/open hint labels on
	// the preview pane's visible matches (URLs, paths, SHAs, …).
	KeyHints

	// KeyApprove taps Enter at the selected session's visible prompt (tool
	// permission, plan approval) without attaching; on an idle claude session
	// it instead accepts the ghost-text prompt suggestion (Right+Enter, gated
	// on the suggestion actually showing in a fresh capture).
	KeyApprove

	// KeySmartDispatch opens the single-line smart-dispatch input: type a free-form
	// description and Atrium routes it to a project and pre-fills the new-session form.
	KeySmartDispatch

	// KeyMultiSelect enters multi-select ("visual") mode from the list, where
	// space marks/unmarks rows and a lifecycle action (pause/resume/kill) applies
	// to the marked set behind a single confirmation.
	KeyMultiSelect

	// KeyToggleMark marks/unmarks the highlighted session while in multi-select
	// mode. It is consumed only by the mode handler, never the default state.
	KeyToggleMark

	// KeyLayoutPreset cycles the named layout presets (monitor / default / review
	// / focus; see app.layoutPresets) on a single key. Each preset sets the
	// list/preview split (or hides the list entirely, in focus), the active
	// preset persists across relaunch, and < / > still fine-tune the split as a
	// custom override that never fights this cycle.
	KeyLayoutPreset

	// KeyScreensaver shows the configured splash pattern full-window until any
	// key (or click) dismisses it. A deliberate easter egg: it has no Registry
	// entry (and therefore no GlobalKeyBindings entry), so it never appears in
	// the help cheatsheet or the hint bar (the coverage guard only walks that
	// map). Its dispatch line is appended by hand in registry.go.
	KeyScreensaver

	// The names below are documented-only: their Registry entries carry
	// DocOnly, so they never enter GlobalKeyStringsMap. They exist so the
	// cheatsheet can reference the keys it documents.

	// KeySessionCycle is ctrl+pgup/pgdn — cycle to the prev/next session in
	// the repo group. Honored only while attached, by the attach layer's
	// escape-sequence scanner (session/tmux/detach.go); the TUI never sees it.
	KeySessionCycle
	// KeyEscape is esc's contextual role on the list: exit scroll mode /
	// clear the committed filter. Handled before dispatch (app/app_update.go),
	// like ctrl+l below — routing either through the dispatch map would put
	// it behind the busy-gate.
	KeyEscape
	// KeyRedraw is ctrl+l, the universal manual-repaint escape hatch.
	KeyRedraw
)

// KillKey is the chord that triggers a kill from the session list. It mirrors the
// in-session kill byte (ctrlX, session/tmux/tmux.go) so the same key tears a session
// down whether you're on the list or attached to it.
const KillKey = "ctrl+x"
