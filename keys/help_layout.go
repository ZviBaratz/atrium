package keys

// HelpRow is one cheatsheet line: a key column derived from the referenced
// bindings' Help().Key labels (never free text — a label that lies about its
// key can't be written), and the row's prose. Rows may merge several bindings
// ("↑/k ↓/j" is KeyUp + KeyDown) because the cheatsheet is a curated
// projection, not a mechanical dump.
type HelpRow struct {
	// Keys are the bindings this row's key column documents (≥1).
	Keys []KeyName
	// Mentions are bindings taught inside this row's Desc prose instead of a
	// key column of their own (space lives in the multi-select row). Each
	// mention's Help().Key must appear verbatim in Desc — pinned by
	// TestHelpGroups_MentionsAreRendered, so deleting the prose undocuments
	// the key loudly. Keep this rare.
	Mentions []KeyName
	// Desc is the cheatsheet prose. Rows whose keys are LayerAttached render
	// with a generated "in a session: " prefix — don't write it here.
	Desc string
	// Compact joins the key labels with " " instead of " / " (paired arrows).
	Compact bool
}

// HelpGroup is one titled cheatsheet section; order on screen is slice order.
type HelpGroup struct {
	Title string
	Rows  []HelpRow
}

// HelpGroups is the ? cheatsheet's layout: the one authored copy of the help
// text, projected to the screen by app/help.go. The drift guards in
// help_layout_test.go tie it to the registry in both directions.
var HelpGroups = []HelpGroup{
	{Title: "Navigate", Rows: []HelpRow{
		{Keys: []KeyName{KeyUp, KeyDown}, Compact: true, Desc: "move selection"},
		{Keys: []KeyName{KeyNextUnread, KeyNextNeedsInput}, Desc: "jump to next unread / blocked"},
		{Keys: []KeyName{KeyTab, KeyShiftTab}, Desc: "next / prev pane"},
		{Keys: []KeyName{KeyTabPreview, KeyTabDiff, KeyTabTerminal}, Desc: "jump to preview / diff / terminal"},
		{Keys: []KeyName{KeyShiftUp, KeyShiftDown}, Compact: true, Desc: "scroll the active pane"},
		{Keys: []KeyName{KeyShrinkList, KeyGrowList}, Desc: "shrink / grow the session list (or drag the divider)"},
		{Keys: []KeyName{KeyLayoutPreset}, Desc: "cycle layout presets (monitor / default / review / focus)"},
		{Keys: []KeyName{KeyEscape}, Desc: "exit scroll mode / clear filter / leave focus"},
	}},
	{Title: "Manage", Rows: []HelpRow{
		{Keys: []KeyName{KeyNew}, Desc: "new session (form, name first)"},
		{Keys: []KeyName{KeyPrompt}, Desc: "new session (form, project first)"},
		{Keys: []KeyName{KeySmartDispatch}, Desc: "smart new (describe it; auto-routes to a project)"},
		{Keys: []KeyName{KeyRename}, Desc: "rename session (label only)"},
		{Keys: []KeyName{KeyAutoName}, Desc: "auto-name session (via its agent)"},
		{Keys: []KeyName{KeyFilter}, Desc: "filter sessions"},
		{Keys: []KeyName{KeyMultiSelect}, Mentions: []KeyName{KeyToggleMark},
			Desc: "multi-select: space marks, p/r/x act on the marked set"},
	}},
	{Title: "Handoff", Rows: []HelpRow{
		{Keys: []KeyName{KeyEnter}, Desc: "attach to the selected session"},
		{Keys: []KeyName{KeyAttachToggle}, Desc: "toggle attach/detach (detach when in, attach from the list)"},
		{Keys: []KeyName{KeyKill}, Desc: "kill the selected/attached session (twice to confirm)"},
		{Keys: []KeyName{KeySessionCycle}, Desc: "cycle to prev / next session in the repo group"},
		{Keys: []KeyName{KeyQuickSend}, Desc: "send a message (without attaching)"},
		{Keys: []KeyName{KeyQueue}, Desc: "manage queued prompts (list / cancel)"},
		{Keys: []KeyName{KeyApprove}, Desc: "approve the agent's prompt (enter picks its default); on idle claude, accept the suggested prompt"},
		{Keys: []KeyName{KeyPause}, Desc: "pause: commit changes + free the worktree"},
		{Keys: []KeyName{KeyPauseAll}, Desc: "pause all active sessions in the current view"},
		{Keys: []KeyName{KeySubmit}, Desc: "commit & push branch"},
		{Keys: []KeyName{KeyCreate}, Desc: "create a PR for the pushed branch (gh)"},
		{Keys: []KeyName{KeyMerge}, Desc: "merge the session's PR (squash)"},
		{Keys: []KeyName{KeyOpenPR}, Desc: "open the session's PR in the browser"},
		{Keys: []KeyName{KeyResume}, Desc: "resume a paused session"},
		{Keys: []KeyName{KeyResumeAll}, Desc: "resume all paused sessions in the current view"},
		{Keys: []KeyName{KeyCopyBranch}, Desc: "copy branch name to clipboard"},
		{Keys: []KeyName{KeyHints}, Desc: "copy/open URLs & paths from the preview"},
	}},
	{Title: "Groups", Rows: []HelpRow{
		{Keys: []KeyName{KeyMoveDown, KeyMoveUp}, Desc: "reorder within a repo group"},
		{Keys: []KeyName{KeyMoveGroupUp, KeyMoveGroupDown}, Desc: "move a whole group up / down"},
		{Keys: []KeyName{KeyMoveAccountUp, KeyMoveAccountDown}, Desc: "move an account cluster up / down"},
		{Keys: []KeyName{KeyCollapse, KeyExpand}, Desc: "collapse / expand group"},
		{Keys: []KeyName{KeyCollapseAll}, Desc: "collapse / expand all"},
	}},
	{Title: "Other", Rows: []HelpRow{
		{Keys: []KeyName{KeyHelp}, Desc: "toggle this cheatsheet"},
		{Keys: []KeyName{KeySettings}, Desc: "settings"},
		{Keys: []KeyName{KeyAccounts}, Desc: "accounts (Claude / GitHub)"},
		{Keys: []KeyName{KeyCmdLog}, Desc: "command log: the tmux/git/gh commands Atrium ran (filter all / session / failures)"},
		{Keys: []KeyName{KeyRedraw}, Desc: "force a full redraw of the screen"},
		{Keys: []KeyName{KeyQuit}, Desc: "quit"},
	}},
}
